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
  ChannelAPIKeyGroup,
  ChannelAPIKeyPage,
  MainStationAccount,
  MainStationMember,
  MainStationPool,
} from "@/lib/api-types"
import { cn } from "@/lib/utils"

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  pool: MainStationPool | null
  channels: Channel[]
  accounts: MainStationAccount[]
  onSaved: (member: MainStationMember) => void
}

export function MemberDialog({
  open,
  onOpenChange,
  pool,
  channels,
  accounts,
  onSaved,
}: Props) {
  const [mode, setMode] = useState<"managed" | "bound">("managed")
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
  const [concurrency, setConcurrency] = useState(10)
  const [loadingSource, setLoadingSource] = useState(false)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!open) return
    setMode("managed")
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
    setConcurrency(10)
  }, [channels, open])

  useEffect(() => {
    if (!open || channelID === 0) return
    setLoadingSource(true)
    Promise.all([
      apiFetch<ChannelAPIKeyGroup[]>(`/channels/${channelID}/api-keys/groups`),
      apiFetch<ChannelAPIKeyPage>(`/channels/${channelID}/api-keys?page=1&page_size=100`),
    ])
      .then(([groups, keys]) => {
        setSourceGroups(groups)
        setSourceKeys(keys.items)
      })
      .catch((error: unknown) => {
        setSourceGroups([])
        setSourceKeys([])
        toast.error(error instanceof Error ? error.message : "加载上游分组失败")
      })
      .finally(() => setLoadingSource(false))
  }, [channelID, open])

  async function handleSave() {
    if (!pool || channelID === 0) {
      toast.error("请选择上游渠道")
      return
    }
    if (mode === "bound" && remoteAccountID === 0) {
      toast.error("请选择未绑定的主站 Account")
      return
    }
    if (mode === "bound" && !manualConfirmed) {
      toast.error("绑定已有 Account 必须人工确认映射")
      return
    }
    const selectedGroup = sourceGroups.find((group) => groupValue(group) === sourceGroupValue)
    setBusy(true)
    try {
      const member = await apiFetch<MainStationMember>(`/main-station/pools/${pool.id}/members`, {
        method: "POST",
        body: JSON.stringify({
          ownership_mode: mode,
          source_channel_id: channelID,
          source_group_id: selectedGroup?.id ?? undefined,
          source_group_name: selectedGroup?.name ?? "",
          source_api_key_id:
            mode === "managed" && sourceKeyValue !== "new" ? Number(sourceKeyValue) : undefined,
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
      toast.success(mode === "managed" ? "托管成员已创建" : "已有 Account 已绑定")
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "添加成员失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>添加池成员</DialogTitle>
          <DialogDescription>{pool?.name}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="grid grid-cols-2 rounded-md border p-1">
            <Button
              type="button"
              variant={mode === "managed" ? "secondary" : "ghost"}
              className="rounded-sm"
              onClick={() => setMode("managed")}
            >
              <Plus className="size-4" />
              创建托管 Account
            </Button>
            <Button
              type="button"
              variant={mode === "bound" ? "secondary" : "ghost"}
              className="rounded-sm"
              onClick={() => setMode("bound")}
            >
              <Link2 className="size-4" />
              绑定已有 Account
            </Button>
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label>上游渠道</Label>
              <Select value={channelID ? String(channelID) : ""} onValueChange={(value) => setChannelID(Number(value))}>
                <SelectTrigger><SelectValue placeholder="选择渠道" /></SelectTrigger>
                <SelectContent>
                  {channels.map((channel) => (
                    <SelectItem key={channel.id} value={String(channel.id)}>{channel.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label>上游分组</Label>
              <Select value={sourceGroupValue} onValueChange={setSourceGroupValue} disabled={loadingSource}>
                <SelectTrigger><SelectValue placeholder={loadingSource ? "加载中" : "选择分组"} /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">不指定分组</SelectItem>
                  {sourceGroups.map((group) => (
                    <SelectItem key={groupValue(group)} value={groupValue(group)}>
                      {group.name} · {group.ratio}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2 sm:col-span-2">
              <Label>源 API Key</Label>
              <Select value={sourceKeyValue} onValueChange={setSourceKeyValue} disabled={loadingSource}>
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  {mode === "managed" ? <SelectItem value="new">创建独立 API Key</SelectItem> : null}
                  {sourceKeys.map((key) => (
                    <SelectItem key={key.id} value={String(key.id)}>{key.name || `Key #${key.id}`}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {mode === "bound" && sourceKeys.length === 0 ? (
                <p className="text-xs text-amber-700">未选择源 API Key 时无法执行 L0/L1 测活。</p>
              ) : null}
            </div>
            {mode === "bound" ? (
              <div className="space-y-2 sm:col-span-2">
                <Label>未绑定的主站 Account</Label>
                <Select
                  value={remoteAccountID ? String(remoteAccountID) : ""}
                  onValueChange={(value) => setRemoteAccountID(Number(value))}
                >
                  <SelectTrigger><SelectValue placeholder="选择 Account" /></SelectTrigger>
                  <SelectContent>
                    {accounts.map((account) => (
                      <SelectItem key={account.remote_account_id} value={String(account.remote_account_id)}>
                        {account.name} · #{account.remote_account_id}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <label className="flex items-start gap-2 rounded-md border p-3 text-sm">
                  <Checkbox
                    checked={manualConfirmed}
                    onCheckedChange={(checked) => setManualConfirmed(checked === true)}
                  />
                  <span>我已核对上游渠道、分组和主站 Account，确认建立人工映射。</span>
                </label>
              </div>
            ) : null}
            <div className="space-y-2">
              <Label htmlFor="member-concurrency">并发</Label>
              <Input id="member-concurrency" type="number" min={1} value={concurrency} onChange={(event) => setConcurrency(Number(event.target.value))} />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-2">
                <Label htmlFor="member-priority">优先级</Label>
                <Input id="member-priority" type="number" min={1} value={priority} onChange={(event) => setPriority(Number(event.target.value))} />
              </div>
              <div className="space-y-2">
                <Label htmlFor="member-weight">权重</Label>
                <Input id="member-weight" type="number" min={1} value={weight} onChange={(event) => setWeight(Number(event.target.value))} />
              </div>
            </div>
            <div className="space-y-2 sm:col-span-2">
              <Label htmlFor="member-health-model">低成本测试模型</Label>
              <Input
                id="member-health-model"
                value={healthModel}
                onChange={(event) => setHealthModel(event.target.value)}
                placeholder="明确指定模型，不自动取第一项"
              />
            </div>
          </div>

          <div className="grid gap-3 border-t pt-4 sm:grid-cols-2">
            <ToggleLine id="member-enabled" label="启用成员" checked={enabled} onCheckedChange={setEnabled} />
            <ToggleLine id="member-health" label="启用测活" checked={healthEnabled} onCheckedChange={setHealthEnabled} />
          </div>
        </div>

        <DialogFooter>
          <Button onClick={handleSave} disabled={busy || !pool}>
            <Save className="size-4" />
            {busy ? "处理中" : "保存成员"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ToggleLine({
  id,
  label,
  checked,
  onCheckedChange,
}: {
  id: string
  label: string
  checked: boolean
  onCheckedChange: (checked: boolean) => void
}) {
  return (
    <div className={cn("flex items-center justify-between gap-3 rounded-md border px-3 py-2")}>
      <Label htmlFor={id}>{label}</Label>
      <Switch id={id} checked={checked} onCheckedChange={onCheckedChange} />
    </div>
  )
}

function groupValue(group: ChannelAPIKeyGroup) {
  return group.id != null ? `id:${group.id}` : `name:${group.name}`
}
