package mainstation

import (
	"errors"
	"time"

	"github.com/bejix/upstream-ops/backend/storage"
)

var (
	ErrNotConfigured     = errors.New("main station is not configured")
	ErrAlreadyConfigured = errors.New("main station is already configured")
	ErrBindingConflict   = errors.New("remote account is already bound")
)

type MigrationStateDTO struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type ConfigDTO struct {
	Configured             bool               `json:"configured"`
	ID                     uint               `json:"id,omitempty"`
	TargetID               uint               `json:"target_id,omitempty"`
	Name                   string             `json:"name,omitempty"`
	BaseURL                string             `json:"base_url,omitempty"`
	HasAdminAPIKey         bool               `json:"has_admin_api_key"`
	Enabled                bool               `json:"enabled"`
	LastSyncStatus         string             `json:"last_sync_status,omitempty"`
	LastSyncAt             *time.Time         `json:"last_sync_at,omitempty"`
	LastSyncError          string             `json:"last_sync_error,omitempty"`
	AutoMarginProtection   bool               `json:"auto_margin_protection"`
	AutoHealthProtection   bool               `json:"auto_health_protection"`
	AutoRecovery           bool               `json:"auto_recovery"`
	ObservationEvaluatedAt *time.Time         `json:"observation_evaluated_at,omitempty"`
	HealthObservedAt       *time.Time         `json:"health_observed_at,omitempty"`
	MarginObservedAt       *time.Time         `json:"margin_observed_at,omitempty"`
	Migration              *MigrationStateDTO `json:"migration,omitempty"`
}

type ConfigInput struct {
	TargetID             uint   `json:"target_id,omitempty"`
	Name                 string `json:"name"`
	BaseURL              string `json:"base_url"`
	AdminAPIKey          string `json:"admin_api_key"`
	Enabled              *bool  `json:"enabled,omitempty"`
	AutoMarginProtection *bool  `json:"auto_margin_protection,omitempty"`
	AutoHealthProtection *bool  `json:"auto_health_protection,omitempty"`
	AutoRecovery         *bool  `json:"auto_recovery,omitempty"`
}

type SyncResult struct {
	Groups          int       `json:"groups"`
	Accounts        int       `json:"accounts"`
	MissingGroups   []int64   `json:"missing_groups"`
	MissingAccounts []int64   `json:"missing_accounts"`
	OrphanedMembers int       `json:"orphaned_members"`
	SyncedAt        time.Time `json:"synced_at"`
}

type GroupDTO struct {
	storage.UpstreamSyncTargetGroup
}

type AccountDTO struct {
	storage.MainStationAccountSnapshot
	Member *AccountMemberDTO `json:"member,omitempty"`
}

type AccountMemberDTO struct {
	ID                       uint       `json:"id"`
	AccountName              string     `json:"account_name,omitempty"`
	OwnershipMode            string     `json:"ownership_mode"`
	BindingStatus            string     `json:"binding_status"`
	Status                   string     `json:"status"`
	Enabled                  bool       `json:"enabled"`
	Preferred                bool       `json:"preferred"`
	SourceChannelID          uint       `json:"source_channel_id"`
	SourceGroupID            *int64     `json:"source_group_id,omitempty"`
	SourceGroupName          string     `json:"source_group_name,omitempty"`
	SourceAPIKeyID           *int64     `json:"source_api_key_id,omitempty"`
	Weight                   int        `json:"weight"`
	Priority                 int        `json:"priority"`
	Concurrency              int        `json:"concurrency"`
	HealthEnabled            bool       `json:"health_enabled"`
	HealthModel              string     `json:"health_model,omitempty"`
	LastHealthStatus         string     `json:"last_health_status"`
	LastHealthAt             *time.Time `json:"last_health_at,omitempty"`
	ConsecutiveHealthSuccess int        `json:"consecutive_health_success"`
	ConsecutiveHealthFailure int        `json:"consecutive_health_failure"`
}

type GroupWorkspaceDTO struct {
	Group                       storage.UpstreamSyncTargetGroup `json:"group"`
	Enabled                     bool                            `json:"enabled"`
	MinimumHealthyAccounts      int                             `json:"minimum_healthy_accounts"`
	MinimumEffectiveConcurrency int                             `json:"minimum_effective_concurrency"`
	RateSortDirection           string                          `json:"rate_sort_direction"`
	HealthPolicy                string                          `json:"health_policy"`
	MarginPolicy                string                          `json:"margin_policy"`
	LastStatus                  string                          `json:"last_status"`
	LastEvaluatedAt             *time.Time                      `json:"last_evaluated_at,omitempty"`
	AccountCount                int                             `json:"account_count"`
	ManagedAccountCount         int                             `json:"managed_account_count"`
}

type GroupSettingsInput struct {
	Enabled                     *bool  `json:"enabled,omitempty"`
	MinimumHealthyAccounts      int    `json:"minimum_healthy_accounts"`
	MinimumEffectiveConcurrency int    `json:"minimum_effective_concurrency"`
	RateSortDirection           string `json:"rate_sort_direction"`
	HealthPolicy                string `json:"health_policy"`
	MarginPolicy                string `json:"margin_policy"`
}

type Page[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Pages    int   `json:"pages"`
}

type PoolInput struct {
	Name                        string `json:"name"`
	Description                 string `json:"description"`
	Platform                    string `json:"platform"`
	Enabled                     *bool  `json:"enabled,omitempty"`
	MinimumHealthyMembers       int    `json:"minimum_healthy_members"`
	MinimumEffectiveConcurrency int    `json:"minimum_effective_concurrency"`
	RateSortDirection           string `json:"rate_sort_direction"`
	HealthPolicy                string `json:"health_policy"`
	MarginPolicy                string `json:"margin_policy"`
	TargetGroupIDs              []uint `json:"target_group_ids"`
}

type PoolDTO struct {
	storage.MainAccountPool
	TargetGroupIDs []uint                            `json:"target_group_ids"`
	Groups         []storage.UpstreamSyncTargetGroup `json:"groups"`
	Members        []storage.MainAccountPoolMember   `json:"members"`
}

type MemberInput struct {
	AccountName            string   `json:"account_name"`
	OwnershipMode          string   `json:"ownership_mode"`
	SourceChannelID        uint     `json:"source_channel_id"`
	SourceGroupID          *int64   `json:"source_group_id,omitempty"`
	SourceGroupName        string   `json:"source_group_name,omitempty"`
	SourceAPIKeyID         *int64   `json:"source_api_key_id,omitempty"`
	RemoteAccountID        *int64   `json:"remote_account_id,omitempty"`
	ManualBindingConfirmed bool     `json:"manual_binding_confirmed"`
	Enabled                *bool    `json:"enabled,omitempty"`
	Preferred              *bool    `json:"preferred,omitempty"`
	ProxyID                *int64   `json:"proxy_id,omitempty"`
	Weight                 int      `json:"weight"`
	Priority               int      `json:"priority"`
	Concurrency            int      `json:"concurrency"`
	RateConvertMode        string   `json:"rate_convert_mode"`
	RateConvertValue       float64  `json:"rate_convert_value"`
	CostAdjustment         float64  `json:"cost_adjustment"`
	ManualCostMultiplier   *float64 `json:"manual_cost_multiplier,omitempty"`
	HealthEnabled          *bool    `json:"health_enabled,omitempty"`
	HealthModel            string   `json:"health_model,omitempty"`
	HealthAPIMode          string   `json:"health_api_mode,omitempty"`
}

type DeleteMemberInput struct {
	DeleteRemoteAccount bool `json:"delete_remote_account"`
	DeleteSourceAPIKey  bool `json:"delete_source_api_key"`
	Confirm             bool `json:"confirm"`
}

type HealthCheckInput struct {
	Level string `json:"level"`
	Force bool   `json:"force"`
}

type HealthStats struct {
	MemberID                  uint       `json:"member_id"`
	LastStatus                string     `json:"last_status"`
	ConsecutiveSuccess        int        `json:"consecutive_success"`
	ConsecutiveFailure        int        `json:"consecutive_failure"`
	Recent20SuccessRate       *float64   `json:"recent_20_success_rate,omitempty"`
	OneHourSuccessRate        *float64   `json:"one_hour_success_rate,omitempty"`
	TwentyFourHourSuccessRate *float64   `json:"twenty_four_hour_success_rate,omitempty"`
	SevenDaySuccessRate       *float64   `json:"seven_day_success_rate,omitempty"`
	AverageLatencyMS          *float64   `json:"average_latency_ms,omitempty"`
	P50LatencyMS              *int64     `json:"p50_latency_ms,omitempty"`
	P95LatencyMS              *int64     `json:"p95_latency_ms,omitempty"`
	LastSuccessAt             *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt             *time.Time `json:"last_failure_at,omitempty"`
	LastErrorClass            string     `json:"last_error_class,omitempty"`
	LastErrorMessage          string     `json:"last_error_message,omitempty"`
	DailyChecks               int64      `json:"daily_checks"`
	DailyTokens               int64      `json:"daily_tokens"`
}

type HealthBudget struct {
	DailyL1Used  int64 `json:"daily_l1_used"`
	DailyL1Limit int   `json:"daily_l1_limit"`
	DailyL2Used  int64 `json:"daily_l2_used"`
	DailyL2Limit int   `json:"daily_l2_limit"`
	DailyTokens  int64 `json:"daily_tokens"`
	TokenLimit   int64 `json:"token_limit"`
}

type HealthCheckResult struct {
	Check  storage.MainAccountHealthCheck `json:"check"`
	Member storage.MainAccountPoolMember  `json:"member"`
	Stats  HealthStats                    `json:"stats"`
	Budget HealthBudget                   `json:"budget"`
}

type MemberHealthSummary struct {
	Member storage.MainAccountPoolMember `json:"member"`
	Stats  HealthStats                   `json:"stats"`
	Budget HealthBudget                  `json:"budget"`
}

type PoolEvaluationResult struct {
	PoolID            uint                             `json:"pool_id"`
	Checks            []storage.MainAccountProfitCheck `json:"checks"`
	Healthy           int                              `json:"healthy"`
	Risk              int                              `json:"risk"`
	Unknown           int                              `json:"unknown"`
	Unsupported       int                              `json:"unsupported"`
	WouldDisable      []uint                           `json:"would_disable_member_ids"`
	ProtectionApplied []uint                           `json:"protection_applied_member_ids"`
	EvaluatedAt       time.Time                        `json:"evaluated_at"`
}

type SchedulingDecision struct {
	RemoteAccountID    int64                          `json:"remote_account_id"`
	DesiredSchedulable bool                           `json:"desired_schedulable"`
	RemoteSchedulable  bool                           `json:"remote_schedulable"`
	Changed            bool                           `json:"changed"`
	Reason             string                         `json:"reason"`
	Locks              []storage.MainAccountGuardLock `json:"locks"`
}

type GuardLockInput struct {
	Reason   string `json:"reason"`
	Evidence any    `json:"evidence,omitempty"`
}

type ProtectionPreview struct {
	HealthReady           bool                           `json:"health_ready"`
	MarginReady           bool                           `json:"margin_ready"`
	UnhealthyMemberIDs    []uint                         `json:"unhealthy_member_ids"`
	MarginRiskMemberIDs   []uint                         `json:"margin_risk_member_ids"`
	SchedulableAccountIDs []int64                        `json:"schedulable_account_ids"`
	ActiveLocks           []storage.MainAccountGuardLock `json:"active_locks"`
}

type PoolCapacityResult struct {
	PoolID               uint   `json:"pool_id"`
	Status               string `json:"status"`
	TotalMembers         int    `json:"total_members"`
	HealthyMembers       int    `json:"healthy_members"`
	ProfitableMembers    int    `json:"profitable_members"`
	QualifiedMembers     int    `json:"qualified_members"`
	SchedulableMembers   int    `json:"schedulable_members"`
	EffectiveConcurrency int    `json:"effective_concurrency"`
}

type BulkOperationResult struct {
	Attempted int      `json:"attempted"`
	Succeeded int      `json:"succeeded"`
	Skipped   int      `json:"skipped"`
	Errors    []string `json:"errors"`
}

type ProtectionPolicyInput struct {
	AutoMarginProtection *bool `json:"auto_margin_protection,omitempty"`
	AutoHealthProtection *bool `json:"auto_health_protection,omitempty"`
	AutoRecovery         *bool `json:"auto_recovery,omitempty"`
}
