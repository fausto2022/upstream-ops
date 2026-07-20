import { useEffect, useRef, useState } from "react"
import { Link2, Plus, Save } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { useConfirm } from "@/components/ui/confirm-dialog"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { apiFetch, type ApiError } from "@/lib/api"
import { formatRatio } from "@/lib/format"
import type {
  Channel,
  ChannelAccountLimits,
  ChannelAPIKeyGroup,
  ChannelAPIKeyPage,
  MainStationAccount,
  MainStationGroupWorkspace,
  MainStationMember,
} from "@/lib/api-types"

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspace: MainStationGroupWorkspace | null
  channels: Channel[]
  accounts: MainStationAccount[]
  initialAccount?: MainStationAccount | null
  onSaved: (member: MainStationMember) => void
}

export function MemberDialog({ open, onOpenChange, workspace, channels, accounts, initialAccount, onSaved }: Props) {
  const { confirm, dialog: confirmDialog } = useConfirm()
  const [mode, setMode] = useState<"managed" | "bound">("managed")
  const [accountName, setAccountName] = useState("")
  const [channelID, setChannelID] = useState(0)
  const [sourceGroups, setSourceGroups] = useState<ChannelAPIKeyGroup[]>([])
  const [sourceGroupValue, setSourceGroupValue] = useState("none")
  const [sourceKeys, setSourceKeys] = useState<ChannelAPIKeyPage["items"]>([])
  const [sourceKeyValue, setSourceKeyValue] = useState("new")
  const [remoteAccountID, setRemoteAccountID] = useState(0)
  const [manualConfirmed, setManualConfirmed] = useState(false)
  const [bindingSuggested, setBindingSuggested] = useState(false)
  const [enabled, setEnabled] = useState(true)
  const [preferred, setPreferred] = useState(false)
  const [healthEnabled, setHealthEnabled] = useState(true)
  const [healthModel, setHealthModel] = useState("")
  const [priority, setPriority] = useState(1)
  const [concurrency, setConcurrency] = useState(0)
  const [concurrencyDetected, setConcurrencyDetected] = useState(false)
  const [concurrencyError, setConcurrencyError] = useState("")
  const [loadingSource, setLoadingSource] = useState(false)
  const [busy, setBusy] = useState(false)
  const sourceRequestRef = useRef(0)
  const selectedChannel = channels.find((channel) => channel.id === channelID)
  const selectedSourceGroup = sourceGroups.find((group) => groupValue(group) === sourceGroupValue)
  const usesGeneratedAPIKey = mode === "managed" && sourceKeyValue === "new"
  const generatedAccountName = automaticManagedName(selectedChannel?.name, selectedSourceGroup?.name)
  const defaultSourceRatio = applyChannelRechargeMultiplier(1, selectedChannel)

  useEffect(() => {
    if (!open) return
    setMode(initialAccount ? "bound" : "managed")
    setAccountName("")
    setChannelID(channels[0]?.id ?? 0)
    setSourceGroups([])
    setSourceGroupValue("none")
    setSourceKeys([])
    setSourceKeyValue(initialAccount ? "none" : "new")
    setRemoteAccountID(initialAccount?.remote_account_id ?? 0)
    setManualConfirmed(false)
    setBindingSuggested(false)
    setEnabled(true)
    setPreferred(false)
    setHealthEnabled(true)
    setHealthModel("")
    setPriority(1)
    setConcurrency(0)
    setConcurrencyDetected(false)
    setConcurrencyError("")
  }, [channels, initialAccount, open])

  useEffect(() => {
    const requestID = ++sourceRequestRef.current
    if (!open || channelID === 0) {
      setLoadingSource(false)
      return
    }
    setLoadingSource(true)
    setConcurrency(0)
    setConcurrencyDetected(false)
    setConcurrencyError("")
    Promise.all([
      apiFetch<ChannelAPIKeyGroup[]>(`/channels/${channelID}/api-keys/groups`),
      apiFetch<ChannelAPIKeyPage>(`/channels/${channelID}/api-keys?page=1&page_size=100`),
      apiFetch<ChannelAccountLimits>(`/channels/${channelID}/account-limits`)
        .then((limits) => ({ limits, error: "" }))
        .catch((error: unknown) => ({
          limits: null,
          error: error instanceof Error ? error.message : "无法读取上游并发",
        })),
    ])
      .then(([groups, keys, limitsResult]) => {
        if (requestID !== sourceRequestRef.current) return
        setSourceGroups(groups)
        setSourceKeys(keys.items)
        if (limitsResult.limits?.concurrency && limitsResult.limits.concurrency > 0) {
          setConcurrency(limitsResult.limits.concurrency)
          setConcurrencyDetected(true)
        } else {
          setConcurrencyError(limitsResult.error || "上游未返回最高并发")
        }
      })
      .catch((error: unknown) => {
        if (requestID !== sourceRequestRef.current) return
        setSourceGroups([])
        setSourceKeys([])
        toast.error(error instanceof Error ? error.message : "加载账号来源失败")
      })
      .finally(() => {
        if (requestID === sourceRequestRef.current) setLoadingSource(false)
      })
  }, [channelID, open])

  useEffect(() => {
    if (!open || mode !== "bound" || channelID === 0) return
    const channel = channels.find((item) => item.id === channelID)
    if (!channel) return
    if (initialAccount && !initialAccount.member) {
      setRemoteAccountID(initialAccount.remote_account_id)
      setBindingSuggested(false)
      return
    }
    const candidates = accounts.filter((account) => !account.member)
    const channelHost = safeHost(channel.site_url)
    const channelName = normalizeMatchText(channel.name)
    const ranked = candidates.map((account) => {
      let score = 0
      if (channelHost && safeHost(account.base_url) === channelHost) score += 100
      const accountName = normalizeMatchText(account.name)
      if (channelName && (accountName.includes(channelName) || channelName.includes(accountName))) score += 20
      return { account, score }
    }).filter((item) => item.score > 0).sort((left, right) => right.score - left.score || left.account.remote_account_id - right.account.remote_account_id)
    if (ranked.length > 0) {
      setRemoteAccountID(ranked[0].account.remote_account_id)
      setBindingSuggested(true)
    } else {
      setBindingSuggested(false)
    }
  }, [accounts, channelID, channels, initialAccount, mode, open])

  function changeMode(nextMode: "managed" | "bound") {
    setMode(nextMode)
    setSourceKeyValue(nextMode === "managed" ? "new" : "none")
  }

  async function handleSave() {
    if (!workspace || channelID === 0) {
      toast.error("请选择账号来源")
      return
    }
    const submittedAccountName = usesGeneratedAPIKey ? generatedAccountName : accountName.trim()
    if (mode === "managed" && !submittedAccountName) {
      toast.error("请输入账号名称")
      return
    }
    if (mode === "bound" && remoteAccountID === 0) {
      toast.error("请选择要接管的主站账号")
      return
    }
    if (mode === "bound" && !manualConfirmed) {
      toast.error("接管已有主站账号前必须确认映射")
      return
    }
    if (priority <= 0) {
      toast.error("优先级必须大于 0")
      return
    }
    let allowNameConflict = false
    while (true) {
      setBusy(true)
      try {
        const member = await apiFetch<MainStationMember>(`/main-station/groups/${workspace.group.id}/accounts`, {
          method: "POST",
          body: JSON.stringify({
            account_name: mode === "managed" ? submittedAccountName : "",
            ownership_mode: mode,
            source_channel_id: channelID,
            source_group_id: selectedSourceGroup?.id ?? undefined,
            source_group_name: selectedSourceGroup?.name ?? "",
            source_api_key_id: sourceKeyValue !== "new" && sourceKeyValue !== "none" ? Number(sourceKeyValue) : undefined,
            remote_account_id: mode === "bound" ? remoteAccountID : undefined,
            manual_binding_confirmed: mode === "bound" ? manualConfirmed : false,
            allow_name_conflict: allowNameConflict,
            enabled,
            preferred,
            priority,
            concurrency,
            rate_convert_mode: "raw",
            rate_convert_value: 1,
            cost_adjustment: 1,
            health_enabled: healthEnabled,
            health_model: healthModel.trim(),
            health_api_mode: "openai_chat",
          }),
        })
        setBusy(false)
        onSaved(member)
        onOpenChange(false)
        toast.success(allowNameConflict ? "同名账号已添加，原账号保持不变" : mode === "managed" ? "账号已添加" : "已有账号已接管")
        return
      } catch (error) {
        setBusy(false)
        if (!allowNameConflict && mode === "managed" && isManagedAccountNameConflict(error)) {
          const approved = await confirm({
            title: "主站已存在同名账号",
            description: "继续后会在当前分组新建另一条同名账号，不会覆盖或接管原账号。确认继续添加吗？",
            confirmLabel: "继续添加",
            cancelLabel: "返回修改",
          })
          if (!approved) return
          allowNameConflict = true
          continue
        }
        toast.error(error instanceof Error ? error.message : "保存账号失败")
        return
      }
    }
  }

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>添加账号</DialogTitle>
            <DialogDescription>{workspace?.group.name ?? "当前主站分组"}</DialogDescription>
          </DialogHeader>

          <div className="space-y-4">
            <div className="grid grid-cols-2 rounded-md border p-1">
              <Button type="button" variant={mode === "managed" ? "secondary" : "ghost"} className="rounded-sm" onClick={() => changeMode("managed")}>
                <Plus className="size-4" />新建账号
              </Button>
              <Button type="button" variant={mode === "bound" ? "secondary" : "ghost"} className="rounded-sm" onClick={() => changeMode("bound")}>
                <Link2 className="size-4" />接管已有账号
              </Button>
            </div>

            <div className="grid gap-4 sm:grid-cols-2">
              {mode === "managed" ? (
                <div className="space-y-2 sm:col-span-2">
                  <Label htmlFor="account-name">账号名称</Label>
                  <Input
                    id="account-name"
                    value={usesGeneratedAPIKey ? generatedAccountName : accountName}
                    onChange={(event) => setAccountName(event.target.value)}
                    placeholder="例如 OpenAI-01"
                    readOnly={usesGeneratedAPIKey}
                  />
                </div>
              ) : (
                <div className="space-y-2 sm:col-span-2">
                  <Label>主站账号</Label>
                  <Select value={remoteAccountID ? String(remoteAccountID) : ""} onValueChange={(value) => setRemoteAccountID(Number(value))}>
                    <SelectTrigger><SelectValue placeholder="选择未接管的账号" /></SelectTrigger>
                    <SelectContent>
                      {accounts.filter((account) => !account.member).map((account) => (
                        <SelectItem key={account.remote_account_id} value={String(account.remote_account_id)}>{account.name} · #{account.remote_account_id}</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  {bindingSuggested ? <p className="text-xs text-emerald-700">已根据上游地址和账号名称自动建议，请人工确认后保存</p> : null}
                </div>
              )}

              <div className="space-y-2">
                <Label>账号来源</Label>
                <Select value={channelID ? String(channelID) : ""} onValueChange={(value) => setChannelID(Number(value))}>
                  <SelectTrigger><SelectValue placeholder="选择来源" /></SelectTrigger>
                  <SelectContent>
                    {channels.map((channel) => <SelectItem key={channel.id} value={String(channel.id)}>{channel.name}</SelectItem>)}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label>来源套餐</Label>
                <Select value={sourceGroupValue} onValueChange={setSourceGroupValue} disabled={loadingSource}>
                  <SelectTrigger><SelectValue placeholder={loadingSource ? "加载中" : "选择套餐"} /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">默认套餐 · {formatRatio(defaultSourceRatio)}</SelectItem>
                    {sourceGroups.map((group) => <SelectItem key={groupValue(group)} value={groupValue(group)}>{group.name} · {formatRatio(group.ratio)}</SelectItem>)}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2 sm:col-span-2">
                <Label>来源 API Key</Label>
                <Select value={sourceKeyValue} onValueChange={setSourceKeyValue} disabled={loadingSource}>
                  <SelectTrigger><SelectValue /></SelectTrigger>
                  <SelectContent>
                    {mode === "managed" ? <SelectItem value="new">自动创建独立 API Key</SelectItem> : <SelectItem value="none">不指定</SelectItem>}
                    {sourceKeys.map((key) => <SelectItem key={key.id} value={String(key.id)}>{key.name || `密钥 #${key.id}`}</SelectItem>)}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label htmlFor="account-concurrency">上游最高并发</Label>
                <Input
                  id="account-concurrency"
                  type="number"
                  min={1}
                  value={concurrency || ""}
                  onChange={(event) => setConcurrency(Number(event.target.value))}
                  placeholder={loadingSource ? "正在读取" : "自动获取失败时手动填写"}
                  disabled={loadingSource}
                />
                <p className={concurrencyDetected ? "text-xs text-emerald-700" : "text-xs text-muted-foreground"}>
                  {concurrencyDetected ? "已从上游账号自动填入，可按实际情况修改" : concurrencyError || "选择账号来源后自动读取"}
                </p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="account-priority">基础优先级</Label>
                <Input id="account-priority" type="number" min={1} step={1} value={priority} onChange={(event) => setPriority(Number(event.target.value))} />
                <p className="text-xs text-muted-foreground">首次创建使用该值；后续定时重排会按标签、健康、延迟和成本调整实际优先级。</p>
              </div>
              <div className="space-y-2 sm:col-span-2">
                <Label htmlFor="account-health-model">账号探活模型（可选）</Label>
                <Input id="account-health-model" value={healthModel} onChange={(event) => setHealthModel(event.target.value)} placeholder="留空继承平台全局模型" />
              </div>
            </div>

            {mode === "bound" ? (
              <label className="flex items-start gap-2 border-t pt-4 text-sm">
                <Checkbox checked={manualConfirmed} onCheckedChange={(checked) => setManualConfirmed(checked === true)} />
                <span>我已核对账号来源与主站账号，确认建立接管关系。</span>
              </label>
            ) : null}
            <div className="flex items-center justify-between border-t pt-4">
              <Label htmlFor="account-preferred">优先调度</Label>
              <Switch id="account-preferred" checked={preferred} onCheckedChange={setPreferred} />
            </div>
            <div className="flex items-center justify-between border-t pt-4">
              <Label htmlFor="account-enabled">添加后启用</Label>
              <Switch id="account-enabled" checked={enabled} onCheckedChange={setEnabled} />
            </div>
          </div>

          <DialogFooter>
            <Button onClick={handleSave} disabled={busy || !workspace}>
              <Save className="size-4" />{busy ? "处理中" : "保存账号"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      {confirmDialog}
    </>
  )
}

function isManagedAccountNameConflict(error: unknown) {
  const body = (error as ApiError | undefined)?.body as { code?: string } | undefined
  return body?.code === "managed_account_name_conflict"
}

function groupValue(group: ChannelAPIKeyGroup) {
  return group.id != null ? `id:${group.id}` : `name:${group.name}`
}

function automaticManagedName(channelName?: string, groupName?: string) {
  const channel = channelName?.trim() ?? ""
  const group = groupName?.trim() || "默认分组"
  return `${channel}${channel ? "-" : ""}${group}`.trim().replace(/\s+/g, "-").slice(0, 120)
}

function applyChannelRechargeMultiplier(value: number, channel?: Channel) {
  const multiplier = channel?.recharge_multiplier
  if (multiplier == null || !Number.isFinite(multiplier) || multiplier <= 0) return value
  const adjusted = channel?.recharge_multiplier_mode === "multiply" ? value * multiplier : value / multiplier
  return Math.round(adjusted * 10_000) / 10_000
}

function safeHost(value?: string) {
  if (!value) return ""
  try { return new URL(value).host.toLowerCase() } catch { return "" }
}

function normalizeMatchText(value: string) {
  return value.toLowerCase().replace(/[^a-z0-9\u4e00-\u9fff]/g, "")
}
