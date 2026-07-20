package mainstation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fausto2022/relaydeck/backend/connector"
	"github.com/fausto2022/relaydeck/backend/connector/sub2api"
	"github.com/fausto2022/relaydeck/backend/storage"
	"gorm.io/gorm"
)

func TestSyncRefreshesSourceAPIKeyGroup(t *testing.T) {
	service, db, admin, channels, member := createSourceBindingSyncFixture(t)
	newGroupID := int64(202)
	channels.keys = []connector.APIKey{{
		ID: *member.SourceAPIKeyID, Name: "managed-key", Status: "active", GroupID: &newGroupID, GroupName: "新分组",
	}}
	now := time.Now()
	if _, err := storage.NewRates(db).Upsert(&storage.RateSnapshot{
		ChannelID: member.SourceChannelID, RemoteGroupID: &newGroupID, ModelName: "新分组", Ratio: 0.25,
		FirstSeenAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatalf("save new source rate: %v", err)
	}

	result, err := service.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync main station: %v", err)
	}
	if result.SourceBindingsChecked != 1 || result.SourceBindingsUpdated != 1 || result.SourceBindingsMissing != 0 || len(result.SourceBindingWarnings) != 0 {
		t.Fatalf("source binding result = %#v", result)
	}
	stored, err := service.store.FindMember(member.PoolID, member.ID)
	if err != nil {
		t.Fatalf("load refreshed member: %v", err)
	}
	if stored.SourceGroupID == nil || *stored.SourceGroupID != newGroupID || stored.SourceGroupName != "新分组" {
		t.Fatalf("refreshed source group = %#v", stored)
	}
	if stored.LastCostMicros != nil || stored.LastCostSource != "" || stored.LastCostAt != nil || stored.LastCostExpiresAt != nil {
		t.Fatalf("stale cost was not cleared = %#v", stored)
	}
	rate, _ := service.sourceGroupRate(stored)
	if rate == nil || *rate != 0.25 {
		t.Fatalf("refreshed source rate = %#v", rate)
	}
	if len(admin.updateRequests) != 0 {
		t.Fatalf("main station accounts must not be rewritten during read sync: %#v", admin.updateRequests)
	}
}

func TestSyncPreservesSourceGroupWhenAPIKeyIsMissing(t *testing.T) {
	service, _, _, channels, member := createSourceBindingSyncFixture(t)
	channels.keys = nil

	result, err := service.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync main station: %v", err)
	}
	if result.SourceBindingsChecked != 1 || result.SourceBindingsUpdated != 0 || result.SourceBindingsMissing != 1 || len(result.SourceBindingWarnings) != 1 {
		t.Fatalf("source binding result = %#v", result)
	}
	stored, err := service.store.FindMember(member.PoolID, member.ID)
	if err != nil {
		t.Fatalf("load preserved member: %v", err)
	}
	if stored.SourceGroupID == nil || *stored.SourceGroupID != *member.SourceGroupID || stored.SourceGroupName != member.SourceGroupName {
		t.Fatalf("source group changed after missing key: %#v", stored)
	}
}

func TestSyncRefreshesSourceAPIKeyToDefaultGroup(t *testing.T) {
	service, _, _, channels, member := createSourceBindingSyncFixture(t)
	channels.keys = []connector.APIKey{{ID: *member.SourceAPIKeyID, Name: "managed-key", Status: "active"}}

	result, err := service.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync main station: %v", err)
	}
	if result.SourceBindingsUpdated != 1 || result.SourceBindingsMissing != 0 {
		t.Fatalf("source binding result = %#v", result)
	}
	stored, err := service.store.FindMember(member.PoolID, member.ID)
	if err != nil {
		t.Fatalf("load refreshed member: %v", err)
	}
	if stored.SourceGroupID != nil || stored.SourceGroupName != "" {
		t.Fatalf("default source group = %#v", stored)
	}
}

func TestSyncPreservesSourceGroupWhenAPIKeyReadFails(t *testing.T) {
	service, _, _, channels, member := createSourceBindingSyncFixture(t)
	channels.listKeysErr = errors.New("upstream unavailable")

	result, err := service.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync main station: %v", err)
	}
	if result.SourceBindingsChecked != 1 || result.SourceBindingsUpdated != 0 || result.SourceBindingsMissing != 0 || len(result.SourceBindingWarnings) != 1 {
		t.Fatalf("source binding result = %#v", result)
	}
	stored, err := service.store.FindMember(member.PoolID, member.ID)
	if err != nil {
		t.Fatalf("load preserved member: %v", err)
	}
	if stored.SourceGroupID == nil || *stored.SourceGroupID != *member.SourceGroupID || stored.SourceGroupName != member.SourceGroupName {
		t.Fatalf("source group changed after read failure: %#v", stored)
	}
}

func createSourceBindingSyncFixture(t *testing.T) (*Service, *gorm.DB, *fakeAdminClient, *fakeChannelService, *storage.MainAccountPoolMember) {
	t.Helper()
	service, db, admin, channels := newTestService(t)
	configureTestStation(t, service)
	admin.groups = []sub2api.AdminGroup{{ID: 11, Name: "主站分组", Platform: "openai", RateMultiplier: 1, Status: "active"}}
	admin.accounts = []sub2api.AdminAccount{{
		ID: 21, Name: "主站账号", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true,
		Priority: 1, Weight: 1, Concurrency: 10, RateMultiplier: 0.5, GroupIDs: []int64{11},
	}}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	channel := createTestChannel(t, db)
	groups, err := service.ListGroups(false)
	if err != nil || len(groups) != 1 {
		t.Fatalf("main station groups = %#v, err=%v", groups, err)
	}
	pool, err := service.CreatePool(PoolInput{Name: "主站分组", Platform: "openai", TargetGroupIDs: []uint{groups[0].ID}})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	oldGroupID := int64(101)
	keyID := int64(301)
	remoteAccountID := int64(21)
	cost := int64(500000)
	observedAt := time.Now()
	expiresAt := observedAt.Add(time.Hour)
	member := &storage.MainAccountPoolMember{
		PoolID: pool.ID, AccountName: "主站账号", SourceChannelID: channel.ID,
		SourceGroupID: &oldGroupID, SourceGroupName: "旧分组", SourceAPIKeyID: &keyID,
		RemoteAccountID: &remoteAccountID, RemoteAccountName: "主站账号", OwnershipMode: "bound",
		BindingStatus: "verified", Status: "active", Enabled: true, HealthEnabled: true,
		Priority: 1, Concurrency: 10, LastCostMicros: &cost, LastCostSource: "source_rate_snapshot",
		LastCostAt: &observedAt, LastCostExpiresAt: &expiresAt,
	}
	if err := service.store.CreateMember(member); err != nil {
		t.Fatalf("create member: %v", err)
	}
	if strings.TrimSpace(member.SourceGroupName) == "" {
		t.Fatal("source group fixture is empty")
	}
	return service, db, admin, channels, member
}
