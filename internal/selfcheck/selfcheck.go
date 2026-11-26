// Package selfcheck runs a read-only pass over everything the plugin depends
// on and logs one line per check, so the log of a real server shows what
// works. It logs counts only: never names, titles, ids, tokens or addresses
// with credentials.
package selfcheck

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

const (
	checkTimeout = 20 * time.Second
	sampleLimit  = 5 // rows asked of the list routes
	upcomingDays = 7 // calendar window
	searchQuery  = "matrix"
	maxErrLen    = 200

	errNotConfigured = "not configured"
	errTooOld        = "needs a newer HolyArrs"
)

// noCount marks a check that has no count to report.
const noCount = -1

type check struct {
	name string
	run  func(ctx context.Context) (n int, err error)
}

// Run does every check in turn, each with its own timeout, and logs one line
// per check and one summary line. It never panics and reports only through
// log. A nil discord func is logged as not configured. When ctx ends, the
// checks that have not started are dropped without a line.
func Run(ctx context.Context, ha core.HolyArrs, discord func(context.Context) error, log *slog.Logger) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	defer func() {
		if r := recover(); r != nil {
			log.Info("selfcheck", "check", "run", "ok", false, "err", "panic")
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}

	checks := []check{
		{"users", func(ctx context.Context) (int, error) { v, err := ha.Users(ctx); return len(v), err }},
		{"recently_added", func(ctx context.Context) (int, error) {
			v, err := ha.RecentlyAdded(ctx, sampleLimit)
			return len(v), err
		}},
		{"watch_people", func(ctx context.Context) (int, error) { v, err := ha.WatchPeople(ctx); return len(v), err }},
		{"recently_watched", func(ctx context.Context) (int, error) {
			v, err := ha.RecentlyWatched(ctx, sampleLimit)
			return len(v), err
		}},
		{"upcoming", func(ctx context.Context) (int, error) { v, err := ha.Upcoming(ctx, upcomingDays); return len(v), err }},
		{"search", func(ctx context.Context) (int, error) { v, err := ha.Search(ctx, searchQuery); return len(v), err }},
		{"links", func(ctx context.Context) (int, error) { v, err := ha.Links(ctx); return len(v), err }},
	}
	if ha == nil {
		for i := range checks {
			checks[i].run = func(context.Context) (int, error) { return noCount, errors.New(errNotConfigured) }
		}
	}
	if discord == nil {
		checks = append(checks, check{"discord", func(context.Context) (int, error) { return noCount, errors.New(errNotConfigured) }})
	} else {
		checks = append(checks, check{"discord", func(ctx context.Context) (int, error) { return noCount, discord(ctx) }})
	}

	passed, failed := 0, 0
	for _, c := range checks {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		n, err := runOne(ctx, c)
		ms := time.Since(start).Milliseconds()

		attrs := []any{"check", c.name, "ok", err == nil}
		if err == nil && n != noCount {
			attrs = append(attrs, "n", n)
		}
		attrs = append(attrs, "ms", ms)
		if err != nil {
			failed++
			attrs = append(attrs, "err", errText(err))
		} else {
			passed++
		}
		log.Info("selfcheck", attrs...)
	}
	log.Info("selfcheck done", "passed", passed, "failed", failed)
}

func runOne(ctx context.Context, c check) (n int, err error) {
	defer func() {
		if r := recover(); r != nil {
			n, err = noCount, errors.New("panic")
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	return c.run(ctx)
}

// credentials matches anything glued to the front of an "@": the user and
// password part of an address, with or without a scheme before it.
var credentials = regexp.MustCompile(`[^\s"'/]*@`)

// errText is the error as it may be logged: a too-old server gets fixed
// wording, request addresses are dropped, credentials are cut out, and the
// text is one short line.
func errText(err error) string {
	if errors.Is(err, core.ErrUnsupported) {
		return errTooOld
	}
	s := dropURL(err)
	s = credentials.ReplaceAllString(s, "[redacted]@")
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.ToValidUTF8(s, "")
	if len(s) > maxErrLen {
		s = strings.ToValidUTF8(s[:maxErrLen], "")
	}
	return s
}

// dropURL is err's text without the address a *url.Error carries.
func dropURL(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		full, inner := err.Error(), ue.Error()
		short := fmt.Sprintf("%s: %s", ue.Op, dropURL(ue.Err))
		if i := strings.Index(full, inner); i >= 0 {
			return full[:i] + short + full[i+len(inner):]
		}
		return short
	}
	return err.Error()
}
