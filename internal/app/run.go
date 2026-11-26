package app

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

// Timing holds the runner's durations. Zero fields take the defaults.
type Timing struct {
	MinBackoff   time.Duration // first restart delay (1s)
	MaxBackoff   time.Duration // cap for the doubling delay (2m)
	HealthyAfter time.Duration // a run this long resets the delay (5m)
	ShutdownWait time.Duration // how long to wait for features on shutdown (10s)
}

// DefaultTiming is what Run uses.
func DefaultTiming() Timing {
	return Timing{
		MinBackoff:   time.Second,
		MaxBackoff:   2 * time.Minute,
		HealthyAfter: 5 * time.Minute,
		ShutdownWait: 10 * time.Second,
	}
}

func (t Timing) withDefaults() Timing {
	d := DefaultTiming()
	if t.MinBackoff <= 0 {
		t.MinBackoff = d.MinBackoff
	}
	if t.MaxBackoff <= 0 {
		t.MaxBackoff = d.MaxBackoff
	}
	if t.MaxBackoff < t.MinBackoff {
		t.MaxBackoff = t.MinBackoff
	}
	if t.HealthyAfter <= 0 {
		t.HealthyAfter = d.HealthyAfter
	}
	if t.ShutdownWait <= 0 {
		t.ShutdownWait = d.ShutdownWait
	}
	return t
}

// backoff is a doubling delay between min and max.
type backoff struct {
	min, max, cur time.Duration
}

func newBackoff(t Timing) *backoff { return &backoff{min: t.MinBackoff, max: t.MaxBackoff} }

// next returns the delay to wait now and doubles the one after it.
func (b *backoff) next() time.Duration {
	d := b.cur
	if d < b.min {
		d = b.min
	}
	if d > b.max {
		d = b.max
	}
	b.cur = d * 2
	if b.cur > b.max {
		b.cur = b.max
	}
	return d
}

func (b *backoff) reset() { b.cur = 0 }

// Run runs the bot until ctx ends, with the default timing. See RunWith.
func Run(ctx context.Context, deps core.Deps, bus *Bus, features []core.Feature) error {
	return RunWith(ctx, deps, bus, features, DefaultTiming())
}

// RunWith opens Discord with every feature's commands, pumps HolyArrs events
// into the bus and runs each feature in its own goroutine. Discord not
// opening (a network or DNS blip at start, a bad token) is retried with
// backoff until ctx ends: a plugin that exits is marked as stopped on its own
// and is not started again. A feature that returns early or panics is logged
// and restarted with backoff; it never stops the others. When ctx ends it
// waits up to ShutdownWait for the features, closes the bus and Discord and
// returns nil. It returns an error only for a wiring mistake.
func RunWith(ctx context.Context, deps core.Deps, bus *Bus, features []core.Feature, timing Timing) error {
	timing = timing.withDefaults()
	log := orDiscard(deps.Log)
	if bus == nil {
		return fmt.Errorf("app: nil bus")
	}
	if deps.DC == nil || deps.HA == nil {
		bus.Close()
		return fmt.Errorf("app: Discord and HolyArrs ports are required")
	}

	router := NewRouter(features, log)
	cmds, err := router.Commands()
	if err != nil {
		bus.Close()
		return err
	}

	// The pump starts first: it is what reads stdin, and stdin closing is how
	// HolyArrs says stop, also while Discord is still being opened. Every
	// feature subscribed in its constructor, so events wait in the bus.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		pumpEvents(ctx, deps.HA, bus, log, timing)
	}()
	if openDiscord(ctx, deps.DC, cmds, router.Handle, log, timing) {
		for _, f := range features {
			wg.Add(1)
			go func(f core.Feature) {
				defer wg.Done()
				supervise(ctx, f, log, timing)
			}(f)
		}
	}

	<-ctx.Done()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(timing.ShutdownWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		log.Warn("features did not stop in time, shutting down anyway", "waited", timing.ShutdownWait)
	}
	bus.Close()
	if err := deps.DC.Close(); err != nil {
		log.Warn("closing Discord failed", "err", err)
	}
	return nil
}

// openDiscord opens Discord, trying again with backoff until it works or ctx
// ends. It reports whether Discord is open. Each failed attempt is logged
// once; the adapter keeps the token out of its errors.
func openDiscord(ctx context.Context, dc core.Discord, cmds []core.Command, router core.InteractionHandler, log *slog.Logger, timing Timing) bool {
	bo := newBackoff(timing)
	for attempt := 1; ctx.Err() == nil; attempt++ {
		err := dc.Open(ctx, cmds, router)
		if err == nil {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		delay := bo.next()
		log.Error("Discord did not open, retrying", "attempt", attempt, "err", err, "in", delay)
		if !sleep(ctx, delay) {
			return false
		}
	}
	return false
}

// pumpEvents copies HolyArrs events into the bus until ctx ends, opening the
// stream again with backoff whenever it fails or closes early.
func pumpEvents(ctx context.Context, ha core.HolyArrs, bus *Bus, log *slog.Logger, timing Timing) {
	bo := newBackoff(timing)
	for ctx.Err() == nil {
		start := time.Now()
		err := pumpOnce(ctx, ha, bus)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) >= timing.HealthyAfter {
			bo.reset()
		}
		delay := bo.next()
		if err != nil {
			log.Warn("event stream failed, retrying", "err", err, "in", delay)
		} else {
			log.Warn("event stream closed, reconnecting", "in", delay)
		}
		if !sleep(ctx, delay) {
			return
		}
	}
}

func pumpOnce(ctx context.Context, ha core.HolyArrs, bus *Bus) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in event stream: %v", r)
		}
	}()
	ch, err := ha.Events(ctx)
	if err != nil {
		return err
	}
	if ch == nil {
		return fmt.Errorf("event stream is nil")
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-ch:
			if !ok {
				return nil
			}
			bus.Publish(e)
		}
	}
}

// supervise keeps one feature running until ctx ends.
func supervise(ctx context.Context, f core.Feature, log *slog.Logger, timing Timing) {
	log = log.With("feature", f.Name())
	bo := newBackoff(timing)
	for ctx.Err() == nil {
		start := time.Now()
		err := runFeature(ctx, f)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) >= timing.HealthyAfter {
			bo.reset()
		}
		delay := bo.next()
		if err != nil {
			log.Error("feature stopped, restarting", "err", err, "in", delay)
		} else {
			log.Error("feature returned before shutdown, restarting", "in", delay)
		}
		if !sleep(ctx, delay) {
			return
		}
	}
}

func runFeature(ctx context.Context, f core.Feature) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	return f.Run(ctx)
}

// sleep waits for d and reports false when ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
