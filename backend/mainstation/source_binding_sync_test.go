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
	channels.groups = []connector.APIKeyGroup{{ID: &newGroupID, Name: "新分组", Ratio: 0.25}}
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

func TestSyncDoesNotPollModelsForUnchangedManagedAccount(t *testing.T) {
	service, _, admin, channels, member := createSourceBindingSyncFixture(t)
	member.OwnershipMode = "managed"
	member.SourceAPIKeyManaged = true
	member.AccountName = "source-旧分组"
	member.RemoteAccountName = "source-旧分组"
	if err := service.store.UpdateMember(member); err != nil {
		t.Fatalf("update managed member fixture: %v", err)
	}
	admin.accounts[0].Name = "source-旧分组"
	channels.keys = []connector.APIKey{{
		ID: *member.SourceAPIKeyID, Name: "旧分组", Status: "active",
		GroupID: member.SourceGroupID, GroupName: member.SourceGroupName,
	}}
	channels.groups = []connector.APIKeyGroup{{ID: member.SourceGroupID, Name: member.SourceGroupName, Ratio: 0.5}}

	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("sync unchanged managed account: %v", err)
	}
	if len(admin.listModelCalls) != 0 || len(admin.syncModelCalls) != 0 {
		t.Fatalf("unchanged account model calls: list=%v sync=%v", admin.listModelCalls, admin.syncModelCalls)
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

func TestSyncRenamesManagedKeyAndMainAccountAfterSourceGroupRename(t *testing.T) {
	service, _, admin, channels, member := createSourceBindingSyncFixture(t)
	member.OwnershipMode = "managed"
	member.SourceAPIKeyManaged = true
	member.AccountName = "source-旧分组"
	member.RemoteAccountName = "source-旧分组"
	if err := service.store.UpdateMember(member); err != nil {
		t.Fatalf("update managed member fixture: %v", err)
	}
	admin.accounts[0].Name = "source-旧分组"
	channels.keys = []connector.APIKey{{
		ID: *member.SourceAPIKeyID, Name: "旧分组", Status: "active", GroupID: member.SourceGroupID, GroupName: "旧分组",
	}}
	channels.groups = []connector.APIKeyGroup{{ID: member.SourceGroupID, Name: "新分组", Ratio: 0.25}}

	result, err := service.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync renamed source group: %v", err)
	}
	if result.SourceBindingsUpdated != 1 || result.SourceBindingsRenamed != 1 || result.SourceBindingsCleaned != 0 {
		t.Fatalf("source binding result = %#v", result)
	}
	if len(channels.updatedKeys) != 1 || channels.updatedKeys[0].Name == nil || *channels.updatedKeys[0].Name != "新分组" {
		t.Fatalf("updated source keys = %#v", channels.updatedKeys)
	}
	if len(admin.updateRequests) != 1 || admin.updateRequests[0].Name != "source-新分组" {
		t.Fatalf("updated main station accounts = %#v", admin.updateRequests)
	}
	stored, err := service.store.FindMember(member.PoolID, member.ID)
	if err != nil {
		t.Fatalf("load renamed member: %v", err)
	}
	if stored.SourceGroupName != "新分组" || stored.AccountName != "source-新分组" || stored.RemoteAccountName != "source-新分组" {
		t.Fatalf("renamed member = %#v", stored)
	}
}

func TestSyncCleansManagedResourcesWhenSourceKeyLosesGroup(t *testing.T) {
	service, _, admin, channels, member := createSourceBindingSyncFixture(t)
	member.OwnershipMode = "managed"
	member.SourceAPIKeyManaged = true
	member.AccountName = "source-旧分组"
	if err := service.store.UpdateMember(member); err != nil {
		t.Fatalf("update managed member fixture: %v", err)
	}
	channels.keys = []connector.APIKey{{ID: *member.SourceAPIKeyID, Name: "旧分组", Status: "active"}}
	channels.groups = []connector.APIKeyGroup{}

	result, err := service.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync unbound managed source key: %v", err)
	}
	if result.SourceBindingsCleaned != 1 || len(admin.deletedAccounts) != 1 || len(channels.deletedKeys) != 1 {
		t.Fatalf("cleanup result=%#v accounts=%v keys=%v", result, admin.deletedAccounts, channels.deletedKeys)
	}
	if _, err := service.store.FindMember(member.PoolID, member.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cleaned member lookup error = %v", err)
	}
	snapshot, err := service.store.FindAccountSnapshot(*member.RemoteAccountID)
	if err != nil || !snapshot.Missing {
		t.Fatalf("cleaned account snapshot = %#v, err=%v", snapshot, err)
	}
}

func TestSyncNeverCleansManuallySelectedKeyWhenSourceGroupIsMissing(t *testing.T) {
	service, _, admin, channels, member := createSourceBindingSyncFixture(t)
	member.OwnershipMode = "managed"
	member.SourceAPIKeyManaged = false
	member.AccountName = "人工账号"
	if err := service.store.UpdateMember(member); err != nil {
		t.Fatalf("update manual key fixture: %v", err)
	}
	channels.keys = []connector.APIKey{{
		ID: *member.SourceAPIKeyID, Name: "人工-Key", Status: "active", GroupID: member.SourceGroupID, GroupName: member.SourceGroupName,
	}}
	channels.groups = []connector.APIKeyGroup{}

	result, err := service.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync missing manual source group: %v", err)
	}
	if result.SourceBindingsCleaned != 0 || len(admin.deletedAccounts) != 0 || len(channels.deletedKeys) != 0 || len(result.SourceBindingWarnings) == 0 {
		t.Fatalf("manual key cleanup result=%#v accounts=%v keys=%v", result, admin.deletedAccounts, channels.deletedKeys)
	}
	if _, err := service.store.FindMember(member.PoolID, member.ID); err != nil {
		t.Fatalf("manual key member was removed: %v", err)
	}
}

func TestSyncCleansUnusedManagedKeyWhenMainAccountWasRemoved(t *testing.T) {
	service, _, admin, channels, member := createSourceBindingSyncFixture(t)
	member.OwnershipMode = "managed"
	member.SourceAPIKeyManaged = true
	if err := service.store.UpdateMember(member); err != nil {
		t.Fatalf("update managed member fixture: %v", err)
	}
	channels.keys = []connector.APIKey{{
		ID: *member.SourceAPIKeyID, Name: "旧分组", Status: "active", GroupID: member.SourceGroupID, GroupName: member.SourceGroupName,
	}}
	channels.groups = []connector.APIKeyGroup{{ID: member.SourceGroupID, Name: member.SourceGroupName, Ratio: 0.5}}
	admin.accounts = nil

	result, err := service.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync removed main account: %v", err)
	}
	if result.SourceBindingsCleaned != 1 || len(channels.deletedKeys) != 1 {
		t.Fatalf("unused managed key cleanup result=%#v keys=%v", result, channels.deletedKeys)
	}
	if _, err := service.store.FindMember(member.PoolID, member.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cleaned member lookup error = %v", err)
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
