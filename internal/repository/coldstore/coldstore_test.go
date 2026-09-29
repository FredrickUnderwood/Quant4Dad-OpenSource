package coldstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
)

func testDigest(body []byte) Digest {
	sum := sha256.Sum256(body)
	return Digest{Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
}
func objectKey(digest Digest) string { return "test/batch/events-" + digest.SHA256 + ".jsonl.gz" }
func TestLocalImmutableObjectPreservesContent(t *testing.T) {
	directory := t.TempDir()
	store := NewLocalStore(directory)
	body := []byte("fixture archive")
	digest := testDigest(body)
	key := objectKey(digest)
	if err := store.PutImmutable(context.Background(), key, bytes.NewReader(body), digest); err != nil {
		t.Fatal(err)
	}
	if err := store.PutImmutable(context.Background(), key, bytes.NewReader(body), digest); err != nil {
		t.Fatal("retry failed", err)
	}
	if err := store.PutImmutable(context.Background(), key, bytes.NewReader([]byte("different")), digest); !errors.Is(err, ErrObjectConflict) {
		t.Fatal("different bytes accepted", err)
	}
	actual, err := os.ReadFile(filepath.Join(directory, key))
	if err != nil || !bytes.Equal(actual, body) {
		t.Fatal("immutable object replaced", err)
	}
	for _, invalid := range []string{"../" + key, "/" + key, "other-file.json"} {
		if err := store.PutImmutable(context.Background(), invalid, bytes.NewReader(body), digest); !errors.Is(err, ErrInvalidObject) {
			t.Fatal("invalid key accepted", invalid, err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, key), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.PutImmutable(context.Background(), key, bytes.NewReader(body), digest); !errors.Is(err, ErrObjectConflict) {
		t.Fatal("corrupt object silently replaced", err)
	}
}
func TestLocalConcurrentPublishAndCancelledUpload(t *testing.T) {
	store := NewLocalStore(t.TempDir())
	body := bytes.Repeat([]byte("fixture"), 100)
	digest := testDigest(body)
	key := objectKey(digest)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- store.PutImmutable(context.Background(), key, bytes.NewReader(body), digest)
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.PutImmutable(ctx, key, bytes.NewReader(body), digest); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled operation succeeded", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func mockOSS(transport roundTripFunc) *OSSStore {
	cfg := oss.LoadDefaultConfig().WithRegion("cn-test").WithEndpoint("https://oss.example.test").WithUsePathStyle(true).WithRetryMaxAttempts(1).WithCredentialsProvider(credentials.NewStaticCredentialsProvider("fixture-ak", "fixture-secret")).WithHttpClient(&http.Client{Transport: transport})
	return &OSSStore{client: oss.NewClient(cfg), bucket: "archive-test"}
}
func response(req *http.Request, status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Length": []string{strconv.Itoa(len(body))}, "Content-Type": []string{"application/xml"}}, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Request: req}
}
func TestOSSImmutablePublicationChecksActualContent(t *testing.T) {
	objects := map[string][]byte{}
	puts := 0
	store := mockOSS(func(req *http.Request) (*http.Response, error) {
		switch req.Method {
		case http.MethodGet:
			body, ok := objects[req.URL.Path]
			if !ok {
				return response(req, 404, []byte(`<Error><Code>NoSuchKey</Code><Message>fixture</Message></Error>`)), nil
			}
			return response(req, 200, body), nil
		case http.MethodPut:
			puts++
			if req.Header.Get("x-oss-forbid-overwrite") != "true" {
				t.Error("missing immutable-write defense")
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			objects[req.URL.Path] = body
			return response(req, 200, nil), nil
		default:
			t.Errorf("unexpected method %s", req.Method)
			return response(req, 500, nil), nil
		}
	})
	body := []byte("immutable fixture")
	digest := testDigest(body)
	key := objectKey(digest)
	for range 2 {
		if err := store.PutImmutable(context.Background(), key, bytes.NewReader(body), digest); err != nil {
			t.Fatal(err)
		}
	}
	if puts != 1 {
		t.Fatal("verified retry uploaded again", puts)
	}
	objects["/archive-test/"+key] = []byte("different content")
	if err := store.PutImmutable(context.Background(), key, bytes.NewReader(body), digest); !errors.Is(err, ErrObjectConflict) {
		t.Fatal("tampered object accepted", err)
	}
	if puts != 1 {
		t.Fatal("mismatched object overwritten")
	}
}
func TestOSSProbeOnlyReadsBucketAndRedactsErrors(t *testing.T) {
	for _, status := range []int{200, 403, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			calls := 0
			store := mockOSS(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodGet || !req.URL.Query().Has("bucketInfo") {
					t.Errorf("probe is not a bucket read: %s %s", req.Method, req.URL.Path)
				}
				if status == 200 {
					return response(req, status, []byte(`<BucketInfo><Bucket><Name>archive-test</Name><Location>cn-test</Location></Bucket></BucketInfo>`)), nil
				}
				return response(req, status, []byte(`<Error><Code>AccessDenied</Code><Message>fixture-sensitive-upstream-diagnostic</Message></Error>`)), nil
			})
			err := store.Probe(context.Background())
			if calls != 1 {
				t.Fatal("unexpected request count", calls)
			}
			if status == 200 && err != nil {
				t.Fatal(err)
			}
			if status == 403 && !errors.Is(err, ErrProbeRejected) {
				t.Fatal(err)
			}
			if status == 500 && !errors.Is(err, ErrStoreUnavailable) {
				t.Fatal(err)
			}
			if err != nil && strings.Contains(err.Error(), "sensitive") {
				t.Fatal("remote diagnostic leaked")
			}
		})
	}
}
