package mainstation

import (
	"context"
	"time"

	"github.com/fausto2022/relaydeck/backend/connector/sub2api"
	"github.com/fausto2022/relaydeck/backend/storage"
)

const mainStationProfitDays = 7

type ProfitSummary struct {
	Available       bool       `json:"available"`
	TodayAvailable  bool       `json:"today_available"`
	TodayRevenue    float64    `json:"today_revenue"`
	TodayCost       float64    `json:"today_cost"`
	TodayProfit     float64    `json:"today_profit"`
	SevenDayRevenue float64    `json:"seven_day_revenue"`
	SevenDayCost    float64    `json:"seven_day_cost"`
	SevenDayProfit  float64    `json:"seven_day_profit"`
	SampledDays     int        `json:"sampled_days"`
	Complete        bool       `json:"complete"`
	LastSampledAt   *time.Time `json:"last_sampled_at,omitempty"`
}

type groupUsageStatsClient interface {
	ListGroupUsageStats(ctx context.Context, target sub2api.AdminTarget, startDate, endDate string) ([]sub2api.AdminGroupUsageStat, error)
}

func (s *Service) syncProfitSnapshots(ctx context.Context, client adminClient, target sub2api.AdminTarget, sampledAt time.Time) {
	statsClient, ok := client.(groupUsageStatsClient)
	if !ok {
		return
	}
	location := shanghaiLocation()
	today := sampledAt.In(location)
	startDay := today.AddDate(0, 0, -(mainStationProfitDays - 1)).Format("2006-01-02")
	existing, err := s.store.ListProfitSnapshotsSince(startDay)
	if err != nil {
		s.logProfitSyncError("list profit snapshots", err)
		return
	}
	present := make(map[string]struct{}, len(existing))
	for _, item := range existing {
		present[item.Day] = struct{}{}
	}
	days := []string{today.Format("2006-01-02")}
	for offset := 1; offset < mainStationProfitDays; offset++ {
		day := today.AddDate(0, 0, -offset).Format("2006-01-02")
		if _, ok := present[day]; !ok {
			days = append(days, day)
		}
	}
	for _, day := range days {
		groups, statsErr := statsClient.ListGroupUsageStats(ctx, target, day, day)
		if statsErr != nil {
			s.logProfitSyncError("fetch main station profit", statsErr)
			return
		}
		var revenue, cost float64
		for _, group := range groups {
			if group.ActualCost == nil || group.AccountCost == nil {
				s.logProfitSyncError("main station profit response has no actual/account cost", nil)
				return
			}
			revenue += *group.ActualCost
			cost += *group.AccountCost
		}
		if err := s.store.UpsertProfitSnapshot(&storage.MainStationProfitSnapshot{
			Day: day, Revenue: revenue, Cost: cost, SampledAt: sampledAt,
		}); err != nil {
			s.logProfitSyncError("save main station profit", err)
			return
		}
	}
}

func (s *Service) ProfitSummary(days int) (*ProfitSummary, error) {
	if days <= 0 || days > mainStationProfitDays {
		days = mainStationProfitDays
	}
	today := s.now().In(shanghaiLocation())
	startDay := today.AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	items, err := s.store.ListProfitSnapshotsSince(startDay)
	if err != nil {
		return nil, err
	}
	summary := &ProfitSummary{SampledDays: len(items), Complete: len(items) == days}
	todayKey := today.Format("2006-01-02")
	for i := range items {
		item := items[i]
		summary.SevenDayRevenue += item.Revenue
		summary.SevenDayCost += item.Cost
		if item.Day == todayKey {
			summary.TodayAvailable = true
			summary.TodayRevenue = item.Revenue
			summary.TodayCost = item.Cost
		}
		if summary.LastSampledAt == nil || item.SampledAt.After(*summary.LastSampledAt) {
			sampledAt := item.SampledAt
			summary.LastSampledAt = &sampledAt
		}
	}
	summary.Available = len(items) > 0
	summary.TodayProfit = summary.TodayRevenue - summary.TodayCost
	summary.SevenDayProfit = summary.SevenDayRevenue - summary.SevenDayCost
	return summary, nil
}

func (s *Service) logProfitSyncError(message string, err error) {
	if s.log != nil {
		if err == nil {
			s.log.Warn(message)
			return
		}
		s.log.Warn(message, "err", err)
	}
}

func shanghaiLocation() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return location
}
