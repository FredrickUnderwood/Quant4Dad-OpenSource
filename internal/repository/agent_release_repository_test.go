package repository

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/utils/tooljson"
)

func TestAgentSignedReleaseAtomicReloadAndTamper(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "active-generation.json")
	private := ed25519.NewKeyFromSeed(make([]byte, 32))
	repo := NewAgentReleaseRepository(path, base64.RawURLEncoding.EncodeToString(private[32:]), "0.0.0", false)
	write := func(version, channel string, tamper bool) {
		t.Helper()
		r := map[string]any{"schema_version": 1, "version": version, "q4d_version": "0.0.0", "channel": channel,
			"image": "registry.example/q4d@sha256:" + strings.Repeat("a", 64), "adapter_version": "v1", "dsh_version": "0.1.2-alpha.5",
			"bridge_protocol": 1, "session_format": 0, "session_binding_format": 1, "event_journal_format": 1}
		body, _ := sonic.Marshal(r)
		canonical, err := tooljson.Canonical(body)
		if err != nil {
			t.Fatal(err)
		}
		r["signature"] = base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, canonical))
		if tamper {
			r["image"] = "registry.example/other@sha256:" + strings.Repeat("b", 64)
		}
		body, _ = sonic.Marshal(map[string]any{"generation": "g1", "release": r})
		if err = os.WriteFile(path+".next", body, 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.Rename(path+".next", path); err != nil {
			t.Fatal(err)
		}
	}
	for _, version := range []string{"v1", "v2", "v1"} {
		write(version, "verified", false)
		current, err := repo.Current(context.Background())
		if err != nil || current.AgentRuntimeVersion != version {
			t.Fatal("release not reloaded", err)
		}
	}
	write("v2", "verified", true)
	if _, err := repo.Current(context.Background()); err == nil {
		t.Fatal("tampered release accepted")
	}
	write("v2", "edge", false)
	if _, err := repo.Current(context.Background()); err == nil {
		t.Fatal("implicit edge accepted")
	}
	write("v1", "verified", false)
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Current(context.Background()); err == nil {
		t.Fatal("public writable release accepted")
	}
}
