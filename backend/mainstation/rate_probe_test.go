package mainstation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fausto2022/relaydeck/backend/storage"
)

func TestQuickTestRateCreatesUsesAndDeletesTemporaryKey(t *testing.T) {
	service, db, _, channels := newTestService(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("probe path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-source-secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["model"] != "gpt-test" {
			t.Fatalf("model = %#v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"test","usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))
	}))
	defer server.Close()

	channel := createTestChannel(t, db)
	channel.SiteURL = server.URL
	if err := db.Save(channel).Error; err != nil {
		t.Fatalf("save channel: %v", err)
	}
	groupID := int64(301)
	rate := &storage.RateSnapshot{ChannelID: channel.ID, RemoteGroupID: &groupID, ModelName: "source-openai", Ratio: 0.2, LastSeenAt: time.Now()}
	if err := db.Create(rate).Error; err != nil {
		t.Fatalf("create rate: %v", err)
	}

	result, err := service.QuickTestRate(context.Background(), channel.ID, rate.ID, RateQuickTestInput{Platform: "openai", Model: "gpt-test"})
	if err != nil {
		t.Fatalf("quick test rate: %v", err)
	}
	if !result.Usable || !result.Reachable || result.Status != "usable" || result.HTTPStatus != http.StatusOK || result.TotalTokens == nil || *result.TotalTokens != 3 {
		t.Fatalf("quick test result = %#v", result)
	}
	if result.TemporaryKeyStatus != "deleted" || !strings.HasPrefix(result.TemporaryKeyName, "测试key-") {
		t.Fatalf("temporary key result = %#v", result)
	}
	if len(channels.createdKeys) != 1 || channels.createdKeys[0].GroupID == nil || *channels.createdKeys[0].GroupID != groupID || channels.createdKeys[0].Group != rate.ModelName {
		t.Fatalf("created keys = %#v", channels.createdKeys)
	}
	if len(channels.deletedKeys) != 1 || channels.deletedKeys[0] != 77 {
		t.Fatalf("deleted keys = %#v", channels.deletedKeys)
	}
	var count int64
	if err := db.Model(&storage.MainStationTemporaryAPIKey{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("temporary key records = %d, err=%v", count, err)
	}
}

func TestQuickTestRateRetriesFailedTemporaryKeyCleanup(t *testing.T) {
	service, db, _, channels := newTestService(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"test"}`))
	}))
	defer server.Close()

	channel := createTestChannel(t, db)
	channel.SiteURL = server.URL
	if err := db.Save(channel).Error; err != nil {
		t.Fatalf("save channel: %v", err)
	}
	groupID := int64(302)
	rate := &storage.RateSnapshot{ChannelID: channel.ID, RemoteGroupID: &groupID, ModelName: "source-openai", Ratio: 0.3, LastSeenAt: now}
	if err := db.Create(rate).Error; err != nil {
		t.Fatalf("create rate: %v", err)
	}
	channels.deleteKeyErr = errors.New("temporary delete failure")
	result, err := service.QuickTestRate(context.Background(), channel.ID, rate.ID, RateQuickTestInput{Platform: "openai", Model: "gpt-test"})
	if err != nil {
		t.Fatalf("quick test rate: %v", err)
	}
	if !result.Usable || result.TemporaryKeyStatus != "pending" || result.CleanupError == "" {
		t.Fatalf("cleanup pending result = %#v", result)
	}
	var count int64
	if err := db.Model(&storage.MainStationTemporaryAPIKey{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("temporary key records = %d, err=%v", count, err)
	}

	channels.deleteKeyErr = nil
	now = now.Add(2 * time.Minute)
	service.CleanupTemporaryAPIKeys(context.Background())
	if err := db.Model(&storage.MainStationTemporaryAPIKey{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("temporary key records after retry = %d, err=%v", count, err)
	}
	if len(channels.deletedKeys) != 2 {
		t.Fatalf("cleanup attempts = %#v", channels.deletedKeys)
	}
}

func TestTemporaryAPIKeyRequestUsesNewAPIGroupName(t *testing.T) {
	expiresAt := time.Date(2026, 7, 19, 12, 10, 0, 0, time.UTC)
	request, err := temporaryAPIKeyRequest(
		&storage.Channel{Type: storage.ChannelTypeNewAPI},
		&storage.RateSnapshot{ModelName: "default"},
		"测试key-ABC123",
		expiresAt,
	)
	if err != nil {
		t.Fatalf("temporary api key request: %v", err)
	}
	if request.Group != "default" || request.GroupID != nil || request.ExpiredTime == nil || *request.ExpiredTime != expiresAt.Unix() || request.RemainQuota == nil || *request.RemainQuota <= 0 {
		t.Fatalf("newapi temporary request = %#v", request)
	}
}
