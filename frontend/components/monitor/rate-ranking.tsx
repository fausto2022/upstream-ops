"use client"

import { useMemo, useState } from "react"
import { ListFilter } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { useChannels, useMultiChannelRates } from "@/lib/queries"
import { formatRatio } from "@/lib/format"
import { cn } from "@/lib/utils"
import type { RateSnapshot } from "@/lib/api-types"

type ProviderType = "openai" | "anthropic" | "gemini" | "grok" | "image" | "other"

const PROVIDERS: Array<{ value: ProviderType; label: string }> = [
  { value: "openai", label: "OpenAI" },
  { value: "anthropic", label: "Anthropic" },
  { value: "gemini", label: "Gemini" },
  { value: "grok", label: "Grok" },
  { value: "image", label: "生图" },
  { value: "other", label: "其他" },
]

const PROVIDER_PATTERNS: Array<{ type: ProviderType; pattern: RegExp }> = [
  { type: "anthropic", pattern: /anthropic|claude|sonnet|opus|haiku|kiro|cc\s*max|ccmax|aws/i },
  { type: "gemini", pattern: /gemini|google/i },
  { type: "grok", pattern: /grok|xai/i },
  { type: "image", pattern: /生图|绘图|画图|image|dall[ -]?e|midjourney|flux/i },
  { type: "openai", pattern: /openai|gpt|codex|\bplus\b|\bpro\b|\bteam\b|快速稳定|散户|无限制|测试/i },
]

function classifyRate(rate: RateSnapshot): ProviderType {
  const text = `${rate.model_name} ${rate.description ?? ""}`
  return PROVIDER_PATTERNS.find((item) => item.pattern.test(text))?.type ?? "other"
}

export function RateRanking() {
  const channels = useChannels()
  const channelIDs = useMemo(() => (channels.data ?? []).map((channel) => channel.id), [channels.data])
  const rates = useMultiChannelRates(channelIDs)
  const [provider, setProvider] = useState<ProviderType>("openai")
  const [showAll, setShowAll] = useState(false)

  const channelMap = useMemo(
    () => new Map((channels.data ?? []).map((channel) => [channel.id, channel])),
    [channels.data],
  )
  const ranked = useMemo(
    () => (rates.data ?? [])
      .filter((rate) => classifyRate(rate) === provider)
      .sort((left, right) => left.ratio - right.ratio || left.model_name.localeCompare(right.model_name)),
    [provider, rates.data],
  )
  const visible = showAll ? ranked : ranked.slice(0, 8)

  return (
    <Card className="overflow-hidden border border-border shadow-none">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-3 sm:px-5">
        <div>
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-semibold">倍率排行</h2>
            <Badge variant="outline" className="tabular-nums">{ranked.length} 个分组</Badge>
          </div>
          <p className="mt-0.5 text-xs text-muted-foreground">按换算后倍率从低到高排列</p>
        </div>
        <Button variant="ghost" size="sm" onClick={() => setShowAll((value) => !value)} disabled={ranked.length <= 8}>
          <ListFilter className="size-4" />{showAll ? "收起" : "查看全部"}
        </Button>
      </div>

      <div className="border-b border-border px-4 py-2 sm:px-5">
        <Tabs value={provider} onValueChange={(value) => { setProvider(value as ProviderType); setShowAll(false) }}>
          <TabsList className="max-w-full">
            {PROVIDERS.map((item) => <TabsTrigger key={item.value} value={item.value}>{item.label}</TabsTrigger>)}
          </TabsList>
        </Tabs>
      </div>

      {rates.loading ? (
        <div className="px-5 py-10 text-center text-sm text-muted-foreground">加载倍率排行中…</div>
      ) : visible.length === 0 ? (
        <div className="px-5 py-10 text-center text-sm text-muted-foreground">该类型暂无已采集分组</div>
      ) : (
        <div className={cn("overflow-x-auto", showAll && "max-h-96 overflow-y-auto")}>
          <table className="w-full min-w-140 text-sm">
            <thead className="sticky top-0 bg-background text-left text-xs text-muted-foreground">
              <tr className="border-b border-border">
                <th className="w-16 px-4 py-2 font-medium">排名</th>
                <th className="w-32 px-3 py-2 font-medium">渠道</th>
                <th className="px-3 py-2 font-medium">分组</th>
                <th className="w-36 px-4 py-2 text-right font-medium">换算后倍率</th>
              </tr>
            </thead>
            <tbody>
              {visible.map((rate, index) => {
                const channel = channelMap.get(rate.channel_id)
                return (
                  <tr key={`${rate.channel_id}-${rate.id}`} className="border-b border-border last:border-b-0 hover:bg-muted/30">
                    <td className="px-4 py-2.5 text-center">
                      <span className={cn("inline-flex size-6 items-center justify-center rounded-md bg-muted text-xs tabular-nums", index === 0 && "bg-foreground text-background")}>{index + 1}</span>
                    </td>
                    <td className="px-3 py-2.5 font-medium">{channel?.name ?? `渠道 #${rate.channel_id}`}</td>
                    <td className="max-w-0 px-3 py-2.5">
                      <div className="truncate font-medium" title={rate.model_name}>{rate.model_name}</div>
                      {rate.description ? <div className="truncate text-xs text-muted-foreground" title={rate.description}>{rate.description}</div> : null}
                    </td>
                    <td className="px-4 py-2.5 text-right font-semibold tabular-nums">{formatRatio(rate.ratio)}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  )
}
