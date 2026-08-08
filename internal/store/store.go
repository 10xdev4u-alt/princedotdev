// Package store is a filesystem-backed object store keyed like S3
// (drafts/<id>/versions/<v>.html), so swapping in real S3 later is a
// one-file change. Draft HTML is stored byte-for-byte.
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store persists draft HTML objects under a data directory.
type Store struct {
	root string
}

// New creates a Store rooted at dataDir/drafts.
func New(dataDir string) (*Store, error) {
	dir := filepath.Join(dataDir, "drafts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{root: dir}, nil
}

// Put writes html at key. Keys are relative paths (no leading slash, no "..").
func (s *Store) Put(key string, data []byte) error {
	if err := validateKey(key); err != nil {
		return err
	}
	full := filepath.Join(s.root, key)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o600)
}

// Get returns the bytes stored at key.
func (s *Store) Get(key string) ([]byte, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(s.root, key))
}

// Delete removes the object at key, if present.
func (s *Store) Delete(key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	return os.Remove(filepath.Join(s.root, key))
}

func validateKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "..") {
		return fmt.Errorf("invalid object key %q", key)
	}
	return nil
}
