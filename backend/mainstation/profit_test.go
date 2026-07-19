package mainstation

import (
	"context"
	"testing"
	"time"

	"github.com/fausto2022/relaydeck/backend/connector/sub2api"
	"github.com/fausto2022/relaydeck/backend/storage"
	"gorm.io/gorm"
)

func TestProfitProtectionUsesFixedPointAndKeepsLocksIndependent(t *testing.T) {
	service, db, admin, _ := newTestService(t)
	current := time.Date(2026, 7, 17, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	service.now = func() time.Time { return current }
	pool, member, group := createProfitMember(t, service, db, admin, current, 1.2, `{"mode":"observe","minimum_margin_basis_points":0,"risk_confirmations":2,"cost_max_age_minutes":60}`)

	first, err := service.EvaluatePool(context.Background(), pool.ID, "manual")
	if err != nil {
		t.Fatalf("first evaluation: %v", err)
	}
	if len(first.Checks) != 1 || first.Checks[0].Status != "risk" || first.Checks[0].MarginBasisPoints != -2000 {
		t.Fatalf("first evaluation = %#v", first)
	}
	if len(first.ProtectionApplied) != 0 || len(admin.schedulableCalls) != 0 {
		t.Fatalf("observation mode wrote remote state: result=%#v calls=%#v", first, admin.schedulableCalls)
	}

	config, err := service.store.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if config.ObservationEvaluatedAt == nil {
		t.Fatal("observation_evaluated_at is nil")
	}
	config.AutoMarginProtection = true
	if err := service.store.SaveConfig(config); err != nil {
		t.Fatalf("enable margin protection: %v", err)
	}
	current = current.Add(5 * time.Minute)
	second, err := service.EvaluatePool(context.Background(), pool.ID, "manual")
	if err != nil {
		t.Fatalf("second evaluation: %v", err)
	}
	if len(second.ProtectionApplied) != 1 || second.ProtectionApplied[0] != member.ID {
		t.Fatalf("second evaluation = %#v", second)
	}
	locks, err := service.ListGuardLocks(*member.RemoteAccountID)
	if err != nil || len(locks) != 1 || locks[0].LockType != "margin" {
		t.Fatalf("margin locks = %#v, err=%v", locks, err)
	}
	if len(admin.schedulableCalls) != 1 || admin.schedulableCalls[0] {
		t.Fatalf("margin schedulable calls = %#v", admin.schedulableCalls)
	}
	decision, err := service.ClearGuardLock(context.Background(), *member.RemoteAccountID, "manual", "admin")
	if err != nil {
		t.Fatalf("clear missing manual lock: %v", err)
	}
	if decision.DesiredSchedulable || decision.RemoteSchedulable || len(decision.Locks) != 1 || decision.Locks[0].LockType != "margin" {
		t.Fatalf("decision after clearing missing manual lock = %#v", decision)
	}

	if _, err := service.ActivateGuardLock(context.Background(), *member.RemoteAccountID, "manual", "maintenance", nil, "admin"); err != nil {
		t.Fatalf("activate manual lock: %v", err)
	}
	if _, err := service.ClearGuardLock(context.Background(), *member.RemoteAccountID, "margin", "margin"); err != nil {
		t.Fatalf("clear margin lock: %v", err)
	}
	if admin.accounts[0].Schedulable {
		t.Fatal("account became schedulable while manual lock remained active")
	}
	remaining, err := service.ListGuardLocks(*member.RemoteAccountID)
	if err != nil || len(remaining) != 1 || remaining[0].LockType != "manual" {
		t.Fatalf("remaining locks = %#v, err=%v", remaining, err)
	}
	if _, err := service.ClearGuardLock(context.Background(), *member.RemoteAccountID, "manual", "admin"); err != nil {
		t.Fatalf("clear manual lock: %v", err)
	}
	if !admin.accounts[0].Schedulable {
		t.Fatal("account did not become schedulable after all locks were cleared")
	}
	if len(admin.schedulableCalls) != 2 || !admin.schedulableCalls[1] {
		t.Fatalf("all schedulable calls = %#v", admin.schedulableCalls)
	}

	beforeCalls := len(admin.schedulableCalls)
	admin.setSchedulableErr = context.DeadlineExceeded
	admin.applyBeforeSetError = true
	if _, err := service.ActivateGuardLock(context.Background(), *member.RemoteAccountID, "manual", "timeout validation", map[string]any{"group_id": group.ID}, "admin"); err != nil {
		t.Fatalf("activate lock with unknown write result: %v", err)
	}
	if len(admin.schedulableCalls) != beforeCalls+1 {
		t.Fatalf("unknown write was repeated: calls=%#v", admin.schedulableCalls)
	}
	if admin.accounts[0].Schedulable {
		t.Fatal("verified final remote state is still schedulable")
	}
}

func TestProfitEvaluationTreatsBreakEvenAsRisk(t *testing.T) {
	service, db, admin, _ := newTestService(t)
	current := time.Date(2026, 7, 17, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	service.now = func() time.Time { return current }
	pool, _, _ := createProfitMember(t, service, db, admin, current, 1, `{"mode":"observe","minimum_margin_basis_points":0,"risk_confirmations":2,"cost_max_age_minutes":60}`)

	result, err := service.EvaluatePool(context.Background(), pool.ID, "manual")
	if err != nil {
		t.Fatalf("evaluate break-even margin: %v", err)
	}
	if len(result.Checks) != 1 || result.Checks[0].Status != "risk" || result.Checks[0].MarginBasisPoints != 0 {
		t.Fatalf("break-even evaluation = %#v", result)
	}
}

func TestProfitEvaluationTreatsMinimumPositiveMarginAsHealthy(t *testing.T) {
	service, db, admin, _ := newTestService(t)
	current := time.Date(2026, 7, 17, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	service.now = func() time.Time { return current }
	pool, _, _ := createProfitMember(t, service, db, admin, current, 0.95, `{"mode":"observe","minimum_margin_basis_points":500,"risk_confirmations":2,"cost_max_age_minutes":60}`)

	result, err := service.EvaluatePool(context.Background(), pool.ID, "manual")
	if err != nil {
		t.Fatalf("evaluate minimum positive margin: %v", err)
	}
	if len(result.Checks) != 1 || result.Checks[0].Status != "healthy" || result.Checks[0].MarginBasisPoints != 500 {
		t.Fatalf("minimum positive margin evaluation = %#v", result)
	}
}

func TestUnsupportedPricingAllowsStandardUsageGroups(t *testing.T) {
	for _, subscriptionType := range []string{"", "standard", "usage", "token", "payg"} {
		group := &storage.UpstreamSyncTargetGroup{SubscriptionType: subscriptionType}
		if unsupportedPricing(group) {
			t.Fatalf("subscription type %q should support profit evaluation", subscriptionType)
		}
	}
	for _, subscriptionType := range []string{"subscription", "monthly", "quota"} {
		group := &storage.UpstreamSyncTargetGroup{SubscriptionType: subscriptionType}
		if !unsupportedPricing(group) {
			t.Fatalf("subscription type %q should not support profit evaluation", subscriptionType)
		}
	}
	if !unsupportedPricing(&storage.UpstreamSyncTargetGroup{ImageSeparateRate: true}) ||
		!unsupportedPricing(&storage.UpstreamSyncTargetGroup{VideoSeparateRate: true}) {
		t.Fatal("separate image or video pricing should not support automatic profit evaluation")
	}
}

func TestProfitEvaluationDoesNotProtectExpiredOrUnsupportedPricing(t *testing.T) {
	service, db, admin, _ := newTestService(t)
	current := time.Date(2026, 7, 17, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	service.now = func() time.Time { return current }
	pool, member, group := createProfitMember(t, service, db, admin, current.Add(-2*time.Hour), 1.2, `{"mode":"observe","minimum_margin_basis_points":0,"risk_confirmations":2,"cost_max_age_minutes":60}`)

	expired, err := service.EvaluatePool(context.Background(), pool.ID, "manual")
	if err != nil {
		t.Fatalf("expired evaluation: %v", err)
	}
	if expired.Checks[0].Status != "unknown" || len(expired.ProtectionApplied) != 0 {
		t.Fatalf("expired evaluation = %#v", expired.Checks[0])
	}

	if err := db.Model(&storage.RateSnapshot{}).
		Where("channel_id = ?", member.SourceChannelID).
		Update("last_seen_at", current).Error; err != nil {
		t.Fatalf("refresh source rate: %v", err)
	}
	if err := db.Model(&storage.UpstreamSyncTargetGroup{}).
		Where("id = ?", group.ID).
		Update("subscription_type", "subscription").Error; err != nil {
		t.Fatalf("mark subscription group: %v", err)
	}
	unsupported, err := service.EvaluatePool(context.Background(), pool.ID, "manual")
	if err != nil {
		t.Fatalf("unsupported evaluation: %v", err)
	}
	if unsupported.Checks[0].Status != "unsupported" || len(admin.schedulableCalls) != 0 {
		t.Fatalf("unsupported evaluation = %#v calls=%#v", unsupported.Checks[0], admin.schedulableCalls)
	}
}

func createProfitMember(t *testing.T, service *Service, db *gorm.DB, admin *fakeAdminClient, rateObservedAt time.Time, costRatio float64, marginPolicy string) (*storage.MainAccountPool, *storage.MainAccountPoolMember, *storage.UpstreamSyncTargetGroup) {
	t.Helper()
	configureTestStation(t, service)
	admin.groups = []sub2api.AdminGroup{{ID: 101, Name: "sale-group", RateMultiplier: 1, Status: "active"}}
	admin.accounts = []sub2api.AdminAccount{{
		ID: 201, Name: "remote", Status: "active", Schedulable: true, RateMultiplier: 0.7,
		Credentials: map[string]any{"base_url": "https://source.example.com", "api_key": "***masked***"},
	}}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("sync station: %v", err)
	}
	channel := createTestChannel(t, db)
	channel.SiteURL = "https://source.example.com"
	if err := db.Save(channel).Error; err != nil {
		t.Fatalf("update channel: %v", err)
	}
	sourceGroupID := int64(301)
	if _, err := service.rates.Upsert(&storage.RateSnapshot{
		ChannelID: channel.ID, RemoteGroupID: &sourceGroupID, ModelName: "source-group",
		Ratio: costRatio, LastSeenAt: rateObservedAt,
	}); err != nil {
		t.Fatalf("save source rate: %v", err)
	}
	groups, err := service.ListGroups(false)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %#v, err=%v", groups, err)
	}
	poolDTO, err := service.CreatePool(PoolInput{
		Name: "profit-pool", Platform: "openai", TargetGroupIDs: []uint{groups[0].ID}, MarginPolicy: marginPolicy,
	})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	remoteID := int64(201)
	member, err := service.CreateMember(context.Background(), poolDTO.ID, MemberInput{
		OwnershipMode: "bound", SourceChannelID: channel.ID, SourceGroupID: &sourceGroupID,
		SourceGroupName: "source-group", RemoteAccountID: &remoteID, ManualBindingConfirmed: true,
		Enabled: boolPtr(true), HealthEnabled: boolPtr(false), CostAdjustment: 1,
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	pool, err := service.store.FindPool(poolDTO.ID)
	if err != nil {
		t.Fatalf("find pool: %v", err)
	}
	group, err := service.targetGroups.FindByID(groups[0].ID)
	if err != nil {
		t.Fatalf("find group: %v", err)
	}
	return pool, member, group
}
