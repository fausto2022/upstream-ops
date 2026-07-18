package mainstation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/bejix/upstream-ops/backend/storage"
	"gorm.io/gorm"
)

func TestHealthChecksUseControlledOutputLimitsAndClassifyFailures(t *testing.T) {
	responseMode := "success"
	l0Calls := 0
	l1Calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-source-secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/v1/sub2api/billing":
			l0Calls++
			_ = json.NewEncoder(w).Encode(map[string]any{"effective_rate_multiplier": 0.8})
		case "/v1/chat/completions":
			l1Calls++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode l1 body: %v", err)
			}
			if body["model"] != "gpt-test" || body["max_tokens"] != float64(4) || body["stream"] != false {
				t.Fatalf("l1 body = %#v", body)
			}
			switch responseMode {
			case "success":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []map[string]any{{"message": map[string]any{"content": "OK"}}},
					"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6},
				})
			case "limit":
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "max_tokens must be at least 16"})
			case "rate_limit":
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "rate limited"})
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	service, db, admin, _ := newTestService(t)
	member := createHealthMember(t, service, db, admin, server.URL, `{"mode":"observe","l0_interval_minutes":5,"l1_interval_minutes":30,"l2_interval_minutes":720,"daily_l1_limit":48,"daily_l2_limit":2}`)

	l0, err := service.CheckMember(context.Background(), member.PoolID, member.ID, HealthCheckInput{Level: "L0", Force: true})
	if err != nil {
		t.Fatalf("L0: %v", err)
	}
	if l0.Check.Status != "success" || l0.Check.Protocol != "sub2api_billing" || l0.Check.TotalTokens != nil || l0Calls != 1 {
		t.Fatalf("L0 result = %#v, calls=%d", l0.Check, l0Calls)
	}

	l1, err := service.CheckMember(context.Background(), member.PoolID, member.ID, HealthCheckInput{Level: "L1", Force: true})
	if err != nil {
		t.Fatalf("L1: %v", err)
	}
	if l1.Check.Status != "success" || l1.Check.TotalTokens == nil || *l1.Check.TotalTokens != 6 || l1Calls != 1 {
		t.Fatalf("L1 result = %#v, calls=%d", l1.Check, l1Calls)
	}

	responseMode = "limit"
	limitResult, err := service.CheckMember(context.Background(), member.PoolID, member.ID, HealthCheckInput{Level: "L1", Force: true})
	if err != nil {
		t.Fatalf("L1 incompatible: %v", err)
	}
	if limitResult.Check.Status != "config_error" || limitResult.Check.ErrorClass != "output_limit_incompatible" {
		t.Fatalf("limit result = %#v", limitResult.Check)
	}
	if limitResult.Member.ConsecutiveHealthFailure != 0 || limitResult.Member.LastHealthStatus != "config_error" {
		t.Fatalf("limit member = %#v", limitResult.Member)
	}

	responseMode = "rate_limit"
	rateLimitResult, err := service.CheckMember(context.Background(), member.PoolID, member.ID, HealthCheckInput{Level: "L1", Force: true})
	if err != nil {
		t.Fatalf("L1 rate limit: %v", err)
	}
	if rateLimitResult.Check.ErrorClass != "rate_limited" || rateLimitResult.Member.CooldownUntil == nil {
		t.Fatalf("rate limit result = %#v member=%#v", rateLimitResult.Check, rateLimitResult.Member)
	}
	if rateLimitResult.Member.ConsecutiveHealthFailure != 0 || rateLimitResult.Member.Status != "degraded" {
		t.Fatalf("rate limit member = %#v", rateLimitResult.Member)
	}

	l2, err := service.CheckMember(context.Background(), member.PoolID, member.ID, HealthCheckInput{Level: "L2", Force: true})
	if err != nil {
		t.Fatalf("L2: %v", err)
	}
	if l2.Check.Status != "success" || l2.Check.Protocol != "sub2api_account_test" || l2.Check.RemoteAccountID != 21 {
		t.Fatalf("L2 result = %#v", l2.Check)
	}
}

func TestGlobalHealthModelCatalogAndInheritance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/models":
			if request.Header.Get("Authorization") != "Bearer sk-source-secret" {
				t.Fatalf("model authorization = %q", request.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "gpt-global"}, {"id": "gpt-other"}}})
		case "/v1/chat/completions":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode health body: %v", err)
			}
			if body["model"] != "gpt-global" {
				t.Fatalf("health model = %#v, want gpt-global", body["model"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": "OK"}}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	service, db, admin, _ := newTestService(t)
	member := createHealthMember(t, service, db, admin, server.URL, "")
	if err := db.Model(member).Update("health_model", "").Error; err != nil {
		t.Fatalf("clear member health model: %v", err)
	}
	config, err := service.UpdateConfig(context.Background(), ConfigInput{HealthModels: map[string]string{"OpenAI": "gpt-global"}})
	if err != nil {
		t.Fatalf("save global health model: %v", err)
	}
	if config.HealthModels["openai"] != "gpt-global" {
		t.Fatalf("health models = %#v", config.HealthModels)
	}
	catalogs, err := service.ListHealthModelCatalogs(context.Background())
	if err != nil {
		t.Fatalf("list health model catalogs: %v", err)
	}
	var openAICatalog *HealthModelCatalog
	for i := range catalogs {
		if catalogs[i].Platform == "openai" {
			openAICatalog = &catalogs[i]
			break
		}
	}
	if openAICatalog == nil || openAICatalog.Error != "" || !containsString(openAICatalog.Models, "gpt-global") || !containsString(openAICatalog.Models, "gpt-other") {
		t.Fatalf("health model catalogs = %#v", catalogs)
	}
	result, err := service.CheckMember(context.Background(), member.PoolID, member.ID, HealthCheckInput{Level: "L1", Force: true})
	if err != nil {
		t.Fatalf("global model health check: %v", err)
	}
	if result.Check.Model != "gpt-global" || result.Check.Status != "success" {
		t.Fatalf("global model result = %#v", result.Check)
	}
}

func TestHealthModelCatalogUsesUnboundMainStationAccount(t *testing.T) {
	service, _, admin, _ := newTestService(t)
	configureTestStation(t, service)
	admin.groups = []sub2api.AdminGroup{{ID: 12, Name: "Claude", Platform: "anthropic", Status: "active"}}
	admin.accounts = []sub2api.AdminAccount{{
		ID: 31, Name: "claude-upstream", Platform: "anthropic", Status: "active", GroupIDs: []int64{12},
	}}
	admin.accountModels = map[int64][]string{31: {"claude-sonnet-4-5", "claude-haiku-4-5"}}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("sync station: %v", err)
	}

	catalogs, err := service.ListHealthModelCatalogs(context.Background())
	if err != nil {
		t.Fatalf("list catalogs: %v", err)
	}
	var anthropic *HealthModelCatalog
	for i := range catalogs {
		if catalogs[i].Platform == "anthropic" {
			anthropic = &catalogs[i]
			break
		}
	}
	if anthropic == nil || anthropic.Error != "" || !containsString(anthropic.Models, "claude-sonnet-4-5") || !containsString(anthropic.Models, "claude-haiku-4-5") || len(admin.syncModelCalls) != 1 || admin.syncModelCalls[0] != 31 {
		t.Fatalf("anthropic catalog = %#v, sync calls = %#v", anthropic, admin.syncModelCalls)
	}
}

func TestHealthModelCatalogHasBuiltinsWithoutAccounts(t *testing.T) {
	service, _, _, _ := newTestService(t)
	configureTestStation(t, service)
	catalogs, err := service.ListHealthModelCatalogs(context.Background())
	if err != nil {
		t.Fatalf("list builtin catalogs: %v", err)
	}
	var anthropic *HealthModelCatalog
	for i := range catalogs {
		if catalogs[i].Platform == "anthropic" {
			anthropic = &catalogs[i]
			break
		}
	}
	if anthropic == nil || anthropic.Error != "" || len(anthropic.Models) == 0 || !containsString(anthropic.Models, "claude-3-5-sonnet-20241022") {
		t.Fatalf("builtin anthropic catalog = %#v", anthropic)
	}
	var grok *HealthModelCatalog
	for i := range catalogs {
		if catalogs[i].Platform == "grok" {
			grok = &catalogs[i]
			break
		}
	}
	if grok == nil || grok.Error != "" || !containsString(grok.Models, "grok-4.5") || normalizeHealthPlatform("xai") != "grok" {
		t.Fatalf("builtin grok catalog = %#v", grok)
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func TestHealthBudgetStopsNonEssentialProbe(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "OK"}}},
			"usage":   map[string]any{"total_tokens": 4},
		})
	}))
	defer server.Close()

	service, db, admin, _ := newTestService(t)
	member := createHealthMember(t, service, db, admin, server.URL, `{"mode":"observe","l1_interval_minutes":30,"daily_l1_limit":1,"daily_l2_limit":1}`)
	current := time.Date(2026, 7, 17, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	service.now = func() time.Time { return current }

	first, err := service.CheckMember(context.Background(), member.PoolID, member.ID, HealthCheckInput{Level: "L1"})
	if err != nil || first.Check.Status != "success" {
		t.Fatalf("first check = %#v, err=%v", first, err)
	}
	current = current.Add(31 * time.Minute)
	second, err := service.CheckMember(context.Background(), member.PoolID, member.ID, HealthCheckInput{Level: "L1"})
	if err != nil {
		t.Fatalf("second check: %v", err)
	}
	if second.Check.Status != "skipped_budget" || second.Check.ErrorClass != "budget_exceeded" {
		t.Fatalf("second check = %#v", second.Check)
	}
	if callCount != 1 {
		t.Fatalf("probe calls = %d, want 1", callCount)
	}
}

func TestImmediateCredentialFailureQuarantinesInObservationMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "invalid token"})
	}))
	defer server.Close()

	service, db, admin, _ := newTestService(t)
	member := createHealthMember(t, service, db, admin, server.URL, "")
	result, err := service.CheckMember(context.Background(), member.PoolID, member.ID, HealthCheckInput{Level: "L0", Force: true})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if result.Check.ErrorClass != "auth_invalid" || result.Member.LastHealthStatus != "unhealthy" || result.Member.Status != "quarantined" {
		t.Fatalf("result = %#v member=%#v", result.Check, result.Member)
	}
}

func TestPreferredMemberAutomaticallyPausesAndRecovers(t *testing.T) {
	responseOK := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !responseOK {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "invalid token"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"effective_rate_multiplier": 0.8})
	}))
	defer server.Close()

	service, db, admin, _ := newTestService(t)
	member := createHealthMember(t, service, db, admin, server.URL, "")
	member.Preferred = true
	if err := db.Save(member).Error; err != nil {
		t.Fatalf("mark preferred: %v", err)
	}
	if _, err := service.CheckMember(context.Background(), member.PoolID, member.ID, HealthCheckInput{Level: "L0", Force: true}); err != nil {
		t.Fatalf("preferred failure: %v", err)
	}
	locks, err := service.ListGuardLocks(*member.RemoteAccountID)
	if err != nil || len(locks) != 1 || locks[0].LockType != "health" {
		t.Fatalf("preferred health locks = %#v, err=%v", locks, err)
	}
	if admin.accounts[0].Schedulable {
		t.Fatal("preferred unhealthy account remained schedulable")
	}

	responseOK = true
	for attempt := 1; attempt <= 3; attempt++ {
		if _, err := service.CheckMember(context.Background(), member.PoolID, member.ID, HealthCheckInput{Level: "L0", Force: true}); err != nil {
			t.Fatalf("preferred recovery attempt %d: %v", attempt, err)
		}
	}
	locks, err = service.ListGuardLocks(*member.RemoteAccountID)
	if err != nil || len(locks) != 0 {
		t.Fatalf("preferred recovery locks = %#v, err=%v", locks, err)
	}
	if !admin.accounts[0].Schedulable {
		t.Fatal("preferred recovered account did not resume scheduling")
	}
}

func TestScheduledHealthChecksContinueForDisabledMemberUntilHealthIsDisabled(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"effective_rate_multiplier": 0.8})
	}))
	defer server.Close()

	service, db, admin, _ := newTestService(t)
	member := createHealthMember(t, service, db, admin, server.URL, `{"l0_interval_minutes":1,"jitter_percent":0}`)
	member.Enabled = false
	member.Status = "disabled"
	member.HealthModel = ""
	if err := db.Save(member).Error; err != nil {
		t.Fatalf("disable member: %v", err)
	}
	current := member.CreatedAt.Add(2 * time.Minute)
	service.now = func() time.Time { return current }
	service.RunDueHealthChecks(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("disabled member probe calls = %d, want 1", calls.Load())
	}

	if err := db.Model(member).Update("health_enabled", false).Error; err != nil {
		t.Fatalf("disable health checks: %v", err)
	}
	current = current.Add(2 * time.Minute)
	service.RunDueHealthChecks(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("health-disabled member probe calls = %d, want 1", calls.Load())
	}
}

func TestHealthRecoveryClearsOnlyHealthLockAfterThreshold(t *testing.T) {
	responseOK := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !responseOK {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "invalid token"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"effective_rate_multiplier": 0.8})
	}))
	defer server.Close()

	service, db, admin, _ := newTestService(t)
	member := createHealthMember(t, service, db, admin, server.URL, "")
	if _, err := service.CheckMember(context.Background(), member.PoolID, member.ID, HealthCheckInput{Level: "L0", Force: true}); err != nil {
		t.Fatalf("observation failure: %v", err)
	}
	config, err := service.store.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if config.HealthObservedAt == nil {
		t.Fatal("health observation evidence was not recorded")
	}
	config.AutoHealthProtection = true
	config.AutoRecovery = true
	if err := service.store.SaveConfig(config); err != nil {
		t.Fatalf("enable health protection: %v", err)
	}
	if _, err := service.CheckMember(context.Background(), member.PoolID, member.ID, HealthCheckInput{Level: "L0", Force: true}); err != nil {
		t.Fatalf("protected failure: %v", err)
	}
	locks, err := service.ListGuardLocks(*member.RemoteAccountID)
	if err != nil || len(locks) != 1 || locks[0].LockType != "health" {
		t.Fatalf("health locks = %#v, err=%v", locks, err)
	}
	if admin.accounts[0].Schedulable {
		t.Fatal("health lock did not disable scheduling")
	}
	if _, err := service.ActivateGuardLock(context.Background(), *member.RemoteAccountID, "margin", "risk", nil, "margin"); err != nil {
		t.Fatalf("activate margin lock: %v", err)
	}
	if _, err := service.ActivateGuardLock(context.Background(), *member.RemoteAccountID, "manual", "maintenance", nil, "admin"); err != nil {
		t.Fatalf("activate manual lock: %v", err)
	}

	responseOK = true
	for attempt := 1; attempt <= 3; attempt++ {
		result, err := service.CheckMember(context.Background(), member.PoolID, member.ID, HealthCheckInput{Level: "L0", Force: true})
		if err != nil {
			t.Fatalf("recovery attempt %d: %v", attempt, err)
		}
		if attempt < 3 && result.Member.LastHealthStatus == "healthy" {
			t.Fatalf("member recovered too early on attempt %d", attempt)
		}
	}
	locks, err = service.ListGuardLocks(*member.RemoteAccountID)
	if err != nil {
		t.Fatalf("list remaining locks: %v", err)
	}
	if len(locks) != 2 || locks[0].LockType != "manual" || locks[1].LockType != "margin" {
		t.Fatalf("remaining locks = %#v", locks)
	}
	if admin.accounts[0].Schedulable {
		t.Fatal("account became schedulable while margin/manual locks remained")
	}
}

func createHealthMember(t *testing.T, service *Service, db *gorm.DB, admin *fakeAdminClient, upstreamURL, healthPolicy string) *storage.MainAccountPoolMember {
	t.Helper()
	configureTestStation(t, service)
	admin.groups = []sub2api.AdminGroup{{ID: 11, Name: "main-group", RateMultiplier: 1, Status: "active"}}
	admin.accounts = []sub2api.AdminAccount{{
		ID: 21, Name: "remote", Status: "active", Schedulable: true,
		Credentials: map[string]any{"base_url": upstreamURL, "api_key": "***masked***"},
	}}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("sync station: %v", err)
	}
	channel := createTestChannel(t, db)
	channel.SiteURL = upstreamURL
	if err := db.Save(channel).Error; err != nil {
		t.Fatalf("update channel url: %v", err)
	}
	groups, err := service.ListGroups(false)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %#v, err=%v", groups, err)
	}
	pool, err := service.CreatePool(PoolInput{
		Name: "health-pool", Platform: "openai", TargetGroupIDs: []uint{groups[0].ID}, HealthPolicy: healthPolicy,
	})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	remoteID := int64(21)
	keyID := int64(77)
	member, err := service.CreateMember(context.Background(), pool.ID, MemberInput{
		OwnershipMode: "bound", SourceChannelID: channel.ID, SourceAPIKeyID: &keyID,
		RemoteAccountID: &remoteID, ManualBindingConfirmed: true, Enabled: boolPtr(true),
		HealthEnabled: boolPtr(true), HealthModel: "gpt-test", HealthAPIMode: "openai_chat",
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	return member
}
