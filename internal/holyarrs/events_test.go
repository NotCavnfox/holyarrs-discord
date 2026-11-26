package holyarrs

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

func recv(t *testing.T, ch <-chan core.Event) core.Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("the channel closed early")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event arrived")
	}
	return core.Event{}
}

func closed(t *testing.T, ch <-chan core.Event) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if ok {
			t.Fatalf("got %+v, want a closed channel", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the channel did not close")
	}
}

func TestEvents(t *testing.T) {
	pr, pw := io.Pipe()
	var closedCalls atomic.Int32
	c := New("http://127.0.0.1:1", "x", pr, func() { closedCalls.Add(1) }, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"event":"library.add","id":4182,"delivery":"00ff","at":"2026-11-25T09:00:00Z","payload":{"moment":"library.add","event_kind":"title_added","subject":"s","text":"t","at":"2026-11-25T08:59:59Z","movie":{"id":12,"name":"Heat","year":1995,"tmdb":"949"}}}`,
		`not json at all`,
		``,
		`{"event":"library.add","payload":{"test":true,"movie":{"id":99}}}`,
		`{"event":"request.made","id":5,"at":"2026-11-25T09:01:00Z","payload":{"moment":"request.made","event_kind":"ask_made","at":"2026-11-25T09:01:00Z","data":{"ask_id":31,"tmdb_id":"70523"}}}`,
		`{"event":"request.approved","payload":{"at":"2026-11-25T09:02:00Z","show":{"id":9,"name":"Dark"},"data":{"ask_id":"31"}}}`,
		`{"event":"request.declined","payload":{"data":{"ask_id":32}}}`,
		`{"event":"request.available","payload":{"data":{"ask_id":31,"title_id":9}}}`,
		`{"event":"x","payload":"` + strings.Repeat("a", maxLine+10) + `"}`,
		`{"event":"big","payload":{"data":{"pad":"` + strings.Repeat("b", maxLine-200) + `"}}}`,
		`{"event":"missed","payload":{"after":4000}}`,
		`{"event":"download.done","payload":{"artist":{"id":4,"name":"Low"},"data":{"path":"x"}}}`,
	}
	go func() {
		for _, l := range lines {
			_, _ = io.WriteString(pw, l+"\n")
		}
	}()

	ev := recv(t, ch)
	if ev.Type != core.EventTitleAdded || ev.WireType != "library.add" || ev.TitleID != 12 || ev.RequestID != "" ||
		!ev.At.Equal(time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("library.add %+v", ev)
	}
	ev = recv(t, ch)
	if ev.Type != core.EventRequestCreated || ev.RequestID != "31" || ev.TitleID != 0 {
		t.Errorf("request.made %+v", ev)
	}
	ev = recv(t, ch)
	if ev.Type != core.EventRequestApproved || ev.RequestID != "31" || ev.TitleID != 9 || ev.At.IsZero() {
		t.Errorf("request.approved %+v", ev)
	}
	if ev = recv(t, ch); ev.Type != core.EventRequestDeclined || ev.RequestID != "32" {
		t.Errorf("request.declined %+v", ev)
	}
	if ev = recv(t, ch); ev.Type != core.EventRequestAvailable || ev.TitleID != 9 || ev.RequestID != "31" {
		t.Errorf("request.available %+v", ev)
	}
	if ev = recv(t, ch); ev.Type != core.EventOther || ev.WireType != "big" {
		t.Errorf("big line: type %s wire %s", ev.Type, ev.WireType)
	}
	if ev = recv(t, ch); ev.Type != core.EventOther || ev.WireType != "missed" {
		t.Errorf("missed %+v", ev)
	}
	if ev = recv(t, ch); ev.Type != core.EventOther || ev.WireType != "download.done" || ev.TitleID != 4 {
		t.Errorf("download.done %+v", ev)
	}

	// A second call takes over: the first channel closes, the new one is fed.
	ch2, _ := c.Events(ctx)
	closed(t, ch)
	go func() { _, _ = io.WriteString(pw, `{"event":"library.add","payload":{"show":{"id":"7"}}}`+"\n") }()
	if ev = recv(t, ch2); ev.TitleID != 7 {
		t.Errorf("second channel %+v", ev)
	}

	// A channel ends with its context; stdin is still read.
	ctx3, cancel3 := context.WithCancel(context.Background())
	ch3, _ := c.Events(ctx3)
	closed(t, ch2)
	cancel3()
	closed(t, ch3)
	if closedCalls.Load() != 0 {
		t.Fatal("onStdinClosed ran before stdin ended")
	}

	// EOF: the callback runs once and the current channel closes.
	ch4, _ := c.Events(ctx)
	go func() {
		_, _ = io.WriteString(pw, `{"event":"library.add","payload":{"movie":{"id":1}}}`) // last line, no newline
		_ = pw.Close()
	}()
	if ev = recv(t, ch4); ev.TitleID != 1 {
		t.Errorf("last line %+v", ev)
	}
	closed(t, ch4)
	if n := closedCalls.Load(); n != 1 {
		t.Fatalf("onStdinClosed ran %d times", n)
	}
	ch5, err := c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	closed(t, ch5)
	if n := closedCalls.Load(); n != 1 {
		t.Fatalf("onStdinClosed ran %d times after a later call", n)
	}
}

func TestEventsEOFBeforeAnyLine(t *testing.T) {
	done := make(chan struct{})
	c := New("http://127.0.0.1:1", "x", strings.NewReader(""), func() { close(done) }, nil)
	ch, _ := c.Events(context.Background())
	closed(t, ch)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("onStdinClosed did not run")
	}
}

func TestEventsWithoutStdin(t *testing.T) {
	c := New("http://127.0.0.1:1", "x", nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	closed(t, ch)
}

func TestSlowReaderDoesNotBlockTakeover(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 200; i++ { // more than the channel holds
		fmt.Fprintf(&sb, `{"event":"library.add","payload":{"movie":{"id":%d}}}`+"\n", i+1)
	}
	pr, pw := io.Pipe()
	c := New("http://127.0.0.1:1", "x", pr, nil, nil)
	ch, _ := c.Events(context.Background())
	go func() { _, _ = io.WriteString(pw, sb.String()) }()
	recv(t, ch) // the reader is running and will block on the full channel
	ch2, _ := c.Events(context.Background())
	n := 0
	for range ch { // drains what was buffered, then sees the close
		n++
	}
	if n > 64 {
		t.Errorf("drained %d events from a channel of 64", n)
	}
	recv(t, ch2)
	_ = pw.Close()
}
