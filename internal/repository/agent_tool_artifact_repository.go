package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

var ErrToolArtifact = errors.New("agent_tool_artifact_unavailable")
var toolArtifactName = regexp.MustCompile(`^[0-9a-f]{64}\.json$`)

type AgentToolArtifactRepository struct{ directory string }

func NewAgentToolArtifactRepository(directory string) (*AgentToolArtifactRepository, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, ErrToolArtifact
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, ErrToolArtifact
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return nil, ErrToolArtifact
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, ErrToolArtifact
	}
	return &AgentToolArtifactRepository{directory}, nil
}
func (r *AgentToolArtifactRepository) Put(body []byte) (string, error) {
	if len(body) == 0 || len(body) > 128<<10 {
		return "", ErrToolArtifact
	}
	unlock, err := r.lock(context.Background(), false)
	if err != nil {
		return "", err
	}
	defer unlock()
	sum := sha256.Sum256(body)
	name := hex.EncodeToString(sum[:]) + ".json"
	f, err := os.CreateTemp(r.directory, ".result-")
	if err != nil {
		return "", ErrToolArtifact
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(body); err != nil {
		return "", ErrToolArtifact
	}
	if err = f.Sync(); err != nil {
		return "", ErrToolArtifact
	}
	if err = f.Close(); err != nil {
		return "", ErrToolArtifact
	}
	if err = os.Rename(f.Name(), filepath.Join(r.directory, name)); err != nil {
		return "", ErrToolArtifact
	}
	dir, err := os.Open(r.directory)
	if err != nil {
		return "", ErrToolArtifact
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return "", ErrToolArtifact
	}
	return name, nil
}
func (r *AgentToolArtifactRepository) Get(name string) ([]byte, error) {
	if !toolArtifactName.MatchString(name) {
		return nil, ErrToolArtifact
	}
	unlock, err := r.lock(context.Background(), false)
	if err != nil {
		return nil, err
	}
	defer unlock()
	path := filepath.Join(r.directory, name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 128<<10 {
		return nil, ErrToolArtifact
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrToolArtifact
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, (128<<10)+1))
	if err != nil || len(body) > 128<<10 {
		return nil, ErrToolArtifact
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:])+".json" != name {
		return nil, ErrToolArtifact
	}
	return body, nil
}
