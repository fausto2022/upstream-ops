package mainstation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/bejix/upstream-ops/backend/crypto"
	"github.com/bejix/upstream-ops/backend/storage"
	"gorm.io/gorm"
)

type fakeAdminClient struct {
	pingErr             error
	groups              []sub2api.AdminGroup
	accounts            []sub2api.AdminAccount
	createRequests      []sub2api.AdminAccount
	updateRequests      []sub2api.AdminAccount
	schedulingUpdates   []sub2api.AdminAccountSchedulingUpdate
	schedulableCalls    []bool
	setSchedulableErr   error
	applyBeforeSetError bool
	deletedAccounts     []int64
	nextAccountID       int64
	accountModels       map[int64][]string
	syncModelCalls      []int64
}

func (f *fakeAdminClient) Ping(context.Context, sub2api.AdminTarget) error { return f.pingErr }
func (f *fakeAdminClient) ListGroups(context.Context, sub2api.AdminTarget, bool) ([]sub2api.AdminGroup, error) {
	return append([]sub2api.AdminGroup(nil), f.groups...), nil
}
func (f *fakeAdminClient) ListAllAccounts(context.Context, sub2api.AdminTarget) ([]sub2api.AdminAccount, error) {
	return append([]sub2api.AdminAccount(nil), f.accounts...), nil
}
func (f *fakeAdminClient) ListGroupRateMultipliers(context.Context, sub2api.AdminTarget, int64) ([]float64, error) {
	return []float64{}, nil
}
func (f *fakeAdminClient) FindAccountByName(_ context.Context, _ sub2api.AdminTarget, name string) (*sub2api.AdminAccount, error) {
	for i := range f.accounts {
		if f.accounts[i].Name == name {
			item := f.accounts[i]
			return &item, nil
		}
	}
	return nil, nil
}
func (f *fakeAdminClient) GetAccount(_ context.Context, _ sub2api.AdminTarget, id int64) (*sub2api.AdminAccount, error) {
	for i := range f.accounts {
		if f.accounts[i].ID == id {
			item := f.accounts[i]
			return &item, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}
func (f *fakeAdminClient) CreateAccount(_ context.Context, _ sub2api.AdminTarget, req sub2api.AdminAccount) (*sub2api.AdminAccount, error) {
	f.createRequests = append(f.createRequests, req)
	if f.nextAccountID == 0 {
		f.nextAccountID = 1000
	}
	req.ID = f.nextAccountID
	f.nextAccountID++
	f.accounts = append(f.accounts, req)
	item := req
	return &item, nil
}
func (f *fakeAdminClient) UpdateAccount(_ context.Context, _ sub2api.AdminTarget, id int64, req sub2api.AdminAccount) (*sub2api.AdminAccount, error) {
	f.updateRequests = append(f.updateRequests, req)
	req.ID = id
	for i := range f.accounts {
		if f.accounts[i].ID == id {
			f.accounts[i] = req
			item := req
			return &item, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}
func (f *fakeAdminClient) UpdateAccountScheduling(_ context.Context, _ sub2api.AdminTarget, id int64, req sub2api.AdminAccountSchedulingUpdate) (*sub2api.AdminAccount, error) {
	f.schedulingUpdates = append(f.schedulingUpdates, req)
	for i := range f.accounts {
		if f.accounts[i].ID == id {
			f.accounts[i].Concurrency = req.Concurrency
			f.accounts[i].Priority = req.Priority
			f.accounts[i].Weight = req.LoadFactor
			f.accounts[i].LoadFactor = float64(req.LoadFactor)
			item := f.accounts[i]
			return &item, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}
func (f *fakeAdminClient) SetAccountSchedulable(_ context.Context, _ sub2api.AdminTarget, id int64, schedulable bool) (*sub2api.AdminAccount, error) {
	f.schedulableCalls = append(f.schedulableCalls, schedulable)
	if f.setSchedulableErr != nil && !f.applyBeforeSetError {
		return nil, f.setSchedulableErr
	}
	for i := range f.accounts {
		if f.accounts[i].ID == id {
			f.accounts[i].Schedulable = schedulable
			if f.setSchedulableErr != nil {
				return nil, f.setSchedulableErr
			}
			item := f.accounts[i]
			return &item, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}
func (f *fakeAdminClient) DeleteAccount(_ context.Context, _ sub2api.AdminTarget, id int64) error {
	f.deletedAccounts = append(f.deletedAccounts, id)
	return nil
}
func (f *fakeAdminClient) SyncAccountModelsFromUpstream(_ context.Context, _ sub2api.AdminTarget, id int64) ([]string, error) {
	f.syncModelCalls = append(f.syncModelCalls, id)
	models := f.accountModels[id]
	if len(models) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return append([]string(nil), models...), nil
}
func (f *fakeAdminClient) ListAccountModels(_ context.Context, _ sub2api.AdminTarget, id int64) ([]string, error) {
	models := f.accountModels[id]
	if len(models) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return append([]string(nil), models...), nil
}
func (f *fakeAdminClient) TestAccount(context.Context, sub2api.AdminTarget, int64, string) (*sub2api.AdminAccountTestResult, error) {
	return &sub2api.AdminAccountTestResult{ResponseText: "ok"}, nil
}

type fakeChannelService struct {
	secret      string
	groups      []connector.APIKeyGroup
	createdKeys []connector.APIKeyCreateRequest
	deletedKeys []int64
	concurrency int
}

func (f *fakeChannelService) RevealAPIKey(context.Context, uint, int64) (string, error) {
	return f.secret, nil
}
func (f *fakeChannelService) CreateAPIKey(_ context.Context, _ uint, req connector.APIKeyCreateRequest) (*connector.APIKey, error) {
	f.createdKeys = append(f.createdKeys, req)
	return &connector.APIKey{ID: 77, Key: f.secret, Name: req.Name}, nil
}
func (f *fakeChannelService) DeleteAPIKey(_ context.Context, _ uint, id int64) error {
	f.deletedKeys = append(f.deletedKeys, id)
	return nil
}
func (f *fakeChannelService) ListAPIKeyGroups(context.Context, uint) ([]connector.APIKeyGroup, error) {
	return append([]connector.APIKeyGroup(nil), f.groups...), nil
}
func (f *fakeChannelService) GetAccountLimits(context.Context, uint) (*connector.AccountLimits, error) {
	concurrency := f.concurrency
	if concurrency <= 0 {
		concurrency = 10
	}
	return &connector.AccountLimits{Concurrency: concurrency}, nil
}

func TestConfigIsSingletonAndConnectionErrorIsRedacted(t *testing.T) {
	service, _, admin, _ := newTestService(t)
	admin.pingErr = errors.New("invalid admin key top-secret")
	_, err := service.CreateConfig(context.Background(), ConfigInput{
		Name: "main", BaseURL: "https://main.example.com", AdminAPIKey: "top-secret",
	})
	if err == nil || strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("create config error = %v", err)
	}
	admin.pingErr = nil
	config, err := service.CreateConfig(context.Background(), ConfigInput{
		Name: "main", BaseURL: "https://main.example.com", AdminAPIKey: "admin-key",
	})
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	if !config.Configured || !config.HasAdminAPIKey || config.AutoMarginProtection || config.AutoHealthProtection || config.AutoRecovery {
		t.Fatalf("config = %#v", config)
	}
	if config.HealthIntervalSeconds != defaultHealthIntervalSeconds {
		t.Fatalf("default health interval = %d", config.HealthIntervalSeconds)
	}
	if config.HealthFailureThreshold != defaultHealthFailureThreshold || config.HealthRecoveryThreshold != defaultHealthRecoveryThreshold {
		t.Fatalf("default health thresholds = %d/%d", config.HealthFailureThreshold, config.HealthRecoveryThreshold)
	}
	interval := 60
	failureThreshold := 20
	recoveryThreshold := 5
	updated, err := service.UpdateConfig(context.Background(), ConfigInput{
		HealthIntervalSeconds: &interval, HealthFailureThreshold: &failureThreshold, HealthRecoveryThreshold: &recoveryThreshold,
	})
	if err != nil {
		t.Fatalf("update health interval: %v", err)
	}
	if updated.HealthIntervalSeconds != interval || updated.HealthFailureThreshold != failureThreshold || updated.HealthRecoveryThreshold != recoveryThreshold {
		t.Fatalf("updated health strategy = %#v", updated)
	}
	invalidInterval := 29
	if _, err := service.UpdateConfig(context.Background(), ConfigInput{HealthIntervalSeconds: &invalidInterval}); err == nil {
		t.Fatal("invalid health interval was accepted")
	}
	if _, err := service.CreateConfig(context.Background(), ConfigInput{
		Name: "other", BaseURL: "https://other.example.com", AdminAPIKey: "other-key",
	}); !errors.Is(err, ErrAlreadyConfigured) {
		t.Fatalf("second create error = %v", err)
	}
}

func TestMainStationGroupsAreDirectAccountWorkspaces(t *testing.T) {
	service, _, admin, _ := newTestService(t)
	configureTestStation(t, service)
	admin.groups = []sub2api.AdminGroup{
		{ID: 11, Name: "OpenAI", Platform: "openai", RateMultiplier: 1, Status: "active"},
		{ID: 12, Name: "Claude", Platform: "anthropic", RateMultiplier: 1, Status: "active"},
	}
	admin.accounts = []sub2api.AdminAccount{
		{ID: 21, Name: "openai-01", Status: "active", GroupIDs: []int64{11}},
		{ID: 22, Name: "shared-01", Status: "active", GroupIDs: []int64{11, 12}},
	}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}

	workspaces, err := service.ListGroupWorkspaces(false)
	if err != nil {
		t.Fatalf("list group workspaces: %v", err)
	}
	if len(workspaces) != 2 || workspaces[0].AccountCount != 2 || workspaces[1].AccountCount != 1 {
		t.Fatalf("workspaces = %#v", workspaces)
	}
	firstPoolID, err := service.GroupPoolID(workspaces[0].Group.ID)
	if err != nil {
		t.Fatalf("resolve group policy: %v", err)
	}
	secondPoolID, err := service.GroupPoolID(workspaces[0].Group.ID)
	if err != nil || secondPoolID != firstPoolID {
		t.Fatalf("group policy is not idempotent: first=%d second=%d err=%v", firstPoolID, secondPoolID, err)
	}
	accounts, err := service.ListGroupAccounts(workspaces[1].Group.ID, false)
	if err != nil {
		t.Fatalf("list group accounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].Name != "shared-01" {
		t.Fatalf("group accounts = %#v", accounts)
	}
	enabled := false
	updated, err := service.UpdateGroupSettings(workspaces[0].Group.ID, GroupSettingsInput{
		Enabled: &enabled, MinimumHealthyAccounts: 2, MinimumEffectiveConcurrency: 20, RateSortDirection: "desc",
	})
	if err != nil {
		t.Fatalf("update group settings: %v", err)
	}
	if updated.Enabled || updated.MinimumHealthyAccounts != 2 || updated.MinimumEffectiveConcurrency != 20 || updated.RateSortDirection != "desc" {
		t.Fatalf("updated workspace = %#v", updated)
	}
}

func TestMainStationAccountUsesLatestSourceGroupRate(t *testing.T) {
	service, db, admin, _ := newTestService(t)
	defaultRate, defaultObservedAt := service.sourceGroupRate(&storage.MainAccountPoolMember{SourceChannelID: 1})
	if defaultRate == nil || *defaultRate != 1 || defaultObservedAt != nil {
		t.Fatalf("default source group rate = %v observed_at=%v", defaultRate, defaultObservedAt)
	}
	configureTestStation(t, service)
	admin.groups = []sub2api.AdminGroup{{ID: 11, Name: "main", RateMultiplier: 1, Status: "active"}}
	admin.accounts = []sub2api.AdminAccount{{ID: 21, Name: "managed", Status: "active", GroupIDs: []int64{11}}}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	groups, err := service.ListGroups(false)
	if err != nil || len(groups) != 1 {
		t.Fatalf("list groups: groups=%#v err=%v", groups, err)
	}
	poolID, err := service.GroupPoolID(groups[0].ID)
	if err != nil {
		t.Fatalf("resolve group pool: %v", err)
	}
	channel := createTestChannel(t, db)
	rechargeMultiplier := 2.0
	channel.RechargeMultiplier = &rechargeMultiplier
	channel.RechargeMultiplierMode = connector.RechargeMultiplierModeDivide
	if err := db.Save(channel).Error; err != nil {
		t.Fatalf("save recharge multiplier: %v", err)
	}
	remoteAccountID := int64(21)
	sourceGroupID := int64(301)
	member := &storage.MainAccountPoolMember{
		PoolID:          poolID,
		SourceChannelID: channel.ID,
		SourceGroupID:   &sourceGroupID,
		SourceGroupName: "plus",
		RemoteAccountID: &remoteAccountID,
		OwnershipMode:   "bound",
		BindingStatus:   "manual_confirmed",
		Status:          "active",
		Enabled:         true,
	}
	if err := service.store.CreateMember(member); err != nil {
		t.Fatalf("create member: %v", err)
	}
	observedAt := time.Now().Add(-time.Minute).Truncate(time.Second)
	if _, err := service.rates.Upsert(&storage.RateSnapshot{
		ChannelID: channel.ID, RemoteGroupID: &sourceGroupID, ModelName: "plus", Ratio: 0.15, LastSeenAt: observedAt,
	}); err != nil {
		t.Fatalf("create source rate: %v", err)
	}
	accounts, err := service.ListGroupAccounts(groups[0].ID, false)
	if err != nil || len(accounts) != 1 || accounts[0].Member == nil {
		t.Fatalf("list group accounts: accounts=%#v err=%v", accounts, err)
	}
	if accounts[0].Member.SourceGroupRateMultiplier == nil || *accounts[0].Member.SourceGroupRateMultiplier != 0.075 ||
		accounts[0].Member.SourceGroupRateObservedAt == nil || !accounts[0].Member.SourceGroupRateObservedAt.Equal(observedAt) {
		t.Fatalf("source group rate = %#v", accounts[0].Member)
	}

	updatedAt := time.Now().Truncate(time.Second)
	if _, err := service.rates.Upsert(&storage.RateSnapshot{
		ChannelID: channel.ID, RemoteGroupID: &sourceGroupID, ModelName: "plus", Ratio: 0.22, LastSeenAt: updatedAt,
	}); err != nil {
		t.Fatalf("update source rate: %v", err)
	}
	accounts, err = service.ListGroupAccounts(groups[0].ID, false)
	if err != nil || accounts[0].Member.SourceGroupRateMultiplier == nil || *accounts[0].Member.SourceGroupRateMultiplier != 0.11 {
		t.Fatalf("updated source group rate: accounts=%#v err=%v", accounts, err)
	}
}

func TestBoundMemberIsUniqueAndBecomesOrphaned(t *testing.T) {
	service, db, admin, _ := newTestService(t)
	configureTestStation(t, service)
	admin.groups = []sub2api.AdminGroup{{ID: 11, Name: "default", RateMultiplier: 1, Status: "active"}}
	admin.accounts = []sub2api.AdminAccount{{
		ID: 21, Name: "existing", Status: "active", Schedulable: true,
		Credentials: map[string]any{"base_url": "https://upstream.example.com", "api_key": "***masked***"},
	}}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	config, err := service.store.GetConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	config.Enabled = false
	if err := service.store.SaveConfig(config); err != nil {
		t.Fatalf("disable main station management: %v", err)
	}
	channel := createTestChannel(t, db)
	groups, err := service.ListGroups(false)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %#v, err=%v", groups, err)
	}
	pool, err := service.CreatePool(PoolInput{
		Name: "pool", Platform: "openai", TargetGroupIDs: []uint{groups[0].ID},
	})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	remoteID := int64(21)
	if _, err := service.CreateMember(context.Background(), pool.ID, MemberInput{
		OwnershipMode: "bound", SourceChannelID: channel.ID, RemoteAccountID: &remoteID,
	}); err == nil || !strings.Contains(err.Error(), "confirmation") {
		t.Fatalf("unconfirmed bind error = %v", err)
	}
	member, err := service.CreateMember(context.Background(), pool.ID, MemberInput{
		OwnershipMode: "bound", SourceChannelID: channel.ID, RemoteAccountID: &remoteID,
		ManualBindingConfirmed: true, Enabled: boolPtr(true),
	})
	if err != nil {
		t.Fatalf("create bound member: %v", err)
	}
	if member.BindingStatus != "manual_confirmed" || member.Status != "active" {
		t.Fatalf("bound member = %#v", member)
	}
	if len(admin.schedulingUpdates) != 1 || admin.schedulingUpdates[0].Concurrency != 12 || admin.schedulingUpdates[0].LoadFactor != 12 || admin.schedulingUpdates[0].Priority != 1 {
		t.Fatalf("bound scheduling updates = %#v", admin.schedulingUpdates)
	}
	if _, err := service.CreateMember(context.Background(), pool.ID, MemberInput{
		OwnershipMode: "bound", SourceChannelID: channel.ID, RemoteAccountID: &remoteID,
		ManualBindingConfirmed: true,
	}); !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("duplicate binding error = %v", err)
	}

	admin.accounts = nil
	result, err := service.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync missing account: %v", err)
	}
	if len(result.MissingAccounts) != 1 || result.MissingAccounts[0] != remoteID || result.OrphanedMembers != 1 {
		t.Fatalf("sync result = %#v", result)
	}
	member, err = service.store.FindMember(pool.ID, member.ID)
	if err != nil {
		t.Fatalf("reload member: %v", err)
	}
	if member.BindingStatus != "orphaned" || member.Status != "orphaned" {
		t.Fatalf("orphaned member = %#v", member)
	}
	if err := service.DeleteMember(context.Background(), pool.ID, member.ID, DeleteMemberInput{Confirm: true}); err != nil {
		t.Fatalf("delete bound member: %v", err)
	}
	if len(admin.deletedAccounts) != 0 || len(admin.schedulableCalls) != 0 {
		t.Fatalf("bound deletion touched remote: deleted=%v schedulable=%v", admin.deletedAccounts, admin.schedulableCalls)
	}
}

func TestManagedMemberCreatesIndependentValidatedAccountAndPreservesRemoteByDefault(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sub2api/billing":
			_ = json.NewEncoder(w).Encode(map[string]any{"effective_rate_multiplier": 0.8})
		case "/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": "OK"}}},
				"usage":   map[string]any{"total_tokens": 4},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	service, db, admin, channels := newTestService(t)
	configureTestStation(t, service)
	admin.groups = []sub2api.AdminGroup{{ID: 31, Name: "main-group", RateMultiplier: 1, Status: "active"}}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	channel := createTestChannel(t, db)
	channel.SiteURL = upstream.URL
	rechargeMultiplier := 2.0
	channel.RechargeMultiplier = &rechargeMultiplier
	channel.RechargeMultiplierMode = connector.RechargeMultiplierModeDivide
	if err := db.Save(channel).Error; err != nil {
		t.Fatalf("update source channel: %v", err)
	}
	sourceGroupID := int64(5)
	channels.groups = []connector.APIKeyGroup{{ID: &sourceGroupID, Name: "source-group", Ratio: 0.8}}
	groups, err := service.ListGroups(false)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %#v, err=%v", groups, err)
	}
	pool, err := service.CreatePool(PoolInput{
		Name: "managed-pool", Platform: "openai", TargetGroupIDs: []uint{groups[0].ID},
	})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	member, err := service.CreateMember(context.Background(), pool.ID, MemberInput{
		AccountName: "OpenAI-01", OwnershipMode: "managed", SourceChannelID: channel.ID, SourceGroupID: &sourceGroupID,
		SourceGroupName: "source-group", Enabled: boolPtr(true), HealthEnabled: boolPtr(true),
		HealthAPIMode: "openai_chat",
		Priority:      1, RateConvertMode: "raw", CostAdjustment: 1,
	})
	if err != nil {
		t.Fatalf("create managed member: %v", err)
	}
	if member.RemoteAccountID == nil || member.SourceAPIKeyID == nil || member.BindingStatus != "verified" {
		t.Fatalf("managed member = %#v", member)
	}
	if len(channels.createdKeys) != 1 || len(admin.createRequests) != 1 {
		t.Fatalf("create calls: keys=%d accounts=%d", len(channels.createdKeys), len(admin.createRequests))
	}
	request := admin.createRequests[0]
	if request.Name != "OpenAI-01" {
		t.Fatalf("managed account name = %q", request.Name)
	}
	if request.Credentials["api_key"] != channels.secret || request.Credentials["base_url"] != channel.SiteURL {
		t.Fatalf("managed account credentials = %#v", request.Credentials)
	}
	if len(request.GroupIDs) != 1 || request.GroupIDs[0] != 31 || request.RateMultiplier != 0.4 {
		t.Fatalf("managed account request = %#v", request)
	}
	if request.Concurrency != channels.concurrency {
		t.Fatalf("managed account concurrency = %d, want %d", request.Concurrency, channels.concurrency)
	}
	if request.Weight != channels.concurrency || request.LoadFactor != float64(channels.concurrency) || request.Priority != 1 {
		t.Fatalf("managed account automatic scheduling = %#v", request)
	}
	if len(admin.schedulableCalls) != 1 || !admin.schedulableCalls[0] {
		t.Fatalf("schedulable calls = %#v", admin.schedulableCalls)
	}
	healthInterval := 1
	healthFailureThreshold := 20
	healthRecoveryThreshold := 4
	updated, err := service.UpdateMember(context.Background(), pool.ID, member.ID, MemberInput{
		AccountName: member.AccountName, SourceChannelID: member.SourceChannelID, SourceGroupID: member.SourceGroupID,
		SourceGroupName: member.SourceGroupName, Enabled: boolPtr(true), HealthEnabled: boolPtr(true),
		HealthAPIMode: "openai_chat", Priority: 9, Concurrency: 37, HealthIntervalSeconds: &healthInterval,
		HealthFailureThreshold: &healthFailureThreshold, HealthRecoveryThreshold: &healthRecoveryThreshold,
	})
	if err != nil {
		t.Fatalf("update managed member: %v", err)
	}
	if updated.Concurrency != 37 || updated.Priority != 9 || updated.Weight != 37 || updated.HealthIntervalSeconds != 1 ||
		updated.HealthFailureThreshold != 20 || updated.HealthRecoveryThreshold != 4 {
		t.Fatalf("updated managed member = %#v", updated)
	}
	if len(admin.updateRequests) != 1 || admin.updateRequests[0].Concurrency != 37 || admin.updateRequests[0].Priority != 1 || admin.updateRequests[0].LoadFactor != 37 {
		t.Fatalf("update requests = %#v", admin.updateRequests)
	}

	if err := service.DeleteMember(context.Background(), pool.ID, member.ID, DeleteMemberInput{Confirm: true}); err != nil {
		t.Fatalf("delete managed member: %v", err)
	}
	if len(admin.deletedAccounts) != 0 || len(channels.deletedKeys) != 0 {
		t.Fatalf("default delete removed remote resources: accounts=%v keys=%v", admin.deletedAccounts, channels.deletedKeys)
	}
	if len(admin.schedulableCalls) != 3 || admin.schedulableCalls[2] {
		t.Fatalf("delete schedulable calls = %#v", admin.schedulableCalls)
	}
}

func newTestService(t *testing.T) (*Service, *gorm.DB, *fakeAdminClient, *fakeChannelService) {
	t.Helper()
	db, err := storage.Open(storage.DBConfig{
		Driver: storage.DBDriverSQLite,
		Path:   filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := storage.AutoMigrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	cipher, err := crypto.NewCipher("test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	admin := &fakeAdminClient{}
	channelSvc := &fakeChannelService{secret: "sk-source-secret", concurrency: 12}
	service := New(
		storage.NewMainStationStore(db),
		storage.NewUpstreamSyncTargets(db),
		storage.NewUpstreamSyncTargetGroups(db),
		storage.NewChannels(db),
		storage.NewRates(db),
		storage.NewUpstreamSyncManagedAccounts(db),
		cipher,
		channelSvc,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	service.adminFactory = func() adminClient { return admin }
	return service, db, admin, channelSvc
}

func configureTestStation(t *testing.T, service *Service) {
	t.Helper()
	if _, err := service.CreateConfig(context.Background(), ConfigInput{
		Name: "main", BaseURL: "https://main.example.com", AdminAPIKey: "admin-key",
	}); err != nil {
		t.Fatalf("configure station: %v", err)
	}
}

func createTestChannel(t *testing.T, db *gorm.DB) *storage.Channel {
	t.Helper()
	channel := &storage.Channel{
		Name: "source", Type: storage.ChannelTypeSub2API, SiteURL: "https://upstream.example.com",
		Username: "user", PasswordCipher: "cipher", MonitorEnabled: true,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create source channel: %v", err)
	}
	return channel
}

func boolPtr(value bool) *bool { return &value }
