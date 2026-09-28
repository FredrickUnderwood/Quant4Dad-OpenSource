package repository

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentToolArtifactsBoundedDurableAndPrivate(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(parent, "results")
	repo, err := NewAgentToolArtifactRepository(directory)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"data":{"count":1},"untrusted_data":true}`)
	ref, err := repo.Put(body)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(directory, ref))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("artifact permissions", err)
	}
	restarted, err := NewAgentToolArtifactRepository(directory)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restarted.Get(ref)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatal("cold result recovery", err)
	}
	if _, err = repo.Put(bytes.Repeat([]byte("x"), (128<<10)+1)); err == nil {
		t.Fatal("oversized artifact")
	}
	if _, err = repo.Get("../secret"); err == nil {
		t.Fatal("path traversal")
	}
	if err = os.WriteFile(filepath.Join(directory, ref), []byte(`{"tampered":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Get(ref); err == nil {
		t.Fatal("tampered result accepted")
	}
	other := strings.Repeat("a", 64) + ".json"
	if err = os.Symlink(filepath.Join(directory, ref), filepath.Join(directory, other)); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Get(other); err == nil {
		t.Fatal("symlink accepted")
	}
	if err = os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err = NewAgentToolArtifactRepository(directory); err == nil {
		t.Fatal("public artifact directory accepted")
	}
}
