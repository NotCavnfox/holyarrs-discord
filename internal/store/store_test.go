package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

var _ core.Store = (*Store)(nil)

type doc struct {
	A string         `json:"a"`
	N int            `json:"n"`
	M map[string]int `json:"m,omitempty"`
}

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, dir
}

func TestNew(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("New(\"\") = nil error")
	}
	_, dir := newStore(t)
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v, want drwx------", fi.Mode())
	}
	// A file where the directory should be is an error.
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(f); err == nil {
		t.Fatal("New on a file = nil error")
	}
}

func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   doc
	}{
		{"simple", doc{A: "x", N: 1}},
		{"zero", doc{}},
		{"map", doc{A: "y", M: map[string]int{"k": 2}}},
		{"unicode", doc{A: "S02E05 · The Bridge <&>"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := newStore(t)
			if err := s.Save("d", tc.in); err != nil {
				t.Fatalf("Save: %v", err)
			}
			var got doc
			if err := s.Load("d", &got); err != nil {
				t.Fatalf("Load: %v", err)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.in) {
				t.Fatalf("got %+v, want %+v", got, tc.in)
			}
			fi, err := os.Stat(filepath.Join(dir, "d.json"))
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0o600 {
				t.Fatalf("file mode = %v, want 0600", fi.Mode().Perm())
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Fatalf("leftover files: %v", entries)
			}
		})
	}
}

func TestSaveIsIndentedAndReplaces(t *testing.T) {
	s, dir := newStore(t)
	if err := s.Save("d", doc{A: "old", N: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("d", doc{A: "new", N: 2}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "d.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"a\": \"new\",\n  \"n\": 2\n}\n"
	if string(raw) != want {
		t.Fatalf("file = %q, want %q", raw, want)
	}
}

func TestLoadMissingLeavesValue(t *testing.T) {
	s, _ := newStore(t)
	v := doc{A: "keep", N: 7}
	if err := s.Load("nope", &v); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if v.A != "keep" || v.N != 7 {
		t.Fatalf("v changed: %+v", v)
	}
}

func TestLoadCorruptNamesFile(t *testing.T) {
	for _, body := range []string{"{", "", "[1,2", `{"a": 5}`} {
		s, dir := newStore(t)
		if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		var v doc
		err := s.Load("bad", &v)
		if err == nil {
			t.Fatalf("body %q: nil error", body)
		}
		if !strings.Contains(err.Error(), "bad.json") {
			t.Fatalf("body %q: error %q does not name the file", body, err)
		}
	}
}

func TestInvalidNames(t *testing.T) {
	s, dir := newStore(t)
	for _, name := range []string{"", "a/b", `a\b`, "..", "../x", "a..b", "/abs", "x\x00y"} {
		if err := s.Save(name, doc{}); err == nil {
			t.Errorf("Save(%q) = nil error", name)
		}
		var v doc
		if err := s.Load(name, &v); err == nil {
			t.Errorf("Load(%q) = nil error", name)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("files written for invalid names: %v", entries)
	}
}

func TestSaveUnencodable(t *testing.T) {
	s, dir := newStore(t)
	if err := s.Save("d", doc{A: "good"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("d", map[string]any{"c": make(chan int)}); err == nil {
		t.Fatal("nil error for unencodable value")
	}
	var v doc
	if err := s.Load("d", &v); err != nil || v.A != "good" {
		t.Fatalf("old document damaged: %+v, %v", v, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestConcurrent(t *testing.T) {
	s, dir := newStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("d%d", i%3)
			for j := 0; j < 20; j++ {
				if err := s.Save(name, doc{A: "v", N: i}); err != nil {
					t.Errorf("Save: %v", err)
				}
				var v doc
				if err := s.Load(name, &v); err != nil {
					t.Errorf("Load: %v", err)
				}
				if v.A != "v" {
					t.Errorf("torn read: %+v", v)
				}
			}
		}(i)
	}
	wg.Wait()
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Fatalf("want 3 files, got %v", entries)
	}
}
