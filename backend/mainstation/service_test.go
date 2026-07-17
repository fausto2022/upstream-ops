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
	schedulableCalls    []bool
	setSchedulableErr   error
	applyBeforeSetError bool
	deletedAccounts     []int64
	nextAccountID       int64
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
func (f *fakeAdminClient) TestAccount(context.Context, sub2api.AdminTarget, int64, string) (*sub2api.AdminAccountTestResult, error) {
	return &sub2api.AdminAccountTestResult{ResponseText: "ok"}, nil
}

type fakeChannelService struct {
	secret      string
	groups      []connector.APIKeyGroup
	createdKeys []connector.APIKeyCreateRequest
	deletedKeys []int64
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
	if _, err := service.CreateConfig(context.Background(), ConfigInput{
		Name: "other", BaseURL: "https://other.example.com", AdminAPIKey: "other-key",
	}); !errors.Is(err, ErrAlreadyConfigured) {
		t.Fatalf("second create error = %v", err)
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
		OwnershipMode: "managed", SourceChannelID: channel.ID, SourceGroupID: &sourceGroupID,
		SourceGroupName: "source-group", Enabled: boolPtr(true), HealthEnabled: boolPtr(true),
		HealthModel: "gpt-test", HealthAPIMode: "openai_chat",
		Weight: 2, Priority: 3, Concurrency: 4, RateConvertMode: "raw", CostAdjustment: 1,
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
	if request.Credentials["api_key"] != channels.secret || request.Credentials["base_url"] != channel.SiteURL {
		t.Fatalf("managed account credentials = %#v", request.Credentials)
	}
	if len(request.GroupIDs) != 1 || request.GroupIDs[0] != 31 || request.RateMultiplier != 0.8 {
		t.Fatalf("managed account request = %#v", request)
	}
	if len(admin.schedulableCalls) != 1 || !admin.schedulableCalls[0] {
		t.Fatalf("schedulable calls = %#v", admin.schedulableCalls)
	}

	if err := service.DeleteMember(context.Background(), pool.ID, member.ID, DeleteMemberInput{Confirm: true}); err != nil {
		t.Fatalf("delete managed member: %v", err)
	}
	if len(admin.deletedAccounts) != 0 || len(channels.deletedKeys) != 0 {
		t.Fatalf("default delete removed remote resources: accounts=%v keys=%v", admin.deletedAccounts, channels.deletedKeys)
	}
	if len(admin.schedulableCalls) != 2 || admin.schedulableCalls[1] {
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
	channelSvc := &fakeChannelService{secret: "sk-source-secret"}
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
