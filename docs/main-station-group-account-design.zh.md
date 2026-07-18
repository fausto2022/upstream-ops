# 主站分组账号管理设计

## 1. 产品模型

主站管理只暴露两层对象：

```text
唯一 Sub2API 主站
├── 主站分组 A
│   ├── Account 1
│   └── Account 2
└── 主站分组 B
    └── Account 3
```

- 主站分组就是用户理解的账号池。
- 主站 Account 就是分组下的账号。
- 不提供独立账号池的创建、删除和多分组关联操作。
- Account 的分组归属以 Sub2API 返回的 `group_ids` 为准。
- 一个 Account 包含多个 `group_ids` 时，会出现在对应的多个分组中。

## 2. 页面结构

主站页面包含三个视图：

1. `账号管理`：左侧选择分组，右侧管理 Account。
2. `风险保护`：配置全局自动保护并执行当前分组的检测和利润评估。
3. `操作记录`：查看当前分组相关的远端写入、锁和策略审计。

账号管理支持：

- 同步主站分组和 Account。
- 按名称、远端 ID 和状态筛选。
- 新建 Account 或接管已有 Account。
- 启用、停用、快速检测和重新应用托管配置。
- 批量检测当前分组。
- 删除托管 Account 或解除已有 Account 的接管关系。

## 3. 添加账号

新建账号时填写：

- 账号名称。
- 账号来源和来源套餐。
- 来源 API Key，默认自动创建独立 Key。
- 并发自动读取上游账号的最高并发，仅在上游不支持时允许手工填写。
- 保留数字优先级作为手工调度层级，不再要求手工填写权重。
- 可给账号添加“优先调度”标签；健康账号中标签账号优先于普通账号。
- Sub2API 负载因子自动等于账号并发，并发只表示容量。
- 主站实际优先级按健康状态、手工优先级、成本、最近成功率和 P95 延迟自动生成。
- 可选的完整检测模型。
- 标签账号异常后自动暂停调度并继续探活，连续恢复后自动解除健康暂停；人工关闭健康检测后不再探活。

系统在当前主站分组创建 Account，先执行 L0 快速检测。配置完整检测模型时继续执行 L1；未配置时不再阻止 Account 创建和调度。

接管已有账号时必须人工确认来源与主站 Account 的映射。接管不会覆盖远端凭据，也不会在解除接管时删除远端资源。

## 4. 分组设置

每个主站分组独立保存：

- 是否参与调度。
- 最少可用账号数。
- 最少有效并发。
- 成本排序方向。
- 健康与利润保护策略。

健康、利润、容量和审计仍复用现有稳定性领域服务。内部策略表不属于公开产品对象，不提供 CRUD API。

## 5. API

```text
GET    /api/main-station/groups
GET    /api/main-station/groups/:id/accounts
PUT    /api/main-station/groups/:id/settings
POST   /api/main-station/groups/:id/accounts
PUT    /api/main-station/groups/:id/accounts/:member_id
DELETE /api/main-station/groups/:id/accounts/:member_id
POST   /api/main-station/groups/:id/accounts/:member_id/sync
POST   /api/main-station/groups/:id/accounts/:member_id/check
POST   /api/main-station/groups/:id/check
POST   /api/main-station/groups/:id/evaluate
GET    /api/main-station/groups/:id/capacity
GET    /api/main-station/groups/:id/health-checks
GET    /api/main-station/groups/:id/health-summary
GET    /api/main-station/groups/:id/profit-checks
```

旧 `/api/main-station/pools` 及其成员路由已移除。
