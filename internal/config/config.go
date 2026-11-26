// Package config reads the HOLYARRS_PLUGIN_* environment into core.Config and
// validates it. Error text names settings, never their values, so a token can
// not end up in a log through this package.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

// Environment names set by HolyArrs.
const (
	EnvData       = "HOLYARRS_PLUGIN_DATA"
	EnvTarget     = "HOLYARRS_PLUGIN_TARGET"
	EnvToken      = "HOLYARRS_PLUGIN_TOKEN"
	SettingPrefix = "HOLYARRS_PLUGIN_SETTING_"
)

const (
	defaultRefreshMinutes = 5
	minRefreshMinutes     = 1
	maxRefreshMinutes     = 1440
)

// Load reads and validates the configuration. getenv is os.Getenv in main.
// The returned error lists every problem, one per line, by setting name.
func Load(getenv func(string) string) (core.Config, error) {
	get := func(name string) string { return strings.TrimSpace(getenv(name)) }
	setting := func(key string) string { return get(SettingPrefix + key) }

	cfg := core.Config{
		DataDir:          get(EnvData),
		HolyArrsTarget:   get(EnvTarget),
		HolyArrsToken:    get(EnvToken),
		BotToken:         setting("BOT_TOKEN"),
		GuildID:          setting("GUILD_ID"),
		InviteURL:        setting("INVITE_URL"),
		DashboardChannel: setting("DASHBOARD_CHANNEL_ID"),
		AnnounceChannel:  setting("ANNOUNCE_CHANNEL_ID"),
		ModChannel:       setting("MOD_CHANNEL_ID"),
		AdminRoleID:      setting("ADMIN_ROLE_ID"),
		LinkedRoleID:     setting("LINKED_ROLE_ID"),
	}

	var problems []string

	var missing []string
	for _, r := range []struct{ name, value string }{
		{EnvData, cfg.DataDir},
		{EnvTarget, cfg.HolyArrsTarget},
		{EnvToken, cfg.HolyArrsToken},
		{"BOT_TOKEN", cfg.BotToken},
		{"GUILD_ID", cfg.GuildID},
	} {
		if r.value == "" {
			missing = append(missing, r.name)
		}
	}
	if len(missing) > 0 {
		problems = append(problems, "missing required: "+strings.Join(missing, ", "))
	}

	for _, s := range []struct{ name, value string }{
		{"GUILD_ID", cfg.GuildID},
		{"DASHBOARD_CHANNEL_ID", cfg.DashboardChannel},
		{"ANNOUNCE_CHANNEL_ID", cfg.AnnounceChannel},
		{"MOD_CHANNEL_ID", cfg.ModChannel},
		{"ADMIN_ROLE_ID", cfg.AdminRoleID},
		{"LINKED_ROLE_ID", cfg.LinkedRoleID},
	} {
		if s.value != "" && !isSnowflake(s.value) {
			problems = append(problems, s.name+" must be a Discord id (digits only)")
		}
	}

	if cfg.InviteURL != "" {
		u, err := url.Parse(cfg.InviteURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			problems = append(problems, "INVITE_URL must be an https link")
		}
	}

	minutes := defaultRefreshMinutes
	if raw := setting("REFRESH_MINUTES"); raw != "" {
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil && errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(raw, "-"):
			minutes = maxRefreshMinutes
		case err != nil && errors.Is(err, strconv.ErrRange):
			minutes = minRefreshMinutes
		case err != nil:
			problems = append(problems, "REFRESH_MINUTES must be a whole number")
		default:
			minutes = min(max(n, minRefreshMinutes), maxRefreshMinutes)
		}
	}
	cfg.RefreshEvery = time.Duration(minutes) * time.Minute

	for _, tg := range []struct {
		name string
		dst  *bool
	}{
		{"ENABLE_DASHBOARD", &cfg.EnableDashboard},
		{"ENABLE_ANNOUNCE", &cfg.EnableAnnounce},
		{"ENABLE_REQUESTS", &cfg.EnableRequests},
		{"ENABLE_HOME_WIDGET", &cfg.EnableHomeWidget},
	} {
		switch strings.ToLower(setting(tg.name)) {
		case "", "true":
			*tg.dst = true
		case "false":
			*tg.dst = false
		default:
			*tg.dst = true
			problems = append(problems, tg.name+` must be "true" or "false"`)
		}
	}

	if len(problems) > 0 {
		return core.Config{}, fmt.Errorf("config: %s", strings.Join(problems, "; "))
	}
	return cfg, nil
}

// Redacted returns cfg as a log value with the secrets replaced by whether
// they are set. Log this instead of the Config itself: core.Config is a plain
// struct, so printing it directly would show the tokens.
func Redacted(cfg core.Config) slog.Value {
	return slog.GroupValue(
		slog.String("bot_token", secret(cfg.BotToken)),
		slog.String("holyarrs_token", secret(cfg.HolyArrsToken)),
		slog.String("guild_id", cfg.GuildID),
		slog.String("invite_url", cfg.InviteURL),
		slog.String("dashboard_channel", cfg.DashboardChannel),
		slog.String("announce_channel", cfg.AnnounceChannel),
		slog.String("mod_channel", cfg.ModChannel),
		slog.String("admin_role_id", cfg.AdminRoleID),
		slog.String("linked_role_id", cfg.LinkedRoleID),
		slog.Duration("refresh_every", cfg.RefreshEvery),
		slog.Bool("enable_dashboard", cfg.EnableDashboard),
		slog.Bool("enable_announce", cfg.EnableAnnounce),
		slog.Bool("enable_requests", cfg.EnableRequests),
		slog.Bool("enable_home_widget", cfg.EnableHomeWidget),
		slog.String("data_dir", cfg.DataDir),
		slog.String("holyarrs_target", cfg.HolyArrsTarget),
	)
}

func secret(v string) string {
	if v == "" {
		return "[unset]"
	}
	return "[redacted]"
}

func isSnowflake(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
