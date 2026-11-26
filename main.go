// Command plugin is the HolyArrs Discord plugin: one process that dials out
// to Discord and to HolyArrs and never listens on a port.
package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/app"
	"github.com/NotCavnfox/holyarrs-discord/internal/config"
	"github.com/NotCavnfox/holyarrs-discord/internal/core"
	"github.com/NotCavnfox/holyarrs-discord/internal/discordgw"
	"github.com/NotCavnfox/holyarrs-discord/internal/features/announce"
	"github.com/NotCavnfox/holyarrs-discord/internal/features/dashboard"
	"github.com/NotCavnfox/holyarrs-discord/internal/features/linking"
	"github.com/NotCavnfox/holyarrs-discord/internal/features/requests"
	"github.com/NotCavnfox/holyarrs-discord/internal/holyarrs"
	"github.com/NotCavnfox/holyarrs-discord/internal/links"
	"github.com/NotCavnfox/holyarrs-discord/internal/selfcheck"
	"github.com/NotCavnfox/holyarrs-discord/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		if os.Getenv(config.EnvTarget) == "" {
			return err
		}
		// Inside HolyArrs: stay up instead of exiting. A plugin that exits is
		// marked as stopped on its own and cannot be started again from the
		// page; one that runs is restarted when the owner saves its settings.
		log.Error("waiting for settings", "err", err)
		checkCtx, endCheck := context.WithCancel(context.Background())
		defer endCheck()
		if target, token := os.Getenv(config.EnvTarget), os.Getenv(config.EnvToken); target != "" && token != "" {
			// The self-check is the only live evidence there is before the
			// settings are filled in. It never reads events, so the adapter
			// gets no stdin: waitForStop stays the one reader of it.
			ha := holyarrs.New(target, token, nil, nil, log)
			go selfcheck.Run(checkCtx, ha, discordgw.Probe, log)
		}
		waitForStop()
		return nil
	}
	log.Info("starting", "config", config.Redacted(cfg))

	// SIGTERM, Ctrl+C or HolyArrs closing stdin all mean: stop cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	st, err := store.New(cfg.DataDir)
	if err != nil {
		return err
	}
	book, err := links.New(st)
	if err != nil {
		return err
	}
	dc, err := discordgw.New(cfg.BotToken, cfg.GuildID, cfg.AdminRoleID, log)
	if err != nil {
		return err
	}
	ha := holyarrs.New(cfg.HolyArrsTarget, cfg.HolyArrsToken, os.Stdin, stop, log)
	// Read-only and in the background: it never delays or stops the start.
	go selfcheck.Run(ctx, ha, discordgw.Probe, log)

	bus := app.NewBus(log)
	deps := core.Deps{
		Cfg:    cfg,
		HA:     ha,
		DC:     dc,
		Store:  st,
		Links:  book,
		Events: bus,
		Log:    log,
		Now:    time.Now,
	}

	// Adding a feature is one line here.
	features := []core.Feature{
		linking.New(deps),
		dashboard.New(deps),
		announce.New(deps),
		requests.New(deps),
	}
	return app.Run(ctx, deps, bus, features)
}

// waitForStop blocks until HolyArrs stops the plugin: SIGTERM, Ctrl+C, or
// stdin closing.
func waitForStop() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		stop()
	}()
	<-ctx.Done()
}
