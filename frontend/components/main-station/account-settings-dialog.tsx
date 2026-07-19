import { useEffect, useState } from "react"
import { Save } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
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
  MainStationAccount,
  MainStationConfig,
  MainStationGroupWorkspace,
  MainStationHealthModelCatalog,
  MainStationMember,
} from "@/lib/api-types"

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspace: MainStationGroupWorkspace | null
  account: MainStationAccount | null
  config: MainStationConfig | null
  onSaved: (member: MainStationMember) => void
}

export function AccountSettingsDialog({ open, onOpenChange, workspace, account, config, onSaved }: Props) {
  const [concurrency, setConcurrency] = useState(1)
  const [priority, setPriority] = useState(1)
  const [preferred, setPreferred] = useState(false)
  const [healthEnabled, setHealthEnabled] = useState(true)
  const [healthModel, setHealthModel] = useState("")
  const [healthInterval, setHealthInterval] = useState("")
  const [healthFailureThreshold, setHealthFailureThreshold] = useState("")
  const [healthRecoveryThreshold, setHealthRecoveryThreshold] = useState("")
  const [modelCatalogs, setModelCatalogs] = useState<MainStationHealthModelCatalog[]>([])
  const [enabled, setEnabled] = useState(true)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!open || !account?.member) return
    setConcurrency(account.member.concurrency)
    setPriority(account.member.priority > 0 ? account.member.priority : 1)
    setPreferred(account.member.preferred)
    setHealthEnabled(account.member.health_enabled)
    setHealthModel(account.member.health_model ?? "")
    setHealthInterval(account.member.health_interval_seconds > 0 ? String(account.member.health_interval_seconds) : "")
    setHealthFailureThreshold(account.member.health_failure_threshold > 0 ? String(account.member.health_failure_threshold) : "")
    setHealthRecoveryThreshold(account.member.health_recovery_threshold > 0 ? String(account.member.health_recovery_threshold) : "")
    setEnabled(account.member.enabled)
    setModelCatalogs([])
    void apiFetch<MainStationHealthModelCatalog[]>("/main-station/health-models")
      .then(setModelCatalogs)
      .catch((error: unknown) => {
        setModelCatalogs([])
        toast.error(error instanceof Error ? error.message : "获取模型列表失败")
      })
  }, [account, open])

  async function handleSave() {
    const member = account?.member
    if (!workspace || !account || !member) return
    if (concurrency <= 0) {
      toast.error("并发必须大于 0")
      return
    }
    if (priority <= 0) {
      toast.error("优先级必须大于 0")
      return
    }
    const healthIntervalSeconds = healthInterval === "" ? 0 : Number(healthInterval)
    if (healthIntervalSeconds !== 0 && (healthIntervalSeconds < 1 || healthIntervalSeconds > 86400)) {
      toast.error("账号探活间隔必须在 1 到 86400 秒之间")
      return
    }
    const failureThreshold = healthFailureThreshold === "" ? 0 : Number(healthFailureThreshold)
    const recoveryThreshold = healthRecoveryThreshold === "" ? 0 : Number(healthRecoveryThreshold)
    if ((failureThreshold !== 0 && (failureThreshold < 1 || failureThreshold > 100)) ||
      (recoveryThreshold !== 0 && (recoveryThreshold < 1 || recoveryThreshold > 100))) {
      toast.error("账号失败和恢复次数必须在 1 到 100 之间")
      return
    }
    const priorityChanged = priority !== member.priority
    setBusy(true)
    try {
      const saved = await apiFetch<MainStationMember>(`/main-station/groups/${workspace.group.id}/accounts/${member.id}`, {
        method: "PUT",
        body: JSON.stringify({
          account_name: member.account_name ?? account.name,
          source_channel_id: member.source_channel_id,
          source_group_id: member.source_group_id ?? null,
          source_group_name: member.source_group_name ?? "",
          enabled,
          preferred,
          priority,
          concurrency,
          health_enabled: healthEnabled,
          health_model: healthModel.trim(),
          health_interval_seconds: healthIntervalSeconds,
          health_failure_threshold: failureThreshold,
          health_recovery_threshold: recoveryThreshold,
          health_api_mode: "openai_chat",
        }),
      })
      onSaved(saved)
      onOpenChange(false)
      if (saved.scheduling_dirty_at) toast.warning("账号设置已保存，启停状态将在后台自动重试同步")
      else if (priorityChanged) toast.success("基础优先级已保存，实际优先级将在下次重排后更新")
      else toast.success("账号设置已保存并应用")
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "保存账号设置失败")
    } finally {
      setBusy(false)
    }
  }

  const platform = healthPlatformKey(account?.platform)
  const globalModel = config?.health_models?.[platform] ?? ""
  const catalog = modelCatalogs.find((item) => item.platform === platform)
  const modelOptions = Array.from(new Set([...(catalog?.models ?? []), healthModel].filter(Boolean)))
  const actualPriority = account?.priority

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>编辑账号</DialogTitle>
          <DialogDescription>{account ? `${account.name} · #${account.remote_account_id}` : "账号设置"}</DialogDescription>
        </DialogHeader>

        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="edit-account-concurrency">并发</Label>
            <Input id="edit-account-concurrency" type="number" min={1} value={concurrency} onChange={(event) => setConcurrency(Number(event.target.value))} />
          </div>
          <div className="space-y-2">
            <Label>当前实际优先级</Label>
            <div className="flex h-9 items-center rounded-md border border-input bg-muted/30 px-3 text-sm font-medium tabular-nums">
              {actualPriority != null && actualPriority > 0 ? actualPriority : "-"}
            </div>
            <p className="text-xs text-muted-foreground">由最近一次自动重排生成</p>
          </div>
          <div className="space-y-2 sm:col-span-2">
            <Label htmlFor="edit-account-priority">基础优先级</Label>
            <Input id="edit-account-priority" type="number" min={1} step={1} value={priority} onChange={(event) => setPriority(Number(event.target.value))} />
            <p className="text-xs text-muted-foreground">保存的是重排基准值；实际优先级会按标签、健康、延迟和成本自动调整。</p>
          </div>
          <div className="space-y-2 sm:col-span-2">
            <Label>账号探活模型</Label>
            <Select value={healthModel || "__inherit__"} onValueChange={(value) => setHealthModel(value === "__inherit__" ? "" : value)}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="__inherit__">继承全局{globalModel ? `（${globalModel}）` : "（仅快速检测）"}</SelectItem>
                {modelOptions.map((model) => <SelectItem key={model} value={model}>{model}</SelectItem>)}
              </SelectContent>
            </Select>
            {catalog?.error ? <p className="text-xs text-destructive">{catalog.error}</p> : null}
          </div>
          <div className="space-y-2 sm:col-span-2">
            <Label htmlFor="edit-account-health-interval">账号探活间隔（秒）</Label>
            <Input
              id="edit-account-health-interval"
              type="number"
              min={1}
              max={86400}
              step={1}
              value={healthInterval}
              onChange={(event) => setHealthInterval(event.target.value)}
              placeholder={`留空继承全局（${config?.health_interval_seconds ?? 30} 秒）`}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="edit-account-health-failures">连续失败停用</Label>
            <Input
              id="edit-account-health-failures"
              type="number"
              min={1}
              max={100}
              step={1}
              value={healthFailureThreshold}
              onChange={(event) => setHealthFailureThreshold(event.target.value)}
              placeholder={`继承全局（${config?.health_failure_threshold ?? 10} 次）`}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="edit-account-health-recovery">连续成功恢复</Label>
            <Input
              id="edit-account-health-recovery"
              type="number"
              min={1}
              max={100}
              step={1}
              value={healthRecoveryThreshold}
              onChange={(event) => setHealthRecoveryThreshold(event.target.value)}
              placeholder={`继承全局（${config?.health_recovery_threshold ?? 3} 次）`}
            />
          </div>
          <div className="flex items-center justify-between gap-4 border-t pt-4 sm:col-span-2">
            <Label htmlFor="edit-account-preferred">优先调度</Label>
            <Switch id="edit-account-preferred" checked={preferred} onCheckedChange={setPreferred} />
          </div>
          <div className="flex items-center justify-between gap-4 border-t pt-4 sm:col-span-2">
            <Label htmlFor="edit-account-health-enabled">启用健康检测</Label>
            <Switch id="edit-account-health-enabled" checked={healthEnabled} onCheckedChange={setHealthEnabled} />
          </div>
          <div className="flex items-center justify-between gap-4 border-t pt-4 sm:col-span-2">
            <Label htmlFor="edit-account-enabled">启用账号</Label>
            <Switch id="edit-account-enabled" checked={enabled} onCheckedChange={setEnabled} />
          </div>
        </div>

        <DialogFooter>
          <Button onClick={handleSave} disabled={busy || !account?.member || !workspace}>
            <Save className="size-4" />{busy ? "保存中" : "保存设置"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function healthPlatformKey(platform?: string) {
  const normalized = (platform ?? "").trim().toLowerCase()
  if (normalized === "claude") return "anthropic"
  if (normalized === "google") return "gemini"
  if (normalized === "xai") return "grok"
  return normalized
}
