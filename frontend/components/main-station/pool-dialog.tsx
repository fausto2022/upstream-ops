import { useEffect, useState } from "react"
import { Save } from "lucide-react"
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
import { Textarea } from "@/components/ui/textarea"
import { apiFetch } from "@/lib/api"
import type {
  MainStationGroup,
  MainStationPool,
} from "@/lib/api-types"

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  pool: MainStationPool | null
  groups: MainStationGroup[]
  onSaved: (pool: MainStationPool) => void
}

export function PoolDialog({ open, onOpenChange, pool, groups, onSaved }: Props) {
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [platform, setPlatform] = useState("openai")
  const [enabled, setEnabled] = useState(true)
  const [minimumHealthy, setMinimumHealthy] = useState(1)
  const [minimumConcurrency, setMinimumConcurrency] = useState(1)
  const [rateSortDirection, setRateSortDirection] = useState<"asc" | "desc">("asc")
  const [groupIDs, setGroupIDs] = useState<number[]>([])
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!open) return
    setName(pool?.name ?? "")
    setDescription(pool?.description ?? "")
    setPlatform(pool?.platform || "openai")
    setEnabled(pool?.enabled ?? true)
    setMinimumHealthy(pool?.minimum_healthy_members ?? 1)
    setMinimumConcurrency(pool?.minimum_effective_concurrency ?? 1)
    setRateSortDirection(pool?.rate_sort_direction ?? "asc")
    setGroupIDs(pool?.target_group_ids ?? [])
  }, [open, pool])

  function toggleGroup(id: number, checked: boolean) {
    setGroupIDs((current) =>
      checked ? Array.from(new Set([...current, id])) : current.filter((value) => value !== id),
    )
  }

  async function handleSave() {
    if (!name.trim()) {
      toast.error("请输入账号池名称")
      return
    }
    if (groupIDs.length === 0) {
      toast.error("至少选择一个主站分组")
      return
    }
    setBusy(true)
    try {
      const saved = await apiFetch<MainStationPool>(
        pool ? `/main-station/pools/${pool.id}` : "/main-station/pools",
        {
          method: pool ? "PUT" : "POST",
          body: JSON.stringify({
            name: name.trim(),
            description: description.trim(),
            platform,
            enabled,
            minimum_healthy_members: minimumHealthy,
            minimum_effective_concurrency: minimumConcurrency,
            rate_sort_direction: rateSortDirection,
            health_policy: pool?.health_policy ?? "",
            margin_policy: pool?.margin_policy ?? "",
            target_group_ids: groupIDs,
          }),
        },
      )
      onSaved(saved)
      onOpenChange(false)
      toast.success(pool ? "账号池已更新" : "账号池已创建")
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "保存账号池失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{pool ? "编辑账号池" : "创建账号池"}</DialogTitle>
          <DialogDescription>一个池可以同时服务多个主站分组。</DialogDescription>
        </DialogHeader>

        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2 sm:col-span-2">
            <Label htmlFor="pool-name">名称</Label>
            <Input id="pool-name" value={name} onChange={(event) => setName(event.target.value)} />
          </div>
          <div className="space-y-2 sm:col-span-2">
            <Label htmlFor="pool-description">说明</Label>
            <Textarea
              id="pool-description"
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              rows={3}
            />
          </div>
          <div className="space-y-2">
            <Label>平台</Label>
            <Select value={platform} onValueChange={setPlatform}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="openai">OpenAI</SelectItem>
                <SelectItem value="anthropic">Anthropic</SelectItem>
                <SelectItem value="gemini">Gemini</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-2">
            <Label>成本排序</Label>
            <Select
              value={rateSortDirection}
              onValueChange={(value) => setRateSortDirection(value as "asc" | "desc")}
            >
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="asc">低成本优先</SelectItem>
                <SelectItem value="desc">高成本优先</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-2">
            <Label htmlFor="pool-minimum-healthy">最小健康成员数</Label>
            <Input
              id="pool-minimum-healthy"
              type="number"
              min={0}
              value={minimumHealthy}
              onChange={(event) => setMinimumHealthy(Number(event.target.value))}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="pool-minimum-concurrency">最小有效并发</Label>
            <Input
              id="pool-minimum-concurrency"
              type="number"
              min={0}
              value={minimumConcurrency}
              onChange={(event) => setMinimumConcurrency(Number(event.target.value))}
            />
          </div>
          <div className="space-y-2 sm:col-span-2">
            <Label>主站分组</Label>
            <div className="grid max-h-48 gap-2 overflow-y-auto rounded-md border p-3 sm:grid-cols-2">
              {groups.map((group) => (
                <label key={group.id} className="flex min-w-0 items-center gap-2 text-sm">
                  <Checkbox
                    checked={groupIDs.includes(group.id)}
                    onCheckedChange={(checked) => toggleGroup(group.id, checked === true)}
                  />
                  <span className="truncate" title={group.name}>{group.name}</span>
                </label>
              ))}
            </div>
          </div>
          <div className="flex items-center justify-between gap-4 border-t pt-4 sm:col-span-2">
            <Label htmlFor="pool-enabled">启用账号池</Label>
            <Switch id="pool-enabled" checked={enabled} onCheckedChange={setEnabled} />
          </div>
        </div>

        <DialogFooter>
          <Button onClick={handleSave} disabled={busy}>
            <Save className="size-4" />
            {busy ? "保存中" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
