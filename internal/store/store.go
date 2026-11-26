// Package store implements core.Store as small JSON files in the plugin data
// folder. Writes go to a temp file that is synced and renamed into place, so
// a crash leaves either the old document or the new one, never half of one.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Store keeps one <name>.json file per document under dir.
type Store struct {
	dir string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// New returns a store rooted at dir, creating it (0700) when needed.
func New(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("store: empty data directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: create data directory: %w", err)
	}
	sweepTemp(dir)
	return &Store{dir: dir, locks: make(map[string]*sync.Mutex)}, nil
}

// sweepTemp removes the temp files a Save left behind when the process was
// killed between its write and its rename. Best effort: nothing else in the
// data folder ends in .tmp, and no Save is running yet.
func sweepTemp(dir string) {
	left, err := filepath.Glob(filepath.Join(dir, "*.*.tmp"))
	if err != nil {
		return
	}
	for _, p := range left {
		if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
			_ = os.Remove(p)
		}
	}
}

// Load fills v from <name>.json. A missing file leaves v as it is.
func (s *Store) Load(name string, v any) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	l := s.lock(name)
	l.Lock()
	defer l.Unlock()

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: read %s: %w", filepath.Base(path), err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("store: %s is not valid JSON: %w", filepath.Base(path), err)
	}
	return nil
}

// Save writes v to <name>.json atomically with mode 0600.
func (s *Store) Save(name string, v any) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode %s: %w", filepath.Base(path), err)
	}
	data = append(data, '\n')

	l := s.lock(name)
	l.Lock()
	defer l.Unlock()

	tmp, err := os.CreateTemp(s.dir, name+".*.tmp") // created 0600
	if err != nil {
		return fmt.Errorf("store: save %s: %w", filepath.Base(path), err)
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("store: save %s: %w", filepath.Base(path), err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fail(err)
	}
	// Best effort: make the rename itself durable.
	if d, err := os.Open(s.dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

func (s *Store) path(name string) (string, error) {
	if name == "" {
		return "", errors.New("store: empty document name")
	}
	if strings.ContainsAny(name, "/\\\x00") || strings.ContainsRune(name, filepath.Separator) || strings.Contains(name, "..") {
		return "", fmt.Errorf("store: invalid document name %q", name)
	}
	return filepath.Join(s.dir, name+".json"), nil
}

func (s *Store) lock(name string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[name]
	if !ok {
		l = new(sync.Mutex)
		s.locks[name] = l
	}
	return l
}
