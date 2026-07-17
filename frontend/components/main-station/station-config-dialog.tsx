import { useEffect, useState } from "react"
import { PlugZap, Save } from "lucide-react"
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
  MainStationConfig,
  UpstreamSyncTarget,
} from "@/lib/api-types"

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  config: MainStationConfig | null
  onSaved: (config: MainStationConfig) => void
}

export function StationConfigDialog({
  open,
  onOpenChange,
  config,
  onSaved,
}: Props) {
  const [targetID, setTargetID] = useState(0)
  const [targets, setTargets] = useState<UpstreamSyncTarget[]>([])
  const [name, setName] = useState("")
  const [baseURL, setBaseURL] = useState("")
  const [adminAPIKey, setAdminAPIKey] = useState("")
  const [enabled, setEnabled] = useState(true)
  const [busy, setBusy] = useState<"test" | "save" | null>(null)

  useEffect(() => {
    if (!open) return
    setTargetID(config?.target_id ?? 0)
    setName(config?.name ?? "")
    setBaseURL(config?.base_url ?? "")
    setAdminAPIKey("")
    setEnabled(config?.enabled ?? true)
    if (!config?.configured && config?.migration?.status === "requires_confirmation") {
      void apiFetch<UpstreamSyncTarget[]>("/upstream-sync/targets")
        .then(setTargets)
        .catch((error: unknown) => {
          toast.error(error instanceof Error ? error.message : "加载候选目标失败")
        })
    } else {
      setTargets([])
    }
  }, [config, open])

  function selectTarget(value: string) {
    const id = Number(value)
    setTargetID(id)
    const target = targets.find((item) => item.id === id)
    if (target) {
      setName(target.name)
      setBaseURL(target.base_url)
      setEnabled(target.enabled)
    }
  }

  async function handleTest() {
    if (!baseURL.trim()) {
      toast.error("请填写主站 Base URL")
      return
    }
    if (!adminAPIKey.trim() && !config?.configured) {
      toast.error("请填写 Admin API Key")
      return
    }
    setBusy("test")
    try {
      await apiFetch("/main-station/test", {
        method: "POST",
        body: JSON.stringify({
          name,
          base_url: baseURL,
          admin_api_key: adminAPIKey,
        }),
      })
      toast.success("主站连接正常")
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "连接测试失败")
    } finally {
      setBusy(null)
    }
  }

  async function handleSave() {
    if (!name.trim() || !baseURL.trim()) {
      toast.error("主站名称和 Base URL 不能为空")
      return
    }
    if (!config?.configured && !adminAPIKey.trim()) {
      toast.error("首次配置必须填写 Admin API Key")
      return
    }
    setBusy("save")
    try {
      const saved = await apiFetch<MainStationConfig>("/main-station", {
        method: config?.configured ? "PUT" : "POST",
        body: JSON.stringify({
          target_id: targetID || undefined,
          name: name.trim(),
          base_url: baseURL.trim(),
          admin_api_key: adminAPIKey.trim(),
          enabled,
        }),
      })
      onSaved(saved)
      onOpenChange(false)
      toast.success(config?.configured ? "主站配置已更新" : "主站配置已创建")
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "保存主站配置失败")
    } finally {
      setBusy(null)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{config?.configured ? "编辑主站" : "配置主站"}</DialogTitle>
          <DialogDescription>
            Admin API Key 仅加密保存。编辑时留空会保留现有密钥。
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          {targets.length > 0 ? (
            <div className="space-y-2">
              <Label>已有目标站点</Label>
              <Select value={targetID ? String(targetID) : ""} onValueChange={selectTarget}>
                <SelectTrigger>
                  <SelectValue placeholder="选择要确认为主站的目标" />
                </SelectTrigger>
                <SelectContent>
                  {targets.map((target) => (
                    <SelectItem key={target.id} value={String(target.id)}>
                      {target.name} · {target.base_url}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          ) : null}
          <div className="space-y-2">
            <Label htmlFor="main-station-name">名称</Label>
            <Input
              id="main-station-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="主站"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="main-station-url">Base URL</Label>
            <Input
              id="main-station-url"
              value={baseURL}
              onChange={(event) => setBaseURL(event.target.value)}
              placeholder="https://sub2api.example.com"
              autoCapitalize="none"
              spellCheck={false}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="main-station-key">Admin API Key</Label>
            <Input
              id="main-station-key"
              type="password"
              value={adminAPIKey}
              onChange={(event) => setAdminAPIKey(event.target.value)}
              placeholder={config?.has_admin_api_key ? "留空保留现有密钥" : "输入管理员密钥"}
              autoComplete="new-password"
            />
          </div>
          <div className="flex items-center justify-between gap-4 border-t pt-4">
            <div>
              <Label htmlFor="main-station-enabled">启用主站管理</Label>
              <p className="mt-1 text-xs text-muted-foreground">关闭后保留配置和历史数据。</p>
            </div>
            <Switch id="main-station-enabled" checked={enabled} onCheckedChange={setEnabled} />
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={handleTest} disabled={busy !== null}>
            <PlugZap className="size-4" />
            {busy === "test" ? "测试中" : "测试连接"}
          </Button>
          <Button onClick={handleSave} disabled={busy !== null}>
            <Save className="size-4" />
            {busy === "save" ? "保存中" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
