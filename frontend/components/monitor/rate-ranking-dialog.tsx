"use client"

import { useEffect, useMemo, useState } from "react"
import { ArrowLeft, CheckCircle2, Clock3, Gauge, Link2, Loader2, Plus, Server, TestTubeDiagonal, TriangleAlert, Unlink, XCircle } from "lucide-react"
import { toast } from "sonner"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { useConfirm } from "@/components/ui/confirm-dialog"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Label } from "@/components/ui/label"
import { Input } from "@/components/ui/input"
import { ScrollArea } from "@/components/ui/scroll-area"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { apiFetch, type ApiError } from "@/lib/api"
import type {
  Channel,
  MainStationConfig,
  MainStationGroupWorkspace,
  MainStationHealthModelCatalog,
  MainStationMember,
  RateQuickTestResult,
  RateProviderType,
  RateSnapshot,
} from "@/lib/api-types"
import { dateTime, formatRatio, relativeTime } from "@/lib/format"
import {
  ALL_RATE_CATEGORY,
  categoryRankingRates,
  latestRateSeenAt,
  type RateCategoryOption,
} from "@/lib/rate-ranking"
import { mainStationHealthAPIMode, mainStationPlatformsMatch } from "@/lib/main-station-platform"
import { cn } from "@/lib/utils"

export const RATE_PROVIDERS: Array<{ value: RateProviderType; label: string }> = [
  { value: "openai", label: "OpenAI" },
  { value: "anthropic", label: "Anthropic" },
  { value: "gemini", label: "Gemini" },
  { value: "antigravity", label: "Antigravity" },
  { value: "grok", label: "Grok" },
]

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  provider: RateProviderType
  onProviderChange: (provider: RateProviderType) => void
  category: string
  onCategoryChange: (category: string) => void
  categoryOptions: RateCategoryOption[]
  rates: RateSnapshot[]
  channels: Channel[]
  initialRateID: number | null
  onAdded: () => void
}

export function RateRankingDialog({ open, onOpenChange, provider, onProviderChange, category, onCategoryChange, categoryOptions, rates, channels, initialRateID, onAdded }: Props) {
  const { confirm, dialog: confirmDialog } = useConfirm()
  const [view, setView] = useState<"list" | "test">("list")
  const [selectedRateID, setSelectedRateID] = useState<number | null>(null)
  const [config, setConfig] = useState<MainStationConfig | null>(null)
  const [catalogs, setCatalogs] = useState<MainStationHealthModelCatalog[]>([])
  const [workspaces, setWorkspaces] = useState<MainStationGroupWorkspace[]>([])
  const [metadataLoading, setMetadataLoading] = useState(false)
  const [metadataError, setMetadataError] = useState("")
  const [model, setModel] = useState("")
  const [testing, setTesting] = useState(false)
  const [result, setResult] = useState<RateQuickTestResult | null>(null)
  const [workspaceID, setWorkspaceID] = useState("")
  const [adding, setAdding] = useState(false)
  const [addedGroupName, setAddedGroupName] = useState("")

  const channelMap = useMemo(() => new Map(channels.map((channel) => [channel.id, channel])), [channels])
  const activeCategory = categoryOptions.some((item) => item.value === category) ? category : ALL_RATE_CATEGORY
  const visibleRates = useMemo(() => categoryRankingRates(rates, activeCategory), [activeCategory, rates])
  const selectedRate = rates.find((rate) => rate.id === selectedRateID) ?? null
  const refreshedAt = useMemo(() => latestRateSeenAt(visibleRates), [visibleRates])
  const providerModels = useMemo(() => {
    const values = [config?.health_models?.[provider] ?? "", ...(catalogs.find((item) => item.platform === provider)?.models ?? [])]
    return Array.from(new Set(values.map((value) => value.trim()).filter(Boolean)))
  }, [catalogs, config, provider])
  const availableWorkspaces = useMemo(() => {
    if (!selectedRate) return []
    const connected = new Set(selectedRate.main_station_groups.map((group) => group.group_id))
    return workspaces
      .filter((workspace) => !workspace.group.missing
        && !connected.has(workspace.group.id)
        && mainStationPlatformsMatch(workspace.group.platform, provider))
      .sort((left, right) => {
        return left.group.sort - right.group.sort || left.group.id - right.group.id
      })
  }, [provider, selectedRate, workspaces])

  useEffect(() => {
    if (!open) return
    setResult(null)
    setAddedGroupName("")
    setWorkspaceID("")
    if (initialRateID != null) {
      setSelectedRateID(initialRateID)
      setView("test")
    } else {
      setSelectedRateID(null)
      setView("list")
    }
  }, [initialRateID, open])

  useEffect(() => {
    if (!open) return
    let cancelled = false
    setMetadataLoading(true)
    setMetadataError("")
    Promise.allSettled([
      apiFetch<MainStationConfig>("/main-station"),
      apiFetch<MainStationHealthModelCatalog[]>("/main-station/health-models"),
      apiFetch<{ items: MainStationGroupWorkspace[] }>("/main-station/groups"),
    ]).then(([configResult, catalogsResult, workspacesResult]) => {
      if (cancelled) return
      if (configResult.status === "fulfilled") setConfig(configResult.value)
      if (catalogsResult.status === "fulfilled") setCatalogs(catalogsResult.value)
      if (workspacesResult.status === "fulfilled") setWorkspaces(workspacesResult.value.items)
      const errors = [configResult, catalogsResult, workspacesResult]
        .filter((item) => item.status === "rejected")
        .map((item) => item.status === "rejected" && item.reason instanceof Error ? item.reason.message : "读取主站信息失败")
      setMetadataError(errors.filter(Boolean).join("；"))
    }).finally(() => {
      if (!cancelled) setMetadataLoading(false)
    })
    return () => { cancelled = true }
  }, [open])

  useEffect(() => {
    if (!selectedRate || model || providerModels.length === 0) return
    setModel(providerModels[0])
  }, [model, providerModels, selectedRate])

  useEffect(() => {
    if (workspaceID || availableWorkspaces.length === 0) return
    setWorkspaceID(String(availableWorkspaces[0].group.id))
  }, [availableWorkspaces, workspaceID])

  function changeProvider(value: string) {
    onProviderChange(value as RateProviderType)
    onCategoryChange(ALL_RATE_CATEGORY)
    setView("list")
    setSelectedRateID(null)
    setResult(null)
    setModel("")
  }

  function openTest(rate: RateSnapshot) {
    setSelectedRateID(rate.id)
    setResult(null)
    setAddedGroupName("")
    setWorkspaceID("")
    setModel(config?.health_models?.[provider]?.trim() || providerModels[0] || "")
    setView("test")
  }

  async function runTest() {
    if (!selectedRate || !model.trim()) {
      toast.error("请选择测试模型")
      return
    }
    setTesting(true)
    setResult(null)
    setAddedGroupName("")
    try {
      const tested = await apiFetch<RateQuickTestResult>(`/channels/${selectedRate.channel_id}/rates/${selectedRate.id}/test`, {
        method: "POST",
        body: JSON.stringify({ platform: provider, model: model.trim() }),
      })
      setResult(tested)
      if (tested.usable) toast.success("快速测试通过")
      else toast.warning(tested.message)
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "快速测试失败")
    } finally {
      setTesting(false)
    }
  }

  async function addToMainStation() {
    if (!selectedRate || !result?.usable || !workspaceID) return
    const workspace = availableWorkspaces.find((item) => String(item.group.id) === workspaceID)
    if (!workspace) {
      toast.error("请选择主站分组")
      return
    }
    let allowNameConflict = false
    while (true) {
      setAdding(true)
      try {
        await apiFetch<MainStationMember>(`/main-station/groups/${workspace.group.id}/accounts`, {
          method: "POST",
          body: JSON.stringify({
            account_name: selectedRate.model_name,
            ownership_mode: "managed",
            source_channel_id: selectedRate.channel_id,
            source_group_id: selectedRate.remote_group_id ?? undefined,
            source_group_name: selectedRate.model_name,
            allow_name_conflict: allowNameConflict,
            enabled: true,
            preferred: false,
            priority: 1,
            concurrency: 0,
            rate_convert_mode: "raw",
            rate_convert_value: 1,
            cost_adjustment: 1,
            health_enabled: true,
            health_model: model.trim(),
            health_api_mode: mainStationHealthAPIMode(workspace.group.platform),
          }),
        })
        setAddedGroupName(workspace.group.name)
        toast.success(`已添加到主站分组「${workspace.group.name}」`)
        onAdded()
        return
      } catch (error) {
        if (!allowNameConflict && isManagedAccountNameConflict(error)) {
          const approved = await confirm({
            title: "主站已存在同名账号",
            description: "继续后会新增另一条同名账号，不会覆盖现有账号。确认继续吗？",
            confirmLabel: "继续添加",
            cancelLabel: "取消",
          })
          if (!approved) return
          allowNameConflict = true
          continue
        }
        toast.error(error instanceof Error ? error.message : "添加主站账号失败")
        return
      } finally {
        setAdding(false)
      }
    }
  }

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="gap-0 overflow-hidden p-0 sm:max-w-6xl" aria-describedby="rate-ranking-dialog-description">
          {view === "list" ? (
            <>
              <DialogHeader className="border-b border-border px-4 py-4 pr-12 sm:px-6">
                <div className="flex flex-wrap items-center gap-2">
                  <DialogTitle>倍率排行榜</DialogTitle>
                  <Badge variant="outline" className="tabular-nums">{visibleRates.length} 个分组</Badge>
                </div>
                <DialogDescription id="rate-ranking-dialog-description">
                  按换算后倍率从低到高排列 · 上次采集 <span title={dateTime(refreshedAt)}>{relativeTime(refreshedAt)}</span>
                </DialogDescription>
              </DialogHeader>
              <div className="border-b border-border px-4 py-2 sm:px-6">
                <Tabs value={provider} onValueChange={changeProvider}>
                  <TabsList className="grid h-auto w-full grid-cols-3 gap-1 sm:flex sm:w-auto">
                    {RATE_PROVIDERS.map((item) => <TabsTrigger key={item.value} value={item.value} className="min-w-0 px-2">{item.label}</TabsTrigger>)}
                  </TabsList>
                </Tabs>
              </div>
              <div className="border-b border-border px-4 py-2 sm:px-6">
                <Tabs value={activeCategory} onValueChange={onCategoryChange}>
                  <TabsList className="h-auto max-w-full justify-start">
                    {categoryOptions.map((item) => (
                      <TabsTrigger key={item.value} value={item.value} className="gap-1.5 px-2.5">
                        {item.label}<span className="text-[10px] tabular-nums text-muted-foreground">{item.count}</span>
                      </TabsTrigger>
                    ))}
                  </TabsList>
                </Tabs>
              </div>
              <ScrollArea className="h-[min(70dvh,640px)]">
                {visibleRates.length === 0 ? (
                  <div className="px-6 py-16 text-center text-sm text-muted-foreground">该类型暂无已采集分组</div>
                ) : (
                  <RankingList rates={visibleRates} channelMap={channelMap} provider={provider} onTest={openTest} />
                )}
              </ScrollArea>
            </>
          ) : selectedRate ? (
            <>
              <DialogHeader className="border-b border-border px-4 py-4 pr-12 sm:px-6">
                <div className="flex items-center gap-2">
                  <Button variant="ghost" size="sm" className="-ml-2" onClick={() => setView("list")}><ArrowLeft />返回排行</Button>
                  <DialogTitle>快速测试</DialogTitle>
                </div>
                <DialogDescription id="rate-ranking-dialog-description">{channelMap.get(selectedRate.channel_id)?.name ?? `渠道 #${selectedRate.channel_id}`} · {selectedRate.model_name}</DialogDescription>
              </DialogHeader>
              <ScrollArea className="h-[min(72dvh,660px)]">
                <div className="space-y-5 px-4 py-5 sm:px-6">
                  <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-end">
                    <div className="space-y-2">
                      <Label htmlFor="rate-quick-test-model">测试模型</Label>
                      {provider === "image" ? (
                        <>
                          <Input
                            id="rate-quick-test-model"
                            list="rate-image-test-models"
                            value={model}
                            onChange={(event) => setModel(event.target.value)}
                            disabled={testing}
                            placeholder={metadataLoading ? "加载模型中" : "例如 gpt-image-1"}
                          />
                          <datalist id="rate-image-test-models">
                            {providerModels.map((item) => <option key={item} value={item} />)}
                          </datalist>
                        </>
                      ) : (
                        <Select value={model} onValueChange={setModel} disabled={testing || providerModels.length === 0}>
                          <SelectTrigger id="rate-quick-test-model" className="w-full"><SelectValue placeholder={metadataLoading ? "加载模型中" : "选择测试模型"} /></SelectTrigger>
                          <SelectContent>
                            {providerModels.map((item) => <SelectItem key={item} value={item}>{item}</SelectItem>)}
                          </SelectContent>
                        </Select>
                      )}
                      {metadataError ? <p className="text-xs text-amber-700 dark:text-amber-300">{metadataError}</p> : null}
                    </div>
                    <Button onClick={() => void runTest()} disabled={testing || !model.trim() || quickTestUnavailableReason(selectedRate, channelMap.get(selectedRate.channel_id), provider) !== ""}>
                      {testing ? <Loader2 className="animate-spin" /> : <TestTubeDiagonal />}{testing ? "测试中" : "开始测试"}
                    </Button>
                  </div>

                  {testing ? <TestingProgress image={provider === "image"} /> : null}
                  {result ? <TestResult result={result} /> : null}

                  {result?.usable ? (
                    <div className="space-y-3 border-t border-border pt-5">
                      <div>
                        <h3 className="text-sm font-semibold">添加到主站</h3>
                        <p className="mt-1 text-xs text-muted-foreground">将使用正式 Key 新建托管账号，临时测试 Key 不会复用。</p>
                      </div>
                      {addedGroupName ? (
                        <div className="flex items-center gap-2 text-sm text-emerald-700 dark:text-emerald-300"><CheckCircle2 className="size-4" />已添加到主站分组「{addedGroupName}」</div>
                      ) : availableWorkspaces.length === 0 ? (
                        <p className="text-sm text-muted-foreground">当前没有同类型的可用主站分组，或该上游分组已经全部接入。</p>
                      ) : (
                        <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-end">
                          <div className="space-y-2">
                            <Label htmlFor="rate-main-station-group">主站分组</Label>
                            <Select value={workspaceID} onValueChange={setWorkspaceID} disabled={adding}>
                              <SelectTrigger id="rate-main-station-group" className="w-full"><SelectValue placeholder="选择主站分组" /></SelectTrigger>
                              <SelectContent>
                                {availableWorkspaces.map((workspace) => (
                                  <SelectItem key={workspace.group.id} value={String(workspace.group.id)}>
                                    {workspace.group.name} · 同类型
                                  </SelectItem>
                                ))}
                              </SelectContent>
                            </Select>
                          </div>
                          <Button onClick={() => void addToMainStation()} disabled={adding || !workspaceID}>
                            {adding ? <Loader2 className="animate-spin" /> : <Plus />}{adding ? "添加中" : "添加到主站"}
                          </Button>
                        </div>
                      )}
                    </div>
                  ) : null}
                </div>
              </ScrollArea>
            </>
          ) : null}
        </DialogContent>
      </Dialog>
      {confirmDialog}
    </>
  )
}

function RankingList({ rates, channelMap, provider, onTest }: { rates: RateSnapshot[]; channelMap: Map<number, Channel>; provider: RateProviderType; onTest: (rate: RateSnapshot) => void }) {
  return (
    <>
      <div className="divide-y divide-border lg:hidden">
        {rates.map((rate, index) => {
          const channel = channelMap.get(rate.channel_id)
          const reason = quickTestUnavailableReason(rate, channel, provider)
          return (
            <div key={`${rate.channel_id}-${rate.id}`} className="grid grid-cols-[2rem_minmax(0,1fr)_auto] items-center gap-2 px-4 py-3">
              <Rank value={index + 1} />
              <div className="min-w-0">
                <div className="truncate text-xs text-muted-foreground">{channel?.name ?? `渠道 #${rate.channel_id}`}</div>
                <div className="truncate text-sm font-medium" title={rate.model_name}>{rate.model_name}</div>
                <RankingCategoryBadge rate={rate} />
                <Connection rate={rate} compact />
              </div>
              <div className="flex flex-col items-end gap-2">
                <span className="text-sm font-semibold tabular-nums">{formatRatio(rate.ratio)}</span>
                <Button variant="outline" size="sm" disabled={!!reason} title={reason} onClick={() => onTest(rate)}><TestTubeDiagonal />快速测试</Button>
              </div>
            </div>
          )
        })}
      </div>
      <div className="hidden lg:block">
        <table className="w-full table-fixed text-sm">
          <thead className="sticky top-0 z-10 bg-background text-left text-xs text-muted-foreground">
            <tr className="border-b border-border">
              <th className="w-16 px-4 py-2 font-medium">排名</th>
              <th className="w-40 px-3 py-2 font-medium">渠道</th>
              <th className="px-3 py-2 font-medium">分组</th>
              <th className="w-52 px-3 py-2 font-medium">主站接入</th>
              <th className="w-28 px-3 py-2 text-right font-medium">倍率</th>
              <th className="w-32 px-4 py-2 text-right font-medium">操作</th>
            </tr>
          </thead>
          <tbody>
            {rates.map((rate, index) => {
              const channel = channelMap.get(rate.channel_id)
              const reason = quickTestUnavailableReason(rate, channel, provider)
              return (
                <tr key={`${rate.channel_id}-${rate.id}`} className="border-b border-border last:border-b-0 hover:bg-muted/30">
                  <td className="px-4 py-2.5 text-center"><Rank value={index + 1} /></td>
                  <td className="px-3 py-2.5 font-medium">{channel?.name ?? `渠道 #${rate.channel_id}`}</td>
                  <td className="max-w-0 px-3 py-2.5">
                    <div className="truncate font-medium" title={rate.model_name}>{rate.model_name}</div>
                    <div className="mt-1 flex min-w-0 items-center gap-2">
                      <RankingCategoryBadge rate={rate} />
                      {rate.description ? <div className="min-w-0 truncate text-xs text-muted-foreground" title={rate.description}>{rate.description}</div> : null}
                    </div>
                  </td>
                  <td className="px-3 py-2.5"><Connection rate={rate} /></td>
                  <td className="px-3 py-2.5 text-right font-semibold tabular-nums">{formatRatio(rate.ratio)}</td>
                  <td className="px-4 py-2.5 text-right"><Button variant="outline" size="sm" disabled={!!reason} title={reason} onClick={() => onTest(rate)}><TestTubeDiagonal />快速测试</Button></td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </>
  )
}

function RankingCategoryBadge({ rate }: { rate: RateSnapshot }) {
  return (
    <Badge variant="outline" className="mt-1 h-5 max-w-full truncate border-border bg-muted/40 px-1.5 text-[10px] font-normal">
      {rate.ranking_category}
    </Badge>
  )
}

function TestingProgress({ image }: { image: boolean }) {
  return (
    <div className="flex items-start gap-3 border-y border-border py-4">
      <Loader2 className="mt-0.5 size-5 animate-spin text-primary" />
      <div>
        <p className="text-sm font-medium">正在测试上游分组</p>
        <p className="mt-1 text-xs text-muted-foreground">创建临时 Key、{image ? "发送 1 次生图请求" : "连续发送 3 次最小请求"}并清理 Key，请勿关闭窗口。</p>
      </div>
    </div>
  )
}

function TestResult({ result }: { result: RateQuickTestResult }) {
  const status = result.usable
    ? { label: "可用", icon: CheckCircle2, cls: "text-emerald-700 dark:text-emerald-300" }
    : result.success_count > 0
      ? { label: "连接不稳定", icon: TriangleAlert, cls: "text-amber-700 dark:text-amber-300" }
    : result.reachable
      ? { label: "可连接但不可用", icon: TriangleAlert, cls: "text-amber-700 dark:text-amber-300" }
      : { label: "不可连接", icon: XCircle, cls: "text-destructive" }
  const StatusIcon = status.icon
  return (
    <div className="space-y-4 border-y border-border py-4">
      <div className={cn("flex items-start gap-3", status.cls)}>
        <StatusIcon className="mt-0.5 size-5" />
        <div>
          <div className="font-semibold">{status.label}</div>
          <p className="mt-1 text-sm text-foreground">{result.message}</p>
        </div>
      </div>
      <div className="grid grid-cols-2 gap-x-6 gap-y-3 text-sm sm:grid-cols-4">
        <Metric icon={Gauge} label="平均延迟" value={`${result.latency_ms} ms`} />
        <Metric icon={Clock3} label="平均首字节" value={`${result.ttfb_ms} ms`} />
        <Metric icon={Server} label="成功次数" value={`${result.success_count}/${result.attempt_count}`} />
        <Metric icon={TestTubeDiagonal} label="测试模型" value={result.model} />
      </div>
      {result.image_url ? (
        <div className="overflow-hidden rounded-md border border-border bg-muted/30">
          <img src={result.image_url} alt="快速测试生成结果" className="mx-auto max-h-96 w-auto max-w-full object-contain" />
        </div>
      ) : null}
      <div className="divide-y divide-border rounded-md border border-border">
        {result.attempts.map((attempt) => (
          <div key={attempt.attempt} className="grid grid-cols-[auto_minmax(0,1fr)] items-center gap-x-3 gap-y-1 px-3 py-2 text-xs sm:grid-cols-[auto_minmax(0,1fr)_auto]">
            <Badge variant="outline" className={attempt.usable ? "text-emerald-700 dark:text-emerald-300" : "text-amber-700 dark:text-amber-300"}>
              第 {attempt.attempt} 次
            </Badge>
            <span className="truncate text-muted-foreground" title={attempt.message}>{attempt.usable ? "成功" : attempt.message}</span>
            <span className="col-span-2 whitespace-nowrap text-muted-foreground tabular-nums sm:col-span-1 sm:text-right">
              延迟 {attempt.latency_ms} ms · TTFB {attempt.ttfb_ms} ms · HTTP {attempt.http_status || "—"}
            </span>
          </div>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
        <span>临时 Key：{result.temporary_key_name}</span>
        <Badge variant="outline" className={result.temporary_key_status === "deleted" ? "text-emerald-700 dark:text-emerald-300" : "text-amber-700 dark:text-amber-300"}>
          {result.temporary_key_status === "deleted" ? "已删除" : "等待后台清理"}
        </Badge>
        {result.cleanup_error ? <span className="text-amber-700 dark:text-amber-300">{result.cleanup_error}</span> : null}
      </div>
    </div>
  )
}

function Metric({ icon: Icon, label, value }: { icon: typeof Gauge; label: string; value: string }) {
  return (
    <div className="min-w-0">
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground"><Icon className="size-3.5" />{label}</div>
      <div className="mt-1 truncate font-medium tabular-nums" title={value}>{value}</div>
    </div>
  )
}

function Rank({ value }: { value: number }) {
  return <span className={cn("inline-flex size-6 items-center justify-center rounded-md bg-muted text-xs tabular-nums", value === 1 && "bg-foreground text-background")}>{value}</span>
}

function Connection({ rate, compact = false }: { rate: RateSnapshot; compact?: boolean }) {
  if (!rate.main_station_connected) {
    return <span className={cn("inline-flex items-center gap-1 text-xs text-muted-foreground", compact && "mt-1")}><Unlink className="size-3" />未接入主站</span>
  }
  const names = rate.main_station_groups.map((group) => group.group_name).join("、")
  return (
    <div className={cn("flex min-w-0 items-center gap-2", compact && "mt-1 flex-wrap gap-1.5")}>
      <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300"><Link2 />已接入</Badge>
      <span className="truncate text-xs text-muted-foreground" title={names}>{names || "主站分组待同步"}</span>
    </div>
  )
}

export function quickTestUnavailableReason(rate: RateSnapshot, channel: Channel | undefined, provider: RateProviderType) {
  if (!channel) return "渠道信息尚未加载"
  if (provider === "other") return "当前分组类型暂不支持快速测试"
  if (channel.type === "sub2api" && rate.remote_group_id == null) return "缺少远端分组 ID，请先同步倍率"
  return ""
}

function isManagedAccountNameConflict(error: unknown) {
  const body = (error as ApiError | undefined)?.body as { code?: string } | undefined
  return body?.code === "managed_account_name_conflict"
}
