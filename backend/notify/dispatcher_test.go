package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fausto2022/relaydeck/backend/crypto"
	"github.com/fausto2022/relaydeck/backend/storage"
)

func TestDispatchReleasesCooldownWhenAllChannelsFail(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "failed", http.StatusInternalServerError)
	}))
	defer server.Close()

	db, err := storage.Open(storage.DBConfig{Driver: storage.DBDriverSQLite, Path: filepath.Join(t.TempDir(), "notify.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := storage.AutoMigrate(db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	cipher, err := crypto.NewCipher("test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	raw, _ := json.Marshal(map[string]any{"url": server.URL, "method": http.MethodPost})
	encrypted, err := cipher.Encrypt(string(raw))
	if err != nil {
		t.Fatalf("encrypt config: %v", err)
	}
	repo := storage.NewNotifications(db)
	if err := repo.CreateChannel(&storage.NotificationChannel{
		Name: "webhook", Type: storage.NotifyWebhook, ConfigCipher: encrypted, Enabled: true,
	}); err != nil {
		t.Fatalf("create notification channel: %v", err)
	}
	dispatcher := NewDispatcher(repo, cipher, slog.New(slog.NewTextHandler(io.Discard, nil)), Policy{
		BalanceLowCooldown: time.Hour,
		SendMaxAttempts:    1,
	})
	message := Message{Event: storage.EventBalanceLow, ChannelID: 1, Subject: "余额不足"}
	for i := 0; i < 2; i++ {
		if err := dispatcher.Dispatch(context.Background(), message); err == nil {
			t.Fatalf("dispatch %d succeeded, want failure", i+1)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("webhook calls = %d, want 2", got)
	}
}

func TestNotificationHTTPClientsHaveTimeout(t *testing.T) {
	client := newNotificationHTTPClient()
	if client.GetClient().Timeout != notificationHTTPTimeout {
		t.Fatalf("notification timeout = %s, want %s", client.GetClient().Timeout, notificationHTTPTimeout)
	}
}

func TestDispatcherSkipsGloballyDisabledEvents(t *testing.T) {
	db, err := storage.Open(storage.DBConfig{Driver: storage.DBDriverSQLite, Path: filepath.Join(t.TempDir(), "disabled.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := storage.AutoMigrate(db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	cipher, err := crypto.NewCipher("test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	repo := storage.NewNotifications(db)
	dispatcher := NewDispatcher(repo, cipher, slog.New(slog.NewTextHandler(io.Discard, nil)), Policy{DisabledEvents: []storage.NotificationEvent{
		storage.EventBalanceLow,
		storage.EventRateChanged,
		storage.EventRateStructureChanged,
	}})
	if err := dispatcher.Dispatch(context.Background(), Message{Event: storage.EventBalanceLow, ChannelID: 1}); err != nil {
		t.Fatalf("dispatch disabled event: %v", err)
	}
	if err := dispatcher.DispatchRateBatch(context.Background(), &storage.Channel{ID: 1}, []RateChange{{GroupName: "default", OldRatio: 1, NewRatio: 2}}); err != nil {
		t.Fatalf("dispatch disabled rate event: %v", err)
	}
	if err := dispatcher.DispatchRateStructureBatch(context.Background(), &storage.Channel{ID: 1}, RateStructureChange{
		Added: []RateChange{{GroupName: "new", NewRatio: 1}},
	}); err != nil {
		t.Fatalf("dispatch disabled rate structure event: %v", err)
	}
	events, total, err := repo.ListEventsPage(1, 10)
	if err != nil {
		t.Fatalf("list alert events: %v", err)
	}
	if total != 3 || len(events) != 3 {
		t.Fatalf("alert events = %d/%d, want 3", len(events), total)
	}
	logs, err := repo.ListLogs(10)
	if err != nil {
		t.Fatalf("list delivery logs: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("delivery logs = %d, want 0", len(logs))
	}
}
