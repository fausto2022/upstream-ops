import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react"
import {
  Activity,
  CircleDollarSign,
  History,
  Link2,
  MoreHorizontal,
  Pause,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Search,
  ServerCog,
  Settings2,
  ShieldAlert,
  Star,
  TestTube2,
  Trash2,
  Users,
} from "lucide-react"
import { toast } from "sonner"
import { AccountSettingsDialog } from "@/components/main-station/account-settings-dialog"
import { GroupSettingsDialog } from "@/components/main-station/group-settings-dialog"
import { HealthHistoryDialog } from "@/components/main-station/health-history-dialog"
import { MemberDialog } from "@/components/main-station/member-dialog"
import { StationConfigDialog } from "@/components/main-station/station-config-dialog"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { useConfirm } from "@/components/ui/confirm-dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { apiFetch } from "@/lib/api"
import type {
  Channel,
  MainStationAccount,
  MainStationAuditLog,
  MainStationBulkOperation,
  MainStationConfig,
  MainStationGroupWorkspace,
  MainStationPage,
  MainStationPoolEvaluation,
  MainStationProtectionPreview,
  MainStationSchedulingDecision,
  MainStationSyncResult,
} from "@/lib/api-types"
import { relativeTime } from "@/lib/format"
import { cn } from "@/lib/utils"

export default function MainStationPage() {
  const { confirm, dialog: confirmDialog } = useConfirm()
  const [config, setConfig] = useState<MainStationConfig | null>(null)
  const [workspaces, setWorkspaces] = useState<MainStationGroupWorkspace[]>([])
  const [accounts, setAccounts] = useState<MainStationAccount[]>([])
  const [channels, setChannels] = useState<Channel[]>([])
  const [auditLogs, setAuditLogs] = useState<MainStationAuditLog[]>([])
  const [preview, setPreview] = useState<MainStationProtectionPreview | null>(null)
  const [selectedGroupID, setSelectedGroupID] = useState<number | null>(null)
  const [search, setSearch] = useState("")
  const [statusFilter, setStatusFilter] = useState("all")
  const [loading, setLoading] = useState(true)
  const [accountsLoading, setAccountsLoading] = useState(false)
  const [error, setError] = useState("")
  const [syncing, setSyncing] = useState(false)
  const [busyAccountID, setBusyAccountID] = useState<number | null>(null)
  const [configOpen, setConfigOpen] = useState(false)
  const [memberOpen, setMemberOpen] = useState(false)
  const [editingAccount, setEditingAccount] = useState<MainStationAccount | null>(null)
  const [healthHistoryAccount, setHealthHistoryAccount] = useState<MainStationAccount | null>(null)
  const [settingsOpen, setSettingsOpen] = useState(false)

  const selectedWorkspace = useMemo(
    () => workspaces.find((workspace) => workspace.group.id === selectedGroupID) ?? null,
    [selectedGroupID, workspaces],
  )

  const loadBase = useCallback(async () => {
    setLoading(true)
    setError("")
    try {
      const station = await apiFetch<MainStationConfig>("/main-station")
      setConfig(station)
      if (!station.configured) {
        setWorkspaces([])
        setAccounts([])
        return
      }
      const [groupResult, channelResult] = await Promise.all([
        apiFetch<{ items: MainStationGroupWorkspace[] }>("/main-station/groups"),
        apiFetch<{ items: Channel[] }>("/channels?page=1&page_size=100"),
      ])
      setWorkspaces(groupResult.items)
      setChannels(channelResult.items)
      setSelectedGroupID((current) => {
        if (current != null && groupResult.items.some((workspace) => workspace.group.id === current)) return current
        return groupResult.items[0]?.group.id ?? null
      })
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载主站数据失败")
    } finally {
      setLoading(false)
    }
  }, [])

  const loadAccounts = useCallback(async (groupID: number | null, silent = false) => {
    if (!silent) setAccountsLoading(true)
    try {
      const result = groupID == null
        ? await apiFetch<MainStationPage<MainStationAccount>>("/main-station/accounts?page=1&page_size=100")
        : await apiFetch<{ items: MainStationAccount[] }>(`/main-station/groups/${groupID}/accounts`)
      setAccounts(result.items)
    } catch (loadError) {
      if (!silent) {
        toast.error(loadError instanceof Error ? loadError.message : "加载账号失败")
        setAccounts([])
      }
    } finally {
      if (!silent) setAccountsLoading(false)
    }
  }, [])

  const loadRisk = useCallback(async (groupID: number | null) => {
    if (!config?.configured) return
    try {
      const query = groupID == null ? "" : `?group_id=${groupID}`
      const [previewResult, auditResult] = await Promise.all([
        apiFetch<MainStationProtectionPreview>("/main-station/protection-preview"),
        apiFetch<MainStationPage<MainStationAuditLog>>(`/main-station/audit-logs${query}${query ? "&" : "?"}page=1&page_size=50`),
      ])
      setPreview(previewResult)
      setAuditLogs(auditResult.items)
    } catch (loadError) {
      toast.error(loadError instanceof Error ? loadError.message : "加载风险数据失败")
    }
  }, [config?.configured])

  useEffect(() => { void loadBase() }, [loadBase])
  useEffect(() => {
    if (!config?.configured) return
    void loadAccounts(selectedGroupID)
    void loadRisk(selectedGroupID)
  }, [config?.configured, loadAccounts, loadRisk, selectedGroupID])
  useEffect(() => {
    if (!config?.configured) return
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void loadAccounts(selectedGroupID, true)
    }, 10_000)
    return () => window.clearInterval(timer)
  }, [config?.configured, loadAccounts, selectedGroupID])

  const filteredAccounts = useMemo(() => {
    const keyword = search.trim().toLowerCase()
    return accounts.filter((account) => {
      if (keyword && !`${account.name} ${account.remote_account_id} ${account.platform ?? ""}`.toLowerCase().includes(keyword)) return false
      if (statusFilter === "enabled" && !account.schedulable) return false
      if (statusFilter === "disabled" && account.schedulable) return false
      if (statusFilter === "unmanaged" && account.member) return false
      if (statusFilter === "unhealthy" && account.member?.last_health_status !== "unhealthy") return false
      return true
    })
  }, [accounts, search, statusFilter])

  const sortedAccounts = useMemo(() => [...filteredAccounts].sort((left, right) => {
    if (Boolean(left.member) !== Boolean(right.member)) return left.member ? -1 : 1
    if (left.schedulable !== right.schedulable) return left.schedulable ? -1 : 1
    const priorityDiff = (left.priority || 1) - (right.priority || 1)
    if (priorityDiff !== 0) return priorityDiff
    return left.name.localeCompare(right.name)
  }), [filteredAccounts])

  async function handleSync() {
    setSyncing(true)
    try {
      const result = await apiFetch<MainStationSyncResult>("/main-station/sync", { method: "POST" })
      toast.success(`已同步 ${result.groups} 个分组、${result.accounts} 个 Account`)
      await loadBase()
      await loadAccounts(selectedGroupID)
    } catch (syncError) {
      toast.error(syncError instanceof Error ? syncError.message : "同步主站失败")
    } finally {
      setSyncing(false)
    }
  }

  async function handleCheck(account: MainStationAccount) {
    if (!selectedWorkspace || !account.member) {
      toast.error("请先接管该账号，再执行检测")
      return
    }
    setBusyAccountID(account.remote_account_id)
    try {
      const platform = healthPlatformKey(account.platform)
      const healthModel = account.member.health_model || config?.health_models?.[platform]
      await apiFetch(`/main-station/groups/${selectedWorkspace.group.id}/accounts/${account.member.id}/check`, {
        method: "POST",
        body: JSON.stringify({ level: healthModel ? "L1" : "L0", force: true }),
      })
      toast.success("账号检测完成")
      await loadAccounts(selectedGroupID)
    } catch (checkError) {
      toast.error(checkError instanceof Error ? checkError.message : "账号检测失败")
    } finally {
      setBusyAccountID(null)
    }
  }

  async function handleSyncAccount(account: MainStationAccount) {
    if (!selectedWorkspace || !account.member || account.member.ownership_mode !== "managed") return
    setBusyAccountID(account.remote_account_id)
    try {
      await apiFetch(`/main-station/groups/${selectedWorkspace.group.id}/accounts/${account.member.id}/sync`, { method: "POST" })
      toast.success("账号配置已重新应用")
      await loadAccounts(selectedGroupID)
    } catch (syncError) {
      toast.error(syncError instanceof Error ? syncError.message : "应用账号配置失败")
    } finally {
      setBusyAccountID(null)
    }
  }

  async function handleScheduling(account: MainStationAccount) {
    if (!account.member) {
      toast.error("请先接管该账号")
      return
    }
    setBusyAccountID(account.remote_account_id)
    try {
      if (account.schedulable) {
        await apiFetch(`/main-station/accounts/${account.remote_account_id}/locks/manual`, {
          method: "POST",
          body: JSON.stringify({ reason: "管理员手动停用" }),
        })
        toast.success("账号已停用")
      } else {
        const decision = await apiFetch<MainStationSchedulingDecision>(`/main-station/accounts/${account.remote_account_id}/locks/manual/clear`, { method: "POST" })
        if (decision.remote_schedulable) toast.success("账号已恢复调度")
        else toast.warning(schedulingBlockedMessage(decision))
      }
      await loadAccounts(selectedGroupID)
      await loadRisk(selectedGroupID)
    } catch (scheduleError) {
      toast.error(scheduleError instanceof Error ? scheduleError.message : "更新账号状态失败")
    } finally {
      setBusyAccountID(null)
    }
  }

  async function handleDelete(account: MainStationAccount) {
    if (!selectedWorkspace || !account.member) return
    const managed = account.member.ownership_mode === "managed"
    const approved = await confirm({
      title: `删除账号“${account.name}”`,
      description: managed ? "将同时删除主站 Account 和自动创建的来源 API Key。" : "只解除接管关系，主站 Account 和来源 API Key 保持不变。",
      confirmLabel: "删除",
      destructive: true,
    })
    if (!approved) return
    setBusyAccountID(account.remote_account_id)
    try {
      await apiFetch(`/main-station/groups/${selectedWorkspace.group.id}/accounts/${account.member.id}`, {
        method: "DELETE",
        body: JSON.stringify({
          confirm: true,
          delete_remote_account: managed,
          delete_source_api_key: managed,
        }),
      })
      toast.success(managed ? "账号已删除" : "接管关系已解除")
      await loadBase()
      await loadAccounts(selectedGroupID)
    } catch (deleteError) {
      toast.error(deleteError instanceof Error ? deleteError.message : "删除账号失败")
    } finally {
      setBusyAccountID(null)
    }
  }

  async function handleBulkCheck() {
    if (!selectedWorkspace) return
    try {
      const result = await apiFetch<MainStationBulkOperation>(`/main-station/groups/${selectedWorkspace.group.id}/check`, {
        method: "POST",
        body: JSON.stringify({ level: "L0", force: false }),
      })
      toast.success(`检测完成：成功 ${result.succeeded}，跳过 ${result.skipped}`)
      await loadAccounts(selectedGroupID)
    } catch (checkError) {
      toast.error(checkError instanceof Error ? checkError.message : "批量检测失败")
    }
  }

  async function handleEvaluate() {
    if (!selectedWorkspace) return
    try {
      const result = await apiFetch<MainStationPoolEvaluation>(`/main-station/groups/${selectedWorkspace.group.id}/evaluate`, { method: "POST" })
      toast.success(`评估完成：正常 ${result.healthy}，风险 ${result.risk}，未知 ${result.unknown}`)
      await loadBase()
      await loadRisk(selectedGroupID)
    } catch (evaluateError) {
      toast.error(evaluateError instanceof Error ? evaluateError.message : "风险评估失败")
    }
  }

  if (loading) {
    return <div className="flex min-h-72 items-center justify-center"><Spinner /></div>
  }

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-lg font-semibold">主站账号</h1>
          <p className="truncate text-sm text-muted-foreground">
            {config?.configured ? `${config.name} · ${config.base_url}` : "尚未配置 Sub2API 主站"}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {config?.configured ? (
            <Button variant="outline" onClick={() => void handleSync()} disabled={syncing}>
              <RefreshCw className={cn("size-4", syncing && "animate-spin")} />{syncing ? "同步中" : "同步主站"}
            </Button>
          ) : null}
          <IconButton label="主站配置" onClick={() => setConfigOpen(true)}><ServerCog className="size-4" /></IconButton>
        </div>
      </header>

      {error ? <Alert variant="destructive"><AlertTitle>主站数据加载失败</AlertTitle><AlertDescription>{error}</AlertDescription></Alert> : null}

      {!config?.configured ? (
        <div className="flex min-h-64 flex-col items-center justify-center gap-3 border-y py-10 text-center">
          <ServerCog className="size-8 text-muted-foreground" />
          <p className="text-sm font-medium">配置唯一主站后开始管理分组和账号</p>
          <Button onClick={() => setConfigOpen(true)}>配置主站</Button>
        </div>
      ) : (
        <Tabs defaultValue="accounts" className="space-y-4">
          <TabsList>
            <TabsTrigger value="accounts"><Users className="size-4" />账号管理</TabsTrigger>
            <TabsTrigger value="risk"><ShieldAlert className="size-4" />风险保护</TabsTrigger>
            <TabsTrigger value="logs"><History className="size-4" />操作记录</TabsTrigger>
          </TabsList>

          <TabsContent value="accounts" className="mt-0">
            <div className="grid min-h-[560px] overflow-hidden border md:grid-cols-[240px_minmax(0,1fr)]">
              <aside className="border-b bg-muted/20 md:border-b-0 md:border-r">
                <div className="border-b px-3 py-3">
                  <p className="text-sm font-medium">主站分组</p>
                  <p className="text-xs text-muted-foreground">{workspaces.length} 个分组</p>
                </div>
                <nav className="max-h-[520px] overflow-y-auto p-2">
                  <GroupButton active={selectedGroupID == null} name="全部账号" count={workspaces.reduce((sum, item) => sum + item.account_count, 0)} icon={<Users className="size-4" />} onClick={() => setSelectedGroupID(null)} />
                  {workspaces.map((workspace) => (
                    <GroupButton
                      key={workspace.group.id}
                      active={selectedGroupID === workspace.group.id}
                      name={workspace.group.name}
                      count={workspace.account_count}
                      status={workspace.last_status}
                      onClick={() => setSelectedGroupID(workspace.group.id)}
                    />
                  ))}
                </nav>
              </aside>

              <section className="min-w-0">
                <div className="flex flex-wrap items-center justify-between gap-3 border-b px-4 py-3">
                  <div className="min-w-0">
                    <div className="flex min-w-0 items-center gap-2">
                      <p className="truncate text-sm font-semibold">{selectedWorkspace?.group.name ?? "全部账号"}</p>
                      {selectedWorkspace ? <Badge variant="outline" className="shrink-0 tabular-nums">倍率 {formatMainStationMultiplier(selectedWorkspace.group.rate_multiplier_micros)}</Badge> : null}
                    </div>
                    <p className="text-xs text-muted-foreground">{accounts.length} 个 Account</p>
                  </div>
                  <div className="flex items-center gap-2">
                    {selectedWorkspace ? <IconButton label="分组设置" onClick={() => setSettingsOpen(true)}><Settings2 className="size-4" /></IconButton> : null}
                    <Button onClick={() => setMemberOpen(true)} disabled={!selectedWorkspace}>
                      <Plus className="size-4" />添加账号
                    </Button>
                  </div>
                </div>

                <div className="flex flex-wrap gap-2 border-b px-4 py-3">
                  <div className="relative min-w-52 flex-1">
                    <Search className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
                    <Input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索名称或 ID" className="pl-9" />
                  </div>
                  <Select value={statusFilter} onValueChange={setStatusFilter}>
                    <SelectTrigger className="w-36"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="all">全部状态</SelectItem>
                      <SelectItem value="enabled">调度中</SelectItem>
                      <SelectItem value="disabled">已停用</SelectItem>
                      <SelectItem value="unhealthy">检测异常</SelectItem>
                      <SelectItem value="unmanaged">未接管</SelectItem>
                    </SelectContent>
                  </Select>
                  {selectedWorkspace ? <Button variant="outline" onClick={() => void handleBulkCheck()}><TestTube2 className="size-4" />批量检测</Button> : null}
                </div>

                <div className="overflow-x-auto">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Account</TableHead>
                        <TableHead>状态</TableHead>
                        <TableHead>实际优先级</TableHead>
                        <TableHead>上游倍率</TableHead>
                        <TableHead>健康</TableHead>
                        <TableHead>连通率</TableHead>
                        <TableHead>来源</TableHead>
                        <TableHead className="w-24 text-right">操作</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {accountsLoading ? <EmptyRow columns={8} text="加载中" /> : null}
                      {!accountsLoading && sortedAccounts.map((account) => (
                        <TableRow key={account.remote_account_id}>
                          <TableCell>
                            <div className="flex max-w-64 items-center gap-1.5">
                              <span className="truncate font-medium" title={account.name}>{account.name}</span>
                              {account.member?.preferred ? (
                                <Tooltip>
                                  <TooltipTrigger asChild>
                                    <span aria-label="优先调度" className="shrink-0 text-amber-500"><Star className="size-3.5 fill-current" /></span>
                                  </TooltipTrigger>
                                  <TooltipContent>优先调度</TooltipContent>
                                </Tooltip>
                              ) : null}
                            </div>
                            <div className="text-xs text-muted-foreground">#{account.remote_account_id} · {account.platform || "未知平台"}</div>
                          </TableCell>
                          <TableCell><ScheduleBadge account={account} /></TableCell>
                          <TableCell><SchedulingPriority account={account} /></TableCell>
                          <TableCell><SourceGroupRate account={account} /></TableCell>
                          <TableCell><HealthBadge account={account} /></TableCell>
                          <TableCell><ConnectivityRate account={account} /></TableCell>
                          <TableCell>
                            {account.member ? (
                              <div className="max-w-40 truncate text-sm" title={channelName(channels, account.member.source_channel_id)}>{channelName(channels, account.member.source_channel_id)}</div>
                            ) : <Badge variant="outline">未接管</Badge>}
                          </TableCell>
                          <TableCell className="text-right">
                            {busyAccountID === account.remote_account_id ? <Spinner className="ml-auto" /> : (
                              <div className="flex justify-end gap-1">
                                {account.member ? (
                                  <>
                                    {selectedWorkspace ? <IconButton label="探测记录" onClick={() => setHealthHistoryAccount(account)}><History className="size-4" /></IconButton> : null}
                                    <IconButton label={account.schedulable ? "停用账号" : "恢复账号"} onClick={() => void handleScheduling(account)}>
                                      {account.schedulable ? <Pause className="size-4" /> : <Play className="size-4" />}
                                    </IconButton>
                                    {selectedWorkspace ? <IconButton label="编辑账号" onClick={() => setEditingAccount(account)}><Pencil className="size-4" /></IconButton> : null}
                                  </>
                                ) : selectedWorkspace ? (
                                  <IconButton label="接管账号" onClick={() => setMemberOpen(true)}><Link2 className="size-4" /></IconButton>
                                ) : null}
                                <AccountMenu account={account} canManage={selectedWorkspace != null} onCheck={handleCheck} onSync={handleSyncAccount} onDelete={handleDelete} />
                              </div>
                            )}
                          </TableCell>
                        </TableRow>
                      ))}
                      {!accountsLoading && filteredAccounts.length === 0 ? <EmptyRow columns={8} text="没有符合条件的账号" /> : null}
                    </TableBody>
                  </Table>
                </div>
              </section>
            </div>
          </TabsContent>

          <TabsContent value="risk" className="mt-0 space-y-4">
            <div className="grid gap-3 sm:grid-cols-3">
              <Metric label="当前分组" value={selectedWorkspace?.group.name ?? "全部"} />
              <Metric label="活动停用原因" value={preview?.active_locks.length ?? 0} danger={(preview?.active_locks.length ?? 0) > 0} />
              <Metric label="利润风险账号" value={preview?.margin_risk_member_ids.length ?? 0} danger={(preview?.margin_risk_member_ids.length ?? 0) > 0} />
            </div>
            <div className="border-y py-4">
              <div className="space-y-3">
                <p className="text-sm font-semibold">分组操作</p>
                <div className="flex flex-wrap gap-2">
                  <Button variant="outline" disabled={!selectedWorkspace} onClick={() => void handleEvaluate()}><CircleDollarSign className="size-4" />成本与利润评估</Button>
                  <Button variant="outline" disabled={!selectedWorkspace} onClick={() => void handleBulkCheck()}><Activity className="size-4" />检测全部账号</Button>
                  <Button variant="outline" disabled={!selectedWorkspace} onClick={() => setSettingsOpen(true)}><Settings2 className="size-4" />分组策略</Button>
                </div>
              </div>
            </div>
            <AuditTable items={auditLogs.filter((item) => !item.success || item.action.includes("lock") || item.action.includes("evaluate"))} accounts={accounts} empty="暂无风险事件" />
          </TabsContent>

          <TabsContent value="logs" className="mt-0">
            <AuditTable items={auditLogs} accounts={accounts} empty="暂无操作记录" />
          </TabsContent>
        </Tabs>
      )}

      <StationConfigDialog open={configOpen} onOpenChange={setConfigOpen} config={config} onSaved={() => void loadBase()} />
      <MemberDialog open={memberOpen} onOpenChange={setMemberOpen} workspace={selectedWorkspace} channels={channels} accounts={accounts} onSaved={() => { void loadBase(); void loadAccounts(selectedGroupID) }} />
      <AccountSettingsDialog
        open={editingAccount != null}
        onOpenChange={(open) => { if (!open) setEditingAccount(null) }}
        workspace={selectedWorkspace}
        account={editingAccount}
        config={config}
        onSaved={() => { void loadBase(); void loadAccounts(selectedGroupID) }}
      />
      <HealthHistoryDialog
        open={healthHistoryAccount != null}
        onOpenChange={(open) => { if (!open) setHealthHistoryAccount(null) }}
        groupID={selectedWorkspace?.group.id ?? null}
        account={healthHistoryAccount}
      />
      <GroupSettingsDialog open={settingsOpen} onOpenChange={setSettingsOpen} workspace={selectedWorkspace} onSaved={(saved) => setWorkspaces((items) => items.map((item) => item.group.id === saved.group.id ? saved : item))} />
      {confirmDialog}
    </div>
  )
}

function healthPlatformKey(platform?: string) {
  const normalized = (platform ?? "").trim().toLowerCase()
  if (normalized === "claude") return "anthropic"
  if (normalized === "google") return "gemini"
  if (normalized === "xai") return "grok"
  return normalized
}

function GroupButton({ active, name, count, status, icon, onClick }: { active: boolean; name: string; count: number; status?: string; icon?: ReactNode; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className={cn("flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-accent", active && "bg-accent font-medium")}>
      {icon ?? <span className={cn("size-2 shrink-0 rounded-full bg-muted-foreground/40", status === "healthy" && "bg-emerald-500", status === "degraded" && "bg-amber-500", status === "critical" && "bg-destructive")} />}
      <span className="min-w-0 flex-1 truncate" title={name}>{name}</span>
      <span className="tabular-nums text-xs text-muted-foreground">{count}</span>
    </button>
  )
}

function AccountMenu({ account, canManage, onCheck, onSync, onDelete }: { account: MainStationAccount; canManage: boolean; onCheck: (account: MainStationAccount) => void; onSync: (account: MainStationAccount) => void; onDelete: (account: MainStationAccount) => void }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild><Button variant="ghost" size="icon" aria-label="更多操作"><MoreHorizontal className="size-4" /></Button></DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {account.member ? <DropdownMenuItem disabled={!canManage} onClick={() => void onCheck(account)}><TestTube2 className="size-4" />检测账号</DropdownMenuItem> : null}
        {account.member?.ownership_mode === "managed" ? <DropdownMenuItem disabled={!canManage} onClick={() => void onSync(account)}><RefreshCw className="size-4" />重新应用配置</DropdownMenuItem> : null}
        {account.member ? <DropdownMenuSeparator /> : null}
        <DropdownMenuItem className="text-destructive focus:text-destructive" disabled={!account.member || !canManage} onClick={() => void onDelete(account)}><Trash2 className="size-4" />删除账号</DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function ScheduleBadge({ account }: { account: MainStationAccount }) {
  if (account.missing) return <Badge variant="destructive">已丢失</Badge>
  if (!account.member) return <Badge variant="outline">未接管</Badge>
  return account.schedulable ? <Badge className="bg-emerald-600 text-white">调度中</Badge> : <Badge variant="secondary">已停用</Badge>
}

function HealthBadge({ account }: { account: MainStationAccount }) {
  const status = account.member?.last_health_status
  if (!account.member) return <span className="text-muted-foreground">-</span>
  if (status === "healthy") return <Badge variant="outline" className="border-emerald-300 text-emerald-700">正常</Badge>
  if (status === "unhealthy") return <Badge variant="destructive">异常</Badge>
  if (status === "degraded") return <Badge variant="outline" className="border-amber-300 text-amber-700">波动</Badge>
  return <Badge variant="outline">未检测</Badge>
}

function ConnectivityRate({ account }: { account: MainStationAccount }) {
  const rate = account.member?.recent_20_success_rate
  if (rate == null) return <span className="text-muted-foreground">-</span>
  const text = rate === Math.round(rate) ? rate.toFixed(0) : rate.toFixed(1)
  const className = rate >= 95 ? "text-emerald-700" : rate >= 80 ? "text-amber-700" : "text-destructive"
  return <span className={cn("text-sm font-medium tabular-nums", className)} title="最近 100 次有效探测成功率">{text}%</span>
}

function SchedulingPriority({ account }: { account: MainStationAccount }) {
  const actualPriority = account.priority > 0 ? account.priority : 1
  const basePriority = account.member?.priority
  if (basePriority == null || basePriority === actualPriority) {
    return <span className="tabular-nums">{actualPriority}</span>
  }
  return (
    <div className="leading-tight" title="系统根据健康状态、人工优先级、成本、连通率和延迟自动调整">
      <div className="font-medium tabular-nums">{actualPriority}</div>
      <div className="text-xs text-muted-foreground">基础 {basePriority}</div>
    </div>
  )
}

function SourceGroupRate({ account }: { account: MainStationAccount }) {
  const member = account.member
  if (!member) return <span className="text-muted-foreground">-</span>
  const rate = member.source_group_rate_multiplier
  const groupName = member.source_group_name || (member.source_group_id != null ? `分组 #${member.source_group_id}` : "默认分组")
  if (rate == null || !Number.isFinite(rate)) {
    return <span className="text-muted-foreground" title={`来源分组：${groupName}；暂无倍率快照`}>-</span>
  }
  const observedAt = member.source_group_rate_observed_at ? `倍率采集于 ${relativeTime(member.source_group_rate_observed_at)}` : "默认分组倍率"
  return (
    <div className="leading-tight" title={`来源分组：${groupName}；${observedAt}`}>
      <div className="font-medium tabular-nums">{rate.toFixed(2)}</div>
      <div className="max-w-24 truncate text-xs text-muted-foreground">{groupName}</div>
    </div>
  )
}

function formatMainStationMultiplier(value: number) {
  if (!Number.isFinite(value)) return "-"
  return (value / 1_000_000).toFixed(2)
}

function schedulingBlockedMessage(decision: MainStationSchedulingDecision) {
  if (decision.locks.length > 0) {
    const labels: Record<string, string> = {
      manual: "人工停用",
      health: "健康异常保护",
      margin: "利润保护",
      sync: "同步保护",
      credential: "凭据异常保护",
      binding: "绑定异常保护",
    }
    return `账号仍被${decision.locks.map((lock) => labels[lock.lock_type] || lock.lock_type).join("、")}停用`
  }
  const reasons: Record<string, string> = {
    "main station management is disabled": "主站管理已停用，账号暂时无法恢复",
    "account pool is disabled": "当前主站分组已停用，账号暂时无法恢复",
    "pool member is disabled": "当前账号已关闭，需先在账号设置中启用",
    "remote account status is not active": "主站 Account 状态不是活动状态",
    "member binding is invalid": "账号绑定关系无效，暂时无法恢复",
  }
  return reasons[decision.reason] || "账号当前仍不满足调度条件"
}

function AuditTable({ items, accounts, empty }: { items: MainStationAuditLog[]; accounts: MainStationAccount[]; empty: string }) {
  return (
    <div className="overflow-x-auto border">
      <Table>
        <TableHeader><TableRow><TableHead>时间</TableHead><TableHead>操作</TableHead><TableHead>Account</TableHead><TableHead>来源</TableHead><TableHead>结果</TableHead><TableHead>详情</TableHead></TableRow></TableHeader>
        <TableBody>
          {items.map((item) => (
            <TableRow key={item.id}>
              <TableCell className="whitespace-nowrap text-xs">{relativeTime(item.created_at)}</TableCell>
              <TableCell>{actionLabel(item.action)}</TableCell>
              <TableCell>{item.remote_account_id ? `${accounts.find((account) => account.remote_account_id === item.remote_account_id)?.name ?? "Account"} #${item.remote_account_id}` : "-"}</TableCell>
              <TableCell>{sourceLabel(item.source)}</TableCell>
              <TableCell>{item.success ? <Badge variant="outline">成功</Badge> : <Badge variant="destructive">失败</Badge>}</TableCell>
              <TableCell className="max-w-80 truncate" title={auditDetail(item.error_message || item.detail)}>{auditDetail(item.error_message || item.detail)}</TableCell>
            </TableRow>
          ))}
          {items.length === 0 ? <EmptyRow columns={6} text={empty} /> : null}
        </TableBody>
      </Table>
    </div>
  )
}

function IconButton({ label, children, onClick }: { label: string; children: ReactNode; onClick: () => void }) {
  return <Tooltip><TooltipTrigger asChild><Button variant="ghost" size="icon" aria-label={label} onClick={onClick}>{children}</Button></TooltipTrigger><TooltipContent>{label}</TooltipContent></Tooltip>
}

function Metric({ label, value, danger = false }: { label: string; value: ReactNode; danger?: boolean }) {
  return <div className="border-l-2 px-3 py-1"><p className="text-xs text-muted-foreground">{label}</p><p className={cn("mt-1 text-lg font-semibold", danger && "text-destructive")}>{value}</p></div>
}

function EmptyRow({ columns, text }: { columns: number; text: string }) {
  return <TableRow><TableCell colSpan={columns} className="h-28 text-center text-sm text-muted-foreground">{text}</TableCell></TableRow>
}

function channelName(channels: Channel[], channelID: number) {
  return channels.find((channel) => channel.id === channelID)?.name ?? `来源 #${channelID}`
}

function actionLabel(action: string) {
  const labels: Record<string, string> = {
    member_bind: "接管账号",
    member_managed_sync: "同步账号",
    member_update: "更新账号",
    member_delete: "删除账号",
    member_unbind: "解除接管",
    group_settings_update: "更新分组设置",
    schedulable_reconcile: "更新调度状态",
    pool_profit_evaluate: "利润评估",
    member_scheduling_rank: "调整账号优先级",
    guard_lock_activate: "启用停用保护",
    guard_lock_clear: "解除停用保护",
    main_station_sync: "同步主站信息",
    protection_policy_update: "更新保护策略",
    main_station_update: "更新主站配置",
  }
  return labels[action] ?? action
}

function sourceLabel(source: string) {
  return ({ manual: "人工操作", admin: "管理员", health: "健康探测", scheduler: "定时任务", margin: "利润保护", system: "系统" } as Record<string, string>)[source] ?? source
}

function auditDetail(detail?: string) {
  if (!detail) return "-"
  const labels: Record<string, string> = {
    "remote state already matches decision": "主站当前调度状态已符合系统决策，无需修改",
    "remote schedulable updated": "已更新主站账号调度状态",
    "automatic scheduling fields applied": "已更新主站账号优先级和调度参数",
    "current health locks reconciled; disabling still preserves existing locks": "已按当前健康状态更新保护锁",
  }
  return labels[detail] ?? detail
}
