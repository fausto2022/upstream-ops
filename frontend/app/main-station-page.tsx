import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react"
import {
  Activity,
  AlertTriangle,
  PauseCircle,
  Pencil,
  Plus,
  RefreshCw,
  ServerCog,
  ShieldCheck,
  TestTube2,
  Trash2,
  Unlock,
} from "lucide-react"
import { toast } from "sonner"
import { MemberDialog } from "@/components/main-station/member-dialog"
import { PoolDialog } from "@/components/main-station/pool-dialog"
import { StationConfigDialog } from "@/components/main-station/station-config-dialog"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { useConfirm } from "@/components/ui/confirm-dialog"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { apiFetch } from "@/lib/api"
import type {
  Channel,
  MainStationAccount,
  MainStationAuditLog,
  MainStationBulkOperation,
  MainStationConfig,
  MainStationGroup,
  MainStationGuardLock,
  MainStationHealthCheck,
  MainStationMember,
  MainStationMemberHealthSummary,
  MainStationPage,
  MainStationPool,
  MainStationPoolCapacity,
  MainStationPoolEvaluation,
  MainStationProfitCheck,
  MainStationProtectionPreview,
  MainStationSyncResult,
} from "@/lib/api-types"
import { relativeTime } from "@/lib/format"
import { cn } from "@/lib/utils"

export default function MainStationPage() {
  const [config, setConfig] = useState<MainStationConfig | null>(null)
  const [groups, setGroups] = useState<MainStationGroup[]>([])
  const [accounts, setAccounts] = useState<MainStationAccount[]>([])
  const [pools, setPools] = useState<MainStationPool[]>([])
  const [channels, setChannels] = useState<Channel[]>([])
  const [selectedPoolID, setSelectedPoolID] = useState<number | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const [syncing, setSyncing] = useState(false)
  const [configOpen, setConfigOpen] = useState(false)
  const [poolOpen, setPoolOpen] = useState(false)
  const [editingPool, setEditingPool] = useState<MainStationPool | null>(null)
  const [memberOpen, setMemberOpen] = useState(false)
  const [memberPool, setMemberPool] = useState<MainStationPool | null>(null)
  const [busyMemberID, setBusyMemberID] = useState<number | null>(null)
  const [healthSummary, setHealthSummary] = useState<MainStationMemberHealthSummary[]>([])
  const [healthChecks, setHealthChecks] = useState<MainStationHealthCheck[]>([])
  const [healthError, setHealthError] = useState("")
  const [healthRefresh, setHealthRefresh] = useState(0)
  const [profitChecks, setProfitChecks] = useState<MainStationProfitCheck[]>([])
  const [locksByAccount, setLocksByAccount] = useState<Record<number, MainStationGuardLock[]>>({})
  const [riskError, setRiskError] = useState("")
  const [riskRefresh, setRiskRefresh] = useState(0)
  const [evaluating, setEvaluating] = useState(false)
  const [protectionPreview, setProtectionPreview] = useState<MainStationProtectionPreview | null>(null)
  const [capacity, setCapacity] = useState<MainStationPoolCapacity | null>(null)
  const [auditLogs, setAuditLogs] = useState<MainStationAuditLog[]>([])
  const [policyBusy, setPolicyBusy] = useState(false)
  const [bulkBusy, setBulkBusy] = useState(false)
  const { confirm, dialog } = useConfirm()

  const load = useCallback(async () => {
    setError("")
    try {
      const station = await apiFetch<MainStationConfig>("/main-station")
      setConfig(station)
      if (!station.configured) {
        setGroups([])
        setAccounts([])
        setPools([])
        return
      }
      const [groupResult, accountResult, poolResult, channelResult] = await Promise.all([
        apiFetch<{ items: MainStationGroup[] }>("/main-station/groups?include_missing=true"),
        apiFetch<MainStationPage<MainStationAccount>>("/main-station/accounts?page=1&page_size=100&include_missing=true"),
        apiFetch<MainStationPage<MainStationPool>>("/main-station/pools?page=1&page_size=100"),
        apiFetch<Channel[]>("/channels"),
      ])
      setGroups(groupResult.items)
      setAccounts(accountResult.items)
      setPools(poolResult.items)
      setChannels(channelResult)
      setSelectedPoolID((current) => {
        if (current && poolResult.items.some((pool) => pool.id === current)) return current
        return poolResult.items[0]?.id ?? null
      })
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载主站数据失败")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const loadHealth = useCallback(async (poolID: number) => {
    setHealthError("")
    try {
      const [summary, checks] = await Promise.all([
        apiFetch<{ items: MainStationMemberHealthSummary[] }>(`/main-station/pools/${poolID}/health-summary`),
        apiFetch<MainStationPage<MainStationHealthCheck>>(`/main-station/pools/${poolID}/health-checks?page=1&page_size=30`),
      ])
      setHealthSummary(summary.items)
      setHealthChecks(checks.items)
    } catch (healthLoadError) {
      setHealthError(healthLoadError instanceof Error ? healthLoadError.message : "加载测活数据失败")
    }
  }, [])

  useEffect(() => {
    if (!config?.configured || selectedPoolID == null) {
      setHealthSummary([])
      setHealthChecks([])
      return
    }
    void loadHealth(selectedPoolID)
  }, [config?.configured, loadHealth, healthRefresh, selectedPoolID])

  const selectedPool = useMemo(
    () => pools.find((pool) => pool.id === selectedPoolID) ?? null,
    [pools, selectedPoolID],
  )
  const loadRisk = useCallback(async (pool: MainStationPool) => {
    setRiskError("")
    try {
      const [checks, preview, capacityResult, audits, lockEntries] = await Promise.all([
        apiFetch<MainStationPage<MainStationProfitCheck>>(
          `/main-station/pools/${pool.id}/profit-checks?page=1&page_size=50`,
        ),
        apiFetch<MainStationProtectionPreview>("/main-station/protection-preview"),
        apiFetch<MainStationPoolCapacity>(`/main-station/pools/${pool.id}/capacity`),
        apiFetch<MainStationPage<MainStationAuditLog>>(
          `/main-station/audit-logs?pool_id=${pool.id}&page=1&page_size=50`,
        ),
        Promise.all(
          pool.members
            .filter((member) => member.remote_account_id != null)
            .map((member) => apiFetch<{ items: MainStationGuardLock[] }>(
              `/main-station/accounts/${member.remote_account_id}/locks`,
            ).then((result) => [member.remote_account_id as number, result.items] as const)),
        ),
      ])
      setProfitChecks(checks.items)
      setProtectionPreview(preview)
      setCapacity(capacityResult)
      setAuditLogs(audits.items)
      setLocksByAccount(Object.fromEntries(lockEntries))
    } catch (riskLoadError) {
      setRiskError(riskLoadError instanceof Error ? riskLoadError.message : "加载利润与锁失败")
    }
  }, [])

  useEffect(() => {
    if (!selectedPool) {
      setProfitChecks([])
      setLocksByAccount({})
      setProtectionPreview(null)
      setCapacity(null)
      setAuditLogs([])
      return
    }
    void loadRisk(selectedPool)
  }, [loadRisk, riskRefresh, selectedPool])

  const unboundAccounts = useMemo(
    () => accounts.filter((account) => !account.bound_member_id && !account.missing),
    [accounts],
  )
  const schedulableAccounts = accounts.filter((account) => account.schedulable && !account.missing).length
  const orphanedMembers = pools.flatMap((pool) => pool.members).filter((member) => member.status === "orphaned").length

  async function handleSync() {
    setSyncing(true)
    try {
      const result = await apiFetch<MainStationSyncResult>("/main-station/sync", { method: "POST" })
      toast.success(`已同步 ${result.groups} 个分组、${result.accounts} 个 Account`)
      if (result.orphaned_members > 0) {
        toast.warning(`${result.orphaned_members} 个成员的远端 Account 已消失`)
      }
      await load()
    } catch (syncError) {
      toast.error(syncError instanceof Error ? syncError.message : "主站同步失败")
    } finally {
      setSyncing(false)
    }
  }

  async function handleDeletePool(pool: MainStationPool) {
    if (!(await confirm({
      title: `删除账号池“${pool.name}”`,
      description: pool.members.length > 0
        ? `该池仍有 ${pool.members.length} 个成员，请先逐个移除成员。`
        : "本操作只删除本地空池，不删除主站分组。",
      confirmLabel: "删除",
      destructive: true,
    }))) return
    try {
      await apiFetch(`/main-station/pools/${pool.id}`, { method: "DELETE" })
      toast.success("账号池已删除")
      await load()
    } catch (deleteError) {
      toast.error(deleteError instanceof Error ? deleteError.message : "删除账号池失败")
    }
  }

  async function handleDeleteMember(pool: MainStationPool, member: MainStationMember) {
    const description = member.ownership_mode === "bound"
      ? "只解除本地绑定，不删除或修改远端 Account。"
      : "本地移除前会暂停远端 Account；默认保留远端 Account 和源 API Key。"
    if (!(await confirm({
      title: `移除成员 #${member.id}`,
      description,
      confirmLabel: "移除",
      destructive: true,
    }))) return
    setBusyMemberID(member.id)
    try {
      await apiFetch(`/main-station/pools/${pool.id}/members/${member.id}`, {
        method: "DELETE",
        body: JSON.stringify({ confirm: true }),
      })
      toast.success("成员已移除")
      await load()
    } catch (deleteError) {
      toast.error(deleteError instanceof Error ? deleteError.message : "移除成员失败")
    } finally {
      setBusyMemberID(null)
    }
  }

  async function handleSyncMember(pool: MainStationPool, member: MainStationMember) {
    setBusyMemberID(member.id)
    try {
      await apiFetch(`/main-station/pools/${pool.id}/members/${member.id}/sync`, { method: "POST" })
      toast.success("托管成员已重新应用")
      await load()
    } catch (syncError) {
      toast.error(syncError instanceof Error ? syncError.message : "应用成员失败")
    } finally {
      setBusyMemberID(null)
    }
  }

  async function handleHealthCheck(pool: MainStationPool, member: MainStationMember, level: "L0" | "L1" | "L2") {
    setBusyMemberID(member.id)
    try {
      const result = await apiFetch<{ check: MainStationHealthCheck }>(`/main-station/pools/${pool.id}/members/${member.id}/check`, {
        method: "POST",
        body: JSON.stringify({ level, force: true }),
      })
      toast.success(`${level} 测活${result.check.status === "success" ? "成功" : "已记录"}`)
      await load()
      setHealthRefresh((value) => value + 1)
    } catch (healthErrorValue) {
      toast.error(healthErrorValue instanceof Error ? healthErrorValue.message : `${level} 测活失败`)
    } finally {
      setBusyMemberID(null)
    }
  }

  async function handleEvaluatePool(pool: MainStationPool) {
    setEvaluating(true)
    try {
      const result = await apiFetch<MainStationPoolEvaluation>(`/main-station/pools/${pool.id}/evaluate`, { method: "POST" })
      if (result.would_disable_member_ids.length > 0) {
        toast.warning(`${result.would_disable_member_ids.length} 个成员低于利润阈值；保护关闭时只记录不停用`)
      } else {
        toast.success("利润评估完成")
      }
      await load()
      setRiskRefresh((value) => value + 1)
    } catch (evaluationError) {
      toast.error(evaluationError instanceof Error ? evaluationError.message : "利润评估失败")
    } finally {
      setEvaluating(false)
    }
  }

  async function handleManualLock(pool: MainStationPool, member: MainStationMember, active: boolean) {
    if (member.remote_account_id == null) return
    const approved = await confirm({
      title: active ? `暂停成员 #${member.id}` : `解除成员 #${member.id} 的人工锁`,
      description: active
        ? "将创建独立 manual 锁并统一重新计算远端调度状态。"
        : "只解除 manual 锁；如果仍有其他锁，Account 不会启用。",
      confirmLabel: active ? "暂停" : "解除人工锁",
      destructive: active,
    })
    if (!approved) return
    setBusyMemberID(member.id)
    try {
      const path = `/main-station/accounts/${member.remote_account_id}/locks/manual${active ? "" : "/clear"}`
      await apiFetch(path, {
        method: "POST",
        body: active ? JSON.stringify({ reason: "管理员手工暂停" }) : undefined,
      })
      toast.success(active ? "人工锁已创建" : "人工锁已解除，调度状态已重新计算")
      await load()
      setRiskRefresh((value) => value + 1)
    } catch (lockError) {
      toast.error(lockError instanceof Error ? lockError.message : "调度锁操作失败")
    } finally {
      setBusyMemberID(null)
    }
  }

  async function handleProtectionToggle(
    field: "auto_margin_protection" | "auto_health_protection" | "auto_recovery",
    value: boolean,
  ) {
    if (!config) return
    const labels = {
      auto_margin_protection: "自动利润保护",
      auto_health_protection: "自动健康保护",
      auto_recovery: "自动恢复",
    }
    const impact = field === "auto_margin_protection"
      ? protectionPreview?.margin_risk_member_ids.length ?? 0
      : field === "auto_health_protection"
        ? protectionPreview?.unhealthy_member_ids.length ?? 0
        : protectionPreview?.active_locks.length ?? 0
    const approved = await confirm({
      title: `${value ? "开启" : "关闭"}${labels[field]}`,
      description: value
        ? `当前预览涉及 ${impact} 个对象。策略生效后仍会执行连续阈值判断，并在写入前重读全部锁。`
        : "关闭后只停止新的自动动作，已有调度锁不会被清除；需要管理员单独解除。",
      confirmLabel: value ? "确认开启" : "切回只通知",
      destructive: value && field !== "auto_recovery" && impact > 0,
    })
    if (!approved) return
    setPolicyBusy(true)
    try {
      const saved = await apiFetch<MainStationConfig>("/main-station/protection", {
        method: "PUT",
        body: JSON.stringify({ [field]: value }),
      })
      setConfig(saved)
      setRiskRefresh((current) => current + 1)
      toast.success(`${labels[field]}已${value ? "开启" : "关闭"}；已有锁保持不变`)
    } catch (policyError) {
      toast.error(policyError instanceof Error ? policyError.message : "保护策略更新失败")
    } finally {
      setPolicyBusy(false)
    }
  }

  async function handleNotifyOnly() {
    if (!config || (!config.auto_margin_protection && !config.auto_health_protection)) return
    if (!(await confirm({
      title: "切换为只通知模式",
      description: "将关闭自动利润和自动健康保护，但不会清除已有锁，也不会立即恢复任何 Account。",
      confirmLabel: "切换模式",
    }))) return
    setPolicyBusy(true)
    try {
      const saved = await apiFetch<MainStationConfig>("/main-station/protection", {
        method: "PUT",
        body: JSON.stringify({
          auto_margin_protection: false,
          auto_health_protection: false,
        }),
      })
      setConfig(saved)
      setRiskRefresh((current) => current + 1)
      toast.success("已切换为只通知模式；已有锁保持不变")
    } catch (policyError) {
      toast.error(policyError instanceof Error ? policyError.message : "切换只通知模式失败")
    } finally {
      setPolicyBusy(false)
    }
  }

  async function handleBulkCheck(pool: MainStationPool) {
    const estimatedRequests = pool.members.filter(
      (member) => member.enabled && member.health_enabled && member.remote_account_id != null,
    ).length
    if (!(await confirm({
      title: `批量检测“${pool.name}”`,
      description: `预计执行 ${estimatedRequests} 次 L0 请求，每个可检测成员 1 次；L0 不产生模型生成 Token，不消耗 L1/L2 Token 预算。`,
      confirmLabel: "开始检测",
    }))) return
    setBulkBusy(true)
    try {
      const result = await apiFetch<MainStationBulkOperation>(`/main-station/pools/${pool.id}/check`, {
        method: "POST",
        body: JSON.stringify({ level: "L0", force: true }),
      })
      const message = `批量检测完成：成功 ${result.succeeded}，跳过 ${result.skipped}，失败 ${result.errors.length}`
      if (result.errors.length > 0) {
        toast.warning(message)
      } else {
        toast.success(message)
      }
      setHealthRefresh((current) => current + 1)
      setRiskRefresh((current) => current + 1)
      await load()
    } catch (bulkError) {
      toast.error(bulkError instanceof Error ? bulkError.message : "批量检测失败")
    } finally {
      setBulkBusy(false)
    }
  }

  async function handleBulkRecover(pool: MainStationPool) {
    if (!(await confirm({
      title: `批量恢复“${pool.name}”`,
      description: "只处理健康且所有目标分组利润正常的成员，只解除 health 和 margin 锁；manual、credential、binding、sync 锁保持不变。",
      confirmLabel: "确认恢复",
    }))) return
    setBulkBusy(true)
    try {
      const result = await apiFetch<MainStationBulkOperation>(`/main-station/pools/${pool.id}/recover`, {
        method: "POST",
        body: JSON.stringify({ confirm: true }),
      })
      const message = `批量恢复完成：成功 ${result.succeeded}，跳过 ${result.skipped}，失败 ${result.errors.length}`
      if (result.errors.length > 0) {
        toast.warning(message)
      } else {
        toast.success(message)
      }
      await load()
      setRiskRefresh((current) => current + 1)
    } catch (bulkError) {
      toast.error(bulkError instanceof Error ? bulkError.message : "批量恢复失败")
    } finally {
      setBulkBusy(false)
    }
  }

  async function handleClearAutomaticLocks(member: MainStationMember) {
    if (member.remote_account_id == null) return
    if (!(await confirm({
      title: `解除成员 #${member.id} 的全部自动锁`,
      description: "将解除 margin、health、sync、credential 和 binding 锁，但保留 manual 锁；随后按剩余锁重新计算调度状态。",
      confirmLabel: "解除自动锁",
    }))) return
    setBusyMemberID(member.id)
    try {
      await apiFetch(`/main-station/accounts/${member.remote_account_id}/locks/automatic/clear`, { method: "POST" })
      toast.success("自动锁已解除；调度状态已按剩余锁重新计算")
      await load()
      setRiskRefresh((current) => current + 1)
    } catch (lockError) {
      toast.error(lockError instanceof Error ? lockError.message : "解除自动锁失败")
    } finally {
      setBusyMemberID(null)
    }
  }

  if (loading) {
    return <div className="flex min-h-72 items-center justify-center"><Spinner className="size-5" /></div>
  }

  return (
    <section className="space-y-4">
      <header className="flex flex-col gap-3 border-b pb-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <ServerCog className="size-5 text-emerald-600" />
            <h1 className="text-lg font-semibold text-foreground">主站</h1>
            {config?.configured ? <StatusBadge value={config.last_sync_status || "unknown"} /> : null}
          </div>
          <p className="mt-1 truncate text-xs text-muted-foreground" title={config?.base_url}>
            {config?.configured ? `${config.name} · ${config.base_url}` : "尚未配置 Sub2API 主站"}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          {config?.configured ? (
            <>
              <Button variant="outline" size="sm" onClick={() => setConfigOpen(true)}>
                <Pencil className="size-4" />编辑
              </Button>
              <Button size="sm" onClick={handleSync} disabled={syncing}>
                <RefreshCw className={cn("size-4", syncing && "animate-spin")} />
                {syncing ? "同步中" : "同步主站"}
              </Button>
            </>
          ) : (
            <Button size="sm" onClick={() => setConfigOpen(true)}>
              <Plus className="size-4" />配置主站
            </Button>
          )}
        </div>
      </header>

      {error ? (
        <Alert variant="destructive">
          <AlertTriangle className="size-4" />
          <AlertTitle>主站数据加载失败</AlertTitle>
          <AlertDescription className="flex flex-wrap items-center justify-between gap-2">
            <span className="break-all">{error}</span>
            <Button variant="outline" size="sm" onClick={() => void load()}>重试</Button>
          </AlertDescription>
        </Alert>
      ) : null}

      {!config?.configured ? (
        <div className="flex min-h-72 flex-col items-center justify-center gap-4 border border-dashed p-6 text-center">
          <ServerCog className="size-8 text-muted-foreground" />
          <div>
            <p className="text-sm font-medium">配置唯一主站后开始管理账号池</p>
            {config?.migration?.status === "requires_confirmation" ? (
              <p className="mt-2 max-w-2xl text-xs text-amber-700">{config.migration.detail}</p>
            ) : null}
          </div>
          <Button onClick={() => setConfigOpen(true)}>配置主站</Button>
        </div>
      ) : (
        <>
          <div className="grid grid-cols-2 divide-x divide-y border sm:grid-cols-4 sm:divide-y-0">
            <Metric label="主站分组" value={groups.filter((group) => !group.missing).length} />
            <Metric label="Account" value={accounts.filter((account) => !account.missing).length} />
            <Metric label="可调度" value={schedulableAccounts} />
            <Metric label="绑定失效" value={orphanedMembers} danger={orphanedMembers > 0} />
          </div>

          <Tabs defaultValue="pools" className="space-y-3">
            <TabsList className="h-auto w-full justify-start overflow-x-auto">
              <TabsTrigger value="pools">账号池</TabsTrigger>
              <TabsTrigger value="health">测活</TabsTrigger>
              <TabsTrigger value="risk">利润与锁</TabsTrigger>
              <TabsTrigger value="audit">审计</TabsTrigger>
              <TabsTrigger value="accounts">主站账号</TabsTrigger>
              <TabsTrigger value="groups">主站分组</TabsTrigger>
            </TabsList>

            <TabsContent value="pools" className="space-y-4">
              <div className="flex items-center justify-between gap-3">
                <p className="text-sm text-muted-foreground">{pools.length} 个账号池</p>
                <Button
                  size="sm"
                  onClick={() => {
                    setEditingPool(null)
                    setPoolOpen(true)
                  }}
                >
                  <Plus className="size-4" />创建账号池
                </Button>
              </div>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>账号池</TableHead>
                    <TableHead>服务分组</TableHead>
                    <TableHead>成员</TableHead>
                    <TableHead>容量阈值</TableHead>
                    <TableHead>状态</TableHead>
                    <TableHead className="w-28 text-right">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {pools.map((pool) => (
                    <TableRow
                      key={pool.id}
                      data-state={selectedPoolID === pool.id ? "selected" : undefined}
                      className="cursor-pointer"
                      onClick={() => setSelectedPoolID(pool.id)}
                    >
                      <TableCell>
                        <div className="max-w-64 truncate font-medium" title={pool.name}>{pool.name}</div>
                        <div className="max-w-64 truncate text-xs text-muted-foreground" title={pool.description}>{pool.description || "—"}</div>
                      </TableCell>
                      <TableCell className="max-w-72 truncate" title={pool.groups.map((group) => group.name).join("、")}>
                        {pool.groups.map((group) => group.name).join("、") || "—"}
                      </TableCell>
                      <TableCell>{pool.members.length}</TableCell>
                      <TableCell>{pool.minimum_healthy_members} 人 / {pool.minimum_effective_concurrency} 并发</TableCell>
                      <TableCell><StatusBadge value={pool.last_status} /></TableCell>
                      <TableCell>
                        <div className="flex justify-end gap-1" onClick={(event) => event.stopPropagation()}>
                          <IconButton label="编辑账号池" onClick={() => { setEditingPool(pool); setPoolOpen(true) }}>
                            <Pencil className="size-4" />
                          </IconButton>
                          <IconButton label="删除账号池" destructive onClick={() => void handleDeletePool(pool)}>
                            <Trash2 className="size-4" />
                          </IconButton>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                  {pools.length === 0 ? <EmptyRow columns={6} text="还没有账号池" /> : null}
                </TableBody>
              </Table>

              {selectedPool ? (
                <section className="space-y-3 border-t pt-4">
                  <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
                    <div>
                      <h2 className="text-sm font-semibold">{selectedPool.name} · 成员</h2>
                      <p className="text-xs text-muted-foreground">托管成员和绑定成员保持独立远端 Account。</p>
                    </div>
                    <Button
                      size="sm"
                      onClick={() => {
                        setMemberPool(selectedPool)
                        setMemberOpen(true)
                      }}
                    >
                      <Plus className="size-4" />添加成员
                    </Button>
                  </div>
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>成员</TableHead>
                        <TableHead>上游</TableHead>
                        <TableHead>主站 Account</TableHead>
                        <TableHead>并发 / 权重</TableHead>
                        <TableHead>绑定</TableHead>
                        <TableHead>状态</TableHead>
                        <TableHead className="w-28 text-right">操作</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {selectedPool.members.map((member) => {
                        const channel = channels.find((item) => item.id === member.source_channel_id)
                        return (
                          <TableRow key={member.id}>
                            <TableCell className="font-medium">#{member.id} · {member.ownership_mode === "managed" ? "托管" : "绑定"}</TableCell>
                            <TableCell>
                              <div className="max-w-56 truncate" title={channel?.name}>{channel?.name || `渠道 #${member.source_channel_id}`}</div>
                              <div className="max-w-56 truncate text-xs text-muted-foreground" title={member.source_group_name}>{member.source_group_name || "默认分组"}</div>
                            </TableCell>
                            <TableCell>
                              <div className="max-w-56 truncate" title={member.remote_account_name}>{member.remote_account_name || "尚未创建"}</div>
                              <div className="text-xs text-muted-foreground">{member.remote_account_id ? `#${member.remote_account_id}` : "—"}</div>
                            </TableCell>
                            <TableCell>{member.concurrency} / {member.weight}</TableCell>
                            <TableCell><StatusBadge value={member.binding_status} /></TableCell>
                            <TableCell><StatusBadge value={member.status} /></TableCell>
                            <TableCell>
                              <div className="flex justify-end gap-1">
                                {member.ownership_mode === "managed" && (member.status === "error" || member.status === "pending") ? (
                                  <IconButton
                                    label="重新应用托管配置"
                                    disabled={busyMemberID === member.id}
                                    onClick={() => void handleSyncMember(selectedPool, member)}
                                  >
                                    <RefreshCw className={cn("size-4", busyMemberID === member.id && "animate-spin")} />
                                  </IconButton>
                                ) : null}
                                <IconButton
                                  label="移除成员"
                                  destructive
                                  disabled={busyMemberID === member.id}
                                  onClick={() => void handleDeleteMember(selectedPool, member)}
                                >
                                  <Trash2 className="size-4" />
                                </IconButton>
                                <div className="ml-1 flex items-center gap-1 border-l pl-1">
                                  {(["L0", "L1", "L2"] as const).map((level) => (
                                    <Button
                                      key={level}
                                      size="sm"
                                      variant="ghost"
                                      className="h-7 px-1.5 text-[11px]"
                                      disabled={busyMemberID === member.id || (level !== "L0" && !member.health_model)}
                                      onClick={() => void handleHealthCheck(selectedPool, member, level)}
                                      title={level === "L0" ? "零生成探测" : level === "L1" ? "低 Token 推理探测" : "主站 Account 链路测试"}
                                    >
                                      {level}
                                    </Button>
                                  ))}
                                </div>
                              </div>
                            </TableCell>
                          </TableRow>
                        )
                      })}
                      {selectedPool.members.length === 0 ? <EmptyRow columns={7} text="账号池中还没有成员" /> : null}
                    </TableBody>
                  </Table>
                </section>
              ) : null}
            </TabsContent>

            <TabsContent value="health" className="space-y-4">
              {healthError ? (
                <Alert variant="destructive">
                  <AlertTriangle className="size-4" />
                  <AlertTitle>测活数据加载失败</AlertTitle>
                  <AlertDescription>{healthError}</AlertDescription>
                </Alert>
              ) : null}
              <div className="flex items-center gap-2 text-sm text-muted-foreground">
                <Activity className="size-4" />
                <span>手工测活会绕过最短间隔和预算；scheduler 仍遵守并发、抖动和预算。</span>
              </div>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>成员</TableHead>
                    <TableHead>状态</TableHead>
                    <TableHead>最近 20 次</TableHead>
                    <TableHead>24 小时</TableHead>
                    <TableHead>P50 / P95</TableHead>
                    <TableHead>连续成功 / 失败</TableHead>
                    <TableHead>今日预算</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {healthSummary.map((summary) => (
                    <TableRow key={summary.member.id}>
                      <TableCell className="font-medium">#{summary.member.id} · {summary.member.remote_account_name || "未绑定"}</TableCell>
                      <TableCell><StatusBadge value={summary.stats.last_status} /></TableCell>
                      <TableCell>{formatPercent(summary.stats.recent_20_success_rate)}</TableCell>
                      <TableCell>{formatPercent(summary.stats.twenty_four_hour_success_rate)}</TableCell>
                      <TableCell>{summary.stats.p50_latency_ms ?? "—"} / {summary.stats.p95_latency_ms ?? "—"} ms</TableCell>
                      <TableCell>{summary.stats.consecutive_success} / {summary.stats.consecutive_failure}</TableCell>
                      <TableCell>
                        L1 {summary.budget.daily_l1_used}/{summary.budget.daily_l1_limit || "∞"} · L2 {summary.budget.daily_l2_used}/{summary.budget.daily_l2_limit || "∞"}
                      </TableCell>
                    </TableRow>
                  ))}
                  {healthSummary.length === 0 ? <EmptyRow columns={7} text="选择一个账号池后查看测活统计" /> : null}
                </TableBody>
              </Table>
              <div className="flex items-center gap-2 text-sm font-semibold">
                <TestTube2 className="size-4 text-emerald-600" />最近测活记录
              </div>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>时间</TableHead>
                    <TableHead>成员</TableHead>
                    <TableHead>层级</TableHead>
                    <TableHead>状态</TableHead>
                    <TableHead>HTTP</TableHead>
                    <TableHead>延迟</TableHead>
                    <TableHead>错误</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {healthChecks.map((check) => (
                    <TableRow key={check.id}>
                      <TableCell>{relativeTime(check.created_at)}</TableCell>
                      <TableCell>#{check.member_id}</TableCell>
                      <TableCell>{check.level}</TableCell>
                      <TableCell><StatusBadge value={check.status} /></TableCell>
                      <TableCell>{check.http_status || "—"}</TableCell>
                      <TableCell>{check.latency_ms} ms</TableCell>
                      <TableCell className="max-w-80 truncate" title={check.message || check.error_class}>{check.error_class || check.message || "—"}</TableCell>
                    </TableRow>
                  ))}
                  {healthChecks.length === 0 ? <EmptyRow columns={7} text="还没有测活记录" /> : null}
                </TableBody>
              </Table>
            </TabsContent>

            <TabsContent value="risk" className="space-y-4">
              {riskError ? (
                <Alert variant="destructive">
                  <AlertTriangle className="size-4" />
                  <AlertTitle>利润与锁加载失败</AlertTitle>
                  <AlertDescription>{riskError}</AlertDescription>
                </Alert>
              ) : null}
              <div className="grid divide-y border lg:grid-cols-[1fr_auto] lg:divide-x lg:divide-y-0">
                <div className="grid grid-cols-2 divide-x divide-y sm:grid-cols-3 sm:divide-y-0">
                  <Metric label="池状态" value={capacity?.status ?? "—"} danger={capacity?.status === "critical"} />
                  <Metric label="合格成员" value={`${capacity?.qualified_members ?? 0}/${capacity?.total_members ?? 0}`} />
                  <Metric label="有效并发" value={`${capacity?.effective_concurrency ?? 0}/${selectedPool?.minimum_effective_concurrency ?? 0}`} danger={capacity?.status === "degraded" || capacity?.status === "critical"} />
                </div>
                <div className="flex flex-wrap items-center gap-2 p-3">
                  <Button size="sm" variant="outline" disabled={!selectedPool || bulkBusy} onClick={() => selectedPool && void handleBulkCheck(selectedPool)}>
                    <TestTube2 className="size-4" />批量 L0
                  </Button>
                  <Button size="sm" variant="outline" disabled={!selectedPool || bulkBusy} onClick={() => selectedPool && void handleBulkRecover(selectedPool)}>
                    <Unlock className="size-4" />批量恢复
                  </Button>
                </div>
              </div>

              <div className="space-y-3 border-y py-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div>
                    <p className="text-sm font-semibold">自动保护策略</p>
                    <p className="text-xs text-muted-foreground">开启前必须有对应观察证据；所有策略默认关闭。</p>
                  </div>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={policyBusy || (!config?.auto_margin_protection && !config?.auto_health_protection)}
                    onClick={() => void handleNotifyOnly()}
                  >
                    <ShieldCheck className="size-4" />只通知模式
                  </Button>
                </div>
                <div className="grid gap-3 md:grid-cols-3">
                  <ProtectionSwitch
                    id="auto-margin-protection"
                    label="自动利润保护"
                    detail={protectionPreview?.margin_ready ? `已评估，当前 ${protectionPreview.margin_risk_member_ids.length} 个风险成员` : "需要先执行只读利润评估"}
                    checked={config?.auto_margin_protection ?? false}
                    disabled={policyBusy || (!protectionPreview?.margin_ready && !config?.auto_margin_protection)}
                    onCheckedChange={(checked) => void handleProtectionToggle("auto_margin_protection", checked)}
                  />
                  <ProtectionSwitch
                    id="auto-health-protection"
                    label="自动健康保护"
                    detail={protectionPreview?.health_ready ? `已观察，当前 ${protectionPreview.unhealthy_member_ids.length} 个异常成员` : "需要先完成至少一次测活"}
                    checked={config?.auto_health_protection ?? false}
                    disabled={policyBusy || (!protectionPreview?.health_ready && !config?.auto_health_protection)}
                    onCheckedChange={(checked) => void handleProtectionToggle("auto_health_protection", checked)}
                  />
                  <ProtectionSwitch
                    id="auto-recovery"
                    label="自动恢复"
                    detail="仅清除达到恢复阈值的对应自动锁"
                    checked={config?.auto_recovery ?? false}
                    disabled={policyBusy}
                    onCheckedChange={(checked) => void handleProtectionToggle("auto_recovery", checked)}
                  />
                </div>
              </div>

              <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
                <div className="flex items-center gap-2 text-sm text-muted-foreground">
                  <ShieldCheck className="size-4" />
                  <span>自动利润保护默认关闭；未知或不支持的计费不会创建 margin 锁。</span>
                </div>
                <Button
                  size="sm"
                  onClick={() => selectedPool && void handleEvaluatePool(selectedPool)}
                  disabled={!selectedPool || evaluating}
                >
                  <RefreshCw className={cn("size-4", evaluating && "animate-spin")} />
                  {evaluating ? "评估中" : "只读评估"}
                </Button>
              </div>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>成员</TableHead>
                    <TableHead>活动锁</TableHead>
                    <TableHead>最终调度</TableHead>
                    <TableHead className="w-56 text-right">锁控制</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {(selectedPool?.members ?? []).map((member) => {
                    const locks = member.remote_account_id == null ? [] : (locksByAccount[member.remote_account_id] ?? [])
                    const hasManual = locks.some((lock) => lock.lock_type === "manual")
                    const account = accounts.find((item) => item.remote_account_id === member.remote_account_id)
                    return (
                      <TableRow key={member.id}>
                        <TableCell className="font-medium">#{member.id} · {member.remote_account_name || "未绑定"}</TableCell>
                        <TableCell>
                          <div className="flex max-w-96 flex-wrap gap-1">
                            {locks.map((lock) => (
                              <Badge key={lock.id} variant="outline" className="border-red-200 bg-red-50 text-red-700" title={lock.reason}>
                                {lock.lock_type}
                              </Badge>
                            ))}
                            {locks.length === 0 ? <span className="text-muted-foreground">无</span> : null}
                          </div>
                        </TableCell>
                        <TableCell><StatusBadge value={account?.schedulable ? "active" : "disabled"} /></TableCell>
                        <TableCell>
                          <div className="flex flex-wrap justify-end gap-1">
                            <Button
                              size="sm"
                              variant="ghost"
                              disabled={member.remote_account_id == null || busyMemberID === member.id || !locks.some((lock) => lock.lock_type !== "manual")}
                              onClick={() => void handleClearAutomaticLocks(member)}
                            >
                              <Unlock className="size-4" />自动锁
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              disabled={member.remote_account_id == null || busyMemberID === member.id}
                              onClick={() => selectedPool && void handleManualLock(selectedPool, member, !hasManual)}
                            >
                              {hasManual ? <Unlock className="size-4" /> : <PauseCircle className="size-4" />}
                              {hasManual ? "解除" : "暂停"}
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    )
                  })}
                  {!selectedPool || selectedPool.members.length === 0 ? <EmptyRow columns={4} text="当前账号池没有成员" /> : null}
                </TableBody>
              </Table>

              <div className="text-sm font-semibold">最近利润快照</div>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>时间</TableHead>
                    <TableHead>成员 / 分组</TableHead>
                    <TableHead>销售倍率</TableHead>
                    <TableHead>成本倍率</TableHead>
                    <TableHead>利润率</TableHead>
                    <TableHead>来源</TableHead>
                    <TableHead>状态</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {profitChecks.map((check) => (
                    <TableRow key={check.id}>
                      <TableCell>{relativeTime(check.observed_at)}</TableCell>
                      <TableCell>#{check.member_id} / 分组 #{check.target_group_id}</TableCell>
                      <TableCell>{formatMicros(check.sale_multiplier_micros)}</TableCell>
                      <TableCell>{formatMicros(check.cost_multiplier_micros)}</TableCell>
                      <TableCell>{(check.margin_basis_points / 100).toFixed(2)}%</TableCell>
                      <TableCell>
                        <div className="max-w-64 truncate" title={`${check.sale_source || "—"} / ${check.cost_source || "—"}`}>
                          {check.sale_source || "—"} / {check.cost_source || "—"}
                        </div>
                      </TableCell>
                      <TableCell><StatusBadge value={check.status} /></TableCell>
                    </TableRow>
                  ))}
                  {profitChecks.length === 0 ? <EmptyRow columns={7} text="尚未执行利润评估" /> : null}
                </TableBody>
              </Table>
            </TabsContent>

            <TabsContent value="audit" className="space-y-3">
              <div className="flex items-center justify-between gap-3">
                <div>
                  <p className="text-sm font-semibold">操作审计</p>
                  <p className="text-xs text-muted-foreground">显示所选账号池最近 50 条远端写入、策略和锁操作记录。</p>
                </div>
                <Button size="sm" variant="outline" disabled={!selectedPool} onClick={() => setRiskRefresh((current) => current + 1)}>
                  <RefreshCw className="size-4" />刷新
                </Button>
              </div>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>时间</TableHead>
                    <TableHead>动作</TableHead>
                    <TableHead>成员 / Account</TableHead>
                    <TableHead>来源</TableHead>
                    <TableHead>结果</TableHead>
                    <TableHead>详情</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {auditLogs.map((audit) => {
                    const detail = audit.error_message || audit.detail || audit.evidence || audit.after || "—"
                    return (
                      <TableRow key={audit.id}>
                        <TableCell>{relativeTime(audit.created_at)}</TableCell>
                        <TableCell className="font-medium">{audit.action}</TableCell>
                        <TableCell>#{audit.member_id ?? "—"} / #{audit.remote_account_id ?? "—"}</TableCell>
                        <TableCell>{audit.source}</TableCell>
                        <TableCell><StatusBadge value={audit.success ? "success" : "failed"} /></TableCell>
                        <TableCell>
                          <div className="max-w-96 truncate" title={detail}>{detail}</div>
                        </TableCell>
                      </TableRow>
                    )
                  })}
                  {!selectedPool || auditLogs.length === 0 ? <EmptyRow columns={6} text={selectedPool ? "还没有审计记录" : "选择一个账号池后查看审计"} /> : null}
                </TableBody>
              </Table>
            </TabsContent>

            <TabsContent value="accounts">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Account</TableHead>
                    <TableHead>平台</TableHead>
                    <TableHead>分组 ID</TableHead>
                    <TableHead>并发</TableHead>
                    <TableHead>调度</TableHead>
                    <TableHead>绑定</TableHead>
                    <TableHead>最近同步</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {accounts.map((account) => (
                    <TableRow key={account.id}>
                      <TableCell>
                        <div className="max-w-72 truncate font-medium" title={account.name}>{account.name}</div>
                        <div className="text-xs text-muted-foreground">#{account.remote_account_id}</div>
                      </TableCell>
                      <TableCell>{account.platform || "—"}</TableCell>
                      <TableCell>{parseGroupIDs(account.group_ids).join(", ") || "—"}</TableCell>
                      <TableCell>{account.concurrency}</TableCell>
                      <TableCell><StatusBadge value={account.missing ? "orphaned" : account.schedulable ? "active" : "disabled"} /></TableCell>
                      <TableCell>{account.bound_member_id ? `成员 #${account.bound_member_id}` : "未绑定"}</TableCell>
                      <TableCell>{relativeTime(account.last_sync_at)}</TableCell>
                    </TableRow>
                  ))}
                  {accounts.length === 0 ? <EmptyRow columns={7} text="尚未同步主站 Account" /> : null}
                </TableBody>
              </Table>
            </TabsContent>

            <TabsContent value="groups">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>分组</TableHead>
                    <TableHead>平台</TableHead>
                    <TableHead>基础倍率</TableHead>
                    <TableHead>高峰倍率</TableHead>
                    <TableHead>计费类型</TableHead>
                    <TableHead>状态</TableHead>
                    <TableHead>最近同步</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {groups.map((group) => (
                    <TableRow key={group.id}>
                      <TableCell>
                        <div className="max-w-72 truncate font-medium" title={group.name}>{group.name}</div>
                        <div className="text-xs text-muted-foreground">#{group.remote_group_id}</div>
                      </TableCell>
                      <TableCell>{group.platform || "—"}</TableCell>
                      <TableCell>{formatMicros(group.rate_multiplier_micros)}</TableCell>
                      <TableCell>{group.peak_enabled ? formatMicros(group.peak_multiplier_micros) : "—"}</TableCell>
                      <TableCell>{group.subscription_type || "按量"}</TableCell>
                      <TableCell><StatusBadge value={group.missing ? "orphaned" : group.status} /></TableCell>
                      <TableCell>{relativeTime(group.last_sync_at)}</TableCell>
                    </TableRow>
                  ))}
                  {groups.length === 0 ? <EmptyRow columns={7} text="尚未同步主站分组" /> : null}
                </TableBody>
              </Table>
            </TabsContent>
          </Tabs>
        </>
      )}

      <StationConfigDialog
        open={configOpen}
        onOpenChange={setConfigOpen}
        config={config}
        onSaved={(saved) => {
          setConfig(saved)
          void load()
        }}
      />
      <PoolDialog
        open={poolOpen}
        onOpenChange={setPoolOpen}
        pool={editingPool}
        groups={groups.filter((group) => !group.missing)}
        onSaved={() => void load()}
      />
      <MemberDialog
        open={memberOpen}
        onOpenChange={setMemberOpen}
        pool={memberPool}
        channels={channels}
        accounts={unboundAccounts}
        onSaved={() => void load()}
      />
      {dialog}
    </section>
  )
}

function Metric({ label, value, danger = false }: { label: string; value: ReactNode; danger?: boolean }) {
  return (
    <div className="min-w-0 p-3">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className={cn("mt-1 text-lg font-semibold", danger && "text-destructive")}>{value}</p>
    </div>
  )
}

function ProtectionSwitch({
  id,
  label,
  detail,
  checked,
  disabled,
  onCheckedChange,
}: {
  id: string
  label: string
  detail: string
  checked: boolean
  disabled: boolean
  onCheckedChange: (checked: boolean) => void
}) {
  return (
    <div className="flex min-w-0 items-center justify-between gap-3 border-l-2 pl-3">
      <div className="min-w-0">
        <label htmlFor={id} className="text-sm font-medium">{label}</label>
        <p className="truncate text-xs text-muted-foreground" title={detail}>{detail}</p>
      </div>
      <Switch id={id} checked={checked} disabled={disabled} onCheckedChange={onCheckedChange} />
    </div>
  )
}

function StatusBadge({ value }: { value: string }) {
  const normalized = value?.toLowerCase() || "unknown"
  const positive = ["success", "active", "healthy", "verified", "manual_confirmed"].includes(normalized)
  const danger = ["failed", "error", "critical", "orphaned", "invalid"].includes(normalized)
  return (
    <Badge
      variant="outline"
      className={cn(
        "max-w-40 truncate border-transparent",
        positive && "bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300",
        danger && "bg-red-50 text-red-700 dark:bg-red-950/40 dark:text-red-300",
        !positive && !danger && "bg-muted text-muted-foreground",
      )}
      title={value}
    >
      {statusLabel(normalized)}
    </Badge>
  )
}

function IconButton({
  label,
  destructive = false,
  disabled = false,
  onClick,
  children,
}: {
  label: string
  destructive?: boolean
  disabled?: boolean
  onClick: () => void
  children: ReactNode
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          size="icon-sm"
          variant="ghost"
          aria-label={label}
          disabled={disabled}
          className={cn(destructive && "text-destructive hover:bg-destructive/10 hover:text-destructive")}
          onClick={onClick}
        >
          {children}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  )
}

function EmptyRow({ columns, text }: { columns: number; text: string }) {
  return <TableRow><TableCell colSpan={columns} className="h-28 text-center text-muted-foreground">{text}</TableCell></TableRow>
}

function parseGroupIDs(raw: string): number[] {
  try {
    const value: unknown = JSON.parse(raw || "[]")
    return Array.isArray(value) ? value.filter((item): item is number => typeof item === "number") : []
  } catch {
    return []
  }
}

function formatMicros(value?: number | null) {
  if (value == null) return "—"
  return (value / 1_000_000).toLocaleString("zh-CN", { maximumFractionDigits: 6 })
}

function formatPercent(value?: number | null) {
  if (value == null || !Number.isFinite(value)) return "—"
  return `${value.toFixed(1)}%`
}

function statusLabel(value: string) {
  const labels: Record<string, string> = {
    success: "正常",
    active: "启用",
    healthy: "健康",
    verified: "已验证",
    manual_confirmed: "人工确认",
    pending: "待处理",
    degraded: "降级",
    disabled: "停用",
    failed: "失败",
    error: "错误",
    critical: "严重",
    orphaned: "绑定失效",
    invalid: "无效",
    unknown: "未知",
  }
  return labels[value] ?? value
}
