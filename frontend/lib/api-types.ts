/**
 * API response shapes for UpstreamOps backend.
 * Keep in sync with backend/storage/*.go and backend/api/*.go.
 */

export type ChannelType = "newapi" | "sub2api"

export type CredentialMode = "password" | "token"

export type RechargeMultiplierMode = "divide" | "multiply"

export type NotificationChannelType =
  | "telegram"
  | "webhook"
  | "email"
  | "wecom"
  | "dingtalk"
  | "feishu"
  | "serverchan3"

export type CaptchaProviderType =
  | "capsolver"
  | "2captcha"
  | "anticaptcha"
  | "yescaptcha"

export type MonitorJob = "login" | "balance" | "rates"

export type NotificationEvent =
  | "balance_low"
  | "rate_changed"
  | "rate_structure_changed"
  | "rate_added"
  | "rate_removed"
  | "announcement"
  | "login_failed"
  | "captcha_failed"
  | "monitor_failed"
  | "subscription_daily_remaining_low"
  | "subscription_weekly_remaining_low"
  | "subscription_monthly_remaining_low"
  | "subscription_expiring"
  | "upstream_sync_group_changed"
  | "main_pool_degraded"
  | "main_pool_critical"
  | "main_member_health_failed"
  | "main_member_health_recovered"
  | "main_member_margin_risk"
  | "main_member_margin_recovered"
  | "main_member_disabled"
  | "main_member_reenabled"
  | "main_member_binding_lost"
  | "main_station_sync_failed"
  | "health_probe_budget_exceeded"

export interface Channel {
  id: number
  name: string
  type: ChannelType
  site_url: string
  username: string
  sort_order: number
  user_id?: string
  credential_mode: CredentialMode
  login_extra_params: string
  turnstile_enabled: boolean
  ignore_announcements: boolean
  subscription_enabled: boolean
  proxy_enabled: boolean
  captcha_config_id?: number | null
  balance_threshold: number
  recharge_multiplier?: number | null
  recharge_multiplier_mode: RechargeMultiplierMode
  monitor_enabled: boolean
  last_balance?: number | null
  last_balance_at?: string | null
  today_cost?: number | null
  total_cost?: number | null
  last_error?: string
  created_at: string
  updated_at: string
}

export interface ChannelPage {
  items: Channel[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface CaptchaConfig {
  id: number
  name: string
  type: CaptchaProviderType
  endpoint?: string
  extra?: string
  enabled: boolean
  proxy_enabled: boolean
  last_balance?: number | null
  balance_unit?: string
  balance_at?: string | null
  balance_error?: string
  created_at: string
  updated_at: string
}

export interface RateSnapshot {
  id: number
  channel_id: number
  remote_group_id?: number | null
  model_name: string
  description?: string
  ratio: number
  completion_ratio: number
  first_seen_at: string
  last_seen_at: string
}

export interface RateChangeLog {
  id: number
  channel_id: number
  model_name: string
  old_ratio: number | null
  new_ratio: number
  old_completion_ratio?: number | null
  new_completion_ratio?: number
  changed_at: string
}

export interface RateChangeLogPage {
  items: RateChangeLog[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface BalanceSnapshot {
  id: number
  channel_id: number
  balance: number
  sampled_at: string
}

export interface NotificationSubscription {
  channel_ids: number[]
  mode: "all" | "groups"
  groups?: string[]
  events?: NotificationEvent[]
}

export interface NotificationChannel {
  id: number
  name: string
  type: NotificationChannelType
  enabled: boolean
  proxy_enabled: boolean
  subscriptions?: string
  created_at: string
  updated_at: string
}

export interface NotificationLog {
  id: number
  channel_id: number
  upstream_channel_id?: number
  channel_name?: string
  channel_type?: string
  event: NotificationEvent
  subject: string
  body: string
  success: boolean
  error_message?: string
  sent_at: string
}

export interface UpstreamAnnouncement {
  id: number
  channel_id: number
  source_key: string
  title?: string
  content: string
  type?: string
  link?: string
  published_at?: string | null
  source_updated_at?: string | null
  first_seen_at: string
}

export interface MonitorLog {
  id: number
  channel_id: number
  job: MonitorJob
  success: boolean
  error_message?: string
  duration_ms: number
  started_at: string
  finished_at: string
}

export interface DashboardLowest {
  channel_id: number
  name: string
  balance: number | null
}

export interface DashboardChannelStat {
  id: number
  name: string
  type: string
  monitor_enabled: boolean
  last_balance?: number | null
  today_cost?: number | null
  total_cost?: number | null
  last_error?: string
}

export interface DashboardSummary {
  total_channels: number
  active_channels: number
  failed_channels: number
  total_balance: number
  today_total_cost: number
  total_cost: number
  lowest_balance: DashboardLowest | null
  channels: DashboardChannelStat[]
  recent_rate_changes: RateChangeLog[]
}

export interface BalanceTrendPoint {
  day: string
  balance: number
}

export interface CostTrendPoint {
  day: string
  cost: number
}

export interface SystemAuthConfig {
  enabled: boolean
  username: string
  password: string
  tokenSecret: string
  sessionTTLHours: number
}

export interface AppConfig {
  title: string
  notificationPrefix: string
}

export interface SystemSchedulerRetentionConfig {
  cron: string
  monitorLogsDays: number
  balanceSnapshotsDays: number
  notificationLogsDays: number
  announcementsDays: number
}

export interface SystemSchedulerConfig {
  balanceCron: string
  rateCron: string
  concurrency: number
  retention: SystemSchedulerRetentionConfig
}

export interface SystemNotificationsConfig {
  batchRateChanges: boolean
  minChangePct: number
  balanceLowCooldownMinutes: number
  subscriptionDailyRemainingThresholdPct: number
  subscriptionWeeklyRemainingThresholdPct: number
  subscriptionMonthlyRemainingThresholdPct: number
  subscriptionExpiryThresholdHours: number
  subscriptionAlertCooldownMinutes: number
  sendMaxAttempts: number
}

export interface SystemProxyConfig {
  enabled: boolean
  versionCheckEnabled: boolean
  protocol: "http" | "https" | "socks5"
  host: string
  port: number
  username: string
  password: string
}

export interface SystemUpstreamConfig {
  timeoutSeconds: number
  userAgent: string
}

export interface SystemConfig {
  app: AppConfig
  auth: SystemAuthConfig
  scheduler: SystemSchedulerConfig
  notifications: SystemNotificationsConfig
  proxy: SystemProxyConfig
  upstream: SystemUpstreamConfig
}

export interface SystemConfigResponse {
  config_path: string
  config: SystemConfig
}

export interface AppVersion {
  name: string
  title: string
  version: string
  latest_version?: string
  update_available?: boolean
  repo_url?: string
  release_url?: string
  update_error?: string
}

export interface ApplyConfigResult {
  applied_sections: string[]
  message: string
}

export interface ChannelRedeemResult {
  message: string
  type: string
  value: number
  new_balance?: number
  new_concurrency?: number
  group_name?: string
  validity_days?: number
}

export type RechargePaymentMethod = "alipay" | "wxpay"
export type SubscriptionPaymentMethod =
  | "balance"
  | "alipay"
  | "wxpay"
  | "stripe"
  | "creem"
  | "waffo_pancake"
  | string

export interface ChannelRechargeMethod {
  type: RechargePaymentMethod
  name: string
  min_amount: number
  max_amount: number
}

export interface ChannelRechargeInfo {
  amount_label: string
  amount_step: number
  min_amount: number
  max_amount: number
  preset_amounts: number[]
  help_text?: string
  help_image_url?: string
  alipay_force_qrcode: boolean
  methods: ChannelRechargeMethod[]
}

export interface ChannelRechargeLaunch {
  mode: "qrcode" | "redirect" | "form" | "success"
  qr_code?: string
  pay_url?: string
  form_action?: string
  form_fields?: Record<string, string>
  expires_at?: string
}

export interface ChannelSubscriptionMethod {
  type: SubscriptionPaymentMethod
  name: string
}

export interface ChannelSubscriptionPlan {
  id: string
  name: string
  description?: string
  price: number
  currency?: string
  validity?: string
  group_name?: string
  quota?: number
  daily_limit_usd?: number | null
  weekly_limit_usd?: number | null
  monthly_limit_usd?: number | null
  features?: string[]
  payment_methods?: string[]
}

export interface ChannelSubscriptionInfo {
  plans: ChannelSubscriptionPlan[]
  methods: ChannelSubscriptionMethod[]
}

export type ChannelSubscriptionLaunch = ChannelRechargeLaunch

export interface ChannelSubscriptionUsageWindow {
  limit_usd: number
  used_usd: number
  remaining_usd: number
  remaining_percent: number
  used_percent: number
  window_start?: string | null
  resets_at?: string | null
  resets_in_seconds: number
}

export interface ChannelSubscriptionUsage {
  id: number
  group_id: number
  group_name: string
  status: string
  starts_at?: string | null
  expires_at?: string | null
  expires_in_days: number
  daily?: ChannelSubscriptionUsageWindow | null
  weekly?: ChannelSubscriptionUsageWindow | null
  monthly?: ChannelSubscriptionUsageWindow | null
}

export interface ChannelSubscriptionUsageInfo {
  items: ChannelSubscriptionUsage[]
}

export type ChannelAPIKeyStatus = "active" | "disabled" | "expired" | "quota_exhausted" | "unknown"

export interface ChannelAPIKey {
  id: number
  key: string
  name: string
  status: ChannelAPIKeyStatus | string
  group?: string
  group_name?: string
  group_description?: string
  group_ratio: number
  group_id?: number | null
  quota: number
  quota_used: number
  unlimited_quota: boolean
  expired_time: number
  expires_at?: string | null
  created_at?: string | null
  updated_at?: string | null
  last_used_at?: string | null
  allow_ips?: string
  ip_whitelist?: string[]
  ip_blacklist?: string[]
  model_limits_enabled: boolean
  model_limits?: string
  cross_group_retry: boolean
  rate_limit_5h: number
  rate_limit_1d: number
  rate_limit_7d: number
  usage_5h: number
  usage_1d: number
  usage_7d: number
}

export interface ChannelAPIKeyPage {
  items: ChannelAPIKey[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface NotificationLogPage {
  items: NotificationLog[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface UpstreamAnnouncementPage {
  items: UpstreamAnnouncement[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface ChannelAPIKeyGroup {
  id?: number | null
  name: string
  description?: string
  ratio: number
}

export interface ChannelAPIKeyReveal {
  key: string
}

export interface UpstreamSyncTarget {
  id: number
  name: string
  base_url: string
  enabled: boolean
  last_check_status?: string
  last_check_at?: string | null
  last_check_error?: string
}

export interface UpstreamSyncTargetGroup {
  id: number
  target_id: number
  remote_group_id: number
  name: string
  platform?: string
  ratio: number
  status: string
  sort: number
  description?: string
  last_sync_at?: string | null
}

export interface UpstreamSyncTargetProxy {
  id: number
  name: string
  protocol: string
  host: string
  port: number
  status: string
}

export type UpstreamSyncRateConvertMode = "raw" | "multiply_100" | "divide_100" | "custom"

export interface UpstreamSyncAccount {
  id?: number
  source_channel_id: number
  source_group_id?: number | null
  source_group_name?: string
  proxy_id?: number | null
  concurrency: number
  weight: number
  rate_convert_mode: UpstreamSyncRateConvertMode
  rate_convert_value: number
  enabled: boolean
  test_enabled: boolean
  test_model?: string
}

export interface UpstreamSyncGroup {
  id: number
  display_name: string
  name_template: string
  name: string
  target_id: number
  target_group_ids: number[]
  platform: string
  model_limits_mode: string
  model_limits?: string
  pool_mode_enabled: boolean
  pool_mode_retry_count: number
  pool_mode_retry_status_codes?: string
  custom_error_codes_enabled: boolean
  custom_error_codes?: string
  rate_sort_direction: "asc" | "desc"
  accounts: UpstreamSyncAccount[]
  enabled: boolean
  apply_status?: string
  apply_error?: string
  last_applied_at?: string | null
}

export interface UpstreamSyncLog {
  id: number
  sync_group_id: number
  target_id: number
  action: string
  success: boolean
  message?: string
  created_at: string
}

export interface UpstreamSyncLogPage {
  items: UpstreamSyncLog[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface ChannelAccountLimits {
  concurrency: number
}

export interface MainStationMigrationState {
  status: string
  detail?: string
}

export interface MainStationConfig {
  configured: boolean
  id?: number
  target_id?: number
  name?: string
  base_url?: string
  has_admin_api_key: boolean
  enabled: boolean
  last_sync_status?: string
  last_sync_at?: string | null
  last_sync_error?: string
  auto_margin_protection: boolean
  auto_health_protection: boolean
  auto_recovery: boolean
  health_models: Record<string, string>
  health_interval_seconds: number
  health_failure_threshold: number
  health_recovery_threshold: number
  observation_evaluated_at?: string | null
  health_observed_at?: string | null
  margin_observed_at?: string | null
  migration?: MainStationMigrationState
}

export interface MainStationHealthModelCatalog {
  platform: string
  models: string[]
  error?: string
}

export interface MainStationGroup extends UpstreamSyncTargetGroup {
  rate_multiplier_micros: number
  peak_enabled: boolean
  peak_start?: string
  peak_end?: string
  peak_multiplier_micros: number
  subscription_type?: string
  image_separate_rate: boolean
  video_separate_rate: boolean
  pricing_metadata?: string
  user_min_rate_micros?: number | null
  user_rates_complete: boolean
  missing: boolean
}

export interface MainStationAccount {
  id: number
  main_station_id: number
  remote_account_id: number
  name: string
  notes?: string
  platform?: string
  type?: string
  status: string
  schedulable: boolean
  concurrency: number
  priority: number
  weight: number
  rate_multiplier_micros: number
  group_ids: string
  base_url?: string
  credentials_present: boolean
  billing_probe?: string
  last_used_at?: string | null
  remote_updated_at?: string | null
  last_sync_at: string
  missing: boolean

  member?: MainStationAccountMember | null
}

export interface MainStationAccountMember {
  id: number
  account_name?: string
  ownership_mode: "managed" | "bound"
  binding_status: "pending" | "verified" | "manual_confirmed" | "invalid" | "orphaned"
  status: "pending" | "active" | "degraded" | "quarantined" | "disabled" | "orphaned" | "error"
  enabled: boolean
  preferred: boolean
  source_channel_id: number
  source_group_id?: number | null
  source_group_name?: string
  source_api_key_id?: number | null
  weight: number
  priority: number
  concurrency: number
  health_enabled: boolean
  health_model?: string
  health_interval_seconds: number
  health_failure_threshold: number
  health_recovery_threshold: number
  recent_20_success_rate?: number | null
  last_health_status: string
  last_health_at?: string | null
  consecutive_health_success: number
  consecutive_health_failure: number
}

export interface MainStationGroupWorkspace {
  group: MainStationGroup
  enabled: boolean
  minimum_healthy_accounts: number
  minimum_effective_concurrency: number
  rate_sort_direction: "asc" | "desc"
  health_policy: string
  margin_policy: string
  last_status: "healthy" | "degraded" | "critical" | "unknown" | string
  last_evaluated_at?: string | null
  account_count: number
  managed_account_count: number
}

export interface MainStationMember {
	id: number
	pool_id: number
	account_name?: string
  legacy_sync_account_id?: number | null
  source_channel_id: number
  source_group_id?: number | null
  source_group_name?: string
  source_api_key_id?: number | null
  remote_account_id?: number | null
  remote_account_name?: string
  ownership_mode: "managed" | "bound"
  binding_status: "pending" | "verified" | "manual_confirmed" | "invalid" | "orphaned"
  status: "pending" | "active" | "degraded" | "quarantined" | "disabled" | "orphaned" | "error"
  enabled: boolean
  preferred: boolean
  proxy_id?: number | null
  weight: number
  priority: number
  concurrency: number
  rate_convert_mode: UpstreamSyncRateConvertMode
  rate_convert_value_micros: number
  cost_adjustment_micros: number
  manual_cost_micros?: number | null
  health_enabled: boolean
  health_model?: string
  health_interval_seconds: number
  health_failure_threshold: number
  health_recovery_threshold: number
  health_api_mode: string
  last_health_status: string
  last_health_at?: string | null
  consecutive_health_success: number
  consecutive_health_failure: number
  cooldown_until?: string | null
  last_cost_micros?: number | null
  last_cost_source?: string
  last_cost_at?: string | null
  last_cost_expires_at?: string | null
  created_at: string
  updated_at: string
}

export interface MainStationPage<T> {
  items: T[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface MainStationSyncResult {
  groups: number
  accounts: number
  missing_groups: number[]
  missing_accounts: number[]
  orphaned_members: number
  synced_at: string
}

export interface MainStationHealthCheck {
  id: number
  pool_id: number
  member_id: number
  remote_account_id: number
  level: "L0" | "L1" | "L2" | string
  protocol?: string
  model?: string
  endpoint?: string
  status: string
  error_class?: string
  http_status?: number
  latency_ms: number
  input_tokens?: number | null
  output_tokens?: number | null
  total_tokens?: number | null
  estimated_cost_micros?: number | null
  message?: string
  triggered_action?: string
  started_at: string
  finished_at: string
  created_at: string
}

export interface MainStationHealthStats {
  member_id: number
  last_status: string
  consecutive_success: number
  consecutive_failure: number
  recent_20_success_rate?: number | null
  one_hour_success_rate?: number | null
  twenty_four_hour_success_rate?: number | null
  seven_day_success_rate?: number | null
  average_latency_ms?: number | null
  p50_latency_ms?: number | null
  p95_latency_ms?: number | null
  last_success_at?: string | null
  last_failure_at?: string | null
  last_error_class?: string
  last_error_message?: string
  daily_checks: number
  daily_tokens: number
}

export interface MainStationHealthBudget {
  daily_l1_used: number
  daily_l1_limit: number
  daily_l2_used: number
  daily_l2_limit: number
  daily_tokens: number
  token_limit: number
}

export interface MainStationMemberHealthSummary {
  member: MainStationMember
  stats: MainStationHealthStats
  budget: MainStationHealthBudget
}

export interface MainStationProfitCheck {
  id: number
  pool_id: number
  member_id: number
  target_group_id: number
  sale_multiplier_micros: number
  cost_multiplier_micros: number
  cost_adjustment_micros: number
  margin_value_micros: number
  margin_basis_points: number
  sale_source?: string
  cost_source?: string
  status: "healthy" | "risk" | "unknown" | "unsupported" | string
  reason?: string
  observed_at: string
  created_at: string
}

export interface MainStationGuardLock {
  id: number
  remote_account_id: number
  member_id: number
  lock_type: "manual" | "margin" | "health" | "sync" | "credential" | "binding" | string
  active: boolean
  reason?: string
  evidence?: string
  created_by: string
  created_at: string
  updated_at: string
  cleared_at?: string | null
  cleared_by?: string
}

export interface MainStationPoolEvaluation {
  pool_id: number
  checks: MainStationProfitCheck[]
  healthy: number
  risk: number
  unknown: number
  unsupported: number
  would_disable_member_ids: number[]
  protection_applied_member_ids: number[]
  evaluated_at: string
}

export interface MainStationProtectionPreview {
  health_ready: boolean
  margin_ready: boolean
  unhealthy_member_ids: number[]
  margin_risk_member_ids: number[]
  schedulable_account_ids: number[]
  active_locks: MainStationGuardLock[]
}

export interface MainStationPoolCapacity {
  pool_id: number
  status: string
  total_members: number
  healthy_members: number
  profitable_members: number
  qualified_members: number
  schedulable_members: number
  effective_concurrency: number
}

export interface MainStationBulkOperation {
  attempted: number
  succeeded: number
  skipped: number
  errors: string[]
}

export interface MainStationAuditLog {
  id: number
  pool_id?: number | null
  member_id?: number | null
  remote_account_id?: number | null
  action: string
  source: string
  success: boolean
  before?: string
  after?: string
  evidence?: string
  detail?: string
  error_message?: string
  created_at: string
}
