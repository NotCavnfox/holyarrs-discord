package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
	"github.com/NotCavnfox/holyarrs-discord/internal/coretest"
)

var _ core.Events = (*Bus)(nil)

// ---- helpers ----

// logCounter is a slog handler that counts records by level and message.
type logCounter struct {
	mu   sync.Mutex
	msgs []string
}

func (h *logCounter) Enabled(context.Context, slog.Level) bool { return true }
func (h *logCounter) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Level.String()+" "+r.Message)
	return nil
}
func (h *logCounter) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *logCounter) WithGroup(string) slog.Handler      { return h }
func (h *logCounter) count(prefix string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if strings.HasPrefix(m, prefix) {
			n++
		}
	}
	return n
}

type feature struct {
	name string
	cmds []core.Command
	run  func(ctx context.Context) error
	runs atomic.Int32
}

func (f *feature) Name() string             { return f.name }
func (f *feature) Commands() []core.Command { return f.cmds }
func (f *feature) Run(ctx context.Context) error {
	f.runs.Add(1)
	if f.run == nil {
		<-ctx.Done()
		return nil
	}
	return f.run(ctx)
}

type buttonFeature struct {
	feature
	got []string
	err error
}

func (f *buttonFeature) HandleComponent(_ context.Context, in *core.Interaction) error {
	f.got = append(f.got, in.CustomID)
	return f.err
}

func cmd(names ...string) []core.Command {
	out := make([]core.Command, len(names))
	for i, n := range names {
		out[i] = core.Command{Name: n}
	}
	return out
}

var fast = Timing{MinBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond, HealthyAfter: time.Hour, ShutdownWait: 2 * time.Second}

// start runs RunWith in the background and returns cancel and the result.
func start(t *testing.T, deps core.Deps, bus *Bus, features []core.Feature, timing Timing) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	res := make(chan error, 1)
	go func() { res <- RunWith(ctx, deps, bus, features, timing) }()
	return cancel, res
}

func wait(t *testing.T, res <-chan error) error {
	t.Helper()
	select {
	case err := <-res:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

// ---- Bus ----

func TestBusFanOut(t *testing.T) {
	b := NewBus(nil)
	a, c := b.Subscribe("a"), b.Subscribe("c")
	if b.Subscribe("a") != a {
		t.Fatal("same name gave a different channel")
	}
	if cap(a) != SubscriberBuffer {
		t.Fatalf("buffer = %d", cap(a))
	}
	for i := 1; i <= 3; i++ {
		b.Publish(core.Event{TitleID: core.TitleID(i)})
	}
	for _, ch := range []<-chan core.Event{a, c} {
		for i := 1; i <= 3; i++ {
			if e := <-ch; e.TitleID != core.TitleID(i) {
				t.Fatalf("got %d, want %d", e.TitleID, i)
			}
		}
	}
}

func TestBusDropsAndLogsOncePerBurst(t *testing.T) {
	tests := []struct {
		name      string
		bursts    int // times the subscriber fills up and then drains
		overflow  int // events lost per burst
		wantWarns int
	}{
		{"no overflow", 1, 0, 0},
		{"one burst", 1, 10, 1},
		{"one long burst", 1, 500, 1},
		{"three bursts", 3, 5, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logs := &logCounter{}
			b := NewBus(slog.New(logs))
			b.PublishWait = 0 // drop at once; the waiting is tested below
			slow, quick := b.Subscribe("slow"), b.Subscribe("quick")
			quickGot := 0
			for burst := 0; burst < tc.bursts; burst++ {
				done := make(chan struct{})
				go func() { // Publish must never block, however full "slow" is.
					defer close(done)
					for i := 0; i < SubscriberBuffer+tc.overflow; i++ {
						b.Publish(core.Event{})
						<-quick
						quickGot++
					}
				}()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("Publish blocked")
				}
				if len(slow) != SubscriberBuffer {
					t.Fatalf("slow holds %d, want %d", len(slow), SubscriberBuffer)
				}
				for len(slow) > 0 {
					<-slow
				}
				b.Publish(core.Event{}) // ends the burst
				<-slow
				<-quick
			}
			if got := logs.count("WARN"); got != tc.wantWarns {
				t.Fatalf("warnings = %d, want %d", got, tc.wantWarns)
			}
			if want := tc.bursts * (SubscriberBuffer + tc.overflow); quickGot != want {
				t.Fatalf("quick subscriber got %d, want %d", quickGot, want)
			}
		})
	}
}

func fill(b *Bus) {
	for i := 0; i < SubscriberBuffer; i++ {
		b.Publish(core.Event{TitleID: core.TitleID(i + 1)})
	}
}

func TestBusDefaults(t *testing.T) {
	b := NewBus(nil)
	if SubscriberBuffer != 256 || DefaultPublishWait != 2*time.Second || b.PublishWait != DefaultPublishWait {
		t.Fatalf("buffer %d, wait %v / %v", SubscriberBuffer, DefaultPublishWait, b.PublishWait)
	}
}

// A subscriber that is merely behind loses nothing: Publish waits for it.
func TestBusWaitsForAFullSubscriber(t *testing.T) {
	logs := &logCounter{}
	b := NewBus(slog.New(logs))
	b.PublishWait = 5 * time.Second
	slow := b.Subscribe("slow")
	fill(b)
	const extra = 300
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < extra; i++ {
			b.Publish(core.Event{TitleID: core.TitleID(SubscriberBuffer + i + 1)})
		}
	}()
	select {
	case <-done:
		t.Fatal("Publish did not wait for the full subscriber")
	case <-time.After(20 * time.Millisecond):
	}
	for i := 1; i <= SubscriberBuffer+extra; i++ {
		select {
		case e := <-slow:
			if e.TitleID != core.TitleID(i) {
				t.Fatalf("event %d arrived as %d", i, e.TitleID)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("event %d never arrived", i)
		}
	}
	<-done
	if n := logs.count("WARN"); n != 0 {
		t.Fatalf("warnings = %d, want none", n)
	}
}

// A stuck subscriber costs one bounded wait, then events for it are dropped
// at once until it has room again. The other subscribers get everything.
func TestBusDropsAfterTheWait(t *testing.T) {
	logs := &logCounter{}
	b := NewBus(slog.New(logs))
	b.PublishWait = 30 * time.Millisecond
	stuck, quick := b.Subscribe("stuck"), b.Subscribe("quick")
	got := 0
	publish := func() time.Duration {
		start := time.Now()
		b.Publish(core.Event{})
		<-quick
		got++
		return time.Since(start)
	}
	for i := 0; i < SubscriberBuffer; i++ {
		publish()
	}
	if d := publish(); d < b.PublishWait {
		t.Fatalf("first overflow returned after %v, want a wait of %v", d, b.PublishWait)
	}
	start := time.Now()
	for i := 0; i < 100; i++ {
		publish()
	}
	if d := time.Since(start); d > 100*b.PublishWait/2 {
		t.Fatalf("100 more drops took %v: every one waited", d)
	}
	if len(stuck) != SubscriberBuffer || got != SubscriberBuffer+101 {
		t.Fatalf("stuck holds %d, quick got %d", len(stuck), got)
	}
	if n := logs.count("WARN event subscriber is full"); n != 1 {
		t.Fatalf("warnings = %d, want 1", n)
	}
	<-stuck
	publish() // room again: delivered, burst over
	if n := logs.count("INFO event subscriber caught up"); n != 1 {
		t.Fatalf("caught-up logs = %d, want 1", n)
	}
}

// Close ends a Publish that is waiting, and nothing is sent on a closed channel.
func TestBusCloseEndsAWaitingPublish(t *testing.T) {
	b := NewBus(nil)
	b.PublishWait = time.Minute
	b.Subscribe("stuck")
	fill(b)
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Publish(core.Event{})
		b.Publish(core.Event{})
	}()
	time.Sleep(10 * time.Millisecond)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		b.Close()
	}()
	for _, c := range []chan struct{}{done, closed} {
		select {
		case <-c:
		case <-time.After(5 * time.Second):
			t.Fatal("Publish or Close hung")
		}
	}
	if _, ok := <-b.Subscribe("late"); ok {
		t.Fatal("late subscriber channel not closed")
	}
	b.Close()
}

// eventsAfterOpen hands back a stream that already holds events.
type eventsAfterOpen struct {
	core.HolyArrs
	asked    atomic.Int32
	backfill []core.Event
}

func (h *eventsAfterOpen) Events(ctx context.Context) (<-chan core.Event, error) {
	if h.asked.Add(1) == 1 {
		ch := make(chan core.Event, len(h.backfill))
		for _, e := range h.backfill {
			ch <- e
		}
		return ch, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// Events that are already waiting when the bot starts reach a feature that
// subscribed in its constructor, however late its Run gets going.
func TestRunLosesNoStartupEvents(t *testing.T) {
	deps, _ := coretest.NewDeps(t)
	const n = 200
	ha := &eventsAfterOpen{HolyArrs: deps.HA}
	for i := 1; i <= n; i++ {
		ha.backfill = append(ha.backfill, core.Event{Type: core.EventTitleAdded, TitleID: core.TitleID(i)})
	}
	deps.HA = ha
	bus := NewBus(deps.Log)
	sub := bus.Subscribe("late") // what a feature's New does
	got := make(chan core.TitleID, n)
	feats := []core.Feature{&feature{name: "late", run: func(ctx context.Context) error {
		select { // a feature that is slow to reach its receive loop
		case <-ctx.Done():
			return nil
		case <-time.After(50 * time.Millisecond):
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			case e := <-sub:
				got <- e.TitleID
			}
		}
	}}}
	cancel, res := start(t, deps, bus, feats, fast)
	for i := 1; i <= n; i++ {
		select {
		case id := <-got:
			if id != core.TitleID(i) {
				t.Fatalf("event %d arrived as %d", i, id)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("event %d was lost", i)
		}
	}
	cancel()
	if err := wait(t, res); err != nil {
		t.Fatal(err)
	}
}

func TestBusClose(t *testing.T) {
	b := NewBus(nil)
	a := b.Subscribe("a")
	b.Publish(core.Event{TitleID: 1})
	b.Close()
	b.Close()               // twice is fine
	b.Publish(core.Event{}) // no panic, no delivery
	if e, ok := <-a; !ok || e.TitleID != 1 {
		t.Fatal("buffered event lost on close")
	}
	if _, ok := <-a; ok {
		t.Fatal("channel not closed")
	}
	if _, ok := <-b.Subscribe("late"); ok {
		t.Fatal("late subscriber channel not closed")
	}
}

func TestBusConcurrent(t *testing.T) {
	b := NewBus(nil)
	b.PublishWait = time.Millisecond
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ch := b.Subscribe(fmt.Sprint("s", i%4))
			for j := 0; j < 200; j++ {
				b.Publish(core.Event{})
				select {
				case <-ch:
				default:
				}
			}
		}(i)
	}
	wg.Wait()
	b.Close()
}

// ---- Router ----

func TestRouterCommands(t *testing.T) {
	tests := []struct {
		name     string
		features []core.Feature
		want     string // joined names, or a piece of the error
		wantErr  bool
	}{
		{"none", nil, "", false},
		{"in feature order", []core.Feature{
			&feature{name: "a", cmds: cmd("link", "unlink")},
			&feature{name: "b"},
			&feature{name: "c", cmds: cmd("me")},
		}, "link,unlink,me", false},
		{"duplicate command across features", []core.Feature{
			&feature{name: "a", cmds: cmd("me")},
			&feature{name: "b", cmds: cmd("me")},
		}, `duplicate command "me"`, true},
		{"duplicate command in one feature", []core.Feature{
			&feature{name: "a", cmds: cmd("me", "me")},
		}, `duplicate command "me"`, true},
		{"duplicate feature name", []core.Feature{
			&feature{name: "a", cmds: cmd("x")},
			&feature{name: "a", cmds: cmd("y")},
		}, `duplicate feature name "a"`, true},
		{"empty feature name", []core.Feature{&feature{name: ""}}, "invalid feature name", true},
		{"colon in feature name", []core.Feature{&feature{name: "a:b"}}, "invalid feature name", true},
		{"nil feature", []core.Feature{nil}, "nil", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmds, err := NewRouter(tc.features, nil).Commands()
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatalf("Commands: %v", err)
			}
			var names []string
			for _, c := range cmds {
				names = append(names, c.Name)
			}
			if got := strings.Join(names, ","); got != tc.want {
				t.Fatalf("commands = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRouterHandle(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name     string
		in       *core.Interaction
		wantVote []string
		wantDash []string
		wantErr  string
	}{
		{"action", &core.Interaction{CustomID: "vote:yes"}, []string{"vote:yes"}, nil, ""},
		{"action and arg", &core.Interaction{CustomID: "vote:yes:42:x"}, []string{"vote:yes:42:x"}, nil, ""},
		{"bare prefix", &core.Interaction{CustomID: "vote"}, []string{"vote"}, nil, ""},
		{"feature error passes through", &core.Interaction{CustomID: "dash:refresh"}, nil, []string{"dash:refresh"}, "boom"},
		{"unknown prefix", &core.Interaction{CustomID: "nope:x"}, nil, nil, `prefix "nope"`},
		{"feature without buttons", &core.Interaction{CustomID: "plain:x"}, nil, nil, `prefix "plain"`},
		{"prefix is not a substring match", &core.Interaction{CustomID: "voter:x"}, nil, nil, `prefix "voter"`},
		{"empty prefix", &core.Interaction{CustomID: ":x"}, nil, nil, `prefix ""`},
		{"no custom id", &core.Interaction{Command: "me"}, nil, nil, "no custom id"},
		{"nil", nil, nil, nil, "nil interaction"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vote := &buttonFeature{feature: feature{name: "vote"}}
			dash := &buttonFeature{feature: feature{name: "dash"}, err: boom}
			r := NewRouter([]core.Feature{vote, &feature{name: "plain"}, dash}, nil)
			var h core.InteractionHandler = r.Handle
			err := h(context.Background(), tc.in)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if fmt.Sprint(vote.got) != fmt.Sprint(tc.wantVote) || fmt.Sprint(dash.got) != fmt.Sprint(tc.wantDash) {
				t.Fatalf("vote got %v, dash got %v", vote.got, dash.got)
			}
		})
	}
}

// ---- Run ----

func TestBackoff(t *testing.T) {
	ms := time.Millisecond
	tests := []struct {
		name     string
		min, max time.Duration
		want     []time.Duration
	}{
		{"defaults", time.Second, 2 * time.Minute, []time.Duration{
			time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second,
			32 * time.Second, 64 * time.Second, 2 * time.Minute, 2 * time.Minute}},
		{"cap between steps", ms, 5 * ms, []time.Duration{ms, 2 * ms, 4 * ms, 5 * ms, 5 * ms}},
		{"min equals max", 3 * ms, 3 * ms, []time.Duration{3 * ms, 3 * ms}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newBackoff(Timing{MinBackoff: tc.min, MaxBackoff: tc.max})
			for i, want := range tc.want {
				if got := b.next(); got != want {
					t.Fatalf("step %d = %v, want %v", i, got, want)
				}
			}
			b.reset()
			if got := b.next(); got != tc.min {
				t.Fatalf("after reset = %v, want %v", got, tc.min)
			}
		})
	}
	d := DefaultTiming()
	if (Timing{}).withDefaults() != d || d.MinBackoff != time.Second || d.MaxBackoff != 2*time.Minute ||
		d.HealthyAfter != 5*time.Minute || d.ShutdownWait != 10*time.Second {
		t.Fatalf("defaults = %+v", d)
	}
}

func TestRunLifecycle(t *testing.T) {
	deps, f := coretest.NewDeps(t)
	bus := NewBus(deps.Log)
	sub := bus.Subscribe("test")
	stopped := make(chan struct{})
	feats := []core.Feature{
		&feature{name: "a", cmds: cmd("link"), run: func(ctx context.Context) error {
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		}},
		&buttonFeature{feature: feature{name: "b", cmds: cmd("me", "search")}},
	}
	cancel, res := start(t, deps, bus, feats, fast)

	coretest.Eventually(t, "event stream", func() bool { return len(f.HA.CallsTo("Events")) == 1 })
	if !f.DC.IsOpen() {
		t.Fatal("Discord not open")
	}
	var names []string
	for _, c := range f.DC.Commands() {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, ","); got != "link,me,search" {
		t.Fatalf("commands = %q", got)
	}
	if err := f.DC.Press(context.Background(), &core.Interaction{CustomID: "b:go"}); err != nil {
		t.Fatalf("router not wired: %v", err)
	}

	if !f.HA.Emit(core.Event{Type: core.EventTitleAdded, TitleID: 9}) {
		t.Fatal("Emit failed")
	}
	select {
	case e := <-sub:
		if e.TitleID != 9 || e.Type != core.EventTitleAdded {
			t.Fatalf("event = %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event did not reach the bus")
	}

	cancel()
	if err := wait(t, res); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("Run returned before the feature stopped")
	}
	if _, ok := <-sub; ok {
		t.Fatal("bus not closed")
	}
	if f.DC.IsOpen() || len(f.DC.CallsTo("Close")) != 1 {
		t.Fatal("Discord not closed exactly once")
	}
	if n := feats[0].(*feature).runs.Load(); n != 1 {
		t.Fatalf("healthy feature ran %d times", n)
	}
}

// flakyOpen fails the first fails calls to Open with an error that holds
// nothing secret, as the real adapter's errors do.
type flakyOpen struct {
	*coretest.FakeDiscord
	fails int32
	tries atomic.Int32
}

func (d *flakyOpen) Open(ctx context.Context, cmds []core.Command, router core.InteractionHandler) error {
	if d.tries.Add(1) <= d.fails {
		return errors.New("discord connect: no such host")
	}
	return d.FakeDiscord.Open(ctx, cmds, router)
}

// lockedBuf is a log sink that goroutines can share.
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A Discord that does not open at start is tried again until it does; the
// process never exits over it.
func TestRunRetriesOpen(t *testing.T) {
	deps, f := coretest.NewDeps(t)
	var logs lockedBuf
	deps.Log = slog.New(slog.NewTextHandler(&logs, nil))
	dc := &flakyOpen{FakeDiscord: f.DC, fails: 3}
	deps.DC = dc
	ft := &feature{name: "a"}
	cancel, res := start(t, deps, NewBus(nil), []core.Feature{ft}, fast)
	coretest.Eventually(t, "the feature to run", func() bool { return ft.runs.Load() == 1 })
	if got := dc.tries.Load(); got != 4 || !f.DC.IsOpen() {
		t.Fatalf("Open tried %d times, open = %v", got, f.DC.IsOpen())
	}
	cancel()
	if err := wait(t, res); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	if n := strings.Count(out, "Discord did not open, retrying"); n != 3 {
		t.Errorf("%d retry lines, want one per failed attempt (3):\n%s", n, out)
	}
	if strings.Contains(out, deps.Cfg.BotToken) {
		t.Errorf("log holds the token:\n%s", out)
	}
}

// While Discord keeps failing the bot stays up, starts no feature, and still
// stops cleanly when told to.
func TestRunStopsWhileOpenFails(t *testing.T) {
	deps, f := coretest.NewDeps(t)
	f.DC.SetErr("Open", errors.New("gateway down"))
	ft := &feature{name: "a"}
	bus := NewBus(nil)
	sub := bus.Subscribe("s")
	cancel, res := start(t, deps, bus, []core.Feature{ft}, fast)
	coretest.Eventually(t, "several attempts", func() bool { return len(f.DC.CallsTo("Open")) >= 3 })
	select {
	case err := <-res:
		t.Fatalf("RunWith returned %v while Discord was down", err)
	default:
	}
	cancel()
	if err := wait(t, res); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ft.runs.Load() != 0 {
		t.Fatal("feature ran although Discord never opened")
	}
	if _, ok := <-sub; ok {
		t.Fatal("bus left open")
	}
	if len(f.DC.CallsTo("Close")) != 1 {
		t.Fatal("Discord not closed")
	}
}

func TestRunStartErrors(t *testing.T) {
	tests := []struct {
		name     string
		features []core.Feature
		openErr  error
		want     string
		opened   int
	}{
		{"duplicate command", []core.Feature{&feature{name: "a", cmds: cmd("x")}, &feature{name: "b", cmds: cmd("x")}}, nil, "duplicate command", 0},
		{"duplicate feature", []core.Feature{&feature{name: "a"}, &feature{name: "a"}}, nil, "duplicate feature", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, f := coretest.NewDeps(t)
			f.DC.SetErr("Open", tc.openErr)
			bus := NewBus(nil)
			sub := bus.Subscribe("s")
			err := RunWith(context.Background(), deps, bus, tc.features, fast)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if tc.openErr != nil && !errors.Is(err, tc.openErr) {
				t.Fatalf("err %v does not wrap %v", err, tc.openErr)
			}
			if got := len(f.DC.CallsTo("Open")); got != tc.opened {
				t.Fatalf("Open calls = %d, want %d", got, tc.opened)
			}
			if _, ok := <-sub; ok {
				t.Fatal("bus left open after a failed start")
			}
			for _, ft := range tc.features {
				if ft.(*feature).runs.Load() != 0 {
					t.Fatal("feature ran although start failed")
				}
			}
		})
	}
	if err := RunWith(context.Background(), core.Deps{}, nil, nil, fast); err == nil {
		t.Fatal("nil bus accepted")
	}
	if err := RunWith(context.Background(), core.Deps{}, NewBus(nil), nil, fast); err == nil {
		t.Fatal("nil ports accepted")
	}
}

func TestRunRestartsFailingFeatures(t *testing.T) {
	tests := []struct {
		name string
		run  func(ctx context.Context) error
	}{
		{"returns error", func(context.Context) error { return errors.New("broken") }},
		{"returns nil early", func(context.Context) error { return nil }},
		{"panics", func(context.Context) error { panic("kaboom") }},
		{"nil map write", func(context.Context) error { var m map[string]int; m["x"] = 1; return nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, _ := coretest.NewDeps(t)
			logs := &logCounter{}
			deps.Log = slog.New(logs)
			bad := &feature{name: "bad", run: tc.run}
			good := &feature{name: "good"}
			cancel, res := start(t, deps, NewBus(nil), []core.Feature{bad, good}, fast)

			coretest.Eventually(t, "restarts", func() bool { return bad.runs.Load() >= 4 })
			if n := good.runs.Load(); n != 1 {
				t.Fatalf("healthy feature ran %d times", n)
			}
			if logs.count("ERROR feature") < 3 {
				t.Fatalf("failures not logged: %v", logs.msgs)
			}
			cancel()
			if err := wait(t, res); err != nil {
				t.Fatalf("Run = %v", err)
			}
		})
	}
}

func TestRunBackoffGrowsAndResets(t *testing.T) {
	tests := []struct {
		name    string
		healthy time.Duration
		// With min 5ms doubling to a 40ms cap, 5 runs need 5+10+20+40 = 75ms.
		// When every run counts as healthy the delay stays 5ms: 20ms.
		wantAtLeast, wantUnder time.Duration
	}{
		{"grows", time.Hour, 75 * time.Millisecond, 0},
		{"resets after a healthy run", time.Nanosecond, 20 * time.Millisecond, 70 * time.Millisecond},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, _ := coretest.NewDeps(t)
			var mu sync.Mutex
			var times []time.Time
			bad := &feature{name: "bad", run: func(context.Context) error {
				mu.Lock()
				times = append(times, time.Now())
				mu.Unlock()
				time.Sleep(10 * time.Microsecond) // longer than the "healthy" nanosecond
				return errors.New("broken")
			}}
			timing := Timing{MinBackoff: 5 * time.Millisecond, MaxBackoff: 40 * time.Millisecond, HealthyAfter: tc.healthy, ShutdownWait: time.Second}
			cancel, res := start(t, deps, NewBus(nil), []core.Feature{bad}, timing)
			coretest.Eventually(t, "five runs", func() bool { return bad.runs.Load() >= 5 })
			cancel()
			if err := wait(t, res); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			took := times[4].Sub(times[0])
			mu.Unlock()
			if took < tc.wantAtLeast {
				t.Fatalf("five runs took %v, want at least %v", took, tc.wantAtLeast)
			}
			if tc.wantUnder > 0 && took >= tc.wantUnder {
				t.Fatalf("five runs took %v, want under %v (backoff did not reset)", took, tc.wantUnder)
			}
		})
	}
}

func TestRunResubscribesToEvents(t *testing.T) {
	tests := []struct {
		name      string
		failFirst bool // re-subscribing fails a few times before it works
	}{
		{"stream closes", false},
		{"subscribe fails for a while", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, f := coretest.NewDeps(t)
			bus := NewBus(nil)
			sub := bus.Subscribe("s")
			cancel, res := start(t, deps, bus, nil, fast)

			coretest.Eventually(t, "first stream", func() bool { return f.HA.Emit(core.Event{TitleID: 1}) })
			if e := <-sub; e.TitleID != 1 {
				t.Fatalf("event = %+v", e)
			}
			before := len(f.HA.CallsTo("Events"))
			if tc.failFirst {
				f.HA.SetErr("Events", core.ErrRateLimited)
			}
			f.HA.CloseEvents()
			if tc.failFirst {
				coretest.Eventually(t, "failed retries", func() bool { return len(f.HA.CallsTo("Events")) >= before+3 })
				f.HA.SetErr("Events", nil)
			}
			// Emit only succeeds on a fresh, open stream.
			coretest.Eventually(t, "second stream", func() bool { return f.HA.Emit(core.Event{TitleID: 2}) })
			select {
			case e := <-sub:
				if e.TitleID != 2 {
					t.Fatalf("event = %+v", e)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("event from the new stream did not arrive")
			}
			if got := len(f.HA.CallsTo("Events")); got < 2 {
				t.Fatalf("Events calls = %d, want at least 2", got)
			}
			cancel()
			if err := wait(t, res); err != nil {
				t.Fatal(err)
			}
			n := len(f.HA.CallsTo("Events"))
			time.Sleep(5 * time.Millisecond)
			if got := len(f.HA.CallsTo("Events")); got != n {
				t.Fatal("pump kept subscribing after shutdown")
			}
		})
	}
}

func TestRunShutdownDoesNotWaitForever(t *testing.T) {
	deps, f := coretest.NewDeps(t)
	logs := &logCounter{}
	deps.Log = slog.New(logs)
	release := make(chan struct{})
	defer close(release)
	stuck := &feature{name: "stuck", run: func(context.Context) error { <-release; return nil }}
	bus := NewBus(nil)
	sub := bus.Subscribe("s")
	timing := fast
	timing.ShutdownWait = 5 * time.Millisecond
	cancel, res := start(t, deps, bus, []core.Feature{stuck}, timing)
	coretest.Eventually(t, "feature start", func() bool { return stuck.runs.Load() == 1 })

	began := time.Now()
	cancel()
	if err := wait(t, res); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if waited := time.Since(began); waited < 5*time.Millisecond {
		t.Fatalf("returned after %v, before the shutdown wait", waited)
	}
	if logs.count("WARN features did not stop") != 1 {
		t.Fatalf("timeout not logged: %v", logs.msgs)
	}
	if _, ok := <-sub; ok {
		t.Fatal("bus not closed")
	}
	if f.DC.IsOpen() {
		t.Fatal("Discord not closed")
	}
}

func TestRunCloseErrorIsNotFatal(t *testing.T) {
	deps, f := coretest.NewDeps(t)
	f.DC.SetErr("Close", errors.New("already gone"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: start, then shut straight down
	if err := Run(ctx, deps, NewBus(nil), []core.Feature{&feature{name: "a"}}); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if len(f.DC.CallsTo("Close")) != 1 {
		t.Fatal("Close not called")
	}
}
