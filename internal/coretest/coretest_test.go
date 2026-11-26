package coretest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

func TestFakeStore(t *testing.T) {
	s := NewFakeStore()
	v := map[string]int{"keep": 1}
	if err := s.Load("x", &v); err != nil || v["keep"] != 1 {
		t.Fatalf("missing doc: %v %v", v, err)
	}
	if err := s.Save("x", map[string]int{"a": 2}); err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	if err := s.Load("x", &got); err != nil || got["a"] != 2 {
		t.Fatalf("round trip: %v %v", got, err)
	}
	boom := errors.New("boom")
	s.SetErr("Save", boom)
	if err := s.Save("x", 1); !errors.Is(err, boom) {
		t.Fatalf("Save err = %v", err)
	}
	s.SetErr("Save", nil)
	if err := s.Save("x", 1); err != nil {
		t.Fatal(err)
	}
	if n := len(s.CallsTo("Save")); n != 3 {
		t.Fatalf("Save calls = %d", n)
	}
}

func TestFakeLinks(t *testing.T) {
	l := NewFakeLinks(core.Link{DiscordID: "1", UserID: "a"})
	if err := l.Put(core.Link{DiscordID: "2", UserID: "a"}); !errors.Is(err, core.ErrAlreadyLinked) || !errors.Is(err, ErrUserLinked) {
		t.Fatalf("err = %v", err)
	}
	if err := l.Put(core.Link{DiscordID: "1", UserID: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.ByUser("a"); ok {
		t.Fatal("old user still indexed")
	}
	if got, ok := l.ByUser("b"); !ok || got.DiscordID != "1" {
		t.Fatalf("ByUser(b) = %v %v", got, ok)
	}
	if err := l.Remove("1"); err != nil || len(l.All()) != 0 {
		t.Fatalf("Remove: %v %v", err, l.All())
	}
}

func TestFakeDiscord(t *testing.T) {
	ctx := context.Background()
	d := NewFakeDiscord()
	for _, want := range []string{"1", "2"} {
		if id, err := d.Send(ctx, "c", core.Message{Content: want}); err != nil || id != want {
			t.Fatalf("Send = %q, %v; want %q", id, err, want)
		}
	}
	d.SetErr("DM", core.ErrForbidden)
	if err := d.DM(ctx, "u", core.Message{}); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("DM err = %v", err)
	}
	_ = d.Pin(ctx, "c", "1")
	calls := d.Calls()
	var order []string
	for _, c := range calls {
		order = append(order, c.Method)
	}
	if len(order) != 4 || order[0] != "Send" || order[2] != "DM" || order[3] != "Pin" {
		t.Fatalf("order = %v", order)
	}
	if calls[1].Args[0] != "c" || calls[1].Args[1].(core.Message).Content != "2" {
		t.Fatalf("args = %v", calls[1].Args)
	}

	pressed := ""
	err := d.Open(ctx, []core.Command{{Name: "ping", Handle: func(context.Context, *core.Interaction) error { pressed = "cmd"; return nil }}},
		func(_ context.Context, in *core.Interaction) error { pressed = in.CustomID; return nil })
	if err != nil || !d.IsOpen() {
		t.Fatalf("Open: %v", err)
	}
	if err := d.Press(ctx, &core.Interaction{CustomID: "f:x"}); err != nil || pressed != "f:x" {
		t.Fatalf("Press: %v %q", err, pressed)
	}
	if err := d.Invoke(ctx, &core.Interaction{Command: "ping"}); err != nil || pressed != "cmd" {
		t.Fatalf("Invoke: %v %q", err, pressed)
	}
	if err := d.Invoke(ctx, &core.Interaction{Command: "nope"}); err == nil {
		t.Fatal("Invoke unknown = nil")
	}
}

func TestFakeHolyArrsEvents(t *testing.T) {
	h := NewFakeHolyArrs()
	if h.Emit(core.Event{}) {
		t.Fatal("Emit with no stream = true")
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := h.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Emit(core.Event{Type: core.EventTitleAdded, TitleID: 7}) {
		t.Fatal("Emit = false")
	}
	if e := <-ch; e.TitleID != 7 {
		t.Fatalf("event = %+v", e)
	}
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("stream open after ctx end")
	}
	h.CloseEvents() // already closed: must not panic

	if _, err := h.Title(context.Background(), 1); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Title err = %v", err)
	}
	h.Set(func(h *FakeHolyArrs) {
		h.Titles[1] = core.Title{ID: 1, Name: "Dune"}
		h.PeopleVal = []core.PersonWatch{{UserID: "a", Plays: 3}}
		h.WatchedVal = []core.WatchedTitle{{Title: core.Title{ID: 1}}, {Title: core.Title{ID: 2}}}
		h.AskedByVal = map[core.TitleID][]core.UserID{1: {"a"}, 2: {"b"}}
	})
	if got, err := h.WatchPeople(context.Background()); err != nil || len(got) != 1 || got[0].Plays != 3 {
		t.Fatalf("WatchPeople = %v %v", got, err)
	}
	if got, err := h.RecentlyWatched(context.Background(), 1); err != nil || len(got) != 1 || got[0].Title.ID != 1 {
		t.Fatalf("RecentlyWatched = %v %v", got, err)
	}
	if got, err := h.AskedBy(context.Background(), []core.TitleID{1, 3}); err != nil || len(got) != 1 || got[1][0] != "a" {
		t.Fatalf("AskedBy = %v %v", got, err)
	}
	if got, err := h.Title(context.Background(), 1); err != nil || got.Name != "Dune" {
		t.Fatalf("Title = %v %v", got, err)
	}
}

func TestNewDepsAndConcurrency(t *testing.T) {
	deps, f := NewDeps(t)
	if !deps.Now().Equal(Now) || deps.Log == nil || deps.HA == nil || deps.DC == nil {
		t.Fatal("deps not wired")
	}
	ch := deps.Events.Subscribe("a")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			_, _ = deps.DC.Send(ctx, "c", core.Message{})
			_, _ = deps.HA.Users(ctx)
			_ = deps.Store.Save("d", 1)
			_ = deps.Links.Put(core.Link{DiscordID: "1", UserID: "a"})
			f.Events.Emit(core.Event{})
			f.HA.SetErr("Users", nil)
		}()
	}
	wg.Wait()
	if len(ch) != 8 || len(f.DC.CallsTo("Send")) != 8 {
		t.Fatalf("events %d, sends %d", len(ch), len(f.DC.CallsTo("Send")))
	}
}

func TestFakeHolyArrsHomeWidget(t *testing.T) {
	h := NewFakeHolyArrs()
	if _, ok := h.HomeWidget(); ok {
		t.Fatal("a widget before any call")
	}
	boom := errors.New("boom")
	h.SetErr("SetHomeWidget", boom)
	if err := h.SetHomeWidget(context.Background(), core.Card{Title: "x"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := h.HomeWidget(); ok {
		t.Fatal("a failed call set the widget")
	}
	h.SetErr("SetHomeWidget", nil)
	card := core.Card{Title: "Discord", Rows: []core.CardRow{{Text: "a", Link: "/settings/account"}}}
	if err := h.SetHomeWidget(context.Background(), card); err != nil {
		t.Fatal(err)
	}
	card.Rows[0].Text = "changed after the call"
	got, ok := h.HomeWidget()
	if !ok || got.Title != "Discord" || len(got.Rows) != 1 || got.Rows[0].Text != "a" {
		t.Fatalf("widget = %+v, %v", got, ok)
	}
	if n := len(h.CallsTo("SetHomeWidget")); n != 2 {
		t.Fatalf("recorded calls = %d", n)
	}
}
