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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fausto2022/relaydeck/backend/connector"
	"github.com/fausto2022/relaydeck/backend/connector/sub2api"
	"github.com/fausto2022/relaydeck/backend/crypto"
	"github.com/fausto2022/relaydeck/backend/notify"
	"github.com/fausto2022/relaydeck/backend/storage"
	"gorm.io/gorm"
)

type fakeAdminClient struct {
	pingErr             error
	groups              []sub2api.AdminGroup
	accounts            []sub2api.AdminAccount
	createRequests      []sub2api.AdminAccount
	updateRequests      []sub2api.AdminAccount
	schedulingUpdates   []sub2api.AdminAccountSchedulingUpdate
	schedulingUpdateErr error
	schedulableCalls    []bool
	setSchedulableErr   error
	applyBeforeSetError bool
	deletedAccounts     []int64
	deleteAccountErr    error
	nextAccountID       int64
	accountModels       map[int64][]string
	syncModelCalls      []int64
	syncModelsErr       error
	syncModelsEmpty     bool
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
	if f.schedulingUpdateErr != nil {
		return nil, f.schedulingUpdateErr
	}
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
	return f.deleteAccountErr
}
func (f *fakeAdminClient) SyncAccountModelsFromUpstream(_ context.Context, _ sub2api.AdminTarget, id int64) ([]string, error) {
	f.syncModelCalls = append(f.syncModelCalls, id)
	if f.syncModelsErr != nil {
		return nil, f.syncModelsErr
	}
	if f.syncModelsEmpty {
		return []string{}, nil
	}
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
	secret       string
	revealKeyErr error
	groups       []connector.APIKeyGroup
	keys         []connector.APIKey
	listKeysErr  error
	createdKeys  []connector.APIKeyCreateRequest
	updatedKeys  []connector.APIKeyUpdateRequest
	createdKeyID int64
	deletedKeys  []int64
	deleteKeyErr error
	concurrency  int
}

func (f *fakeChannelService) RevealAPIKey(context.Context, uint, int64) (string, error) {
	if f.revealKeyErr != nil {
		return "", f.revealKeyErr
	}
	return f.secret, nil
}
func (f *fakeChannelService) CreateAPIKey(_ context.Context, _ uint, req connector.APIKeyCreateRequest) (*connector.APIKey, error) {
	f.createdKeys = append(f.createdKeys, req)
	keyID := f.createdKeyID
	if keyID == 0 {
		keyID = 77
	}
	return &connector.APIKey{ID: keyID, Key: f.secret, Name: req.Name}, nil
}
func (f *fakeChannelService) UpdateAPIKey(_ context.Context, _ uint, id int64, req connector.APIKeyUpdateRequest) (*connector.APIKey, error) {
	f.updatedKeys = append(f.updatedKeys, req)
	for i := range f.keys {
		if f.keys[i].ID == id {
			if req.Name != nil {
				f.keys[i].Name = *req.Name
			}
			item := f.keys[i]
			return &item, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}
func (f *fakeChannelService) DeleteAPIKey(_ context.Context, _ uint, id int64) error {
	f.deletedKeys = append(f.deletedKeys, id)
	return f.deleteKeyErr
}
func (f *fakeChannelService) ListAPIKeyGroups(context.Context, uint) ([]connector.APIKeyGroup, error) {
	return append([]connector.APIKeyGroup(nil), f.groups...), nil
}
func (f *fakeChannelService) ListAPIKeys(context.Context, uint, connector.APIKeyQuery) (*connector.APIKeyPage, error) {
	if f.listKeysErr != nil {
		return nil, f.listKeysErr
	}
	return &connector.APIKeyPage{Items: append([]connector.APIKey(nil), f.keys...), Total: int64(len(f.keys)), Page: 1, PageSize: 100, Pages: 1}, nil
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
	if config.MinimumMarginBasisPoints != 0 {
		t.Fatalf("default minimum margin = %d", config.MinimumMarginBasisPoints)
	}
	interval := 60
	failureThreshold := 20
	recoveryThreshold := 5
	minimumMargin := int64(2000)
	updated, err := service.UpdateConfig(context.Background(), ConfigInput{
		HealthIntervalSeconds: &interval, HealthFailureThreshold: &failureThreshold, HealthRecoveryThreshold: &recoveryThreshold,
		MinimumMarginBasisPoints: &minimumMargin,
	})
	if err != nil {
		t.Fatalf("update health interval: %v", err)
	}
	if updated.HealthIntervalSeconds != interval || updated.HealthFailureThreshold != failureThreshold || updated.HealthRecoveryThreshold != recoveryThreshold || updated.MinimumMarginBasisPoints != minimumMargin {
		t.Fatalf("updated health strategy = %#v", updated)
	}
	invalidMargin := int64(10000)
	if _, err := service.UpdateConfig(context.Background(), ConfigInput{MinimumMarginBasisPoints: &invalidMargin}); err == nil {
		t.Fatal("invalid minimum margin was accepted")
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
	updated, err := service.UpdateGroupSettings(context.Background(), workspaces[0].Group.ID, GroupSettingsInput{
		Enabled: &enabled, MinimumHealthyAccounts: 2, MinimumEffectiveConcurrency: 20, RateSortDirection: "desc",
	})
	if err != nil {
		t.Fatalf("update group settings: %v", err)
	}
	if updated.Enabled || updated.MinimumHealthyAccounts != 2 || updated.MinimumEffectiveConcurrency != 20 || updated.RateSortDirection != "desc" {
		t.Fatalf("updated workspace = %#v", updated)
	}
	groupMinimum := int64(1500)
	updated, err = service.UpdateGroupSettings(context.Background(), workspaces[0].Group.ID, GroupSettingsInput{
		Enabled: &enabled, MinimumHealthyAccounts: 2, MinimumEffectiveConcurrency: 20, RateSortDirection: "desc",
		MinimumMarginBasisPoints: &groupMinimum,
	})
	if err != nil {
		t.Fatalf("set group minimum margin: %v", err)
	}
	if updated.MinimumMarginBasisPoints == nil || *updated.MinimumMarginBasisPoints != groupMinimum {
		t.Fatalf("group minimum margin = %#v", updated.MinimumMarginBasisPoints)
	}
	updated, err = service.UpdateGroupSettings(context.Background(), workspaces[0].Group.ID, GroupSettingsInput{
		Enabled: &enabled, MinimumHealthyAccounts: 2, MinimumEffectiveConcurrency: 20, RateSortDirection: "desc",
	})
	if err != nil {
		t.Fatalf("clear group minimum margin: %v", err)
	}
	if updated.MinimumMarginBasisPoints != nil {
		t.Fatalf("group minimum margin did not return to inheritance: %#v", updated.MinimumMarginBasisPoints)
	}
	legacyMargin := `{"mode":"observe","minimum_margin_basis_points":1200,"risk_confirmations":2,"cost_max_age_minutes":60}`
	legacyPool, err := service.store.FindPool(firstPoolID)
	if err != nil {
		t.Fatalf("load legacy margin pool: %v", err)
	}
	legacyPool.MarginPolicyJSON = legacyMargin
	if err := service.store.UpdatePool(legacyPool, []uint{workspaces[0].Group.ID}); err != nil {
		t.Fatalf("save legacy margin policy: %v", err)
	}
	legacyWorkspaces, err := service.ListGroupWorkspaces(false)
	if err != nil || legacyWorkspaces[0].MinimumMarginBasisPoints == nil || *legacyWorkspaces[0].MinimumMarginBasisPoints != 1200 {
		t.Fatalf("legacy margin override = %#v, err=%v", legacyWorkspaces, err)
	}
	updated, err = service.UpdateGroupSettings(context.Background(), workspaces[0].Group.ID, GroupSettingsInput{
		Enabled: &enabled, MinimumHealthyAccounts: 2, MinimumEffectiveConcurrency: 20, RateSortDirection: "desc",
		MarginPolicy: legacyMargin,
	})
	if err != nil {
		t.Fatalf("clear legacy group minimum margin: %v", err)
	}
	if updated.MinimumMarginBasisPoints != nil || strings.Contains(updated.MarginPolicy, "minimum_margin_basis_points") {
		t.Fatalf("legacy margin override was not cleared: %#v", updated)
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
	if err := service.store.AppendProfitCheck(&storage.MainAccountProfitCheck{
		PoolID:               poolID,
		MemberID:             member.ID,
		TargetGroupID:        groups[0].ID,
		SaleMultiplierMicros: 150000,
		CostMultiplierMicros: 165000,
		MarginValueMicros:    -15000,
		MarginBasisPoints:    -1000,
		Status:               "risk",
		ObservedAt:           time.Now().Add(-time.Minute).Truncate(time.Second),
	}); err != nil {
		t.Fatalf("create previous profit check: %v", err)
	}
	profitObservedAt := time.Now().Add(-30 * time.Second).Truncate(time.Second)
	if err := service.store.AppendProfitCheck(&storage.MainAccountProfitCheck{
		PoolID:               poolID,
		MemberID:             member.ID,
		TargetGroupID:        groups[0].ID,
		SaleMultiplierMicros: 150000,
		CostMultiplierMicros: 108000,
		MarginValueMicros:    42000,
		MarginBasisPoints:    2800,
		Status:               "healthy",
		ObservedAt:           profitObservedAt,
	}); err != nil {
		t.Fatalf("create profit check: %v", err)
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
	if accounts[0].Member.LatestProfit == nil || accounts[0].Member.LatestProfit.Status != "healthy" ||
		accounts[0].Member.LatestProfit.SaleMultiplierMicros != 150000 ||
		accounts[0].Member.LatestProfit.CostMultiplierMicros != 108000 ||
		accounts[0].Member.LatestProfit.MarginBasisPoints != 2800 ||
		!accounts[0].Member.LatestProfit.ObservedAt.Equal(profitObservedAt) {
		t.Fatalf("latest profit = %#v", accounts[0].Member.LatestProfit)
	}
	if accounts[0].Member.CurrentProfit == nil || accounts[0].Member.CurrentProfit.Status != "healthy" ||
		accounts[0].Member.CurrentProfit.SaleMultiplierMicros != 1000000 ||
		accounts[0].Member.CurrentProfit.CostMultiplierMicros != 75000 ||
		accounts[0].Member.CurrentProfit.MarginBasisPoints != 9250 {
		t.Fatalf("current profit = %#v", accounts[0].Member.CurrentProfit)
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
	if accounts[0].Member.CurrentProfit == nil || accounts[0].Member.CurrentProfit.CostMultiplierMicros != 110000 ||
		accounts[0].Member.CurrentProfit.MarginBasisPoints != 8900 {
		t.Fatalf("updated current profit = %#v", accounts[0].Member.CurrentProfit)
	}
}

func TestMainStationSyncReportsPricingChanges(t *testing.T) {
	service, _, admin, _ := newTestService(t)
	configureTestStation(t, service)
	admin.groups = []sub2api.AdminGroup{{ID: 11, Name: "main", RateMultiplier: 1, Status: "active"}}

	first, err := service.Sync(context.Background())
	if err != nil || !first.PricingChanged {
		t.Fatalf("first sync = %#v, err=%v", first, err)
	}
	unchanged, err := service.Sync(context.Background())
	if err != nil || unchanged.PricingChanged {
		t.Fatalf("unchanged sync = %#v, err=%v", unchanged, err)
	}
	admin.groups[0].RateMultiplier = 0.8
	changed, err := service.Sync(context.Background())
	if err != nil || !changed.PricingChanged {
		t.Fatalf("changed sync = %#v, err=%v", changed, err)
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
	if len(admin.schedulingUpdates) != 0 {
		t.Fatalf("bound creation should defer ranking until the configured ranking tick: %#v", admin.schedulingUpdates)
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
	admin.accountModels = map[int64][]string{1000: {"gpt-test"}}
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
	originalAccount := sub2api.AdminAccount{
		ID: 88, Name: "source-source-group", Status: "active", GroupIDs: []int64{999},
		Notes: "manual account", Priority: 27, Concurrency: 3,
	}
	admin.accounts = append(admin.accounts, originalAccount)
	input := MemberInput{
		AccountName: "OpenAI-01", OwnershipMode: "managed", SourceChannelID: channel.ID, SourceGroupID: &sourceGroupID,
		SourceGroupName: "source-group", Enabled: boolPtr(true), HealthEnabled: boolPtr(true),
		HealthAPIMode: "openai_chat",
		Priority:      1, RateConvertMode: "raw", CostAdjustment: 1,
	}
	if _, err := service.CreateMember(context.Background(), pool.ID, input); !errors.Is(err, ErrManagedAccountNameConflict) {
		t.Fatalf("duplicate managed account name error = %v", err)
	}
	members, err := service.store.ListMembers(pool.ID)
	if err != nil {
		t.Fatalf("list members after duplicate warning: %v", err)
	}
	if len(members) != 0 || len(channels.createdKeys) != 0 || len(admin.createRequests) != 0 || len(admin.accounts) != 1 {
		t.Fatalf("duplicate warning changed state: members=%#v keys=%#v requests=%#v accounts=%#v", members, channels.createdKeys, admin.createRequests, admin.accounts)
	}
	input.AllowNameConflict = true
	member, err := service.CreateMember(context.Background(), pool.ID, input)
	if err != nil {
		t.Fatalf("create managed member: %v", err)
	}
	if member.RemoteAccountID == nil || member.SourceAPIKeyID == nil || !member.SourceAPIKeyManaged || member.BindingStatus != "verified" {
		t.Fatalf("managed member = %#v", member)
	}
	if len(channels.createdKeys) != 1 || len(admin.createRequests) != 1 {
		t.Fatalf("create calls: keys=%d accounts=%d", len(channels.createdKeys), len(admin.createRequests))
	}
	if len(admin.accounts) != 2 || admin.accounts[0].ID != originalAccount.ID ||
		!slices.Equal(admin.accounts[0].GroupIDs, originalAccount.GroupIDs) ||
		admin.accounts[0].Notes != originalAccount.Notes || admin.accounts[0].Priority != originalAccount.Priority ||
		admin.accounts[0].Concurrency != originalAccount.Concurrency {
		t.Fatalf("original duplicate account was modified: got=%#v want=%#v", admin.accounts[0], originalAccount)
	}
	if channels.createdKeys[0].Name != "source-group" {
		t.Fatalf("managed source api key name = %q", channels.createdKeys[0].Name)
	}
	request := admin.createRequests[0]
	if request.Name != "source-source-group" || member.AccountName != request.Name {
		t.Fatalf("managed account name = %q", request.Name)
	}
	if request.Credentials["api_key"] != channels.secret || request.Credentials["base_url"] != channel.SiteURL {
		t.Fatalf("managed account credentials = %#v", request.Credentials)
	}
	if request.Credentials["pool_mode"] != true || request.Credentials["pool_mode_retry_count"] != managedAccountPoolModeRetryCount {
		t.Fatalf("managed account pool mode = %#v", request.Credentials)
	}
	retryStatusCodes, ok := request.Credentials["pool_mode_retry_status_codes"].([]int)
	if !ok || !slices.Equal(retryStatusCodes, managedAccountPoolModeRetryStatusCodes()) {
		t.Fatalf("managed account pool mode retry status codes = %#v", request.Credentials["pool_mode_retry_status_codes"])
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
	if len(admin.syncModelCalls) != 1 || admin.syncModelCalls[0] != *member.RemoteAccountID {
		t.Fatalf("sync model calls = %#v", admin.syncModelCalls)
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
	if len(admin.updateRequests) != 0 {
		t.Fatalf("scheduling-only edit should not run a full managed sync: %#v", admin.updateRequests)
	}
	service.RunDueRankings(context.Background())
	if len(admin.schedulingUpdates) != 1 || admin.schedulingUpdates[0].Concurrency != 37 || admin.schedulingUpdates[0].Priority != 1 || admin.schedulingUpdates[0].LoadFactor != 37 {
		t.Fatalf("deferred scheduling updates = %#v", admin.schedulingUpdates)
	}

	if err := service.DeleteMember(context.Background(), pool.ID, member.ID, DeleteMemberInput{Confirm: true}); err != nil {
		t.Fatalf("delete managed member: %v", err)
	}
	if len(admin.deletedAccounts) != 0 || len(channels.deletedKeys) != 0 {
		t.Fatalf("default delete removed remote resources: accounts=%v keys=%v", admin.deletedAccounts, channels.deletedKeys)
	}
	if len(admin.schedulableCalls) != 3 || admin.schedulableCalls[1] || !admin.schedulableCalls[2] {
		t.Fatalf("delete schedulable calls = %#v", admin.schedulableCalls)
	}
}

func TestManagedMemberMarkerDoesNotMatchAnotherMemberID(t *testing.T) {
	if hasManagedMemberMarker("RelayDeck managed member:10", 1) {
		t.Fatal("member 1 marker matched member 10")
	}
	if !hasManagedMemberMarker("RelayDeck managed member:10", 10) {
		t.Fatal("member 10 marker was not recognized")
	}
}

func TestEnsureManagedSourceAPIKeyRecreatesMissingRemoteKey(t *testing.T) {
	service, db, _, channels := newTestService(t)
	channel := createTestChannel(t, db)
	pool := &storage.MainAccountPool{Name: "managed-pool", Platform: "openai"}
	if err := service.store.CreatePool(pool, nil); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	oldKeyID := int64(41)
	member := &storage.MainAccountPoolMember{
		PoolID: pool.ID, AccountName: "OpenAI-01", OwnershipMode: "managed",
		SourceChannelID: channel.ID, SourceGroupName: "source-group", SourceAPIKeyID: &oldKeyID, SourceAPIKeyManaged: true,
	}
	if err := service.store.CreateMember(member); err != nil {
		t.Fatalf("create member: %v", err)
	}
	channels.revealKeyErr = connector.HTTPStatusError(http.StatusUnauthorized, []byte("unauthorized"))
	if _, err := service.ensureManagedSourceAPIKey(context.Background(), pool, member); err == nil || len(channels.createdKeys) != 0 {
		t.Fatalf("non-missing key error should not create a replacement: err=%v keys=%#v", err, channels.createdKeys)
	}
	channels.revealKeyErr = connector.HTTPStatusError(http.StatusNotFound, []byte("api key not found"))
	channels.createdKeyID = 88

	secret, err := service.ensureManagedSourceAPIKey(context.Background(), pool, member)
	if err != nil {
		t.Fatalf("ensure managed source api key: %v", err)
	}
	if secret != channels.secret || member.SourceAPIKeyID == nil || *member.SourceAPIKeyID != 88 {
		t.Fatalf("recreated key: secret=%q member=%#v", secret, member)
	}
	if len(channels.createdKeys) != 1 || channels.createdKeys[0].Name != "source-group" {
		t.Fatalf("created keys = %#v", channels.createdKeys)
	}
	stored, err := service.store.FindMember(pool.ID, member.ID)
	if err != nil || stored.SourceAPIKeyID == nil || *stored.SourceAPIKeyID != 88 || stored.AccountName != "source-source-group" {
		t.Fatalf("stored member = %#v, err=%v", stored, err)
	}
}

func TestManagedAutomaticNameUsesChannelAndDefaultGroup(t *testing.T) {
	service, db, _, _ := newTestService(t)
	channel := createTestChannel(t, db)
	pool := &storage.MainAccountPool{Name: "main-pool"}
	member := &storage.MainAccountPoolMember{SourceChannelID: channel.ID}
	if got := service.managedAutomaticName(pool, member); got != "source-默认分组" {
		t.Fatalf("automatic managed name = %q", got)
	}
}

func TestSyncManagedAccountModelsRejectsErrorsAndEmptyResults(t *testing.T) {
	service, _, admin, _ := newTestService(t)
	target := sub2api.AdminTarget{BaseURL: "https://main.example.com", APIKey: "admin-key"}
	admin.syncModelsErr = errors.New("upstream unavailable")
	if err := service.syncManagedAccountModels(context.Background(), admin, target, 31); err == nil || !strings.Contains(err.Error(), "upstream unavailable") {
		t.Fatalf("sync model error = %v", err)
	}
	admin.syncModelsErr = nil
	admin.syncModelsEmpty = true
	if err := service.syncManagedAccountModels(context.Background(), admin, target, 31); err == nil || !strings.Contains(err.Error(), "returned no models") {
		t.Fatalf("empty sync model error = %v", err)
	}
	admin.syncModelsEmpty = false
	admin.accountModels = map[int64][]string{31: {"gpt-test"}}
	if err := service.syncManagedAccountModels(context.Background(), admin, target, 31); err != nil {
		t.Fatalf("sync managed account models: %v", err)
	}
}

func TestSyncMemberRecreatesMissingManagedRemoteAccount(t *testing.T) {
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
	admin.accountModels = map[int64][]string{1000: {"gpt-test"}, 1001: {"gpt-test"}}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	channel := createTestChannel(t, db)
	channel.SiteURL = upstream.URL
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
		AccountName: "OpenAI-01", OwnershipMode: "managed", SourceChannelID: channel.ID,
		SourceGroupID: &sourceGroupID, SourceGroupName: "source-group", Enabled: boolPtr(true),
		HealthEnabled: boolPtr(true), HealthAPIMode: "openai_chat", Priority: 1, CostAdjustment: 1,
	})
	if err != nil || member.RemoteAccountID == nil {
		t.Fatalf("create managed member: member=%#v err=%v", member, err)
	}
	oldRemoteAccountID := *member.RemoteAccountID
	admin.accounts = nil

	recreated, err := service.SyncMember(context.Background(), pool.ID, member.ID)
	if err != nil {
		t.Fatalf("recreate missing managed account: %v", err)
	}
	if recreated.RemoteAccountID == nil || *recreated.RemoteAccountID == oldRemoteAccountID {
		t.Fatalf("recreated member = %#v", recreated)
	}
	if len(admin.createRequests) != 2 {
		t.Fatalf("create requests = %#v", admin.createRequests)
	}
	if !slices.Equal(admin.syncModelCalls, []int64{oldRemoteAccountID, *recreated.RemoteAccountID}) {
		t.Fatalf("sync model calls = %#v", admin.syncModelCalls)
	}
	request := admin.createRequests[1]
	if request.Credentials["pool_mode"] != true || request.Credentials["pool_mode_retry_count"] != managedAccountPoolModeRetryCount {
		t.Fatalf("recreated account pool mode = %#v", request.Credentials)
	}
}

func TestUpdateBoundMemberEnabledReconcilesRemoteScheduling(t *testing.T) {
	service, _, admin, pool, member := createBoundSchedulingMember(t)
	admin.schedulableCalls = nil

	disabled, err := service.UpdateMember(context.Background(), pool.ID, member.ID, MemberInput{
		AccountName: member.AccountName, SourceChannelID: member.SourceChannelID,
		Enabled: boolPtr(false), Priority: member.Priority, Concurrency: member.Concurrency,
	})
	if err != nil {
		t.Fatalf("disable bound member: %v", err)
	}
	if disabled.Enabled || len(admin.schedulableCalls) != 1 || admin.schedulableCalls[0] {
		t.Fatalf("disabled member=%#v calls=%#v", disabled, admin.schedulableCalls)
	}
	if disabled.SchedulingDirtyAt != nil || disabled.LastSchedulingAt == nil || disabled.LastSchedulingError != "" {
		t.Fatalf("disabled scheduling state = %#v", disabled)
	}

	enabled, err := service.UpdateMember(context.Background(), pool.ID, member.ID, MemberInput{
		AccountName: member.AccountName, SourceChannelID: member.SourceChannelID,
		Enabled: boolPtr(true), Priority: member.Priority, Concurrency: member.Concurrency,
	})
	if err != nil {
		t.Fatalf("enable bound member: %v", err)
	}
	if !enabled.Enabled || len(admin.schedulableCalls) != 2 || !admin.schedulableCalls[1] {
		t.Fatalf("enabled member=%#v calls=%#v", enabled, admin.schedulableCalls)
	}
}

func TestSchedulingTransitionsSendDisabledAndReenabledNotifications(t *testing.T) {
	service, db, _, pool, member := createBoundSchedulingMember(t)
	events := make(chan storage.NotificationEvent, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Event storage.NotificationEvent `json:"event"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		events <- body.Event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	configBody, err := json.Marshal(map[string]any{"url": server.URL, "method": http.MethodPost})
	if err != nil {
		t.Fatalf("marshal notification config: %v", err)
	}
	configCipher, err := service.cipher.Encrypt(string(configBody))
	if err != nil {
		t.Fatalf("encrypt notification config: %v", err)
	}
	notifications := storage.NewNotifications(db)
	if err := notifications.CreateChannel(&storage.NotificationChannel{
		Name: "webhook", Type: storage.NotifyWebhook, ConfigCipher: configCipher, Subscriptions: "[]", Enabled: true,
	}); err != nil {
		t.Fatalf("create notification channel: %v", err)
	}
	service.SetDispatcher(notify.NewDispatcher(
		notifications,
		service.cipher,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		notify.Policy{SendMaxAttempts: 1},
	))

	for _, enabled := range []bool{false, true} {
		if _, err := service.UpdateMember(context.Background(), pool.ID, member.ID, MemberInput{
			AccountName: member.AccountName, SourceChannelID: member.SourceChannelID,
			Enabled: boolPtr(enabled), Priority: member.Priority, Concurrency: member.Concurrency,
		}); err != nil {
			t.Fatalf("update member enabled=%v: %v", enabled, err)
		}
	}

	assertEvent := func(want storage.NotificationEvent) {
		t.Helper()
		select {
		case event := <-events:
			if event != want {
				t.Fatalf("notification event = %q, want %q", event, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for notification %q", want)
		}
	}
	assertEvent(storage.EventMainMemberDisabled)
	assertEvent(storage.EventMainMemberReenabled)
}

func TestDeleteManagedMemberToleratesAlreadyMissingRemoteResources(t *testing.T) {
	service, db, admin, channels := newTestService(t)
	configureTestStation(t, service)
	admin.groups = []sub2api.AdminGroup{{ID: 11, Name: "default", Platform: "openai", RateMultiplier: 1, Status: "active"}}
	admin.accounts = []sub2api.AdminAccount{{ID: 21, Name: "managed", Platform: "openai", Status: "active", Schedulable: true, Priority: 100, GroupIDs: []int64{11}}}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	channel := createTestChannel(t, db)
	groups, err := service.ListGroups(false)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups=%#v err=%v", groups, err)
	}
	pool, err := service.CreatePool(PoolInput{Name: "pool", Platform: "openai", TargetGroupIDs: []uint{groups[0].ID}})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	remoteID := int64(21)
	keyID := int64(77)
	member := &storage.MainAccountPoolMember{
		PoolID: pool.ID, AccountName: "managed", OwnershipMode: "managed", SourceChannelID: channel.ID,
		SourceAPIKeyID: &keyID, RemoteAccountID: &remoteID, RemoteAccountName: "managed", Enabled: true,
		Priority: 100, Concurrency: 10, BindingStatus: "verified", Status: "active", LastHealthStatus: "healthy",
	}
	if err := service.store.CreateMember(member); err != nil {
		t.Fatalf("create member: %v", err)
	}
	admin.deleteAccountErr = errors.New("status 404: account not found")
	channels.deleteKeyErr = errors.New("status 404: api key not found")
	if err := service.DeleteMember(context.Background(), pool.ID, member.ID, DeleteMemberInput{
		Confirm: true, DeleteRemoteAccount: true, DeleteSourceAPIKey: true,
	}); err != nil {
		t.Fatalf("delete missing managed resources: %v", err)
	}
	if _, err := service.store.FindMember(pool.ID, member.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deleted member lookup error = %v", err)
	}
	locks, err := service.store.ListActiveGuardLocks(remoteID)
	if err != nil || len(locks) != 0 {
		t.Fatalf("deleted member locks=%#v err=%v", locks, err)
	}
}

func TestUpdateGroupEnabledReconcilesAllMembers(t *testing.T) {
	service, _, admin, pool, _ := createBoundSchedulingMember(t)
	admin.schedulableCalls = nil
	groupIDs, err := service.store.ListPoolGroupIDs(pool.ID)
	if err != nil || len(groupIDs) != 1 {
		t.Fatalf("pool groups=%v err=%v", groupIDs, err)
	}
	workspaces, err := service.ListGroupWorkspaces(false)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces=%#v err=%v", workspaces, err)
	}
	workspace := workspaces[0]

	disabled := false
	if _, err := service.UpdateGroupSettings(context.Background(), groupIDs[0], GroupSettingsInput{
		Enabled: &disabled, MinimumHealthyAccounts: workspace.MinimumHealthyAccounts,
		MinimumEffectiveConcurrency: workspace.MinimumEffectiveConcurrency, RateSortDirection: workspace.RateSortDirection,
		HealthPolicy: workspace.HealthPolicy, MarginPolicy: workspace.MarginPolicy,
	}); err != nil {
		t.Fatalf("disable group: %v", err)
	}
	if len(admin.schedulableCalls) != 1 || admin.schedulableCalls[0] {
		t.Fatalf("disable group calls=%#v", admin.schedulableCalls)
	}

	enabled := true
	if _, err := service.UpdateGroupSettings(context.Background(), groupIDs[0], GroupSettingsInput{
		Enabled: &enabled, MinimumHealthyAccounts: workspace.MinimumHealthyAccounts,
		MinimumEffectiveConcurrency: workspace.MinimumEffectiveConcurrency, RateSortDirection: workspace.RateSortDirection,
		HealthPolicy: workspace.HealthPolicy, MarginPolicy: workspace.MarginPolicy,
	}); err != nil {
		t.Fatalf("enable group: %v", err)
	}
	if len(admin.schedulableCalls) != 2 || !admin.schedulableCalls[1] {
		t.Fatalf("enable group calls=%#v", admin.schedulableCalls)
	}
}

func TestSchedulingReconcileFailureIsRetried(t *testing.T) {
	service, _, admin, pool, member := createBoundSchedulingMember(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	admin.schedulableCalls = nil
	admin.setSchedulableErr = errors.New("temporary scheduling failure")

	updated, err := service.UpdateMember(context.Background(), pool.ID, member.ID, MemberInput{
		AccountName: member.AccountName, SourceChannelID: member.SourceChannelID,
		Enabled: boolPtr(false), Priority: member.Priority, Concurrency: member.Concurrency,
	})
	if err != nil {
		t.Fatalf("save disabled member: %v", err)
	}
	if updated.SchedulingDirtyAt == nil || updated.LastSchedulingAt == nil || updated.LastSchedulingError == "" {
		t.Fatalf("failed scheduling state = %#v", updated)
	}

	admin.setSchedulableErr = nil
	now = now.Add(schedulingRetryInterval)
	service.RunDueSchedulingReconciles(context.Background())
	retried, err := service.store.FindMember(pool.ID, member.ID)
	if err != nil {
		t.Fatalf("reload retried member: %v", err)
	}
	if retried.SchedulingDirtyAt != nil || retried.LastSchedulingError != "" {
		t.Fatalf("retried scheduling state = %#v", retried)
	}
	if len(admin.schedulableCalls) != 2 || admin.schedulableCalls[0] || admin.schedulableCalls[1] {
		t.Fatalf("retry calls=%#v", admin.schedulableCalls)
	}
}

func createBoundSchedulingMember(t *testing.T) (*Service, *gorm.DB, *fakeAdminClient, *PoolDTO, *storage.MainAccountPoolMember) {
	t.Helper()
	service, db, admin, _ := newTestService(t)
	configureTestStation(t, service)
	admin.groups = []sub2api.AdminGroup{{ID: 11, Name: "default", Platform: "openai", RateMultiplier: 1, Status: "active"}}
	admin.accounts = []sub2api.AdminAccount{{
		ID: 21, Name: "existing", Platform: "openai", Status: "active", Schedulable: true, Priority: 10, GroupIDs: []int64{11},
	}}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	channel := createTestChannel(t, db)
	groups, err := service.ListGroups(false)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups=%#v err=%v", groups, err)
	}
	pool, err := service.CreatePool(PoolInput{Name: "pool", Platform: "openai", TargetGroupIDs: []uint{groups[0].ID}})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	remoteID := int64(21)
	member, err := service.CreateMember(context.Background(), pool.ID, MemberInput{
		OwnershipMode: "bound", SourceChannelID: channel.ID, RemoteAccountID: &remoteID,
		ManualBindingConfirmed: true, Enabled: boolPtr(true), Priority: 1,
	})
	if err != nil {
		t.Fatalf("create bound member: %v", err)
	}
	return service, db, admin, pool, member
}

func TestRecommendBindingsUsesEndpointNameAndSourceGroup(t *testing.T) {
	service, db, admin, channels := newTestService(t)
	configureTestStation(t, service)
	admin.groups = []sub2api.AdminGroup{{ID: 11, Name: "OpenAI", Platform: "openai", RateMultiplier: 1, Status: "active"}}
	admin.accounts = []sub2api.AdminAccount{{
		ID: 21, Name: "OpenAI-01", Platform: "openai", Status: "active", GroupIDs: []int64{11},
		Credentials: map[string]any{"base_url": "https://upstream.example.com", "api_key": "***masked***"},
	}}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	channel := createTestChannel(t, db)
	channel.Name = "OpenAI source"
	if err := db.Save(channel).Error; err != nil {
		t.Fatalf("update channel: %v", err)
	}
	groupID := int64(301)
	channels.keys = []connector.APIKey{{ID: 77, Name: "OpenAI-01", Status: "active", GroupID: &groupID, GroupName: "OpenAI"}}
	groups, err := service.ListGroups(false)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %#v, err=%v", groups, err)
	}

	result, err := service.RecommendBindings(context.Background(), groups[0].ID)
	if err != nil {
		t.Fatalf("recommend bindings: %v", err)
	}
	if len(result.Items) != 1 || len(result.Items[0].Candidates) != 1 {
		t.Fatalf("recommendations = %#v", result)
	}
	recommendation := result.Items[0]
	candidate := recommendation.Candidates[0]
	if recommendation.Confidence != "high" || recommendation.Conflict || candidate.SourceChannelID != channel.ID ||
		candidate.SourceAPIKeyID == nil || *candidate.SourceAPIKeyID != 77 || candidate.SourceGroupID == nil || *candidate.SourceGroupID != groupID {
		t.Fatalf("recommendation = %#v", recommendation)
	}
}

func TestBindMembersBatchAllowsPartialSuccess(t *testing.T) {
	service, db, admin, _ := newTestService(t)
	configureTestStation(t, service)
	admin.groups = []sub2api.AdminGroup{{ID: 11, Name: "OpenAI", Platform: "openai", RateMultiplier: 1, Status: "active"}}
	admin.accounts = []sub2api.AdminAccount{{
		ID: 21, Name: "OpenAI-01", Platform: "openai", Status: "active", Schedulable: true, GroupIDs: []int64{11},
		Credentials: map[string]any{"base_url": "https://upstream.example.com", "api_key": "***masked***"},
	}}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	channel := createTestChannel(t, db)
	groups, err := service.ListGroups(false)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %#v, err=%v", groups, err)
	}
	validID := int64(21)
	invalidID := int64(999)
	result, err := service.BindMembersBatch(context.Background(), groups[0].ID, BindingBatchInput{Items: []MemberInput{
		{RemoteAccountID: &validID, SourceChannelID: channel.ID, Concurrency: 12},
		{RemoteAccountID: &invalidID, SourceChannelID: channel.ID, Concurrency: 12},
	}})
	if err != nil {
		t.Fatalf("bind members batch: %v", err)
	}
	if result.Succeeded != 1 || result.Failed != 1 || !result.Items[0].Success || result.Items[1].Success {
		t.Fatalf("batch result = %#v", result)
	}
	if result.Items[0].Member == nil || result.Items[0].Member.RemoteAccountID == nil || *result.Items[0].Member.RemoteAccountID != validID {
		t.Fatalf("bound member = %#v", result.Items[0].Member)
	}
	if !strings.Contains(result.Items[1].Error, "selected group") {
		t.Fatalf("invalid row error = %q", result.Items[1].Error)
	}
}

func TestListRateConnectionsMatchesCurrentMainStationGroups(t *testing.T) {
	service, db, admin, _ := newTestService(t)
	configureTestStation(t, service)
	admin.groups = []sub2api.AdminGroup{
		{ID: 11, Name: "main-openai", Platform: "openai", RateMultiplier: 1, Status: "active"},
		{ID: 12, Name: "main-backup", Platform: "openai", RateMultiplier: 1, Status: "active"},
	}
	admin.accounts = []sub2api.AdminAccount{
		{ID: 21, Name: "account-a", Status: "active", GroupIDs: []int64{11}},
		{ID: 22, Name: "account-b", Status: "active", GroupIDs: []int64{12}},
	}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	groups, err := service.ListGroups(false)
	if err != nil || len(groups) != 2 {
		t.Fatalf("list groups: groups=%#v err=%v", groups, err)
	}
	channel := createTestChannel(t, db)
	sourceGroupID := int64(301)
	for i := range groups {
		poolID, poolErr := service.GroupPoolID(groups[i].ID)
		if poolErr != nil {
			t.Fatalf("resolve pool: %v", poolErr)
		}
		remoteAccountID := int64(21 + i)
		member := &storage.MainAccountPoolMember{
			PoolID: poolID, SourceChannelID: channel.ID, SourceGroupID: &sourceGroupID,
			SourceGroupName: "source-openai", RemoteAccountID: &remoteAccountID,
			OwnershipMode: "bound", BindingStatus: "verified", Status: "active", Enabled: true,
		}
		if err := service.store.CreateMember(member); err != nil {
			t.Fatalf("create member: %v", err)
		}
	}
	rates := []storage.RateSnapshot{
		{ID: 1, ChannelID: channel.ID, RemoteGroupID: &sourceGroupID, ModelName: "source-openai"},
		{ID: 2, ChannelID: channel.ID, ModelName: "not-connected"},
	}
	connections, err := service.ListRateConnections(channel.ID, rates)
	if err != nil {
		t.Fatalf("list rate connections: %v", err)
	}
	if len(connections[1]) != 2 || connections[1][0].GroupName != "main-openai" || connections[1][1].GroupName != "main-backup" {
		t.Fatalf("connected groups = %#v", connections[1])
	}
	if len(connections[2]) != 0 {
		t.Fatalf("unconnected rate groups = %#v", connections[2])
	}

	poolID, err := service.GroupPoolID(groups[0].ID)
	if err != nil {
		t.Fatalf("resolve default pool: %v", err)
	}
	defaultRemoteAccountID := int64(23)
	if err := service.store.CreateMember(&storage.MainAccountPoolMember{
		PoolID: poolID, SourceChannelID: channel.ID, RemoteAccountID: &defaultRemoteAccountID,
		OwnershipMode: "bound", BindingStatus: "verified", Status: "active", Enabled: true,
	}); err != nil {
		t.Fatalf("create default member: %v", err)
	}
	defaultConnections, err := service.ListRateConnections(channel.ID, []storage.RateSnapshot{{ID: 3, ChannelID: channel.ID, ModelName: "default"}})
	if err != nil || len(defaultConnections[3]) != 1 || defaultConnections[3][0].GroupName != "main-openai" {
		t.Fatalf("default group connections = %#v err=%v", defaultConnections[3], err)
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
