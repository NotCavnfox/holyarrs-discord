package links_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
	"github.com/NotCavnfox/holyarrs-discord/internal/coretest"
	"github.com/NotCavnfox/holyarrs-discord/internal/links"
)

var _ core.Links = (*links.Book)(nil)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func newBook(t *testing.T) (*links.Book, *coretest.FakeStore) {
	t.Helper()
	st := coretest.NewFakeStore()
	b, err := links.New(st)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b, st
}

func TestPut(t *testing.T) {
	alice := core.Link{DiscordID: "1", UserID: "a", UserName: "Alice", LinkedAt: t0}
	tests := []struct {
		name    string
		seed    []core.Link
		put     core.Link
		wantErr error
		anyErr  bool
		wantLen int
	}{
		{name: "first", put: alice, wantLen: 1},
		{name: "second member", seed: []core.Link{alice}, put: core.Link{DiscordID: "2", UserID: "b"}, wantLen: 2},
		{name: "same pair updates", seed: []core.Link{alice}, put: core.Link{DiscordID: "1", UserID: "a", ShowWatching: true}, wantLen: 1},
		{name: "member switches user", seed: []core.Link{alice}, put: core.Link{DiscordID: "1", UserID: "b"}, wantLen: 1},
		{name: "user taken", seed: []core.Link{alice}, put: core.Link{DiscordID: "2", UserID: "a"}, wantErr: core.ErrAlreadyLinked, wantLen: 1},
		{name: "no discord id", put: core.Link{UserID: "a"}, anyErr: true},
		{name: "no user id", put: core.Link{DiscordID: "1"}, anyErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, st := newBook(t)
			for _, l := range tc.seed {
				if err := b.Put(l); err != nil {
					t.Fatal(err)
				}
			}
			saves := len(st.CallsTo("Save"))
			err := b.Put(tc.put)
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
			case tc.anyErr:
				if err == nil {
					t.Fatal("nil error")
				}
			case err != nil:
				t.Fatalf("Put: %v", err)
			}
			if got := len(b.All()); got != tc.wantLen {
				t.Fatalf("len = %d, want %d", got, tc.wantLen)
			}
			wantSaves := saves + 1
			if err != nil {
				wantSaves = saves
			}
			if got := len(st.CallsTo("Save")); got != wantSaves {
				t.Fatalf("saves = %d, want %d", got, wantSaves)
			}
			if err == nil {
				if got, ok := b.ByDiscord(tc.put.DiscordID); !ok || got != tc.put {
					t.Fatalf("ByDiscord = %+v %v", got, ok)
				}
				if got, ok := b.ByUser(tc.put.UserID); !ok || got != tc.put {
					t.Fatalf("ByUser = %+v %v", got, ok)
				}
			}
		})
	}
}

func TestReplaceFreesOldUser(t *testing.T) {
	b, _ := newBook(t)
	must(t, b.Put(core.Link{DiscordID: "1", UserID: "a"}))
	must(t, b.Put(core.Link{DiscordID: "1", UserID: "b"}))
	if _, ok := b.ByUser("a"); ok {
		t.Fatal("user a still indexed after replace")
	}
	must(t, b.Put(core.Link{DiscordID: "2", UserID: "a"}))
}

func TestRemove(t *testing.T) {
	b, st := newBook(t)
	must(t, b.Put(core.Link{DiscordID: "1", UserID: "a"}))
	must(t, b.Put(core.Link{DiscordID: "2", UserID: "b"}))
	saves := len(st.CallsTo("Save"))

	must(t, b.Remove("nobody"))
	if got := len(st.CallsTo("Save")); got != saves {
		t.Fatalf("no-op remove saved (%d -> %d)", saves, got)
	}
	must(t, b.Remove("1"))
	if got := len(st.CallsTo("Save")); got != saves+1 {
		t.Fatalf("remove did not save once (%d -> %d)", saves, got)
	}
	if _, ok := b.ByDiscord("1"); ok {
		t.Fatal("still found by Discord id")
	}
	if _, ok := b.ByUser("a"); ok {
		t.Fatal("still found by user")
	}
	if all := b.All(); len(all) != 1 || all[0].DiscordID != "2" {
		t.Fatalf("All = %+v", all)
	}
}

func TestPersistsAcrossReload(t *testing.T) {
	b, st := newBook(t)
	want := []core.Link{
		{DiscordID: "1", UserID: "a", UserName: "Alice", ShowWatching: true, LinkedAt: t0},
		{DiscordID: "2", UserID: "b", UserName: "Bob", AlertsOff: true, LinkedAt: t0.Add(time.Hour)},
	}
	must(t, b.Put(want[1]))
	must(t, b.Put(want[0]))

	b2, err := links.New(st)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := b2.All()
	if len(got) != 2 {
		t.Fatalf("All = %+v", got)
	}
	for i := range want {
		if got[i].DiscordID != want[i].DiscordID || got[i].UserID != want[i].UserID ||
			got[i].UserName != want[i].UserName || got[i].ShowWatching != want[i].ShowWatching ||
			got[i].AlertsOff != want[i].AlertsOff || !got[i].LinkedAt.Equal(want[i].LinkedAt) {
			t.Fatalf("link %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if l, ok := b2.ByUser("b"); !ok || l.DiscordID != "2" {
		t.Fatalf("ByUser after reload = %+v %v", l, ok)
	}
}

// A links.json written before the alerts choice existed has no such field:
// those members have alerts on, and saving them again adds nothing.
func TestAlertsDefaultOn(t *testing.T) {
	st := coretest.NewFakeStore()
	st.SetRaw(links.Document, []byte(`{"version":1,"links":[`+
		`{"discord_id":"1","user_id":"a","user_name":"Alice","show_watching":true,"linked_at":"2026-01-02T03:04:05Z"}]}`))
	b, err := links.New(st)
	if err != nil {
		t.Fatal(err)
	}
	l, ok := b.ByDiscord("1")
	if !ok || l.AlertsOff || !l.ShowWatching {
		t.Fatalf("old link = %+v %v, want alerts on", l, ok)
	}
	must(t, b.Put(l))
	if raw, _ := st.Raw(links.Document); strings.Contains(string(raw), "alerts") {
		t.Errorf("alerts on was written out: %s", raw)
	}
	l.AlertsOff = true
	must(t, b.Put(l))
	if raw, _ := st.Raw(links.Document); !strings.Contains(string(raw), `"alerts_off":true`) {
		t.Errorf("alerts off was not saved: %s", raw)
	}
	b2, err := links.New(st)
	if err != nil {
		t.Fatal(err)
	}
	if l, _ := b2.ByDiscord("1"); !l.AlertsOff {
		t.Error("alerts off did not survive a reload")
	}
}

func TestSaveFailureChangesNothing(t *testing.T) {
	b, st := newBook(t)
	must(t, b.Put(core.Link{DiscordID: "1", UserID: "a"}))
	boom := errors.New("disk full")
	st.SetErr("Save", boom)

	if err := b.Put(core.Link{DiscordID: "2", UserID: "b"}); !errors.Is(err, boom) {
		t.Fatalf("Put err = %v", err)
	}
	if err := b.Put(core.Link{DiscordID: "1", UserID: "c"}); !errors.Is(err, boom) {
		t.Fatalf("Put err = %v", err)
	}
	if err := b.Remove("1"); !errors.Is(err, boom) {
		t.Fatalf("Remove err = %v", err)
	}
	if all := b.All(); len(all) != 1 || all[0].UserID != "a" {
		t.Fatalf("book changed after failed saves: %+v", all)
	}
	if _, ok := b.ByUser("a"); !ok {
		t.Fatal("index lost user a")
	}
	if _, ok := b.ByUser("c"); ok {
		t.Fatal("index gained user c")
	}
}

func TestNewErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"corrupt", `{`, "load"},
		{"duplicate discord", `{"links":[{"discord_id":"1","user_id":"a"},{"discord_id":"1","user_id":"b"}]}`, "entry 1 in links.json repeats a Discord id"},
		{"duplicate user", `{"links":[{"discord_id":"1","user_id":"a"},{"discord_id":"2","user_id":"a"}]}`, "entry 1 in links.json repeats a user id"},
		{"empty id", `{"links":[{"discord_id":"","user_id":"a"}]}`, "empty id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := coretest.NewFakeStore()
			st.SetRaw(links.Document, []byte(tc.raw))
			_, err := links.New(st)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	if _, err := links.New(nil); err == nil {
		t.Fatal("New(nil) = nil error")
	}
	st := coretest.NewFakeStore()
	st.SetErr("Load", errors.New("io"))
	if _, err := links.New(st); err == nil {
		t.Fatal("load error swallowed")
	}
}

// Load errors are logged: they name the entry, never an id.
func TestNewErrorsHoldNoIDs(t *testing.T) {
	for _, raw := range []string{
		`{"links":[{"discord_id":"302845117690216450","user_id":"71"},{"discord_id":"302845117690216450","user_id":"72"}]}`,
		`{"links":[{"discord_id":"302845117690216450","user_id":"71"},{"discord_id":"302845117690216451","user_id":"71"}]}`,
	} {
		st := coretest.NewFakeStore()
		st.SetRaw(links.Document, []byte(raw))
		_, err := links.New(st)
		if err == nil || strings.Contains(err.Error(), "3028451176902164") || strings.Contains(err.Error(), "71") {
			t.Errorf("err = %v, want one without ids", err)
		}
	}
}

func TestOnChange(t *testing.T) {
	b, st := newBook(t)
	var got []string
	b.OnChange(func(ev core.Event) {
		b.All() // the book is unlocked while fn runs
		got = append(got, string(ev.Type)+" "+ev.Member)
	})
	must(t, b.Put(core.Link{DiscordID: "1", UserID: "a"}))
	must(t, b.Put(core.Link{DiscordID: "1", UserID: "a", ShowWatching: true}))
	must(t, b.Put(core.Link{DiscordID: "1", UserID: "b"})) // another user: the old link is gone first
	must(t, b.Remove("1"))
	must(t, b.Remove("1")) // nothing there: no event
	if err := b.Put(core.Link{DiscordID: "2"}); err == nil {
		t.Fatal("bad link accepted")
	}
	st.SetErr("Save", errors.New("disk"))
	if err := b.Put(core.Link{DiscordID: "3", UserID: "c"}); err == nil {
		t.Fatal("save error swallowed")
	}
	want := "link.changed 1|link.changed 1|link.removed 1|link.changed 1|link.removed 1"
	if s := strings.Join(got, "|"); s != want {
		t.Fatalf("events %s, want %s", s, want)
	}
}

func TestConcurrent(t *testing.T) {
	b, _ := newBook(t)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprint(i)
			// Everyone also fights over user "shared": exactly one may win.
			_ = b.Put(core.Link{DiscordID: id, UserID: "shared"})
			_ = b.Put(core.Link{DiscordID: "x" + id, UserID: core.UserID("u" + id)})
			b.ByDiscord(id)
			b.ByUser("shared")
			b.All()
			if i%2 == 0 {
				_ = b.Remove("x" + id)
			}
		}(i)
	}
	wg.Wait()
	shared := 0
	for _, l := range b.All() {
		if l.UserID == "shared" {
			shared++
		}
	}
	if shared != 1 {
		t.Fatalf("user linked to %d Discord ids, want 1", shared)
	}
	if got := len(b.All()); got != 1+8 {
		t.Fatalf("len = %d, want 9", got)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
