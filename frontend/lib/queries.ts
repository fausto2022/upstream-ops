"use client"

import { useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api"
import { useRefreshTick } from "@/lib/refresh-context"
import type {
  AppVersion,
  AlertEventPage,
  BalanceTrendPoint,
  CaptchaConfig,
  Channel,
  ChannelPage,
  CostTrendPoint,
  DashboardSummary,
  NotificationChannel,
  NotificationLogPage,
  RateChangeLogPage,
  RateRankingConfig,
  RateSnapshot,
  SystemConfigResponse,
  UpstreamAnnouncementPage,
} from "@/lib/api-types"

export interface QueryState<T> {
  data: T | null
  loading: boolean
  error: string | null
  refetch: () => void
  setData: (data: T) => void
}

/**
 * In-flight 请求去重：同一个 URL 在同一个 tick 内只发一次，所有 useApi 共享 Promise。
 *
 * 为什么需要：useDashboardSummary() 在 5 个组件里都被调用，没去重的话每次 mount /
 * refresh 都会发 5 个相同请求。开发环境叠加 StrictMode 翻倍后会更夸张。
 */
const inflight = new Map<string, Promise<unknown>>()

/** Cache 已完成的响应一小段时间，便于同一帧内挂载的多个组件共享结果（即使第一次的 Promise 已经 resolve）。 */
interface CacheEntry {
  data: unknown
  expiresAt: number
}
const cache = new Map<string, CacheEntry>()
const CACHE_TTL_MS = 800

function cacheKey(path: string, tick: number, bump: number) {
  return `${path}#${tick}#${bump}`
}

export function fetchShared<T>(path: string, key: string): Promise<T> {
  const now = Date.now()

  const cached = cache.get(key)
  if (cached && cached.expiresAt > now) {
    return Promise.resolve(cached.data as T)
  }

  const existing = inflight.get(key) as Promise<T> | undefined
  if (existing) return existing

  const p = apiFetch<T>(path, { cache: "no-store" })
    .then((d) => {
      const entry = { data: d, expiresAt: Date.now() + CACHE_TTL_MS }
      cache.set(key, entry)
      globalThis.setTimeout(() => {
        if (cache.get(key) === entry) cache.delete(key)
      }, CACHE_TTL_MS)
      return d
    })
    .finally(() => {
      // 让下一帧（refresh tick++）拉到新的数据，不要永远 hold 住旧 promise
      inflight.delete(key)
    })
  inflight.set(key, p)
  return p
}

/**
 * useApi 通用数据获取 hook（stale-while-revalidate）。
 * - 首次加载：loading = true，组件显示加载占位
 * - 后续刷新（refresh tick / refetch）：保留旧 data 继续展示，loading 不切回 true，后台静默拉新
 * - 同 URL + 同 tick 的并发调用共享一次请求
 */
function useApi<T>(path: string | null, watchRefresh = true): QueryState<T> {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState<boolean>(path !== null)
  const [error, setError] = useState<string | null>(null)
  const [bump, setBump] = useState(0)
  const refreshTick = useRefreshTick()
  const globalTick = watchRefresh ? refreshTick : 0

  // 已经拿到过数据吗？用 ref 防止 setLoading 写回触发额外 effect。
  const hasDataRef = useRef(false)

  useEffect(() => {
    if (path === null) {
      setLoading(false)
      return
    }
    let cancelled = false
    // 关键：只有第一次（还没拿到过数据）才展示 loading；后续 polling / refetch 静默进行，
    // 避免组件因 loading=true 短暂消失再回来造成"闪屏"。
    if (!hasDataRef.current) setLoading(true)
    setError(null)
    fetchShared<T>(path, cacheKey(path, globalTick, bump))
      .then((d) => {
        if (cancelled) return
        hasDataRef.current = true
        setData(d)
      })
      .catch((e: Error) => {
        if (cancelled) return
        setError(e.message)
      })
      .finally(() => {
        if (cancelled) return
        setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [path, bump, globalTick])

  return {
    data,
    loading,
    error,
    refetch: () => setBump((b) => b + 1),
    setData: (nextData) => {
      hasDataRef.current = true
      setData(nextData)
    },
  }
}

export function useDashboardSummary() {
  return useApi<DashboardSummary>("/dashboard/summary")
}

export function useAppVersion() {
  return useApi<AppVersion>("/version", false)
}

export function useBalanceTrend(days = 7) {
  return useApi<BalanceTrendPoint[]>(`/dashboard/balance-trend?days=${days}`)
}

export function useCostTrend(days = 7) {
  return useApi<CostTrendPoint[]>(`/dashboard/cost-trend?days=${days}`)
}

export function useChannels() {
  return useApi<Channel[]>("/channels")
}

export function useChannelsPage(page = 1, pageSize = 9) {
  return useApi<ChannelPage>(`/channels?page=${page}&page_size=${pageSize}`)
}

export function useChannelRates(channelID: number | null) {
  return useApi<RateSnapshot[]>(channelID == null ? null : `/channels/${channelID}/rates`)
}

export function mergeSettledChannelRates(
  previous: RateSnapshot[] | null,
  channelIDs: number[],
  results: PromiseSettledResult<RateSnapshot[]>[],
) {
  const latest: RateSnapshot[] = []
  const failedChannelIDs = new Set<number>()
  let fulfilledCount = 0

  results.forEach((result, index) => {
    const channelID = channelIDs[index]
    if (result.status === "fulfilled") {
      fulfilledCount += 1
      latest.push(...result.value)
      return
    }
    if (channelID != null) failedChannelIDs.add(channelID)
  })

  if (fulfilledCount === 0 && previous === null) return null

  const retained = (previous ?? []).filter((rate) => failedChannelIDs.has(rate.channel_id))
  return [...latest, ...retained]
}

// useMultiChannelRates 把多个上游渠道的倍率分组拉回来合并去重，
// 供订阅规则"多选渠道 + 指定分组"场景使用。复用 fetchShared 缓存，
// 单渠道请求仍与 useChannelRates 共享，不会重复打接口。
export function useMultiChannelRates(channelIDs: number[]) {
  const [data, setData] = useState<RateSnapshot[] | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [bump, setBump] = useState(0)
  const hasDataRef = useRef(false)
  const refreshTick = useRefreshTick()
  const key = channelIDs.slice().sort((a, b) => a - b).join(",")

  useEffect(() => {
    if (channelIDs.length === 0) {
      setData(null)
      setLoading(false)
      setError(null)
      hasDataRef.current = false
      return
    }
    let cancelled = false
    if (!hasDataRef.current) setLoading(true)
    setError(null)
    Promise.allSettled(
      channelIDs.map((id) =>
        fetchShared<RateSnapshot[]>(
          `/channels/${id}/rates`,
          cacheKey(`/channels/${id}/rates`, refreshTick, bump),
        ),
      ),
    )
      .then((results) => {
        if (cancelled) return
        const failedCount = results.filter((result) => result.status === "rejected").length
        setData((previous) => {
          const next = mergeSettledChannelRates(previous, channelIDs, results)
          if (next !== null) hasDataRef.current = true
          return next
        })
        if (failedCount > 0) {
          setError(`${failedCount} 个渠道的倍率加载失败，已保留上次数据`)
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
    // channelIDs 是数组引用，用排序后的 key 字符串做依赖避免每次渲染都触发
  }, [key, refreshTick, bump])

  return { data, loading, error, refetch: () => setBump((b) => b + 1) }
}

export function useRateChanges(page = 1, pageSize = 20, channelID?: number) {
  const q = new URLSearchParams()
  q.set("page", String(page))
  q.set("page_size", String(pageSize))
  if (channelID != null) q.set("channel_id", String(channelID))
  return useApi<RateChangeLogPage>(`/rate-changes?${q.toString()}`)
}

export function useNotificationChannels() {
  return useApi<NotificationChannel[]>("/notifications/channels")
}

export function useNotificationLogs(page = 1, pageSize = 20) {
  return useApi<NotificationLogPage>(
    `/notifications/logs?page=${page}&page_size=${pageSize}`,
  )
}

export function useAnnouncements(page = 1, pageSize = 20) {
  return useApi<UpstreamAnnouncementPage>(
    `/announcements?page=${page}&page_size=${pageSize}`,
  )
}

export function useCaptchaConfigs(enabled = true) {
  return useApi<CaptchaConfig[]>(enabled ? "/captcha-configs" : null)
}

export function useSystemConfig() {
  return useApi<SystemConfigResponse>("/settings/config")
}

export function useAlertEvents(page = 1, pageSize = 20) {
  return useApi<AlertEventPage>(
    `/notifications/events?page=${page}&page_size=${pageSize}`,
  )
}

export function useRateRankingConfig() {
  return useApi<RateRankingConfig>("/settings/rate-ranking", false)
}
