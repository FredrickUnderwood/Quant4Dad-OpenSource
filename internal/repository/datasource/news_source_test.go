package datasource

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/quant4dad/internal/domain"
)

// pagesFetcher wraps fixed "page -> items" data as a FetchPageFunc, using the
// decimal page number as the cursor, and records how many pages were requested so
// tests can assert that paging stopped early. When errAtPage >= 0 that page
// returns an error.
func pagesFetcher(pages [][]string, errAtPage int) (FetchPageFunc, *int) {
	calls := 0
	fn := func(_ context.Context, cursor string) ([]*domain.News, string, error) {
		page := 0
		if cursor != "" {
			page, _ = strconv.Atoi(cursor)
		}
		calls++
		if errAtPage >= 0 && page == errAtPage {
			return nil, "", errors.New("boom")
		}
		if page >= len(pages) {
			return nil, "", nil
		}
		items := make([]*domain.News, 0, len(pages[page]))
		for _, id := range pages[page] {
			items = append(items, &domain.News{ExternalID: id})
		}
		return items, strconv.Itoa(page + 1), nil
	}
	return fn, &calls
}

func ids(items []*domain.News) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ExternalID)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A seen hit stops the walk: collect only the new items ahead of it on the first
// page, and don't request a second page.
func TestPaginateStopsOnSeen(t *testing.T) {
	pages := [][]string{{"a", "b", "c"}, {"d", "e"}}
	fetch, calls := pagesFetcher(pages, -1)
	seen := func(id string) bool { return id == "c" } // c is already stored
	got, err := Paginate(context.Background(), seen, fetch)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !eq(ids(got), []string{"a", "b"}) {
		t.Errorf("want [a b], got %v", ids(got))
	}
	if *calls != 1 {
		t.Errorf("should stop paging once caught up, want 1 page, got %d", *calls)
	}
}

// Items repeated across pages within one batch are deduplicated.
func TestPaginateDedupAcrossPages(t *testing.T) {
	pages := [][]string{{"a", "b"}, {"b", "c"}}
	fetch, _ := pagesFetcher(pages, -1)
	got, err := Paginate(context.Background(), func(string) bool { return false }, fetch)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !eq(ids(got), []string{"a", "b", "c"}) {
		t.Errorf("want [a b c], got %v", ids(got))
	}
}

// When seen is always false (a first collection into an empty table), the
// NewsMaxPages cap keeps the walk from running forever.
func TestPaginateMaxPagesCap(t *testing.T) {
	pages := make([][]string, NewsMaxPages+5)
	for i := range pages {
		pages[i] = []string{"id-" + strconv.Itoa(i)}
	}
	fetch, calls := pagesFetcher(pages, -1)
	got, err := Paginate(context.Background(), func(string) bool { return false }, fetch)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if *calls != NewsMaxPages {
		t.Errorf("want the %d page cap, got %d", NewsMaxPages, *calls)
	}
	if len(got) != NewsMaxPages {
		t.Errorf("want %d items, got %d", NewsMaxPages, len(got))
	}
}

// Paging failing after some items were collected: swallow the error, return what
// was collected (partial success).
func TestPaginatePartialOnLaterError(t *testing.T) {
	pages := [][]string{{"a", "b"}}
	fetch, _ := pagesFetcher(pages, 1) // page 0 fine, page 1 errors
	got, err := Paginate(context.Background(), func(string) bool { return false }, fetch)
	if err != nil {
		t.Fatalf("errors on later pages should be swallowed, got err: %v", err)
	}
	if !eq(ids(got), []string{"a", "b"}) {
		t.Errorf("want [a b], got %v", ids(got))
	}
}

// The first page failing fails the whole batch and returns the error.
func TestPaginateErrorOnFirstPage(t *testing.T) {
	fetch, _ := pagesFetcher(nil, 0)
	got, err := Paginate(context.Background(), func(string) bool { return false }, fetch)
	if err == nil {
		t.Fatalf("want err, got nil (items=%v)", ids(got))
	}
}

// ClampTitle truncates by character, not byte, so multi-byte titles are not cut
// mid-character. The CJK literals below are the point of the test.
func TestClampTitle(t *testing.T) {
	short := "短标题"
	if got := ClampTitle(short); got != short {
		t.Errorf("should leave a title under the limit alone, got %q", got)
	}
	long := make([]rune, NewsTitleMaxLen+10)
	for i := range long {
		long[i] = '资'
	}
	got := []rune(ClampTitle(string(long)))
	if len(got) != NewsTitleMaxLen {
		t.Errorf("want %d characters, got %d", NewsTitleMaxLen, len(got))
	}
	if got[len(got)-1] != '…' {
		t.Errorf("truncation should end with an ellipsis, got %q", string(got[len(got)-1]))
	}
}

// Nothing is registered by default; after registering, the source is retrievable
// by name.
func TestNewsSourceRegistry(t *testing.T) {
	if names := NewsSourceNames(); len(names) != 0 {
		t.Fatalf("no news source should be registered by default, got %v", names)
	}
	RegisterNewsSource(stubNewsSource{})
	t.Cleanup(func() {
		newsSourceMu.Lock()
		delete(newsSources, "stub")
		newsSourceMu.Unlock()
	})
	if names := NewsSourceNames(); !eq(names, []string{"stub"}) {
		t.Errorf("want [stub], got %v", names)
	}
	if _, ok := NewsSources()["stub"]; !ok {
		t.Error("a registered source should be present in NewsSources()")
	}
}

type stubNewsSource struct{}

func (stubNewsSource) Name() string { return "stub" }

func (stubNewsSource) Fetch(context.Context, SeenFunc) ([]*domain.News, error) { return nil, nil }
