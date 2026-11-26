package linking

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

const (
	// accountPath is My account in HolyArrs, where a link code is typed.
	// Card links must be a path inside HolyArrs; this form passes its check.
	accountPath = "/settings/account"
	// widgetTextMax is HolyArrs' limit for one card text, in characters.
	widgetTextMax  = 200
	widgetMaxRetry = 5 * time.Minute
)

// homeCard is the Discord card on the HolyArrs Home page. Plugin pages open
// for the owner only; this card is what every other signed-in user sees.
func homeCard(inviteURL string, linked int) core.Card {
	c := core.Card{Title: "Discord"}
	// The card cannot link outside HolyArrs, so the invite is text to copy. A
	// link that would be cut or refused is left out rather than shown broken.
	if u := safeInvite(inviteURL); u != "" && utf8.RuneCountInString(u) <= widgetTextMax && !strings.ContainsAny(u, "<>") {
		c.Rows = append(c.Rows, core.CardRow{Text: "Join the server", Sub: u})
	}
	c.Rows = append(c.Rows, core.CardRow{
		Text: "Link your account",
		Sub:  "Run /link in Discord, then type the code under My account, Linked plugins",
		Link: accountPath,
	})
	switch {
	case linked == 1:
		c.Rows = append(c.Rows, core.CardRow{Text: "1 member linked"})
	case linked > 1:
		c.Rows = append(c.Rows, core.CardRow{Text: fmt.Sprintf("%d members linked", linked)})
	}
	return c
}

// syncWidget publishes the Home card when it is not up yet or the number of
// links changed. HolyArrs keeps the card until the plugin stops, so there is
// no keep-alive. A failure is logged and tried again after a growing wait.
func (f *Feature) syncWidget(ctx context.Context) {
	if !f.d.Cfg.EnableHomeWidget || ctx.Err() != nil {
		return
	}
	n := len(f.d.Links.All())
	if n == f.widgetSent || time.Now().Before(f.widgetAfter) {
		return
	}
	if err := f.d.HA.SetHomeWidget(ctx, homeCard(f.d.Cfg.InviteURL, n)); err != nil {
		if ctx.Err() != nil {
			return
		}
		if f.widgetWait <= 0 {
			f.widgetWait = f.WidgetRetry
		}
		f.d.Log.Warn("linking: home widget not published, retrying", "err", err, "in", f.widgetWait)
		f.widgetAfter = time.Now().Add(f.widgetWait)
		f.widgetWait = min(f.widgetWait*2, widgetMaxRetry)
		return
	}
	f.widgetSent, f.widgetWait, f.widgetAfter = n, 0, time.Time{}
}
