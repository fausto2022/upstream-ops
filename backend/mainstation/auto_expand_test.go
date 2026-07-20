package mainstation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fausto2022/relaydeck/backend/connector"
	"github.com/fausto2022/relaydeck/backend/connector/sub2api"
	"github.com/fausto2022/relaydeck/backend/storage"
	"gorm.io/gorm"
)

type autoExpansionTestFixture struct {
	service      *Service
	db           *gorm.DB
	admin        *fakeAdminClient
	channels     *fakeChannelService
	pool         *storage.MainAccountPool
	rate         *storage.RateSnapshot
	now          time.Time
	chatCalls    atomic.Int32
	billingCalls atomic.Int32
	chatStatus   atomic.Int32
}

func TestAutoExpansionTestsAndAddsBestProfitableCandidateOnce(t *testing.T) {
	fixture := newAutoExpansionTestFixture(t, 7000)

	fixture.service.RunAutoExpansion(context.Background())

	members, err := fixture.service.store.ListMembers(fixture.pool.ID)
	if err != nil {
		t.Fatalf("list auto-expanded members: %v", err)
	}
	if len(members) != 1 || members[0].SourceGroupID == nil || *members[0].SourceGroupID != *fixture.rate.RemoteGroupID {
		t.Fatalf("auto-expanded members = %#v", members)
	}
	if fixture.chatCalls.Load() != rateQuickTestAttempts+1 || fixture.billingCalls.Load() != 1 {
		t.Fatalf("probe calls: chat=%d billing=%d", fixture.chatCalls.Load(), fixture.billingCalls.Load())
	}
	if len(fixture.channels.createdKeys) != 2 || len(fixture.channels.deletedKeys) != 1 {
		t.Fatalf("source keys: created=%#v deleted=%#v", fixture.channels.createdKeys, fixture.channels.deletedKeys)
	}
	expectedName := fixture.service.managedAutomaticName(fixture.pool, &members[0])
	if fixture.channels.createdKeys[1].Name != expectedName {
		t.Fatalf("managed source key name = %q, want %q", fixture.channels.createdKeys[1].Name, expectedName)
	}
	if len(fixture.admin.createRequests) != 1 || fixture.admin.createRequests[0].RateMultiplier != 0.2 || fixture.admin.createRequests[0].Name != expectedName {
		t.Fatalf("main station account requests = %#v", fixture.admin.createRequests)
	}
	attempt, err := fixture.service.store.FindAutoExpansionAttempt(fixture.pool.ID, fixture.rate.ID)
	if err != nil || attempt.Status != "added" || attempt.CostMultiplierMicros != 200000 || attempt.MarginBasisPoints != 8000 {
		t.Fatalf("auto expansion attempt = %#v, err=%v", attempt, err)
	}
	updatedPool, err := fixture.service.store.FindPool(fixture.pool.ID)
	if err != nil || updatedPool.LastAutoExpandAt == nil || updatedPool.LastAutoExpandError != "" {
		t.Fatalf("updated pool = %#v, err=%v", updatedPool, err)
	}

	fixture.service.RunAutoExpansion(context.Background())
	if fixture.chatCalls.Load() != rateQuickTestAttempts+1 || len(fixture.channels.createdKeys) != 2 || len(fixture.admin.createRequests) != 1 {
		t.Fatalf("duplicate expansion ran: chat=%d keys=%d accounts=%d", fixture.chatCalls.Load(), len(fixture.channels.createdKeys), len(fixture.admin.createRequests))
	}
}

func TestAutoExpansionCoolsDownFailuresButRetriesChangedRates(t *testing.T) {
	fixture := newAutoExpansionTestFixture(t, 1000)
	fixture.chatStatus.Store(http.StatusTooManyRequests)

	fixture.service.RunAutoExpansion(context.Background())
	if fixture.chatCalls.Load() != rateQuickTestAttempts {
		t.Fatalf("first failed test calls = %d", fixture.chatCalls.Load())
	}
	attempt, err := fixture.service.store.FindAutoExpansionAttempt(fixture.pool.ID, fixture.rate.ID)
	if err != nil || attempt.Status != "failed" || attempt.NextAttemptAt == nil || !attempt.NextAttemptAt.Equal(fixture.now.Add(autoExpansionFailureCooldown)) {
		t.Fatalf("failed attempt = %#v, err=%v", attempt, err)
	}

	fixture.service.RunAutoExpansion(context.Background())
	if fixture.chatCalls.Load() != rateQuickTestAttempts {
		t.Fatalf("cooldown did not suppress retry: calls=%d", fixture.chatCalls.Load())
	}

	fixture.rate.Ratio = 0.38
	if err := fixture.saveRate(); err != nil {
		t.Fatalf("update candidate rate: %v", err)
	}
	fixture.service.RunAutoExpansion(context.Background())
	if fixture.chatCalls.Load() != rateQuickTestAttempts*2 {
		t.Fatalf("changed rate did not bypass cooldown: calls=%d", fixture.chatCalls.Load())
	}
}

func TestAutoExpansionRequiresMarginStrictlyAboveThreshold(t *testing.T) {
	fixture := newAutoExpansionTestFixture(t, 8000)

	fixture.service.RunAutoExpansion(context.Background())

	if fixture.chatCalls.Load() != 0 || len(fixture.channels.createdKeys) != 0 || len(fixture.admin.createRequests) != 0 {
		t.Fatalf("break-even threshold candidate was tested: chat=%d keys=%d accounts=%d", fixture.chatCalls.Load(), len(fixture.channels.createdKeys), len(fixture.admin.createRequests))
	}
}

func TestAutoExpansionRetriesIncompleteManagedMemberWithoutDuplicatingIt(t *testing.T) {
	fixture := newAutoExpansionTestFixture(t, 1000)
	fixture.admin.syncModelsErr = errors.New("temporary model sync failure")

	fixture.service.RunAutoExpansion(context.Background())
	members, err := fixture.service.store.ListMembers(fixture.pool.ID)
	if err != nil || len(members) != 1 || members[0].Status != "error" {
		t.Fatalf("incomplete members = %#v, err=%v", members, err)
	}
	attempt, err := fixture.service.store.FindAutoExpansionAttempt(fixture.pool.ID, fixture.rate.ID)
	if err != nil || attempt.Status != "added_error" || attempt.NextAttemptAt == nil {
		t.Fatalf("incomplete attempt = %#v, err=%v", attempt, err)
	}
	fixture.service.RunAutoExpansion(context.Background())
	if fixture.chatCalls.Load() != rateQuickTestAttempts {
		t.Fatalf("incomplete member ignored cooldown: calls=%d", fixture.chatCalls.Load())
	}

	fixture.admin.syncModelsErr = nil
	fixture.now = fixture.now.Add(autoExpansionErrorCooldown + time.Second)
	fixture.rate.LastSeenAt = fixture.now
	if err := fixture.saveRate(); err != nil {
		t.Fatalf("refresh candidate rate: %v", err)
	}
	fixture.service.RunAutoExpansion(context.Background())

	members, err = fixture.service.store.ListMembers(fixture.pool.ID)
	if err != nil || len(members) != 1 || members[0].Status != "active" {
		t.Fatalf("retried members = %#v, err=%v", members, err)
	}
	if len(fixture.channels.createdKeys) != 3 || len(fixture.admin.createRequests) != 1 {
		t.Fatalf("retry duplicated resources: keys=%#v accounts=%#v", fixture.channels.createdKeys, fixture.admin.createRequests)
	}
}

func newAutoExpansionTestFixture(t *testing.T, minimumMarginBasisPoints int64) *autoExpansionTestFixture {
	t.Helper()
	fixture := &autoExpansionTestFixture{now: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer sk-source-secret" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		switch request.URL.Path {
		case "/v1/sub2api/billing":
			fixture.billingCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"effective_rate_multiplier": 0.2})
		case "/v1/chat/completions":
			fixture.chatCalls.Add(1)
			if status := int(fixture.chatStatus.Load()); status != 0 {
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "temporary upstream failure"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": "OK"}}},
				"usage":   map[string]any{"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	service, db, admin, channels := newTestService(t)
	fixture.service = service
	fixture.admin = admin
	fixture.channels = channels
	service.now = func() time.Time { return fixture.now }
	if _, err := service.CreateConfig(context.Background(), ConfigInput{
		Name: "main", BaseURL: "https://main.example.com", AdminAPIKey: "admin-key",
		HealthModels: map[string]string{"openai": "gpt-test"},
	}); err != nil {
		t.Fatalf("configure main station: %v", err)
	}
	admin.groups = []sub2api.AdminGroup{{
		ID: 31, Name: "main-openai", Platform: "openai", Ratio: 1, RateMultiplier: 1,
		Status: "active", SubscriptionType: "standard",
	}}
	admin.accountModels = map[int64][]string{1000: {"gpt-test"}}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("sync main station: %v", err)
	}

	channel := createTestChannel(t, db)
	channel.SiteURL = server.URL
	rechargeMultiplier := 2.0
	channel.RechargeMultiplier = &rechargeMultiplier
	channel.RechargeMultiplierMode = connector.RechargeMultiplierModeDivide
	if err := db.Save(channel).Error; err != nil {
		t.Fatalf("save source channel: %v", err)
	}
	groupID := int64(301)
	channels.groups = []connector.APIKeyGroup{{ID: &groupID, Name: "OpenAI Economy", Ratio: 0.4}}
	rate := &storage.RateSnapshot{
		ChannelID: channel.ID, RemoteGroupID: &groupID, ModelName: "OpenAI Economy",
		Ratio: 0.4, LastSeenAt: fixture.now,
	}
	if err := db.Create(rate).Error; err != nil {
		t.Fatalf("create source rate: %v", err)
	}
	fixture.rate = rate

	groups, err := service.ListGroups(false)
	if err != nil || len(groups) != 1 {
		t.Fatalf("main station groups = %#v, err=%v", groups, err)
	}
	enabled := true
	workspace, err := service.UpdateGroupSettings(context.Background(), groups[0].ID, GroupSettingsInput{
		Enabled: &enabled, MinimumHealthyAccounts: 1, MinimumEffectiveConcurrency: 1,
		RateSortDirection: "asc", AutoExpandEnabled: true,
		AutoExpandMinMarginBasisPoints: minimumMarginBasisPoints,
	})
	if err != nil {
		t.Fatalf("enable automatic expansion: %v", err)
	}
	poolID, err := service.GroupPoolID(workspace.Group.ID)
	if err != nil {
		t.Fatalf("resolve group pool: %v", err)
	}
	fixture.pool, err = service.store.FindPool(poolID)
	if err != nil {
		t.Fatalf("load group pool: %v", err)
	}
	fixture.db = db
	return fixture
}

func (fixture *autoExpansionTestFixture) saveRate() error {
	return fixture.db.Save(fixture.rate).Error
}
