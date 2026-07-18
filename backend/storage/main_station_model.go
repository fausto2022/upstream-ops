package storage

import "time"

const (
	MainStationSingletonID uint = 1
	MainStationScale            = int64(1_000_000)
)

// MainStationConfig 将一条现有目标站点提升为唯一 Sub2API 主站。
type MainStationConfig struct {
	ID                      uint       `gorm:"primaryKey;check:id = 1" json:"id"`
	TargetID                uint       `gorm:"not null;uniqueIndex" json:"target_id"`
	Enabled                 bool       `gorm:"not null;default:true" json:"enabled"`
	LastSyncStatus          string     `gorm:"size:32" json:"last_sync_status,omitempty"`
	LastSyncAt              *time.Time `json:"last_sync_at,omitempty"`
	LastSyncError           string     `gorm:"type:text" json:"last_sync_error,omitempty"`
	AutoMarginProtection    bool       `gorm:"not null;default:false" json:"auto_margin_protection"`
	AutoHealthProtection    bool       `gorm:"not null;default:false" json:"auto_health_protection"`
	AutoRecovery            bool       `gorm:"not null;default:false" json:"auto_recovery"`
	HealthModelsJSON        string     `gorm:"type:text;not null;default:'{}'" json:"-"`
	HealthIntervalSeconds   int        `gorm:"not null;default:30" json:"health_interval_seconds"`
	HealthFailureThreshold  int        `gorm:"not null;default:10" json:"health_failure_threshold"`
	HealthRecoveryThreshold int        `gorm:"not null;default:3" json:"health_recovery_threshold"`
	SyncIntervalSeconds     int        `gorm:"not null;default:300" json:"sync_interval_seconds"`
	ObservationEvaluatedAt  *time.Time `json:"observation_evaluated_at,omitempty"`
	HealthObservedAt        *time.Time `json:"health_observed_at,omitempty"`
	MarginObservedAt        *time.Time `json:"margin_observed_at,omitempty"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

func (MainStationConfig) TableName() string { return "main_station_configs" }

// MainStationMigrationState 保存旧同步数据无法自动选择主站时的可见状态。
type MainStationMigrationState struct {
	ID        uint      `gorm:"primaryKey;check:id = 1" json:"id"`
	Status    string    `gorm:"size:32;not null" json:"status"`
	Detail    string    `gorm:"type:text" json:"detail,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (MainStationMigrationState) TableName() string { return "main_station_migration_states" }

// MainStationAccountSnapshot 只保存远端 Account 的非敏感快照。
type MainStationAccountSnapshot struct {
	ID                   uint       `gorm:"primaryKey" json:"id"`
	MainStationID        uint       `gorm:"not null;uniqueIndex:idx_main_station_remote_account" json:"main_station_id"`
	RemoteAccountID      int64      `gorm:"not null;uniqueIndex:idx_main_station_remote_account" json:"remote_account_id"`
	Name                 string     `gorm:"size:256;not null" json:"name"`
	Notes                string     `gorm:"type:text" json:"notes,omitempty"`
	Platform             string     `gorm:"size:64" json:"platform,omitempty"`
	Type                 string     `gorm:"size:64" json:"type,omitempty"`
	Status               string     `gorm:"size:32;index" json:"status"`
	Schedulable          bool       `gorm:"not null;default:false" json:"schedulable"`
	Concurrency          int        `gorm:"not null;default:0" json:"concurrency"`
	Priority             int        `gorm:"not null;default:0" json:"priority"`
	Weight               int        `gorm:"not null;default:1" json:"weight"`
	RateMultiplierMicros int64      `gorm:"not null;default:0" json:"rate_multiplier_micros"`
	GroupIDsJSON         string     `gorm:"type:text;not null" json:"group_ids"`
	BaseURL              string     `gorm:"size:512" json:"base_url,omitempty"`
	CredentialsPresent   bool       `gorm:"not null;default:false" json:"credentials_present"`
	BillingProbeJSON     string     `gorm:"type:text" json:"billing_probe,omitempty"`
	LastUsedAt           *time.Time `json:"last_used_at,omitempty"`
	RemoteUpdatedAt      *time.Time `json:"remote_updated_at,omitempty"`
	LastSyncAt           time.Time  `gorm:"not null;index" json:"last_sync_at"`
	Missing              bool       `gorm:"not null;default:false;index" json:"missing"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

func (MainStationAccountSnapshot) TableName() string { return "main_station_account_snapshots" }

// MainStationProfitSnapshot 保存主站按自然日汇总的实际收入与账号成本。
type MainStationProfitSnapshot struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	MainStationID uint      `gorm:"not null;uniqueIndex:idx_main_station_profit_day" json:"main_station_id"`
	Day           string    `gorm:"size:10;not null;uniqueIndex:idx_main_station_profit_day" json:"day"`
	Revenue       float64   `gorm:"not null" json:"revenue"`
	Cost          float64   `gorm:"not null" json:"cost"`
	SampledAt     time.Time `gorm:"not null;index" json:"sampled_at"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (MainStationProfitSnapshot) TableName() string { return "main_station_profit_snapshots" }

// MainAccountPool 是 RelayDeck 的逻辑账号池，不对应单条远端 Account。
type MainAccountPool struct {
	ID                          uint       `gorm:"primaryKey" json:"id"`
	LegacySyncGroupID           *uint      `gorm:"uniqueIndex" json:"legacy_sync_group_id,omitempty"`
	Name                        string     `gorm:"size:256;not null;uniqueIndex" json:"name"`
	Description                 string     `gorm:"type:text" json:"description,omitempty"`
	Platform                    string     `gorm:"size:64" json:"platform,omitempty"`
	Enabled                     bool       `gorm:"not null;default:true" json:"enabled"`
	MinimumHealthyMembers       int        `gorm:"not null;default:1" json:"minimum_healthy_members"`
	MinimumEffectiveConcurrency int        `gorm:"not null;default:1" json:"minimum_effective_concurrency"`
	RateSortDirection           string     `gorm:"size:16;not null;default:'asc'" json:"rate_sort_direction"`
	HealthPolicyJSON            string     `gorm:"type:text;not null" json:"health_policy"`
	MarginPolicyJSON            string     `gorm:"type:text;not null" json:"margin_policy"`
	LastStatus                  string     `gorm:"size:32;not null;default:'unknown';index" json:"last_status"`
	LastEvaluatedAt             *time.Time `json:"last_evaluated_at,omitempty"`
	CreatedAt                   time.Time  `json:"created_at"`
	UpdatedAt                   time.Time  `json:"updated_at"`
}

func (MainAccountPool) TableName() string { return "main_account_pools" }

type MainAccountPoolGroup struct {
	PoolID        uint      `gorm:"primaryKey" json:"pool_id"`
	TargetGroupID uint      `gorm:"primaryKey;index" json:"target_group_id"`
	CreatedAt     time.Time `json:"created_at"`
}

func (MainAccountPoolGroup) TableName() string { return "main_account_pool_groups" }

type MainAccountPoolMember struct {
	ID                       uint       `gorm:"primaryKey" json:"id"`
	PoolID                   uint       `gorm:"not null;index" json:"pool_id"`
	AccountName              string     `gorm:"size:256;not null;default:''" json:"account_name,omitempty"`
	LegacySyncAccountID      *uint      `gorm:"uniqueIndex" json:"legacy_sync_account_id,omitempty"`
	SourceChannelID          uint       `gorm:"not null;index" json:"source_channel_id"`
	SourceGroupID            *int64     `json:"source_group_id,omitempty"`
	SourceGroupName          string     `gorm:"size:256;not null;default:''" json:"source_group_name,omitempty"`
	SourceAPIKeyID           *int64     `json:"source_api_key_id,omitempty"`
	RemoteAccountID          *int64     `gorm:"uniqueIndex" json:"remote_account_id,omitempty"`
	RemoteAccountName        string     `gorm:"size:256;not null;default:''" json:"remote_account_name,omitempty"`
	OwnershipMode            string     `gorm:"size:16;not null;index" json:"ownership_mode"`
	BindingStatus            string     `gorm:"size:32;not null;index" json:"binding_status"`
	Status                   string     `gorm:"size:32;not null;index" json:"status"`
	Enabled                  bool       `gorm:"not null;default:true" json:"enabled"`
	Preferred                bool       `gorm:"not null;default:false;index" json:"preferred"`
	ProxyID                  *int64     `json:"proxy_id,omitempty"`
	Weight                   int        `gorm:"not null;default:1" json:"weight"`
	Priority                 int        `gorm:"not null;default:1" json:"priority"`
	Concurrency              int        `gorm:"not null;default:10" json:"concurrency"`
	RateConvertMode          string     `gorm:"size:32;not null;default:'raw'" json:"rate_convert_mode"`
	RateConvertValueMicros   int64      `gorm:"not null;default:1000000" json:"rate_convert_value_micros"`
	CostAdjustmentMicros     int64      `gorm:"not null;default:1000000" json:"cost_adjustment_micros"`
	ManualCostMicros         *int64     `json:"manual_cost_micros,omitempty"`
	HealthEnabled            bool       `gorm:"not null;default:true" json:"health_enabled"`
	HealthModel              string     `gorm:"size:256;not null;default:''" json:"health_model,omitempty"`
	HealthIntervalSeconds    int        `gorm:"not null;default:0" json:"health_interval_seconds"`
	HealthFailureThreshold   int        `gorm:"not null;default:0" json:"health_failure_threshold"`
	HealthRecoveryThreshold  int        `gorm:"not null;default:0" json:"health_recovery_threshold"`
	HealthAPIMode            string     `gorm:"size:32;not null;default:'openai_chat'" json:"health_api_mode"`
	LastHealthStatus         string     `gorm:"size:32;not null;default:'unknown';index" json:"last_health_status"`
	LastHealthAt             *time.Time `json:"last_health_at,omitempty"`
	ConsecutiveHealthSuccess int        `gorm:"not null;default:0" json:"consecutive_health_success"`
	ConsecutiveHealthFailure int        `gorm:"not null;default:0" json:"consecutive_health_failure"`
	CooldownUntil            *time.Time `json:"cooldown_until,omitempty"`
	LastCostMicros           *int64     `json:"last_cost_micros,omitempty"`
	LastCostSource           string     `gorm:"size:64" json:"last_cost_source,omitempty"`
	LastCostAt               *time.Time `json:"last_cost_at,omitempty"`
	LastCostExpiresAt        *time.Time `json:"last_cost_expires_at,omitempty"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
}

func (MainAccountPoolMember) TableName() string { return "main_account_pool_members" }

type MainAccountHealthCheck struct {
	ID                  uint      `gorm:"primaryKey" json:"id"`
	PoolID              uint      `gorm:"not null;index" json:"pool_id"`
	MemberID            uint      `gorm:"not null;index" json:"member_id"`
	RemoteAccountID     int64     `gorm:"not null;index" json:"remote_account_id"`
	Level               string    `gorm:"size:8;not null;index" json:"level"`
	Protocol            string    `gorm:"size:32" json:"protocol,omitempty"`
	Model               string    `gorm:"size:256" json:"model,omitempty"`
	Endpoint            string    `gorm:"size:512" json:"endpoint,omitempty"`
	Status              string    `gorm:"size:32;not null;index" json:"status"`
	ErrorClass          string    `gorm:"size:64;index" json:"error_class,omitempty"`
	HTTPStatus          int       `json:"http_status,omitempty"`
	LatencyMS           int64     `gorm:"not null;default:0" json:"latency_ms"`
	InputTokens         *int64    `json:"input_tokens,omitempty"`
	OutputTokens        *int64    `json:"output_tokens,omitempty"`
	TotalTokens         *int64    `json:"total_tokens,omitempty"`
	EstimatedCostMicros *int64    `json:"estimated_cost_micros,omitempty"`
	Message             string    `gorm:"type:text" json:"message,omitempty"`
	TriggeredAction     string    `gorm:"size:64" json:"triggered_action,omitempty"`
	StartedAt           time.Time `gorm:"not null;index" json:"started_at"`
	FinishedAt          time.Time `gorm:"not null" json:"finished_at"`
	CreatedAt           time.Time `gorm:"not null;index" json:"created_at"`
}

func (MainAccountHealthCheck) TableName() string { return "main_account_health_checks" }

type MainAccountProfitCheck struct {
	ID                   uint      `gorm:"primaryKey" json:"id"`
	PoolID               uint      `gorm:"not null;index" json:"pool_id"`
	MemberID             uint      `gorm:"not null;index" json:"member_id"`
	TargetGroupID        uint      `gorm:"not null;index" json:"target_group_id"`
	SaleMultiplierMicros int64     `gorm:"not null;default:0" json:"sale_multiplier_micros"`
	CostMultiplierMicros int64     `gorm:"not null;default:0" json:"cost_multiplier_micros"`
	CostAdjustmentMicros int64     `gorm:"not null;default:1000000" json:"cost_adjustment_micros"`
	MarginValueMicros    int64     `gorm:"not null;default:0" json:"margin_value_micros"`
	MarginBasisPoints    int64     `gorm:"not null;default:0" json:"margin_basis_points"`
	SaleSource           string    `gorm:"size:64" json:"sale_source,omitempty"`
	CostSource           string    `gorm:"size:64" json:"cost_source,omitempty"`
	Status               string    `gorm:"size:32;not null;index" json:"status"`
	Reason               string    `gorm:"type:text" json:"reason,omitempty"`
	ObservedAt           time.Time `gorm:"not null;index" json:"observed_at"`
	CreatedAt            time.Time `gorm:"not null;index" json:"created_at"`
}

func (MainAccountProfitCheck) TableName() string { return "main_account_profit_checks" }

type MainAccountGuardLock struct {
	ID              uint       `gorm:"primaryKey" json:"id"`
	RemoteAccountID int64      `gorm:"not null;uniqueIndex:idx_main_account_lock;index" json:"remote_account_id"`
	MemberID        uint       `gorm:"not null;index" json:"member_id"`
	LockType        string     `gorm:"size:32;not null;uniqueIndex:idx_main_account_lock;index" json:"lock_type"`
	Active          bool       `gorm:"not null;default:true;index" json:"active"`
	Reason          string     `gorm:"type:text" json:"reason,omitempty"`
	EvidenceJSON    string     `gorm:"type:text" json:"evidence,omitempty"`
	CreatedBy       string     `gorm:"size:32;not null" json:"created_by"`
	CreatedAt       time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt       time.Time  `gorm:"not null" json:"updated_at"`
	ClearedAt       *time.Time `json:"cleared_at,omitempty"`
	ClearedBy       string     `gorm:"size:32" json:"cleared_by,omitempty"`
}

func (MainAccountGuardLock) TableName() string { return "main_account_guard_locks" }

type MainAccountAuditLog struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	PoolID          *uint     `gorm:"index" json:"pool_id,omitempty"`
	MemberID        *uint     `gorm:"index" json:"member_id,omitempty"`
	RemoteAccountID *int64    `gorm:"index" json:"remote_account_id,omitempty"`
	Action          string    `gorm:"size:64;not null;index" json:"action"`
	Source          string    `gorm:"size:32;not null;index" json:"source"`
	Success         bool      `gorm:"not null;index" json:"success"`
	BeforeJSON      string    `gorm:"type:text" json:"before,omitempty"`
	AfterJSON       string    `gorm:"type:text" json:"after,omitempty"`
	EvidenceJSON    string    `gorm:"type:text" json:"evidence,omitempty"`
	Detail          string    `gorm:"type:text" json:"detail,omitempty"`
	ErrorMessage    string    `gorm:"type:text" json:"error_message,omitempty"`
	CreatedAt       time.Time `gorm:"not null;index" json:"created_at"`
}

func (MainAccountAuditLog) TableName() string { return "main_account_audit_logs" }

type MainStationNotificationCooldown struct {
	DedupKey   string    `gorm:"size:512;primaryKey" json:"dedup_key"`
	Event      string    `gorm:"size:64;not null;index" json:"event"`
	PoolID     uint      `gorm:"not null;index" json:"pool_id"`
	MemberID   uint      `gorm:"not null;index" json:"member_id"`
	GroupID    uint      `gorm:"not null;index" json:"group_id"`
	LastSentAt time.Time `gorm:"not null;index" json:"last_sent_at"`
}

func (MainStationNotificationCooldown) TableName() string {
	return "main_station_notification_cooldowns"
}
