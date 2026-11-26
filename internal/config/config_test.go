package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

const (
	botSecret    = "BOT.s3cr3t-token-value"
	pluginSecret = "PLUGIN.s3cr3t-token-value"
)

func baseEnv() map[string]string {
	return map[string]string{
		EnvData:                     "/data",
		EnvTarget:                   "http://holyarrs:8080",
		EnvToken:                    pluginSecret,
		SettingPrefix + "BOT_TOKEN": botSecret,
		SettingPrefix + "GUILD_ID":  "123456789012345678",
	}
}

func env(over map[string]string) func(string) string {
	m := baseEnv()
	for k, v := range over {
		if v == "<unset>" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DataDir != "/data" || cfg.HolyArrsTarget != "http://holyarrs:8080" ||
		cfg.HolyArrsToken != pluginSecret || cfg.BotToken != botSecret || cfg.GuildID != "123456789012345678" {
		t.Fatalf("required fields wrong: %s", Redacted(cfg))
	}
	if cfg.RefreshEvery != 5*time.Minute {
		t.Errorf("RefreshEvery = %v", cfg.RefreshEvery)
	}
	if !cfg.EnableDashboard || !cfg.EnableAnnounce || !cfg.EnableRequests || !cfg.EnableHomeWidget {
		t.Errorf("toggles should default to true: %s", Redacted(cfg))
	}
	if cfg.InviteURL != "" || cfg.DashboardChannel != "" || cfg.AdminRoleID != "" {
		t.Errorf("optional fields should be empty: %s", Redacted(cfg))
	}
}

func TestLoadAllSettings(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		SettingPrefix + "INVITE_URL":           " https://discord.gg/abc ",
		SettingPrefix + "DASHBOARD_CHANNEL_ID": "11",
		SettingPrefix + "ANNOUNCE_CHANNEL_ID":  "22",
		SettingPrefix + "ADMIN_ROLE_ID":        "44",
		SettingPrefix + "LINKED_ROLE_ID":       "55",
		SettingPrefix + "REFRESH_MINUTES":      "15",
		SettingPrefix + "ENABLE_DASHBOARD":     "false",
		SettingPrefix + "ENABLE_ANNOUNCE":      "true",
		SettingPrefix + "ENABLE_REQUESTS":      "FALSE",
		SettingPrefix + "ENABLE_HOME_WIDGET":   "false",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := fmt.Sprint(cfg.InviteURL, cfg.DashboardChannel, cfg.AnnounceChannel,
		cfg.AdminRoleID, cfg.LinkedRoleID, cfg.RefreshEvery, cfg.EnableDashboard, cfg.EnableAnnounce, cfg.EnableRequests, cfg.EnableHomeWidget)
	want := fmt.Sprint("https://discord.gg/abc", "11", "22", "44", "55", 15*time.Minute, false, true, false, false)
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestRefreshMinutes(t *testing.T) {
	tests := []struct {
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{"", 5 * time.Minute, false},
		{"1", time.Minute, false},
		{"0", time.Minute, false},
		{"-7", time.Minute, false},
		{"1440", 1440 * time.Minute, false},
		{"1441", 1440 * time.Minute, false},
		{"99999999999999999999999", 1440 * time.Minute, false},
		{"-99999999999999999999999", time.Minute, false},
		{" 30 ", 30 * time.Minute, false},
		{"five", 0, true},
		{"2.5", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			cfg, err := Load(env(map[string]string{SettingPrefix + "REFRESH_MINUTES": tc.raw}))
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "REFRESH_MINUTES") {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.RefreshEvery != tc.want {
				t.Fatalf("RefreshEvery = %v, want %v", cfg.RefreshEvery, tc.want)
			}
		})
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name string
		over map[string]string
		want []string // every one must appear in the error
	}{
		{"no data dir", map[string]string{EnvData: "<unset>"}, []string{EnvData}},
		{"blank target", map[string]string{EnvTarget: "   "}, []string{EnvTarget}},
		{"no plugin token", map[string]string{EnvToken: "<unset>"}, []string{EnvToken}},
		{"no bot token", map[string]string{SettingPrefix + "BOT_TOKEN": "<unset>"}, []string{"BOT_TOKEN"}},
		{"no guild", map[string]string{SettingPrefix + "GUILD_ID": "<unset>"}, []string{"GUILD_ID"}},
		{"everything missing", map[string]string{
			EnvData: "<unset>", EnvTarget: "<unset>", EnvToken: "<unset>",
			SettingPrefix + "BOT_TOKEN": "<unset>", SettingPrefix + "GUILD_ID": "<unset>",
		}, []string{EnvData, EnvTarget, EnvToken, "BOT_TOKEN", "GUILD_ID"}},
		{"guild not digits", map[string]string{SettingPrefix + "GUILD_ID": "12ab"}, []string{"GUILD_ID"}},
		{"channel mention", map[string]string{SettingPrefix + "DASHBOARD_CHANNEL_ID": "<#123>"}, []string{"DASHBOARD_CHANNEL_ID"}},
		{"announce not digits", map[string]string{SettingPrefix + "ANNOUNCE_CHANNEL_ID": "general"}, []string{"ANNOUNCE_CHANNEL_ID"}},
		{"dashboard negative", map[string]string{SettingPrefix + "DASHBOARD_CHANNEL_ID": "-1"}, []string{"DASHBOARD_CHANNEL_ID"}},
		{"admin role name", map[string]string{SettingPrefix + "ADMIN_ROLE_ID": "@admin"}, []string{"ADMIN_ROLE_ID"}},
		{"linked role spaces", map[string]string{SettingPrefix + "LINKED_ROLE_ID": "1 2"}, []string{"LINKED_ROLE_ID"}},
		{"snowflake too long", map[string]string{SettingPrefix + "GUILD_ID": strings.Repeat("9", 21)}, []string{"GUILD_ID"}},
		{"invite http", map[string]string{SettingPrefix + "INVITE_URL": "http://discord.gg/abc"}, []string{"INVITE_URL"}},
		{"invite no scheme", map[string]string{SettingPrefix + "INVITE_URL": "discord.gg/abc"}, []string{"INVITE_URL"}},
		{"invite no host", map[string]string{SettingPrefix + "INVITE_URL": "https://"}, []string{"INVITE_URL"}},
		{"invite garbage", map[string]string{SettingPrefix + "INVITE_URL": "https://%zz"}, []string{"INVITE_URL"}},
		{"toggle yes", map[string]string{SettingPrefix + "ENABLE_DASHBOARD": "yes"}, []string{"ENABLE_DASHBOARD"}},
		{"toggle 1", map[string]string{SettingPrefix + "ENABLE_ANNOUNCE": "1"}, []string{"ENABLE_ANNOUNCE"}},
		{"toggle off", map[string]string{SettingPrefix + "ENABLE_REQUESTS": "off"}, []string{"ENABLE_REQUESTS"}},
		{"toggle widget", map[string]string{SettingPrefix + "ENABLE_HOME_WIDGET": "on"}, []string{"ENABLE_HOME_WIDGET"}},
		{"several at once", map[string]string{
			EnvData:                           "<unset>",
			SettingPrefix + "ADMIN_ROLE_ID":   "x",
			SettingPrefix + "INVITE_URL":      "ftp://x",
			SettingPrefix + "REFRESH_MINUTES": "soon",
			SettingPrefix + "ENABLE_REQUESTS": "maybe",
		}, []string{EnvData, "ADMIN_ROLE_ID", "INVITE_URL", "REFRESH_MINUTES", "ENABLE_REQUESTS"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(env(tc.over))
			if err == nil {
				t.Fatal("nil error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not name %s", err, w)
				}
			}
			for _, s := range []string{botSecret, pluginSecret} {
				if strings.Contains(err.Error(), s) {
					t.Errorf("error leaks a token: %q", err)
				}
			}
			if cfg.BotToken != "" || cfg.HolyArrsToken != "" {
				t.Error("config returned alongside an error still holds tokens")
			}
		})
	}
}

func TestErrorsNeverEchoValues(t *testing.T) {
	// A token pasted into the wrong setting must not come back in the error.
	for _, key := range []string{"GUILD_ID", "DASHBOARD_CHANNEL_ID", "INVITE_URL", "REFRESH_MINUTES", "ENABLE_DASHBOARD", "ADMIN_ROLE_ID"} {
		_, err := Load(env(map[string]string{SettingPrefix + key: botSecret}))
		if err == nil {
			t.Fatalf("%s: nil error", key)
		}
		if strings.Contains(err.Error(), botSecret) {
			t.Errorf("%s: error echoes the value: %q", key, err)
		}
	}
}

func TestRedacted(t *testing.T) {
	const invite, channel, role = "https://discord.gg/sEcReTinvite", "222222222222222222", "333333333333333333"
	cfg, err := Load(env(map[string]string{
		SettingPrefix + "INVITE_URL":           invite,
		SettingPrefix + "DASHBOARD_CHANNEL_ID": channel,
		SettingPrefix + "ADMIN_ROLE_ID":        role,
	}))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("config", "cfg", Redacted(cfg))
	out := buf.String() + Redacted(cfg).String()
	// Counts only: no token, no Discord id, no invite link.
	for _, s := range []string{botSecret, pluginSecret, cfg.GuildID, invite, "sEcReTinvite", channel, role} {
		if strings.Contains(out, s) {
			t.Fatalf("Redacted leaks %q: %s", s, out)
		}
	}
	for _, w := range []string{"[redacted]", "guild_set=true", "invite_set=true", "dashboard_channel_set=true",
		"announce_channel_set=false", "admin_role_set=true", "linked_role_set=false"} {
		if !strings.Contains(out, w) {
			t.Fatalf("output lacks %s: %s", w, out)
		}
	}
	cfg.BotToken = ""
	if !strings.Contains(Redacted(cfg).String(), "[unset]") {
		t.Fatal("unset token not shown as [unset]")
	}
}
