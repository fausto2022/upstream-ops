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
import { Switch } from "@/components/ui/switch"
import { apiFetch } from "@/lib/api"
import type {
  MainStationAccount,
  MainStationGroupWorkspace,
  MainStationMember,
} from "@/lib/api-types"

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspace: MainStationGroupWorkspace | null
  account: MainStationAccount | null
  onSaved: (member: MainStationMember) => void
}

export function AccountSettingsDialog({ open, onOpenChange, workspace, account, onSaved }: Props) {
  const [concurrency, setConcurrency] = useState(1)
  const [priority, setPriority] = useState(1)
  const [preferred, setPreferred] = useState(false)
  const [healthEnabled, setHealthEnabled] = useState(true)
  const [healthModel, setHealthModel] = useState("")
  const [enabled, setEnabled] = useState(true)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!open || !account?.member) return
    setConcurrency(account.member.concurrency)
    setPriority(account.member.priority > 0 ? account.member.priority : 1)
    setPreferred(account.member.preferred)
    setHealthEnabled(account.member.health_enabled)
    setHealthModel(account.member.health_model ?? "")
    setEnabled(account.member.enabled)
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
          health_api_mode: "openai_chat",
        }),
      })
      onSaved(saved)
      onOpenChange(false)
      toast.success("账号设置已保存并应用")
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "保存账号设置失败")
    } finally {
      setBusy(false)
    }
  }

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
            <Label htmlFor="edit-account-priority">优先级</Label>
            <Input id="edit-account-priority" type="number" min={1} step={1} value={priority} onChange={(event) => setPriority(Number(event.target.value))} />
          </div>
          <div className="space-y-2 sm:col-span-2">
            <Label htmlFor="edit-account-health-model">完整检测模型（可选）</Label>
            <Input id="edit-account-health-model" value={healthModel} onChange={(event) => setHealthModel(event.target.value)} placeholder="留空时只执行快速检测" />
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
