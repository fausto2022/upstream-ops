package mainstation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/fausto2022/relaydeck/backend/config"
	"github.com/fausto2022/relaydeck/backend/connector"
	"github.com/fausto2022/relaydeck/backend/connector/sub2api"
	"github.com/fausto2022/relaydeck/backend/crypto"
	"github.com/fausto2022/relaydeck/backend/notify"
	"github.com/fausto2022/relaydeck/backend/storage"
	"gorm.io/gorm"
)

const (
	defaultHealthIntervalSeconds       = 30
	minimumGlobalHealthIntervalSeconds = 30
	minimumMemberHealthIntervalSeconds = 1
	maximumHealthIntervalSeconds       = 86400
	defaultHealthFailureThreshold      = 10
	defaultHealthRecoveryThreshold     = 3
	defaultRankingIntervalSeconds      = 30
	minimumRankingIntervalSeconds      = 5
	defaultMainStationSyncInterval     = 300
	maximumHealthThreshold             = 100
)

type globalHealthSettings struct {
	Models            map[string]string
	IntervalSeconds   int
	FailureThreshold  int
	RecoveryThreshold int
}

type channelService interface {
	RevealAPIKey(ctx context.Context, channelID uint, keyID int64) (string, error)
	CreateAPIKey(ctx context.Context, channelID uint, req connector.APIKeyCreateRequest) (*connector.APIKey, error)
	DeleteAPIKey(ctx context.Context, channelID uint, keyID int64) error
	ListAPIKeys(ctx context.Context, channelID uint, query connector.APIKeyQuery) (*connector.APIKeyPage, error)
	ListAPIKeyGroups(ctx context.Context, channelID uint) ([]connector.APIKeyGroup, error)
	GetAccountLimits(ctx context.Context, channelID uint) (*connector.AccountLimits, error)
}

type adminClient interface {
	Ping(ctx context.Context, target sub2api.AdminTarget) error
	ListGroups(ctx context.Context, target sub2api.AdminTarget, includeInactive bool) ([]sub2api.AdminGroup, error)
	ListGroupRateMultipliers(ctx context.Context, target sub2api.AdminTarget, groupID int64) ([]float64, error)
	ListAllAccounts(ctx context.Context, target sub2api.AdminTarget) ([]sub2api.AdminAccount, error)
	FindAccountByName(ctx context.Context, target sub2api.AdminTarget, name string) (*sub2api.AdminAccount, error)
	GetAccount(ctx context.Context, target sub2api.AdminTarget, id int64) (*sub2api.AdminAccount, error)
	CreateAccount(ctx context.Context, target sub2api.AdminTarget, req sub2api.AdminAccount) (*sub2api.AdminAccount, error)
	UpdateAccount(ctx context.Context, target sub2api.AdminTarget, id int64, req sub2api.AdminAccount) (*sub2api.AdminAccount, error)
	UpdateAccountScheduling(ctx context.Context, target sub2api.AdminTarget, id int64, req sub2api.AdminAccountSchedulingUpdate) (*sub2api.AdminAccount, error)
	SetAccountSchedulable(ctx context.Context, target sub2api.AdminTarget, id int64, schedulable bool) (*sub2api.AdminAccount, error)
	DeleteAccount(ctx context.Context, target sub2api.AdminTarget, id int64) error
	SyncAccountModelsFromUpstream(ctx context.Context, target sub2api.AdminTarget, id int64) ([]string, error)
	ListAccountModels(ctx context.Context, target sub2api.AdminTarget, id int64) ([]string, error)
	TestAccount(ctx context.Context, target sub2api.AdminTarget, id int64, modelID string) (*sub2api.AdminAccountTestResult, error)
}

type Service struct {
	store            *storage.MainStationStore
	targets          *storage.UpstreamSyncTargets
	targetGroups     *storage.UpstreamSyncTargetGroups
	channels         *storage.Channels
	rates            *storage.Rates
	managedAccounts  *storage.UpstreamSyncManagedAccounts
	cipher           *crypto.Cipher
	channelSvc       channelService
	log              *slog.Logger
	dispatcher       *notify.Dispatcher
	adminFactory     func() adminClient
	healthMu         sync.Mutex
	healthRunning    map[string]struct{}
	healthGlobal     chan struct{}
	healthChannels   map[uint]chan struct{}
	healthScheduleMu sync.Mutex
	syncScheduleMu   sync.Mutex
	rateTestMu       sync.Mutex
	rateTests        map[string]struct{}
	tempCleanupMu    sync.Mutex
	tempCleanupAt    time.Time
	probeConfigMu    sync.RWMutex
	proxyConfig      config.ProxyConfig
	probeTimeout     time.Duration
	probeUserAgent   string
	now              func() time.Time
	scheduleLocks    sync.Map
	rankingLocks     sync.Map
}

func (s *Service) SetDispatcher(dispatcher *notify.Dispatcher) {
	s.dispatcher = dispatcher
}

func (s *Service) DeleteHistoryBefore(cutoff time.Time) (storage.MainStationRetentionResult, error) {
	return s.store.DeleteHistoryBefore(cutoff)
}

func New(
	store *storage.MainStationStore,
	targets *storage.UpstreamSyncTargets,
	targetGroups *storage.UpstreamSyncTargetGroups,
	channels *storage.Channels,
	rates *storage.Rates,
	managedAccounts *storage.UpstreamSyncManagedAccounts,
	cipher *crypto.Cipher,
	channelSvc channelService,
	log *slog.Logger,
) *Service {
	return &Service{
		store:           store,
		targets:         targets,
		targetGroups:    targetGroups,
		channels:        channels,
		rates:           rates,
		managedAccounts: managedAccounts,
		cipher:          cipher,
		channelSvc:      channelSvc,
		log:             log,
		adminFactory: func() adminClient {
			return sub2api.NewAdminClient()
		},
		healthRunning:  make(map[string]struct{}),
		healthGlobal:   make(chan struct{}, 4),
		healthChannels: make(map[uint]chan struct{}),
		rateTests:      make(map[string]struct{}),
		probeTimeout:   15 * time.Second,
		probeUserAgent: "RelayDeck/main-station-health",
		now:            time.Now,
	}
}

func (s *Service) UpdateProbeConfig(proxy config.ProxyConfig, timeout time.Duration, userAgent string) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if strings.TrimSpace(userAgent) == "" {
		userAgent = "RelayDeck/main-station-health"
	}
	s.probeConfigMu.Lock()
	s.proxyConfig = proxy
	s.probeTimeout = timeout
	s.probeUserAgent = strings.TrimSpace(userAgent)
	s.probeConfigMu.Unlock()
}

func (s *Service) GetConfig() (*ConfigDTO, error) {
	dto := &ConfigDTO{
		HealthModels:            map[string]string{},
		HealthIntervalSeconds:   defaultHealthIntervalSeconds,
		HealthFailureThreshold:  defaultHealthFailureThreshold,
		HealthRecoveryThreshold: defaultHealthRecoveryThreshold,
		RankingIntervalSeconds:  defaultRankingIntervalSeconds,
		SyncIntervalSeconds:     defaultMainStationSyncInterval,
	}
	if state, err := s.store.GetMigrationState(); err == nil {
		dto.Migration = &MigrationStateDTO{Status: state.Status, Detail: state.Detail}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	config, err := s.store.GetConfig()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return dto, nil
	}
	if err != nil {
		return nil, err
	}
	target, err := s.targets.FindByID(config.TargetID)
	if err != nil {
		return nil, fmt.Errorf("load main station target: %w", err)
	}
	dto.Configured = true
	dto.ID = config.ID
	dto.TargetID = target.ID
	dto.Name = target.Name
	dto.BaseURL = target.BaseURL
	dto.HasAdminAPIKey = strings.TrimSpace(target.AdminAPIKeyCipher) != ""
	dto.Enabled = config.Enabled
	dto.LastSyncStatus = config.LastSyncStatus
	dto.LastSyncAt = config.LastSyncAt
	dto.LastSyncError = config.LastSyncError
	dto.AutoMarginProtection = config.AutoMarginProtection
	dto.AutoHealthProtection = config.AutoHealthProtection
	dto.AutoRecovery = config.AutoRecovery
	dto.HealthModels = decodeHealthModels(config.HealthModelsJSON)
	dto.HealthIntervalSeconds = normalizedGlobalHealthInterval(config.HealthIntervalSeconds)
	dto.HealthFailureThreshold = normalizedHealthThreshold(config.HealthFailureThreshold, defaultHealthFailureThreshold)
	dto.HealthRecoveryThreshold = normalizedHealthThreshold(config.HealthRecoveryThreshold, defaultHealthRecoveryThreshold)
	dto.RankingIntervalSeconds = normalizedRankingInterval(config.RankingIntervalSeconds)
	dto.SyncIntervalSeconds = normalizedSyncInterval(config.SyncIntervalSeconds)
	dto.ObservationEvaluatedAt = config.ObservationEvaluatedAt
	dto.HealthObservedAt = config.HealthObservedAt
	dto.MarginObservedAt = config.MarginObservedAt
	return dto, nil
}

func (s *Service) ListTargetCandidates() ([]storage.UpstreamSyncTarget, error) {
	return s.targets.List()
}

func (s *Service) CreateConfig(ctx context.Context, in ConfigInput) (*ConfigDTO, error) {
	if _, err := s.store.GetConfig(); err == nil {
		return nil, ErrAlreadyConfigured
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, errors.New("main station name is required")
	}
	baseURL, err := normalizeMainStationURL(in.BaseURL)
	if err != nil {
		return nil, err
	}
	apiKey := strings.TrimSpace(in.AdminAPIKey)
	if apiKey == "" {
		return nil, errors.New("admin api key is required")
	}
	if err := s.adminFactory().Ping(ctx, sub2api.AdminTarget{BaseURL: baseURL, APIKey: apiKey}); err != nil {
		return nil, fmt.Errorf("test main station connection: %w", redactSecretError(err, apiKey))
	}
	cipherText, err := s.cipher.Encrypt(apiKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt main station admin api key: %w", err)
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	healthModelsJSON, err := encodeHealthModels(in.HealthModels)
	if err != nil {
		return nil, err
	}
	healthIntervalSeconds := defaultHealthIntervalSeconds
	if in.HealthIntervalSeconds != nil {
		healthIntervalSeconds = *in.HealthIntervalSeconds
		if err := validateGlobalHealthInterval(healthIntervalSeconds); err != nil {
			return nil, err
		}
	}
	healthFailureThreshold := defaultHealthFailureThreshold
	if in.HealthFailureThreshold != nil {
		healthFailureThreshold = *in.HealthFailureThreshold
		if err := validateHealthThreshold("health failure threshold", healthFailureThreshold); err != nil {
			return nil, err
		}
	}
	healthRecoveryThreshold := defaultHealthRecoveryThreshold
	if in.HealthRecoveryThreshold != nil {
		healthRecoveryThreshold = *in.HealthRecoveryThreshold
		if err := validateHealthThreshold("health recovery threshold", healthRecoveryThreshold); err != nil {
			return nil, err
		}
	}
	syncIntervalSeconds := defaultMainStationSyncInterval
	if in.SyncIntervalSeconds != nil {
		syncIntervalSeconds = *in.SyncIntervalSeconds
		if err := validateSyncInterval(syncIntervalSeconds); err != nil {
			return nil, err
		}
	}
	rankingIntervalSeconds := defaultRankingIntervalSeconds
	if in.RankingIntervalSeconds != nil {
		rankingIntervalSeconds = *in.RankingIntervalSeconds
		if err := validateRankingInterval(rankingIntervalSeconds); err != nil {
			return nil, err
		}
	}
	config := &storage.MainStationConfig{
		ID:                      storage.MainStationSingletonID,
		Enabled:                 enabled,
		HealthModelsJSON:        healthModelsJSON,
		HealthIntervalSeconds:   healthIntervalSeconds,
		HealthFailureThreshold:  healthFailureThreshold,
		HealthRecoveryThreshold: healthRecoveryThreshold,
		RankingIntervalSeconds:  rankingIntervalSeconds,
		SyncIntervalSeconds:     syncIntervalSeconds,
	}
	target := &storage.UpstreamSyncTarget{
		Name:              name,
		BaseURL:           baseURL,
		AdminAPIKeyCipher: cipherText,
		Enabled:           enabled,
		LastCheckStatus:   "success",
		LastCheckAt:       ptrTime(time.Now()),
	}
	if in.TargetID != 0 {
		existing, err := s.targets.FindByID(in.TargetID)
		if err != nil {
			return nil, fmt.Errorf("load selected target: %w", err)
		}
		target.ID = existing.ID
		target.CreatedAt = existing.CreatedAt
		if err := s.store.AttachConfigToTarget(target, config); err != nil {
			return nil, err
		}
	} else if err := s.store.CreateConfigWithTarget(target, config); err != nil {
		return nil, err
	}
	if err := s.store.MigrateLegacyData(); err != nil {
		return nil, fmt.Errorf("migrate legacy main station data: %w", err)
	}
	if err := s.store.MarkAllPoolRankingsDirty(s.now()); err != nil && s.log != nil {
		s.log.Warn("mark main station rankings dirty after create", "err", err)
	}
	schedulingErr := s.reconcileAllScheduling(ctx, "manual")
	if schedulingErr != nil {
		if s.log != nil {
			s.log.Warn("main station created; scheduling reconcile queued for retry", "err", schedulingErr)
		}
	}
	detail := ""
	errText := ""
	if schedulingErr != nil {
		detail = "main station created; scheduling reconcile queued for retry"
		errText = sanitizeText(schedulingErr.Error())
	}
	_ = s.appendAudit(nil, nil, nil, "main_station_create", "manual", schedulingErr == nil, nil, target, nil, detail, errText)
	return s.GetConfig()
}

func (s *Service) UpdateConfig(ctx context.Context, in ConfigInput) (*ConfigDTO, error) {
	config, target, apiKey, err := s.loadAdminTarget()
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = target.Name
	}
	baseURL := target.BaseURL
	if strings.TrimSpace(in.BaseURL) != "" {
		baseURL, err = normalizeMainStationURL(in.BaseURL)
		if err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(in.AdminAPIKey) != "" {
		apiKey = strings.TrimSpace(in.AdminAPIKey)
	}
	if apiKey == "" {
		return nil, errors.New("admin api key is required")
	}
	if err := s.adminFactory().Ping(ctx, sub2api.AdminTarget{BaseURL: baseURL, APIKey: apiKey}); err != nil {
		return nil, fmt.Errorf("test main station connection: %w", redactSecretError(err, apiKey))
	}
	before := *target
	beforeConfig := *config
	target.Name = name
	target.BaseURL = baseURL
	target.LastCheckStatus = "success"
	target.LastCheckAt = ptrTime(time.Now())
	target.LastCheckError = ""
	if strings.TrimSpace(in.AdminAPIKey) != "" {
		target.AdminAPIKeyCipher, err = s.cipher.Encrypt(apiKey)
		if err != nil {
			return nil, fmt.Errorf("encrypt main station admin api key: %w", err)
		}
	}
	enabledChanged := false
	if in.Enabled != nil {
		enabledChanged = config.Enabled != *in.Enabled
		config.Enabled = *in.Enabled
		target.Enabled = *in.Enabled
	}
	if in.AutoMarginProtection != nil {
		if *in.AutoMarginProtection && !config.AutoMarginProtection && config.MarginObservedAt == nil {
			return nil, errors.New("run a read-only margin evaluation before enabling automatic margin protection")
		}
		config.AutoMarginProtection = *in.AutoMarginProtection
	}
	if in.AutoHealthProtection != nil {
		if *in.AutoHealthProtection && !config.AutoHealthProtection && config.HealthObservedAt == nil {
			return nil, errors.New("run a health check before enabling automatic health protection")
		}
		config.AutoHealthProtection = *in.AutoHealthProtection
	}
	if in.AutoRecovery != nil {
		config.AutoRecovery = *in.AutoRecovery
	}
	if in.HealthModels != nil {
		config.HealthModelsJSON, err = encodeHealthModels(in.HealthModels)
		if err != nil {
			return nil, err
		}
	}
	if in.HealthIntervalSeconds != nil {
		if err := validateGlobalHealthInterval(*in.HealthIntervalSeconds); err != nil {
			return nil, err
		}
		config.HealthIntervalSeconds = *in.HealthIntervalSeconds
	}
	if in.HealthFailureThreshold != nil {
		if err := validateHealthThreshold("health failure threshold", *in.HealthFailureThreshold); err != nil {
			return nil, err
		}
		config.HealthFailureThreshold = *in.HealthFailureThreshold
	}
	if in.HealthRecoveryThreshold != nil {
		if err := validateHealthThreshold("health recovery threshold", *in.HealthRecoveryThreshold); err != nil {
			return nil, err
		}
		config.HealthRecoveryThreshold = *in.HealthRecoveryThreshold
	}
	if in.SyncIntervalSeconds != nil {
		if err := validateSyncInterval(*in.SyncIntervalSeconds); err != nil {
			return nil, err
		}
		config.SyncIntervalSeconds = *in.SyncIntervalSeconds
	}
	rankingChanged := false
	if in.RankingIntervalSeconds != nil {
		if err := validateRankingInterval(*in.RankingIntervalSeconds); err != nil {
			return nil, err
		}
		rankingChanged = config.RankingIntervalSeconds != *in.RankingIntervalSeconds
		config.RankingIntervalSeconds = *in.RankingIntervalSeconds
	}
	if err := s.store.UpdateConfigWithTarget(target, config); err != nil {
		return nil, err
	}
	if err := s.reconcileHealthProtectionPolicy(ctx, &beforeConfig, config, "manual"); err != nil {
		return nil, fmt.Errorf("reconcile health protection policy: %w", err)
	}
	if rankingChanged || enabledChanged {
		if err := s.store.MarkAllPoolRankingsDirty(s.now()); err != nil && s.log != nil {
			s.log.Warn("mark main station rankings dirty after config change", "err", err)
		}
	}
	var schedulingErr error
	if enabledChanged {
		schedulingErr = s.reconcileAllScheduling(ctx, "manual")
		if schedulingErr != nil {
			if s.log != nil {
				s.log.Warn("main station saved; scheduling reconcile queued for retry", "err", schedulingErr)
			}
		}
	}
	detail := ""
	errText := ""
	if schedulingErr != nil {
		detail = "main station saved; scheduling reconcile queued for retry"
		errText = sanitizeText(schedulingErr.Error())
	}
	_ = s.appendAudit(nil, nil, nil, "main_station_update", "manual", schedulingErr == nil, before, target, nil, detail, errText)
	return s.GetConfig()
}

func normalizedRankingInterval(value int) int {
	if value < minimumRankingIntervalSeconds || value > maximumHealthIntervalSeconds {
		return defaultRankingIntervalSeconds
	}
	return value
}

func validateRankingInterval(value int) error {
	if value < minimumRankingIntervalSeconds || value > maximumHealthIntervalSeconds {
		return fmt.Errorf("ranking interval must be between %d and %d seconds", minimumRankingIntervalSeconds, maximumHealthIntervalSeconds)
	}
	return nil
}

func normalizedSyncInterval(value int) int {
	if value < 30 || value > 86400 {
		return defaultMainStationSyncInterval
	}
	return value
}

func validateSyncInterval(value int) error {
	if value < 30 || value > 86400 {
		return errors.New("main station sync interval must be between 30 and 86400 seconds")
	}
	return nil
}

func (s *Service) TestConnection(ctx context.Context, in *ConfigInput) error {
	if in != nil && strings.TrimSpace(in.BaseURL) != "" {
		baseURL, err := normalizeMainStationURL(in.BaseURL)
		if err != nil {
			return err
		}
		apiKey := strings.TrimSpace(in.AdminAPIKey)
		if apiKey == "" {
			return errors.New("admin api key is required")
		}
		if err := s.adminFactory().Ping(ctx, sub2api.AdminTarget{BaseURL: baseURL, APIKey: apiKey}); err != nil {
			return fmt.Errorf("test main station connection: %w", redactSecretError(err, apiKey))
		}
		return nil
	}
	_, target, apiKey, err := s.loadAdminTarget()
	if err != nil {
		return err
	}
	if err := s.adminFactory().Ping(ctx, sub2api.AdminTarget{BaseURL: target.BaseURL, APIKey: apiKey}); err != nil {
		return fmt.Errorf("test main station connection: %w", redactSecretError(err, apiKey))
	}
	return nil
}

func (s *Service) loadAdminTarget() (*storage.MainStationConfig, *storage.UpstreamSyncTarget, string, error) {
	config, err := s.store.GetConfig()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, "", ErrNotConfigured
	}
	if err != nil {
		return nil, nil, "", err
	}
	target, err := s.targets.FindByID(config.TargetID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("load main station target: %w", err)
	}
	apiKey, err := s.cipher.Decrypt(target.AdminAPIKeyCipher)
	if err != nil {
		return nil, nil, "", fmt.Errorf("decrypt main station admin api key: %w", err)
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, nil, "", errors.New("main station admin api key is missing")
	}
	return config, target, apiKey, nil
}

func (s *Service) appendAudit(poolID, memberID *uint, remoteAccountID *int64, action, source string, success bool, before, after, evidence any, detail, errText string) error {
	return s.store.AppendAudit(&storage.MainAccountAuditLog{
		PoolID:          poolID,
		MemberID:        memberID,
		RemoteAccountID: remoteAccountID,
		Action:          action,
		Source:          source,
		Success:         success,
		BeforeJSON:      safeJSON(before),
		AfterJSON:       safeJSON(after),
		EvidenceJSON:    safeJSON(evidence),
		Detail:          sanitizeText(detail),
		ErrorMessage:    sanitizeText(errText),
	})
}

func safeJSON(value any) string {
	if value == nil {
		return ""
	}
	body, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return ""
	}
	sanitized, err := json.Marshal(redactSensitiveJSON(decoded))
	if err != nil {
		return ""
	}
	return sanitizeText(string(sanitized))
}

func redactSensitiveJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if sensitiveJSONKey(key) {
				out[key] = "[redacted]"
				continue
			}
			out[key] = redactSensitiveJSON(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = redactSensitiveJSON(typed[i])
		}
		return out
	default:
		return value
	}
}

func sensitiveJSONKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if strings.HasSuffix(key, "_cipher") {
		return true
	}
	switch key {
	case "password", "secret", "api_key", "admin_api_key", "access_token", "refresh_token",
		"token", "cookie", "csrf", "csrf_token", "authorization", "client_secret":
		return true
	default:
		return false
	}
}

func sanitizeText(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 4096 {
		value = value[:4096]
	}
	return value
}

func normalizeMainStationURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid main station url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("main station url must use http or https")
	}
	if parsed.Hostname() == "" || parsed.User != nil {
		return "", errors.New("main station url must contain a host and no user info")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("main station url must not contain query or fragment")
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil {
		if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return "", errors.New("main station url uses a forbidden address")
		}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func redactSecretError(err error, secret string) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	if secret != "" {
		text = strings.ReplaceAll(text, secret, "[redacted]")
	}
	return errors.New(sanitizeText(text))
}

func ptrTime(value time.Time) *time.Time { return &value }

func validateGlobalHealthInterval(seconds int) error {
	if seconds < minimumGlobalHealthIntervalSeconds || seconds > maximumHealthIntervalSeconds {
		return fmt.Errorf("health interval must be between %d and %d seconds", minimumGlobalHealthIntervalSeconds, maximumHealthIntervalSeconds)
	}
	return nil
}

func validateMemberHealthInterval(seconds int) error {
	if seconds == 0 {
		return nil
	}
	if seconds < minimumMemberHealthIntervalSeconds || seconds > maximumHealthIntervalSeconds {
		return fmt.Errorf("member health interval must be between %d and %d seconds", minimumMemberHealthIntervalSeconds, maximumHealthIntervalSeconds)
	}
	return nil
}

func normalizedGlobalHealthInterval(seconds int) int {
	if seconds < minimumGlobalHealthIntervalSeconds || seconds > maximumHealthIntervalSeconds {
		return defaultHealthIntervalSeconds
	}
	return seconds
}

func effectiveHealthInterval(memberSeconds, globalSeconds int) time.Duration {
	if memberSeconds >= minimumMemberHealthIntervalSeconds && memberSeconds <= maximumHealthIntervalSeconds {
		return time.Duration(memberSeconds) * time.Second
	}
	return time.Duration(normalizedGlobalHealthInterval(globalSeconds)) * time.Second
}

func validateMemberHealthThreshold(name string, value int) error {
	if value == 0 {
		return nil
	}
	return validateHealthThreshold(name, value)
}

func validateHealthThreshold(name string, value int) error {
	if value < 1 || value > maximumHealthThreshold {
		return fmt.Errorf("%s must be between 1 and %d", name, maximumHealthThreshold)
	}
	return nil
}

func normalizedHealthThreshold(value, fallback int) int {
	if value < 1 || value > maximumHealthThreshold {
		return fallback
	}
	return value
}

func effectiveHealthThreshold(memberValue, globalValue, fallback int) int {
	if memberValue >= 1 && memberValue <= maximumHealthThreshold {
		return memberValue
	}
	return normalizedHealthThreshold(globalValue, fallback)
}

func (s *Service) configuredHealthSettings() globalHealthSettings {
	config, err := s.store.GetConfig()
	if err != nil {
		return globalHealthSettings{
			Models: map[string]string{}, IntervalSeconds: defaultHealthIntervalSeconds,
			FailureThreshold: defaultHealthFailureThreshold, RecoveryThreshold: defaultHealthRecoveryThreshold,
		}
	}
	return globalHealthSettings{
		Models:            decodeHealthModels(config.HealthModelsJSON),
		IntervalSeconds:   normalizedGlobalHealthInterval(config.HealthIntervalSeconds),
		FailureThreshold:  normalizedHealthThreshold(config.HealthFailureThreshold, defaultHealthFailureThreshold),
		RecoveryThreshold: normalizedHealthThreshold(config.HealthRecoveryThreshold, defaultHealthRecoveryThreshold),
	}
}
