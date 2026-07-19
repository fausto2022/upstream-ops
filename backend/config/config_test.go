package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fausto2022/relaydeck/backend/storage"
)

func TestLoadAppliesUpstreamDefaults(t *testing.T) {
	cfg, err := LoadFile(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.Upstream.TimeoutSeconds != DefaultUpstreamTimeoutSeconds {
		t.Fatalf("timeout seconds = %d", cfg.Upstream.TimeoutSeconds)
	}
	if cfg.Upstream.UserAgent != DefaultUpstreamUserAgent {
		t.Fatalf("user agent = %q", cfg.Upstream.UserAgent)
	}
}

func TestLoadWithPathPrefersPersistedAuthOverEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	want := AuthConfig{
		Enabled:         true,
		Username:        "saved-admin",
		Password:        "saved-password",
		TokenSecret:     "saved-token-secret",
		SessionTTLHours: 72,
	}
	if err := Save(path, &Config{Auth: want}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	t.Setenv("AUTH_ENABLED", "true")
	t.Setenv("ADMIN_USERNAME", "env-admin")
	t.Setenv("ADMIN_PASSWORD", "env-password")
	t.Setenv("AUTH_TOKEN_SECRET", "env-token-secret")

	cfg, usedPath, err := LoadWithPath(path)
	if err != nil {
		t.Fatalf("LoadWithPath: %v", err)
	}
	if usedPath != path {
		t.Fatalf("used path = %q, want %q", usedPath, path)
	}
	if cfg.Auth != want {
		t.Fatalf("auth = %#v, want %#v", cfg.Auth, want)
	}
}

func TestLoadWithPathUsesEnvironmentForAuthBootstrap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("AUTH_ENABLED", "true")
	t.Setenv("ADMIN_USERNAME", "bootstrap-admin")
	t.Setenv("ADMIN_PASSWORD", "bootstrap-password")
	t.Setenv("AUTH_TOKEN_SECRET", "bootstrap-token-secret")

	cfg, _, err := LoadWithPath(path)
	if err != nil {
		t.Fatalf("LoadWithPath missing config: %v", err)
	}
	if !cfg.Auth.Enabled || cfg.Auth.Username != "bootstrap-admin" || cfg.Auth.Password != "bootstrap-password" ||
		cfg.Auth.TokenSecret != "bootstrap-token-secret" {
		t.Fatalf("bootstrap auth = %#v", cfg.Auth)
	}

	if err := os.WriteFile(path, []byte("server:\n  port: 8418\n"), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
	cfg, _, err = LoadWithPath(path)
	if err != nil {
		t.Fatalf("LoadWithPath legacy config: %v", err)
	}
	if cfg.Auth.Username != "bootstrap-admin" || cfg.Auth.Password != "bootstrap-password" {
		t.Fatalf("legacy bootstrap auth = %#v", cfg.Auth)
	}
}

func TestUpstreamConfigWithDefaultsKeepsCustomUserAgent(t *testing.T) {
	cfg := UpstreamConfig{
		TimeoutSeconds: 0,
		UserAgent:      "custom-agent",
	}.WithDefaults()
	if cfg.TimeoutSeconds != DefaultUpstreamTimeoutSeconds {
		t.Fatalf("timeout seconds = %d", cfg.TimeoutSeconds)
	}
	if cfg.UserAgent != "custom-agent" {
		t.Fatalf("user agent = %q", cfg.UserAgent)
	}
}

func TestNotificationDisabledEventsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	want := []storage.NotificationEvent{storage.EventBalanceLow, storage.EventMainMemberDisabled}
	if err := Save(path, &Config{Notifications: NotificationsConfig{DisabledEvents: want}}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Notifications.DisabledEvents) != len(want) {
		t.Fatalf("disabled events = %#v", cfg.Notifications.DisabledEvents)
	}
	for i := range want {
		if cfg.Notifications.DisabledEvents[i] != want[i] {
			t.Fatalf("disabled events = %#v", cfg.Notifications.DisabledEvents)
		}
	}
}
