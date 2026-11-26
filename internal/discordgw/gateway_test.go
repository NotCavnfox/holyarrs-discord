package discordgw

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

const testToken = "tkn-do-not-print"

type notice struct {
	in   *core.Interaction
	text string
}

// testGateway is a gateway that never connects: routing is wired by hand
// and failure notices are captured instead of sent.
func testGateway(t *testing.T, cmds []core.Command, router core.InteractionHandler) (*Gateway, chan notice, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	g, err := New(testToken, "g1", "admins", slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	routes, err := buildRoutes(cmds)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	g.base, g.cancel, g.routes, g.router = ctx, cancel, routes, router
	notices := make(chan notice, 4)
	g.notify = func(in *core.Interaction, text string) { notices <- notice{in, text} }
	return g, notices, logs
}

func wait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
		panic("unreachable")
	}
}

func none[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected value: %+v", v)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestNew(t *testing.T) {
	if _, err := New("", "g", "", nil); err == nil {
		t.Error("empty token must fail")
	}
	if _, err := New("  Bot  ", "g", "", nil); err == nil {
		t.Error("blank token must fail")
	}
	_, err := New(testToken, "", "", nil)
	if err == nil {
		t.Error("empty guild must fail")
	} else if strings.Contains(err.Error(), testToken) {
		t.Error("token in error")
	}
	g, err := New("Bot "+testToken, "g", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if g.s.Token != "Bot "+testToken {
		t.Error("token must carry exactly one Bot prefix")
	}
	if g.s.Identify.Intents != discordgo.IntentsGuilds {
		t.Errorf("intents: %v", g.s.Identify.Intents)
	}
	// The proxy-aware defaults must stay in place.
	if g.s.Client.Transport != nil {
		t.Error("HTTP client must use the default transport (proxy from environment)")
	}
	if g.s.Dialer == nil || g.s.Dialer.Proxy == nil {
		t.Error("websocket dialer must keep its environment proxy")
	}
}

func TestCloseTwiceAndBeforeOpen(t *testing.T) {
	g, err := New(testToken, "g", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Errorf("first close: %v", err)
	}
	if err := g.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
	if err := g.Open(context.Background(), nil, nil); err == nil {
		t.Error("open after close must fail")
	}
}

func TestOpenRejectsBadCommandsWithoutConnecting(t *testing.T) {
	g, err := New(testToken, "g", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Open(context.Background(), []core.Command{{Name: "nohandler"}}, nil); err == nil {
		t.Error("a command without a handler must fail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.Open(ctx, nil, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled open: %v", err)
	}
}

func TestDispatchCommand(t *testing.T) {
	got := make(chan *core.Interaction, 1)
	var deadline time.Duration
	g, notices, _ := testGateway(t, []core.Command{{
		Name: "purge-vote",
		Sub: []core.Command{{Name: "start", AdminOnly: true, Handle: func(ctx context.Context, in *core.Interaction) error {
			if d, ok := ctx.Deadline(); ok {
				deadline = time.Until(d)
			}
			got <- in
			return nil
		}}},
	}}, nil)

	g.dispatch(decode(t, subJSON))
	in := wait(t, got)
	if in.Command != "purge-vote start" || in.Options["days"] != "30" {
		t.Errorf("handler got %+v", in)
	}
	if deadline < 13*time.Minute || deadline > handlerTimeout {
		t.Errorf("handler deadline: %v", deadline)
	}
	g.wg.Wait()
	none(t, notices)
}

func TestDispatchAdminRefusal(t *testing.T) {
	called := make(chan struct{}, 1)
	g, notices, _ := testGateway(t, []core.Command{{
		Name: "request", AdminOnly: true,
		Handle: func(context.Context, *core.Interaction) error { called <- struct{}{}; return nil },
	}}, nil)

	g.dispatch(decode(t, slashJSON)) // caller has neither the role nor the permission
	n := wait(t, notices)
	if n.text != refuseText || n.in.UserID != "u1" {
		t.Errorf("refusal: %+v", n)
	}
	g.wg.Wait()
	none(t, called)
}

func TestDispatchErrorAndPanic(t *testing.T) {
	g, notices, logs := testGateway(t, []core.Command{
		{Name: "request", Handle: func(context.Context, *core.Interaction) error { return errors.New("boom") }},
		{Name: "purge-vote", Sub: []core.Command{{Name: "start", Handle: func(context.Context, *core.Interaction) error {
			panic("kaboom")
		}}}},
	}, nil)

	g.dispatch(decode(t, slashJSON))
	if n := wait(t, notices); n.text != failText {
		t.Errorf("error notice: %+v", n)
	}
	g.dispatch(decode(t, subJSON))
	if n := wait(t, notices); n.text != failText {
		t.Errorf("panic notice: %+v", n)
	}
	g.wg.Wait()
	out := logs.String()
	if !strings.Contains(out, "boom") || !strings.Contains(out, "kaboom") {
		t.Errorf("failures must be logged: %s", out)
	}
	if strings.Contains(out, testToken) || strings.Contains(out, "tok2") {
		t.Errorf("a token reached the log: %s", out)
	}
}

func TestDispatchUnknownCommand(t *testing.T) {
	g, notices, _ := testGateway(t, nil, nil)
	g.dispatch(decode(t, slashJSON))
	if n := wait(t, notices); n.text != failText {
		t.Errorf("notice: %+v", n)
	}
}

func TestDispatchComponent(t *testing.T) {
	got := make(chan *core.Interaction, 1)
	g, notices, _ := testGateway(t, []core.Command{{Name: "request", Handle: noop}},
		func(_ context.Context, in *core.Interaction) error { got <- in; return nil })

	g.dispatch(decode(t, buttonJSON))
	in := wait(t, got)
	if in.CustomID != "vote:yes:7" || in.MessageID != "m9" || !in.IsAdmin {
		t.Errorf("router got %+v", in)
	}
	g.wg.Wait()
	none(t, notices)

	// No router: dropped, no panic.
	g.router = nil
	g.dispatch(decode(t, buttonJSON))
	none(t, notices)
}

func TestDispatchIgnoresOtherKindsAndStopsAfterCancel(t *testing.T) {
	called := make(chan struct{}, 1)
	g, notices, _ := testGateway(t, []core.Command{{
		Name: "request", Handle: func(context.Context, *core.Interaction) error { called <- struct{}{}; return nil },
	}}, nil)

	g.dispatch(&discordgo.Interaction{Type: discordgo.InteractionApplicationCommandAutocomplete})
	g.dispatch(&discordgo.Interaction{Type: discordgo.InteractionPing})
	none(t, called)

	g.cancel()
	g.dispatch(decode(t, slashJSON))
	none(t, called)
	none(t, notices)
}

func TestInteractionGuards(t *testing.T) {
	g, err := New(testToken, "g", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := g.Respond(ctx, nil, core.Message{}, false); err == nil {
		t.Error("nil interaction must fail")
	}
	if err := g.Defer(ctx, &core.Interaction{ID: "1"}, false); err == nil {
		t.Error("interaction without a token must fail")
	}
	if err := g.Update(ctx, &core.Interaction{Token: "t"}, core.Message{}); err == nil {
		t.Error("interaction without an id must fail")
	}
	if err := g.FollowUp(ctx, &core.Interaction{ID: "1", Token: "t"}, core.Message{}, false); err == nil {
		t.Error("follow-up before Open must fail")
	}
}
