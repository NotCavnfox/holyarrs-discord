package linking

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
	"github.com/NotCavnfox/holyarrs-discord/internal/coretest"
)

func TestHomeCard(t *testing.T) {
	join := core.CardRow{Text: "Join the server", Sub: "https://discord.gg/abc123"}
	link := core.CardRow{Text: "Link your account", Sub: "Run /link in Discord, then type the code under My account, Linked plugins", Link: "/settings/account"}
	for _, tc := range []struct {
		name   string
		invite string
		linked int
		want   []core.CardRow
	}{
		{"three links", "https://discord.gg/abc123", 3, []core.CardRow{join, link, {Text: "3 members linked"}}},
		{"one link", "https://discord.gg/abc123", 1, []core.CardRow{join, link, {Text: "1 member linked"}}},
		{"no links", "https://discord.gg/abc123", 0, []core.CardRow{join, link}},
		{"no invite", "", 2, []core.CardRow{link, {Text: "2 members linked"}}},
		{"invite not a link", "discord.gg/abc123", 0, []core.CardRow{link}},
		{"invite with markup", "https://discord.gg/<abc>", 0, []core.CardRow{link}},
		{"invite too long", "https://discord.gg/" + strings.Repeat("a", 200), 0, []core.CardRow{link}},
	} {
		got := homeCard(tc.invite, tc.linked)
		if got.Title != "Discord" || !reflect.DeepEqual(got.Rows, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
		// HolyArrs refuses a card outside these limits.
		if len(got.Rows) > 20 {
			t.Errorf("%s: %d rows", tc.name, len(got.Rows))
		}
		for _, row := range got.Rows {
			for _, s := range []string{row.Text, row.Sub} {
				if utf8.RuneCountInString(s) > 200 || strings.ContainsAny(s, "<>") {
					t.Errorf("%s: row text %q", tc.name, s)
				}
			}
			if row.Link != "" && row.Link != "/settings/account" {
				t.Errorf("%s: link %q", tc.name, row.Link)
			}
		}
	}
}

// widgetRig is a rig with the Home widget turned on.
func widgetRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	r.deps.Cfg.EnableHomeWidget = true
	r.restart()
	return r
}

// cards lists every card HolyArrs accepted or refused, in order.
func (r *rig) cards() []core.Card {
	var out []core.Card
	for _, c := range r.ha.CallsTo("SetHomeWidget") {
		out = append(out, c.Args[0].(core.Card))
	}
	return out
}

func lastRow(c core.Card) string { return c.Rows[len(c.Rows)-1].Text }

func TestRunPublishesHomeWidget(t *testing.T) {
	r := widgetRig(t)
	r.remote(core.RemoteLink{UserID: "7", External: dA})
	start(t, r, time.Millisecond, time.Millisecond)

	coretest.Eventually(t, "the first card", func() bool { return len(r.cards()) >= 1 })
	if want := homeCard("https://discord.gg/abc", 1); !reflect.DeepEqual(r.cards()[0], want) {
		t.Fatalf("first card\n got %+v\nwant %+v", r.cards()[0], want)
	}

	// Unchanged link count: nothing is sent again, however many passes run.
	time.Sleep(30 * time.Millisecond)
	if n := len(r.cards()); n != 1 {
		t.Fatalf("card published %d times with no change", n)
	}

	r.remote(core.RemoteLink{UserID: "7", External: dA}, core.RemoteLink{UserID: "9", External: dB}, core.RemoteLink{UserID: "5", External: dC})
	coretest.Eventually(t, "the card to count three", func() bool {
		c, _ := r.ha.HomeWidget()
		return lastRow(c) == "3 members linked"
	})

	r.remote()
	coretest.Eventually(t, "the card to stop counting", func() bool {
		c, _ := r.ha.HomeWidget()
		return lastRow(c) == "Link your account"
	})
}

func TestUnlinkUpdatesHomeWidgetWithoutWaiting(t *testing.T) {
	r := widgetRig(t)
	r.put(t, core.Link{DiscordID: dA, UserID: "7", UserName: "Kim_*"})
	r.remote(core.RemoteLink{UserID: "7", External: dA})
	start(t, r, time.Hour, time.Hour)
	coretest.Eventually(t, "the first card", func() bool {
		c, ok := r.ha.HomeWidget()
		return ok && lastRow(c) == "1 member linked"
	})
	r.cmd(t, "unlink", dA)
	coretest.Eventually(t, "the card after /unlink", func() bool {
		c, _ := r.ha.HomeWidget()
		return lastRow(c) == "Link your account"
	})
	if n := len(r.ha.CallsTo("Links")); n != 1 {
		t.Errorf("Links read %d times, want only the pass at start", n)
	}
}

func TestRunRetriesHomeWidget(t *testing.T) {
	r := widgetRig(t)
	r.ha.SetErr("SetHomeWidget", errors.New("widget route down"))
	start(t, r, time.Hour, time.Hour) // only the widget's own retry wait drives this
	coretest.Eventually(t, "three failed tries", func() bool { return len(r.cards()) >= 3 })
	if _, ok := r.ha.HomeWidget(); ok {
		t.Fatal("a card was published while the route was down")
	}
	r.ha.SetErr("SetHomeWidget", nil)
	coretest.Eventually(t, "the card", func() bool { _, ok := r.ha.HomeWidget(); return ok })
	c, _ := r.ha.HomeWidget()
	if want := homeCard("https://discord.gg/abc", 0); !reflect.DeepEqual(c, want) {
		t.Fatalf("card after retries\n got %+v\nwant %+v", c, want)
	}
	if n := len(r.ha.CallsTo("Links")); n != 1 {
		t.Errorf("widget retries read the links %d times, want 1", n)
	}
}

func TestRunRepublishesHomeWidgetOnRestart(t *testing.T) {
	r := widgetRig(t)
	stop := start(t, r, time.Hour, time.Hour)
	coretest.Eventually(t, "the first card", func() bool { return len(r.cards()) == 1 })
	stop()
	r.restart() // a new process: HolyArrs forgot the card
	start(t, r, time.Hour, time.Hour)
	coretest.Eventually(t, "the card after a restart", func() bool { return len(r.cards()) == 2 })
}

func TestSyncWidgetBacksOff(t *testing.T) {
	r := widgetRig(t)
	r.ha.SetErr("SetHomeWidget", errors.New("widget route down"))
	f := r.f
	f.WidgetRetry = time.Minute
	for range 5 {
		f.syncWidget(context.Background())
	}
	if calls := len(r.cards()); calls != 1 {
		t.Fatalf("calls inside the backoff wait = %d, want 1", calls)
	}
	if f.widgetWait != 2*time.Minute {
		t.Errorf("next wait = %v, want doubled", f.widgetWait)
	}
	f.widgetWait, f.widgetAfter = 4*time.Minute, time.Time{}
	f.syncWidget(context.Background())
	if f.widgetWait != widgetMaxRetry {
		t.Errorf("wait = %v, want capped at %v", f.widgetWait, widgetMaxRetry)
	}
}

func TestHomeWidgetOff(t *testing.T) {
	r := newRig(t) // EnableHomeWidget is false in the rig
	stop := start(t, r, time.Millisecond, time.Millisecond)
	coretest.Eventually(t, "a few passes", func() bool { return len(r.ha.CallsTo("Links")) >= 5 })
	stop()
	if n := len(r.cards()); n != 0 {
		t.Fatalf("widget published %d times while turned off", n)
	}
}
