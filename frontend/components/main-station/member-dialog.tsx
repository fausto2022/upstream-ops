import { useEffect, useState } from "react"
import { Link2, Plus, Save } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
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
import { apiFetch } from "@/lib/api"
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
  onSaved: (member: MainStationMember) => void
}

export function MemberDialog({ open, onOpenChange, workspace, channels, accounts, onSaved }: Props) {
  const [mode, setMode] = useState<"managed" | "bound">("managed")
  const [accountName, setAccountName] = useState("")
  const [channelID, setChannelID] = useState(0)
  const [sourceGroups, setSourceGroups] = useState<ChannelAPIKeyGroup[]>([])
  const [sourceGroupValue, setSourceGroupValue] = useState("none")
  const [sourceKeys, setSourceKeys] = useState<ChannelAPIKeyPage["items"]>([])
  const [sourceKeyValue, setSourceKeyValue] = useState("new")
  const [remoteAccountID, setRemoteAccountID] = useState(0)
  const [manualConfirmed, setManualConfirmed] = useState(false)
  const [enabled, setEnabled] = useState(true)
  const [healthEnabled, setHealthEnabled] = useState(true)
  const [healthModel, setHealthModel] = useState("")
  const [weight, setWeight] = useState(1)
  const [priority, setPriority] = useState(1)
  const [concurrency, setConcurrency] = useState(0)
  const [concurrencyDetected, setConcurrencyDetected] = useState(false)
  const [concurrencyError, setConcurrencyError] = useState("")
  const [loadingSource, setLoadingSource] = useState(false)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!open) return
    setMode("managed")
    setAccountName("")
    setChannelID(channels[0]?.id ?? 0)
    setSourceGroups([])
    setSourceGroupValue("none")
    setSourceKeys([])
    setSourceKeyValue("new")
    setRemoteAccountID(0)
    setManualConfirmed(false)
    setEnabled(true)
    setHealthEnabled(true)
    setHealthModel("")
    setWeight(1)
    setPriority(1)
    setConcurrency(0)
    setConcurrencyDetected(false)
    setConcurrencyError("")
  }, [channels, open])

  useEffect(() => {
    if (!open || channelID === 0) return
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
        setSourceGroups([])
        setSourceKeys([])
        toast.error(error instanceof Error ? error.message : "加载账号来源失败")
      })
      .finally(() => setLoadingSource(false))
  }, [channelID, open])

  function changeMode(nextMode: "managed" | "bound") {
    setMode(nextMode)
    setSourceKeyValue(nextMode === "managed" ? "new" : "none")
  }

  async function handleSave() {
    if (!workspace || channelID === 0) {
      toast.error("请选择账号来源")
      return
    }
    if (mode === "managed" && !accountName.trim()) {
      toast.error("请输入账号名称")
      return
    }
    if (mode === "bound" && remoteAccountID === 0) {
      toast.error("请选择要接管的主站 Account")
      return
    }
    if (mode === "bound" && !manualConfirmed) {
      toast.error("接管已有 Account 前必须确认映射")
      return
    }
    const selectedGroup = sourceGroups.find((group) => groupValue(group) === sourceGroupValue)
    setBusy(true)
    try {
      const member = await apiFetch<MainStationMember>(`/main-station/groups/${workspace.group.id}/accounts`, {
        method: "POST",
        body: JSON.stringify({
          account_name: mode === "managed" ? accountName.trim() : "",
          ownership_mode: mode,
          source_channel_id: channelID,
          source_group_id: selectedGroup?.id ?? undefined,
          source_group_name: selectedGroup?.name ?? "",
          source_api_key_id: sourceKeyValue !== "new" && sourceKeyValue !== "none" ? Number(sourceKeyValue) : undefined,
          remote_account_id: mode === "bound" ? remoteAccountID : undefined,
          manual_binding_confirmed: mode === "bound" ? manualConfirmed : false,
          enabled,
          weight,
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
      onSaved(member)
      onOpenChange(false)
      toast.success(mode === "managed" ? "账号已添加" : "已有账号已接管")
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "保存账号失败")
    } finally {
      setBusy(false)
    }
  }

  return (
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
                <Input id="account-name" value={accountName} onChange={(event) => setAccountName(event.target.value)} placeholder="例如 OpenAI-01" />
              </div>
            ) : (
              <div className="space-y-2 sm:col-span-2">
                <Label>主站 Account</Label>
                <Select value={remoteAccountID ? String(remoteAccountID) : ""} onValueChange={(value) => setRemoteAccountID(Number(value))}>
                  <SelectTrigger><SelectValue placeholder="选择未接管的账号" /></SelectTrigger>
                  <SelectContent>
                    {accounts.filter((account) => !account.member).map((account) => (
                      <SelectItem key={account.remote_account_id} value={String(account.remote_account_id)}>{account.name} · #{account.remote_account_id}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
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
                  <SelectItem value="none">默认套餐</SelectItem>
                  {sourceGroups.map((group) => <SelectItem key={groupValue(group)} value={groupValue(group)}>{group.name} · {group.ratio}</SelectItem>)}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2 sm:col-span-2">
              <Label>来源 API Key</Label>
              <Select value={sourceKeyValue} onValueChange={setSourceKeyValue} disabled={loadingSource}>
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  {mode === "managed" ? <SelectItem value="new">自动创建独立 API Key</SelectItem> : <SelectItem value="none">不指定</SelectItem>}
                  {sourceKeys.map((key) => <SelectItem key={key.id} value={String(key.id)}>{key.name || `Key #${key.id}`}</SelectItem>)}
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
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-2">
                <Label htmlFor="account-priority">优先级</Label>
                <Input id="account-priority" type="number" min={1} value={priority} onChange={(event) => setPriority(Number(event.target.value))} />
              </div>
              <div className="space-y-2">
                <Label htmlFor="account-weight">权重</Label>
                <Input id="account-weight" type="number" min={1} value={weight} onChange={(event) => setWeight(Number(event.target.value))} />
              </div>
            </div>
            <div className="space-y-2 sm:col-span-2">
              <Label htmlFor="account-health-model">完整检测模型（可选）</Label>
              <Input id="account-health-model" value={healthModel} onChange={(event) => setHealthModel(event.target.value)} placeholder="留空时只执行快速检测" />
            </div>
          </div>

          {mode === "bound" ? (
            <label className="flex items-start gap-2 border-t pt-4 text-sm">
              <Checkbox checked={manualConfirmed} onCheckedChange={(checked) => setManualConfirmed(checked === true)} />
              <span>我已核对账号来源与主站 Account，确认建立接管关系。</span>
            </label>
          ) : null}
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
  )
}

function groupValue(group: ChannelAPIKeyGroup) {
  return group.id != null ? `id:${group.id}` : `name:${group.name}`
}
