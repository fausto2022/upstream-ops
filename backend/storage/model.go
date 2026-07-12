package storage

import "time"

// ChannelType 上游渠道类型。
type ChannelType string

const (
	ChannelTypeNewAPI  ChannelType = "newapi"
	ChannelTypeSub2API ChannelType = "sub2api"
)

// CredentialMode 渠道凭据模式：
//   - password: 经典模式，存账号 + 密码，由 Connector 走完整登录流程
//   - token:    跳过登录，存用户已有的 cookie / access_token，直接构造 AuthSession
//
// token 模式不依赖打码 / 不会自动续期，token 失效时表现为 last_error 显示鉴权失败。
type CredentialMode string

const (
	CredentialModePassword CredentialMode = "password"
	CredentialModeToken    CredentialMode = "token"
)

// Channel 上游渠道账号。Password / Turnstile API key 等敏感字段都加密保存。
//
// 注意：会话凭据（access_token / refresh_token / cookie / csrf）单独存放在 AuthSession 表。
//
// CredentialMode + PasswordCipher 的语义重载：
//   - password 模式（默认）：Username + PasswordCipher 存账号密码，由 Connector.Login 用
//   - token    模式：PasswordCipher 存 JSON blob（NewAPI: {cookie,user_id} / Sub2API: {access_token,refresh_token}），
//     channel.Service 解析后直接构造 AuthSession，跳过 Login。Username 字段在 token 模式下保留
//     用户填写的备注（一般是邮箱），仅做展示。
//
// 复用 PasswordCipher 而不新增 TokenCipher 是为了让现有的 GORM 行 / 加密路径 / 迁移流程零变动。
type Channel struct {
	ID                     uint           `gorm:"primaryKey" json:"id"`
	Name                   string         `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Type                   ChannelType    `gorm:"size:32;not null;index" json:"type"`
	SiteURL                string         `gorm:"size:512;not null" json:"site_url"`
	Username               string         `gorm:"size:256;not null" json:"username"`
	SortOrder              int            `gorm:"not null;default:1" json:"sort_order"`
	PasswordCipher         string         `gorm:"size:4096;not null" json:"-"`
	CredentialMode         CredentialMode `gorm:"size:16;not null;default:'password'" json:"credential_mode"`
	LoginExtraParams       string         `gorm:"type:text" json:"login_extra_params"`
	TurnstileEnabled       bool           `gorm:"default:false" json:"turnstile_enabled"`
	IgnoreAnnouncements    bool           `gorm:"default:false" json:"ignore_announcements"`
	SubscriptionEnabled    bool           `gorm:"default:false" json:"subscription_enabled"`
	ProxyEnabled           bool           `gorm:"default:false" json:"proxy_enabled"`
	CaptchaConfigID        *uint          `json:"captcha_config_id,omitempty"`
	BalanceThreshold       float64        `gorm:"default:0" json:"balance_threshold"`
	RechargeMultiplier     *float64       `json:"recharge_multiplier,omitempty"`
	RechargeMultiplierMode string         `gorm:"size:16;not null;default:'divide'" json:"recharge_multiplier_mode"`
	MonitorEnabled         bool           `gorm:"default:true" json:"monitor_enabled"`

	// 最近一次采集结果（聚合视图，便于列表页直接展示）
	LastBalance   *float64   `json:"last_balance,omitempty"`
	LastBalanceAt *time.Time `json:"last_balance_at,omitempty"`
	TodayCost     *float64   `json:"today_cost,omitempty"`
	TotalCost     *float64   `json:"total_cost,omitempty"`
	LastError     string     `gorm:"type:text" json:"last_error,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Channel) TableName() string { return "channels" }

// AuthSession 渠道登录后保存的凭据，按 ChannelID 一对一关联。
// *Cipher 字段都用 AES-GCM 加密；UserID 是上游账号 ID 字符串（非敏感），明文存放。
type AuthSession struct {
	ChannelID          uint       `gorm:"primaryKey" json:"channel_id"`
	UserID             string     `gorm:"size:64" json:"user_id,omitempty"`
	AccessTokenCipher  string     `gorm:"type:text" json:"-"`
	RefreshTokenCipher string     `gorm:"type:text" json:"-"`
	CookieCipher       string     `gorm:"type:text" json:"-"`
	CSRFTokenCipher    string     `gorm:"size:1024" json:"-"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
	LastLoginAt        *time.Time `json:"last_login_at,omitempty"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

func (AuthSession) TableName() string { return "auth_sessions" }

// CaptchaProviderType 打码平台类型。
type CaptchaProviderType string

const (
	CaptchaCapSolver   CaptchaProviderType = "capsolver"
	CaptchaTwoCaptcha  CaptchaProviderType = "2captcha"
	CaptchaAntiCaptcha CaptchaProviderType = "anticaptcha"
	CaptchaYesCaptcha  CaptchaProviderType = "yescaptcha"
)

// CaptchaConfig 打码平台配置。APIKeyCipher 加密保存，Extra 存放各平台差异化 JSON。
type CaptchaConfig struct {
	ID           uint                `gorm:"primaryKey" json:"id"`
	Name         string              `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Type         CaptchaProviderType `gorm:"size:32;not null;index" json:"type"`
	APIKeyCipher string              `gorm:"size:1024" json:"-"`
	Endpoint     string              `gorm:"size:512" json:"endpoint,omitempty"`
	Extra        string              `gorm:"type:text" json:"extra,omitempty"`
	Enabled      bool                `gorm:"default:true" json:"enabled"`
	ProxyEnabled bool                `gorm:"default:false" json:"proxy_enabled"`
	LastBalance  *float64            `json:"last_balance,omitempty"`
	BalanceUnit  string              `gorm:"size:32" json:"balance_unit,omitempty"`
	BalanceAt    *time.Time          `json:"balance_at,omitempty"`
	BalanceError string              `gorm:"type:text" json:"balance_error,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

func (CaptchaConfig) TableName() string { return "captcha_configs" }

// RateSnapshot 渠道当前观察到的模型 / 分组倍率快照。upsert per (channel_id, model_name)。
// 实际的"变化历史"在 RateChangeLog；此表只保存当前状态。
type RateSnapshot struct {
	ID              uint    `gorm:"primaryKey" json:"id"`
	ChannelID       uint    `gorm:"not null;uniqueIndex:idx_rate_chan_model" json:"channel_id"`
	RemoteGroupID   *int64  `json:"remote_group_id,omitempty"`
	ModelName       string  `gorm:"size:256;not null;uniqueIndex:idx_rate_chan_model" json:"model_name"`
	Description     string  `gorm:"size:512" json:"description,omitempty"`
	Ratio           float64 `gorm:"not null" json:"ratio"`
	CompletionRatio float64 `json:"completion_ratio"`

	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

func (RateSnapshot) TableName() string { return "rate_snapshots" }

// RateChangeLog 倍率变化历史。每次扫描发现差异时写入一行。
type RateChangeLog struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	ChannelID          uint      `gorm:"not null;index" json:"channel_id"`
	ModelName          string    `gorm:"size:256;not null;index" json:"model_name"`
	OldRatio           *float64  `json:"old_ratio,omitempty"`
	NewRatio           float64   `gorm:"not null" json:"new_ratio"`
	OldCompletionRatio *float64  `json:"old_completion_ratio,omitempty"`
	NewCompletionRatio float64   `json:"new_completion_ratio"`
	ChangedAt          time.Time `gorm:"not null;index" json:"changed_at"`
}

func (RateChangeLog) TableName() string { return "rate_change_logs" }

// UpstreamAnnouncement 保存从上游渠道同步到的公告。
type UpstreamAnnouncement struct {
	ID              uint       `gorm:"primaryKey" json:"id"`
	ChannelID       uint       `gorm:"not null;uniqueIndex:idx_announcement_chan_source;index" json:"channel_id"`
	SourceKey       string     `gorm:"size:512;not null;uniqueIndex:idx_announcement_chan_source" json:"source_key"`
	Title           string     `gorm:"size:512" json:"title,omitempty"`
	Content         string     `gorm:"type:text;not null" json:"content"`
	Type            string     `gorm:"size:64" json:"type,omitempty"`
	Link            string     `gorm:"size:512" json:"link,omitempty"`
	PublishedAt     *time.Time `json:"published_at,omitempty"`
	SourceUpdatedAt *time.Time `json:"source_updated_at,omitempty"`
	FirstSeenAt     time.Time  `gorm:"not null;index" json:"first_seen_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (UpstreamAnnouncement) TableName() string { return "upstream_announcements" }

// BalanceSnapshot 周期性余额采样，用于图表展示。
type BalanceSnapshot struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ChannelID uint      `gorm:"not null;index" json:"channel_id"`
	Balance   float64   `gorm:"not null" json:"balance"`
	SampledAt time.Time `gorm:"not null;index" json:"sampled_at"`
}

func (BalanceSnapshot) TableName() string { return "balance_snapshots" }

// CostSnapshot 周期性消费采样，用于图表展示。
type CostSnapshot struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ChannelID uint      `gorm:"not null;index" json:"channel_id"`
	TodayCost float64   `gorm:"not null" json:"today_cost"`
	SampledAt time.Time `gorm:"not null;index" json:"sampled_at"`
}

func (CostSnapshot) TableName() string { return "cost_snapshots" }

// NotificationChannelType 通知渠道类型。第一版至少 telegram，其它预留。
type NotificationChannelType string

const (
	NotifyTelegram    NotificationChannelType = "telegram"
	NotifyWebhook     NotificationChannelType = "webhook"
	NotifyEmail       NotificationChannelType = "email"
	NotifyWecom       NotificationChannelType = "wecom"
	NotifyDingTalk    NotificationChannelType = "dingtalk"
	NotifyFeishu      NotificationChannelType = "feishu"
	NotifyServerChan3 NotificationChannelType = "serverchan3"
)

// NotificationChannel 通知渠道配置。ConfigCipher 加密保存 JSON 配置（含 token / webhook url / 密码等）。
//
// Subscriptions 是 JSON 数组，记录该渠道关心的上游、事件和分组过滤；为空 / "[]" 表示订阅一切。
// 非敏感数据，明文保存，方便 Dispatcher 直接读取过滤而不解密。
type NotificationChannel struct {
	ID            uint                    `gorm:"primaryKey" json:"id"`
	Name          string                  `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Type          NotificationChannelType `gorm:"size:32;not null;index" json:"type"`
	ConfigCipher  string                  `gorm:"type:text;not null" json:"-"`
	Subscriptions string                  `gorm:"size:4096;not null;default:'[]'" json:"subscriptions"`
	Enabled       bool                    `gorm:"default:true" json:"enabled"`
	ProxyEnabled  bool                    `gorm:"default:false" json:"proxy_enabled"`
	CreatedAt     time.Time               `json:"created_at"`
	UpdatedAt     time.Time               `json:"updated_at"`
}

func (NotificationChannel) TableName() string { return "notification_channels" }

// NotificationEvent 系统内部触发的通知事件类型。
type NotificationEvent string

const (
	EventBalanceLow               NotificationEvent = "balance_low"
	EventRateChanged              NotificationEvent = "rate_changed"
	EventRateStructureChanged     NotificationEvent = "rate_structure_changed"
	EventRateAdded                NotificationEvent = "rate_added"
	EventRateRemoved              NotificationEvent = "rate_removed"
	EventAnnouncement             NotificationEvent = "announcement"
	EventLoginFailed              NotificationEvent = "login_failed"
	EventCaptchaFailed            NotificationEvent = "captcha_failed"
	EventMonitorFailed            NotificationEvent = "monitor_failed"
	EventSubscriptionDailyLow     NotificationEvent = "subscription_daily_remaining_low"
	EventSubscriptionWeeklyLow    NotificationEvent = "subscription_weekly_remaining_low"
	EventSubscriptionMonthlyLow   NotificationEvent = "subscription_monthly_remaining_low"
	EventSubscriptionExpiring     NotificationEvent = "subscription_expiring"
	EventUpstreamSyncGroupChanged NotificationEvent = "upstream_sync_group_changed"
)

// NotificationLog 通知发送记录。
type NotificationLog struct {
	ID                uint              `gorm:"primaryKey" json:"id"`
	ChannelID         uint              `gorm:"not null;index" json:"channel_id"`
	UpstreamChannelID uint              `gorm:"not null;default:0;index" json:"upstream_channel_id,omitempty"`
	Event             NotificationEvent `gorm:"size:64;not null;index" json:"event"`
	Subject           string            `gorm:"size:512;not null" json:"subject"`
	Body              string            `gorm:"type:text" json:"body"`
	Success           bool              `gorm:"not null" json:"success"`
	ErrorMessage      string            `gorm:"type:text" json:"error_message,omitempty"`
	SentAt            time.Time         `gorm:"not null;index" json:"sent_at"`
}

func (NotificationLog) TableName() string { return "notification_logs" }

// NotificationCooldown 跨重启持久化的通知冷却记录。
//
// 业务键 (ChannelID, Event)：标记某渠道某类事件最近一次发送时间。
// Dispatcher 在发送 cooldown-aware 事件（如 balance_low）前查这张表，
// 命中且未过 cooldown 就跳过。
//
// 不和 NotificationLog 合并是因为：
//   - NotificationLog 是审计/历史日志（用户可见、可清理）
//   - NotificationCooldown 是去抖控制平面（仅最新一条、原子 upsert）
//
// ChannelID 这里指的是**上游渠道**（storage.Channel），不是通知渠道。
type NotificationCooldown struct {
	ChannelID  uint              `gorm:"primaryKey" json:"channel_id"`
	Event      NotificationEvent `gorm:"primaryKey;size:64" json:"event"`
	LastSentAt time.Time         `gorm:"not null" json:"last_sent_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

func (NotificationCooldown) TableName() string { return "notification_cooldowns" }

// MonitorJob 监控任务类型。
type MonitorJob string

const (
	MonitorJobLogin   MonitorJob = "login"
	MonitorJobBalance MonitorJob = "balance"
	MonitorJobRates   MonitorJob = "rates"
)

// MonitorLog 每次扫描 / 登录尝试的结果，便于诊断失败。
type MonitorLog struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	ChannelID    uint       `gorm:"not null;index" json:"channel_id"`
	Job          MonitorJob `gorm:"size:32;not null;index" json:"job"`
	Success      bool       `gorm:"not null" json:"success"`
	ErrorMessage string     `gorm:"type:text" json:"error_message,omitempty"`
	DurationMS   int64      `json:"duration_ms"`
	StartedAt    time.Time  `gorm:"not null;index" json:"started_at"`
	FinishedAt   time.Time  `json:"finished_at"`
}

func (MonitorLog) TableName() string { return "monitor_logs" }

// UpstreamSyncTarget 目标 Sub2API 站点配置。
//
// 管理员 API Key 单独加密保存，检测结果只作为状态缓存，不影响已保存的同步分组。
type UpstreamSyncTarget struct {
	ID                uint       `gorm:"primaryKey" json:"id"`
	Name              string     `gorm:"size:128;not null;uniqueIndex" json:"name"`
	BaseURL           string     `gorm:"size:512;not null" json:"base_url"`
	AdminAPIKeyCipher string     `gorm:"type:text;not null" json:"-"`
	Enabled           bool       `gorm:"default:true" json:"enabled"`
	LastCheckStatus   string     `gorm:"size:32" json:"last_check_status,omitempty"`
	LastCheckAt       *time.Time `json:"last_check_at,omitempty"`
	LastCheckError    string     `gorm:"type:text" json:"last_check_error,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

func (UpstreamSyncTarget) TableName() string { return "upstream_sync_targets" }

// UpstreamSyncTargetGroup 是目标 Sub2API 分组缓存。
//
// 同一个目标站点内按 (target_id, remote_group_id) upsert
type UpstreamSyncTargetGroup struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	TargetID      uint       `gorm:"not null;uniqueIndex:idx_upstream_sync_target_group" json:"target_id"`
	RemoteGroupID int64      `gorm:"not null;uniqueIndex:idx_upstream_sync_target_group" json:"remote_group_id"`
	Name          string     `gorm:"size:256;not null" json:"name"`
	Platform      string     `gorm:"size:64" json:"platform,omitempty"`
	Ratio         float64    `gorm:"not null" json:"ratio"`
	Status        string     `gorm:"size:32;index" json:"status"`
	Sort          int        `json:"sort"`
	Description   string     `gorm:"type:text" json:"description,omitempty"`
	LastSyncAt    *time.Time `json:"last_sync_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (UpstreamSyncTargetGroup) TableName() string { return "upstream_sync_target_groups" }

// UpstreamSyncGroup 保存一组目标分组同步配置。
//
// 分组名称和名称模板创建后固定，不允许二次修改，避免远端对象命名漂移。
type UpstreamSyncGroup struct {
	ID                       uint       `gorm:"primaryKey" json:"id"`
	DisplayName              string     `gorm:"size:256;not null;default:''" json:"display_name"`
	NameTemplate             string     `gorm:"size:256;not null" json:"name_template"`
	Name                     string     `gorm:"size:256;not null;uniqueIndex" json:"name"`
	TargetID                 uint       `gorm:"not null;index" json:"target_id"`
	TargetGroupIDsJSON       string     `gorm:"type:text;not null" json:"target_group_ids"`
	Platform                 string     `gorm:"size:64;not null" json:"platform"`
	ModelLimitsMode          string     `gorm:"size:32;not null;default:'sync_upstream'" json:"model_limits_mode"`
	ModelLimitsText          string     `gorm:"type:text" json:"model_limits,omitempty"`
	PoolModeEnabled          bool       `gorm:"default:false" json:"pool_mode_enabled"`
	PoolModeRetryCount       int        `gorm:"default:10" json:"pool_mode_retry_count"`
	PoolModeRetryStatusCodes string     `gorm:"type:text" json:"pool_mode_retry_status_codes,omitempty"`
	CustomErrorCodesEnabled  bool       `gorm:"default:false" json:"custom_error_codes_enabled"`
	CustomErrorCodes         string     `gorm:"type:text" json:"custom_error_codes,omitempty"`
	RateSortDirection        string     `gorm:"size:16;not null;default:'asc'" json:"rate_sort_direction"`
	Enabled                  bool       `gorm:"default:true" json:"enabled"`
	ApplyStatus              string     `gorm:"size:64" json:"apply_status,omitempty"`
	ApplyError               string     `gorm:"type:text" json:"apply_error,omitempty"`
	LastAppliedAt            *time.Time `json:"last_applied_at,omitempty"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
}

func (UpstreamSyncGroup) TableName() string { return "upstream_sync_groups" }

// UpstreamSyncAccount 是同步分组下的一条账号同步配置。
type UpstreamSyncAccount struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	SyncGroupID      uint      `gorm:"not null;index" json:"sync_group_id"`
	Position         int       `gorm:"not null;default:0" json:"position"`
	SourceChannelID  uint      `gorm:"not null;index" json:"source_channel_id"`
	SourceGroupID    *int64    `json:"source_group_id,omitempty"`
	SourceGroupName  string    `gorm:"size:256;not null;default:''" json:"source_group_name,omitempty"`
	ProxyID          *int64    `json:"proxy_id,omitempty"`
	Concurrency      int       `gorm:"default:10" json:"concurrency"`
	Weight           int       `gorm:"default:1" json:"weight"`
	RateConvertMode  string    `gorm:"size:32;not null;default:'raw'" json:"rate_convert_mode"`
	RateConvertValue float64   `gorm:"default:1" json:"rate_convert_value"`
	Enabled          bool      `gorm:"default:true" json:"enabled"`
	TestEnabled      bool      `gorm:"default:false" json:"test_enabled"`
	TestModel        string    `gorm:"size:256;not null;default:''" json:"test_model,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (UpstreamSyncAccount) TableName() string { return "upstream_sync_accounts" }

// UpstreamSyncManagedAccount 记录同步账号在远端生成的两个对象映射，便于幂等更新和删除确认。
type UpstreamSyncManagedAccount struct {
	ID                 uint       `gorm:"primaryKey" json:"id"`
	SyncGroupID        uint       `gorm:"not null;index" json:"sync_group_id"`
	SyncAccountID      uint       `gorm:"not null;uniqueIndex" json:"sync_account_id"`
	SourceAPIKeyID     int64      `gorm:"not null" json:"source_api_key_id"`
	SourceAPIKeyName   string     `gorm:"size:256;not null" json:"source_api_key_name"`
	TargetAccountID    int64      `gorm:"not null" json:"target_account_id"`
	TargetAccountName  string     `gorm:"size:256;not null" json:"target_account_name"`
	TargetGroupIDsJSON string     `gorm:"type:text;not null" json:"target_group_ids"`
	LastAppliedAt      *time.Time `json:"last_applied_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

func (UpstreamSyncManagedAccount) TableName() string { return "upstream_sync_managed_accounts" }

// UpstreamSyncLog 记录同步分组的执行结果。
type UpstreamSyncLog struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	SyncGroupID uint      `gorm:"not null;index" json:"sync_group_id"`
	TargetID    uint      `gorm:"not null;index" json:"target_id"`
	Action      string    `gorm:"size:64;not null;index" json:"action"`
	Success     bool      `gorm:"not null" json:"success"`
	Message     string    `gorm:"type:text" json:"message,omitempty"`
	Detail      string    `gorm:"type:text" json:"detail,omitempty"`
	CreatedAt   time.Time `gorm:"not null;index" json:"created_at"`
}

func (UpstreamSyncLog) TableName() string { return "upstream_sync_logs" }
