# 主站账号池技术设计

## 1. 调查结论

### 1.1 现有数据关系

```text
UpstreamSyncTarget
  ├─ UpstreamSyncTargetGroup (target_id, remote_group_id 唯一)
  └─ UpstreamSyncGroup
       ├─ TargetGroupIDsJSON -> UpstreamSyncTargetGroup.id[]
       ├─ UpstreamSyncAccount (源渠道、源分组、权重、并发、测活模型)
       └─ UpstreamSyncManagedAccount
            ├─ SyncAccountID -> UpstreamSyncAccount.id（一对一）
            ├─ SourceAPIKeyID -> 源站 API Key
            └─ TargetAccountID -> 目标 Sub2API Account
```

当前 `syncer.Service.ApplySyncGroup` 会读取同步分组、创建或复用源 API Key、创建或更新远端 Account、同步模型、执行 Account test，并保存托管映射。实际远端对象仍由 Sub2API 调度。

### 1.2 直接写 `schedulable` 的路径

`backend/syncer/service.go` 当前存在以下绕过统一决策的写入：

- 测试或模型同步失败时直接写 `false`。
- Account test 成功后直接写 `true`。
- `syncRemoteAccountSchedulable` 根据远端 `status == active` 直接写入对应布尔值。
- 占位 Account、跳过账号和应用失败清理会直接写 `false`。

其中失败路径可以继续触发 `sync` 或 `health` 锁，但所有最终远端写入必须收口到统一调度决策服务；成功路径不得直接启用 Account。

### 1.3 迁移、scheduler、通知和前端

- 数据库在 `storage.AutoMigrate` 中统一使用 GORM `AutoMigrate`，SQLite 使用 WAL 和单连接，MySQL 使用官方 GORM driver。
- scheduler 使用 `robfig/cron`，余额和倍率任务各自带 5 分钟 context timeout；现有同步器挂在倍率扫描之后执行。
- 通知冷却使用 `notification_cooldowns` 持久化，但键目前是 `(channel_id, event)`，主站事件需要独立的复合去抖键。
- 前端使用 React Router、集中 `lib/api.ts`、`lib/api-types.ts`、Radix/shadcn 风格组件和 Sonner toast；当前无独立 i18n 框架，界面文本以中文为主。
- “上游动态同步”目前位于设置页，主站功能需要新增 `/main-station` 一级路由和侧栏入口。

### 1.4 已确认的 Sub2API 管理契约

当前 connector 与 `httptest` 已覆盖以下契约：

- `x-api-key` 管理员鉴权。
- `GET /api/v1/admin/groups/all`。
- `GET /api/v1/admin/accounts?page=&page_size=`。
- `POST /api/v1/admin/accounts`、`PUT /api/v1/admin/accounts/:id`。
- `POST /api/v1/admin/accounts/:id/schedulable`，请求体为 `{ "schedulable": bool }`。
- `POST /api/v1/admin/accounts/:id/models/sync-upstream`。
- `POST /api/v1/admin/accounts/:id/test`，返回 SSE 事件。

现有实现的问题是 Account 列表在多处固定读取前 100 或 1000 条，未按返回分页信息循环；Group 只兼容数组和 `items`，未保存高峰倍率、用户专属倍率及完整计费元数据。

2026-07-17 尝试浅克隆 `https://github.com/Wei-Shaw/sub2api.git` 核对官方源码，但当前执行环境无法连接 GitHub 443。实现阶段以现有 connector 契约、需求文档和新增 mock 契约测试为事实基线；恢复网络后应重新核对字段名和分页 envelope，不允许据未验证字段开启自动保护。

## 2. 领域模型与迁移

### 2.1 新表

- `main_station_configs`：固定单例 `id=1`，唯一关联一条 `upstream_sync_targets`，保存同步状态和保护总开关。
- `main_station_account_snapshots`：按 `(main_station_id, remote_account_id)` 唯一保存远端 Account 非敏感快照。
- `main_account_pools`：逻辑池、容量阈值、健康/利润策略和评估状态。
- `main_account_pool_groups`：池与 `upstream_sync_target_groups` 的多对多关系。
- `main_account_pool_members`：池成员、ownership、绑定状态、源映射、健康/成本摘要；`remote_account_id` 唯一。
- `main_account_health_checks`：L0/L1/L2 明细。
- `main_account_profit_checks`：成员与主站分组粒度的利润快照。
- `main_account_guard_locks`：`(remote_account_id, lock_type)` 唯一，每类锁独立激活和解除。
- `main_account_audit_logs`：远端写入、锁、绑定、策略和自动操作证据。
- `main_station_notification_cooldowns`：按 `event + pool + member + group` 持久化去抖。

所有倍率计算在业务层转换为固定精度整数：倍率使用 `1e6` scale，利润率使用 basis points；数据库兼容字段可继续保存十进制字符串或整数，禁止直接使用裸 `float64` 做风险阈值判断。

### 2.2 旧同步数据迁移

迁移在 `AutoMigrate` 完成新表后幂等执行：

1. 若现有同步分组涉及多个不同 `target_id`，不自动选择主站，只写迁移待确认状态。
2. 若只涉及一个目标，创建或复用 `main_station_configs(id=1)`。
3. 每个 `UpstreamSyncGroup` 按 `legacy_sync_group_id` 唯一创建账号池，并迁移目标分组关联。
4. 每个 `UpstreamSyncAccount` 按 `legacy_sync_account_id` 唯一创建托管成员。
5. 存在 `UpstreamSyncManagedAccount` 时迁移精确远端 Account 和源 API Key 映射；缺失时成员保持 `pending`。
6. 旧表和旧数据全部保留；自动利润停用、自动健康停用和自动恢复均保持关闭。

## 3. 服务边界

### 3.1 `backend/mainstation`

新增主站领域服务，负责：

- 单例配置、连接测试和完整分页同步。
- 账号池、分组关系、托管/绑定成员和删除语义。
- L0/L1/L2 执行、错误分类、预算、统计和状态机。
- 成本来源解析、固定精度利润计算和观察模式。
- 调度锁、统一调度决策、串行化远端写入和审计。
- 池容量评估、通知和定时任务。

该服务只通过 interface 使用 channel API Key 能力和 Sub2API Admin client，测试全部注入 mock/`httptest`。

### 3.2 同步器接入

`syncer.Service` 注入一个最小 `SchedulingGuard` interface：

```go
type SchedulingGuard interface {
    ReconcileAccount(ctx context.Context, remoteAccountID int64, source string) error
    ActivateLock(ctx context.Context, remoteAccountID int64, lockType, reason string, evidence any) error
}
```

同步失败创建或更新 `sync` 锁，成功只解除属于同步器的 `sync` 锁，然后调用 `ReconcileAccount`。未迁移到主站领域的旧同步组保留兼容路径，但禁止成功后直接写 `schedulable=true`；只有统一服务确认无活动锁时才能启用。

## 4. API 契约

按需求文档提供 `/api/main-station`、`/groups`、`/accounts`、`/pools`、成员、测活、利润、锁和审计接口。列表统一支持：

- `page`、`page_size`，默认 1/20，最大 100。
- 稳定排序，默认 `id ASC` 或事件时间 `DESC, id DESC`。
- 可选状态、池、成员、分组和层级过滤。

写接口必须在 service 层校验单例、资源归属、绑定唯一性、远端快照状态和危险操作确认字段。API 永不返回 Admin API Key、完整源 API Key、Cookie、Token 或密文。

## 5. 测活与调度

- L0 只调用 billing/models 等零生成接口。
- L1 必须使用管理员配置模型，并按协议写入 `max_tokens`、`max_output_tokens` 或 `maxOutputTokens`；不允许取模型列表第一项。
- L2 只调用指定远端 Account 的管理员 test 接口，默认低频。
- 429 进入冷却；401/403/402 可快速隔离；Timeout/5xx 和空响应按连续阈值处理；输出限制不兼容标记配置异常。
- scheduler 使用有界 worker、成员级运行锁、最短间隔和确定性抖动；启动后只安排未来时间，不补跑全部遗漏任务。
- 所有 `schedulable` 写入前在事务外重读绑定、池、成员和全部活动锁；同一远端 Account 使用进程内互斥串行化，并在写入前后记录审计。
- HTTP 超时后先重新读取远端 Account 最终状态；创建类操作使用本地幂等键和远端名称复用，未知结果不盲目重复创建。

## 6. 前端

新增一级“主站”工作台，使用紧凑 tabs 和表格覆盖：总览、账号池、主站账号、主站分组、测活、风控与审计。危险操作使用现有 ConfirmDialog，异步失败统一 toast，所有图标使用 `lucide-react`。

主站未配置时首屏直接显示配置表单；配置后展示同步状态、池列表和风险摘要。自动保护开关只有在至少一次只读评估后可启用，并显示预计影响范围。移动端表格使用横向滚动或转换为紧凑列表，长名称和错误摘要必须可截断并通过 tooltip 查看。

## 7. 主要风险与兼容策略

- **官方字段未联网复核**：缺失或未知计费字段一律落为 `unknown/unsupported`，不得触发自动停用。
- **旧同步器并发写入**：先完成统一调度服务和回归测试，再开启任何自动保护。
- **远端写入超时**：查询最终状态并保留审计，禁止重复创建。
- **SQLite 写并发**：沿用单连接并缩短事务；远端 HTTP 不放在数据库事务内。
- **MySQL 唯一约束**：使用普通复合唯一索引和显式状态行，不依赖 partial index。
- **倍率精度**：风险比较使用固定精度整数，浮点仅用于兼容 API 展示。
- **绑定成员凭据不可验证**：要求显式人工确认，默认不覆盖凭据、不删除远端资源。

## 8. 实施文件清单

- 存储：`backend/storage/model.go`、`storage.go`、新增 `main_station.go` 及迁移测试。
- Connector：`backend/connector/sub2api/admin.go`、新增完整分页和兼容字段测试。
- 领域服务：新增 `backend/mainstation/*`。
- 同步器：`backend/syncer/service.go` 及多锁回归测试。
- API/装配：新增 `backend/api/main_station.go`，修改 `api.go`、`cmd/server/main.go`、scheduler。
- 通知：扩展事件类型、主站复合去抖和消息构造。
- 前端：新增 `frontend/app/main-station-page.tsx` 和相关组件，修改路由、侧栏、API 类型/client。
- 运维：README、Compose、workflow 和迁移/回滚说明。
