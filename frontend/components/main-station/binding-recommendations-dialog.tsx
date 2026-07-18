import { useCallback, useEffect, useMemo, useState } from "react"
import { Link2, RefreshCw } from "lucide-react"
import { toast } from "sonner"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
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
import { apiFetch } from "@/lib/api"
import type {
  MainStationBindingBatchResult,
  MainStationBindingCandidate,
  MainStationBindingRecommendation,
  MainStationBindingRecommendationResult,
  MainStationGroupWorkspace,
} from "@/lib/api-types"

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspace: MainStationGroupWorkspace | null
  onSaved: () => void
}

export function BindingRecommendationsDialog({ open, onOpenChange, workspace, onSaved }: Props) {
  const [items, setItems] = useState<MainStationBindingRecommendation[]>([])
  const [warnings, setWarnings] = useState<string[]>([])
  const [choices, setChoices] = useState<Record<number, string>>({})
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    if (!workspace) return
    setLoading(true)
    try {
      const result = await apiFetch<MainStationBindingRecommendationResult>(`/main-station/groups/${workspace.group.id}/binding-recommendations`)
      const nextChoices: Record<number, string> = {}
      const nextSelected = new Set<number>()
      for (const item of result.items) {
        if (item.suggested_candidate_id) nextChoices[item.remote_account_id] = item.suggested_candidate_id
        const candidate = item.candidates.find((entry) => entry.id === item.suggested_candidate_id)
        if (item.confidence === "high" && !item.conflict && candidate?.source_api_key_id) {
          nextSelected.add(item.remote_account_id)
        }
      }
      setItems(result.items)
      setWarnings(result.warnings)
      setChoices(nextChoices)
      setSelected(nextSelected)
    } catch (error) {
      setItems([])
      setWarnings([])
      toast.error(error instanceof Error ? error.message : "生成绑定推荐失败")
    } finally {
      setLoading(false)
    }
  }, [workspace])

  useEffect(() => {
    if (open) void load()
  }, [load, open])

  const selectedItems = useMemo(() => items.filter((item) => selected.has(item.remote_account_id)), [items, selected])

  function changeSelected(remoteAccountID: number, checked: boolean) {
    setSelected((current) => {
      const next = new Set(current)
      if (checked) next.add(remoteAccountID)
      else next.delete(remoteAccountID)
      return next
    })
  }

  async function bindSelected() {
    if (!workspace || selectedItems.length === 0) {
      toast.error("请先选择要绑定的账号")
      return
    }
    const payload = selectedItems.flatMap((item) => {
      const candidate = selectedCandidate(item, choices[item.remote_account_id])
      if (!candidate) return []
      return [{
        ownership_mode: "bound",
        remote_account_id: item.remote_account_id,
        source_channel_id: candidate.source_channel_id,
        source_group_id: candidate.source_group_id ?? undefined,
        source_group_name: candidate.source_group_name ?? "",
        source_api_key_id: candidate.source_api_key_id ?? undefined,
        concurrency: candidate.concurrency,
        priority: 1,
        manual_binding_confirmed: true,
        enabled: true,
        health_enabled: true,
        rate_convert_mode: "raw",
        rate_convert_value: 1,
        cost_adjustment: 1,
        health_api_mode: "openai_chat",
      }]
    })
    if (payload.length !== selectedItems.length) {
      toast.error("存在未选择绑定候选的账号")
      return
    }
    setBusy(true)
    try {
      const result = await apiFetch<MainStationBindingBatchResult>(`/main-station/groups/${workspace.group.id}/accounts/bind-batch`, {
        method: "POST",
        body: JSON.stringify({ items: payload }),
      })
      if (result.succeeded > 0) toast.success(`已接管 ${result.succeeded} 个账号`)
      if (result.failed > 0) {
        const firstError = result.items.find((item) => !item.success)?.error
        toast.error(`${result.failed} 个账号接管失败${firstError ? `：${firstError}` : ""}`)
      }
      if (result.ranking_error) toast.warning(`账号已接管，但优先级重算失败：${result.ranking_error}`)
      onSaved()
      if (result.failed === 0) onOpenChange(false)
      else await load()
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "批量接管失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="h-[calc(100dvh-1rem)] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden p-0 sm:h-[90dvh] sm:max-w-5xl">
        <DialogHeader className="px-4 pt-4 sm:px-6 sm:pt-6">
          <DialogTitle>推荐绑定</DialogTitle>
          <DialogDescription>{workspace?.group.name ?? "当前分组"} · 系统只推荐，绑定前由你确认</DialogDescription>
        </DialogHeader>

        <div className="min-h-0 overflow-y-auto px-4 sm:px-6">
          {warnings.length > 0 ? (
            <Alert className="mb-4">
              <AlertTitle>部分上游信息读取失败</AlertTitle>
              <AlertDescription>{warnings.slice(0, 3).join("；")}</AlertDescription>
            </Alert>
          ) : null}

          {loading ? <div className="flex min-h-64 items-center justify-center"><Spinner /></div> : null}
          {!loading && items.length === 0 ? <div className="flex min-h-64 items-center justify-center text-sm text-muted-foreground">当前分组没有未接管账号</div> : null}

          {!loading && items.length > 0 ? (
            <>
              <div className="hidden md:block">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="w-10"><span className="sr-only">选择</span></TableHead>
                      <TableHead>主站账号</TableHead>
                      <TableHead className="w-[360px]">推荐绑定</TableHead>
                      <TableHead>匹配</TableHead>
                      <TableHead>依据</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {items.map((item) => {
                      const candidate = selectedCandidate(item, choices[item.remote_account_id])
                      return (
                        <TableRow key={item.remote_account_id}>
                          <TableCell><Checkbox disabled={!candidate} checked={selected.has(item.remote_account_id)} onCheckedChange={(checked) => changeSelected(item.remote_account_id, checked === true)} /></TableCell>
                          <TableCell>
                            <div className="max-w-48 truncate font-medium" title={item.remote_account_name}>{item.remote_account_name}</div>
                            <div className="text-xs text-muted-foreground">#{item.remote_account_id} · {item.platform || "未知平台"}</div>
                          </TableCell>
                          <TableCell><CandidateSelect item={item} value={choices[item.remote_account_id]} onChange={(value) => setChoices((current) => ({ ...current, [item.remote_account_id]: value }))} /></TableCell>
                          <TableCell><ConfidenceBadge item={item} candidate={candidate} /></TableCell>
                          <TableCell><p className="max-w-56 text-xs text-muted-foreground">{candidate?.reasons.slice(0, 2).join("；") || "暂无可靠候选"}</p></TableCell>
                        </TableRow>
                      )
                    })}
                  </TableBody>
                </Table>
              </div>

              <div className="space-y-3 md:hidden">
                {items.map((item) => {
                  const candidate = selectedCandidate(item, choices[item.remote_account_id])
                  return (
                    <div key={item.remote_account_id} className="space-y-3 rounded-md border p-3">
                      <div className="flex items-start gap-3">
                        <Checkbox className="mt-0.5" disabled={!candidate} checked={selected.has(item.remote_account_id)} onCheckedChange={(checked) => changeSelected(item.remote_account_id, checked === true)} />
                        <div className="min-w-0 flex-1">
                          <p className="truncate text-sm font-medium">{item.remote_account_name}</p>
                          <p className="text-xs text-muted-foreground">#{item.remote_account_id} · {item.platform || "未知平台"}</p>
                        </div>
                        <ConfidenceBadge item={item} candidate={candidate} />
                      </div>
                      <CandidateSelect item={item} value={choices[item.remote_account_id]} onChange={(value) => setChoices((current) => ({ ...current, [item.remote_account_id]: value }))} />
                      <p className="text-xs text-muted-foreground">{candidate?.reasons.slice(0, 2).join("；") || "暂无可靠候选"}</p>
                    </div>
                  )
                })}
              </div>
            </>
          ) : null}
        </div>

        <DialogFooter className="border-t bg-background px-4 py-4 sm:px-6">
          <Button variant="outline" onClick={() => void load()} disabled={loading || busy}><RefreshCw className="size-4" />重新计算</Button>
          <Button onClick={() => void bindSelected()} disabled={loading || busy || selectedItems.length === 0}>
            {busy ? <Spinner /> : <Link2 className="size-4" />}确认并绑定 {selectedItems.length > 0 ? `${selectedItems.length} 个` : ""}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function CandidateSelect({ item, value, onChange }: { item: MainStationBindingRecommendation; value?: string; onChange: (value: string) => void }) {
  return (
    <Select value={value ?? ""} onValueChange={onChange} disabled={item.candidates.length === 0}>
      <SelectTrigger className="w-full"><SelectValue placeholder="暂无推荐" /></SelectTrigger>
      <SelectContent className="max-w-[calc(100vw-2rem)]">
        {item.candidates.map((candidate) => <SelectItem key={candidate.id} value={candidate.id}>{candidateLabel(candidate)}</SelectItem>)}
      </SelectContent>
    </Select>
  )
}

function candidateLabel(candidate: MainStationBindingCandidate) {
  const parts = [candidate.source_channel_name]
  if (candidate.source_group_name) parts.push(candidate.source_group_name)
  parts.push(candidate.source_api_key_name || "未指定 API Key")
  return parts.join(" · ")
}

function selectedCandidate(item: MainStationBindingRecommendation, candidateID?: string) {
  return item.candidates.find((candidate) => candidate.id === candidateID)
}

function ConfidenceBadge({ item, candidate }: { item: MainStationBindingRecommendation; candidate?: MainStationBindingCandidate }) {
  if (item.conflict) return <Badge variant="outline" className="border-amber-300 text-amber-700">待确认</Badge>
  if (candidate?.confidence === "high") return <Badge variant="outline" className="border-emerald-300 text-emerald-700">高 {candidate.score}</Badge>
  if (candidate?.confidence === "medium") return <Badge variant="outline">中 {candidate.score}</Badge>
  if (candidate?.confidence === "low") return <Badge variant="secondary">低 {candidate.score}</Badge>
  return <Badge variant="secondary">无推荐</Badge>
}
