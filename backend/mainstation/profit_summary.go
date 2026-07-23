package mainstation

import (
	"context"
	"errors"
	"time"

	"github.com/fausto2022/relaydeck/backend/connector/sub2api"
	"github.com/fausto2022/relaydeck/backend/storage"
	"gorm.io/gorm"
)

const mainStationProfitDays = 7

type ProfitSummary struct {
	Available                 bool       `json:"available"`
	TodayAvailable            bool       `json:"today_available"`
	TodayRevenue              float64    `json:"today_revenue"`
	TodayGuaranteedRevenue    float64    `json:"today_guaranteed_revenue"`
	TodayCost                 float64    `json:"today_cost"`
	TodayProfit               float64    `json:"today_profit"`
	SevenDayRevenue           float64    `json:"seven_day_revenue"`
	SevenDayGuaranteedRevenue float64    `json:"seven_day_guaranteed_revenue"`
	SevenDayCost              float64    `json:"seven_day_cost"`
	SevenDayProfit            float64    `json:"seven_day_profit"`
	SampledDays               int        `json:"sampled_days"`
	Complete                  bool       `json:"complete"`
	GuaranteedRevenueRatioBP  int64      `json:"guaranteed_revenue_ratio_basis_points"`
	LastSampledAt             *time.Time `json:"last_sampled_at,omitempty"`
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
		var revenue float64
		for _, group := range groups {
			if group.ActualCost == nil {
				s.logProfitSyncError("main station profit response has no actual cost", nil)
				return
			}
			revenue += *group.ActualCost
		}
		if err := s.store.UpsertProfitSnapshot(&storage.MainStationProfitSnapshot{
			Day: day, Revenue: revenue, SampledAt: sampledAt,
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
	ratioBasisPoints := defaultGuaranteedRevenueRatioBP
	config, configErr := s.store.GetConfig()
	if configErr == nil {
		ratioBasisPoints = normalizedGuaranteedRevenueRatioBP(config.GuaranteedRevenueRatioBP)
	} else if !errors.Is(configErr, gorm.ErrRecordNotFound) {
		return nil, configErr
	}
	summary := &ProfitSummary{
		SampledDays: len(items), Complete: len(items) == days,
		GuaranteedRevenueRatioBP: ratioBasisPoints,
	}
	todayKey := today.Format("2006-01-02")
	for i := range items {
		item := items[i]
		summary.SevenDayRevenue += item.Revenue
		if item.Day == todayKey {
			summary.TodayAvailable = true
			summary.TodayRevenue = item.Revenue
		}
		if summary.LastSampledAt == nil || item.SampledAt.After(*summary.LastSampledAt) {
			sampledAt := item.SampledAt
			summary.LastSampledAt = &sampledAt
		}
	}
	summary.Available = len(items) > 0
	if err := s.applyUpstreamCosts(summary, days, todayKey); err != nil {
		return nil, err
	}
	summary.TodayProfit = summary.TodayRevenue - summary.TodayCost
	summary.SevenDayProfit = summary.SevenDayRevenue - summary.SevenDayCost
	summary.TodayGuaranteedRevenue = guaranteedNetRevenue(summary.TodayRevenue, summary.TodayCost, ratioBasisPoints)
	summary.SevenDayGuaranteedRevenue = guaranteedNetRevenue(summary.SevenDayRevenue, summary.SevenDayCost, ratioBasisPoints)
	return summary, nil
}

func guaranteedNetRevenue(revenue, cost float64, ratioBasisPoints int64) float64 {
	convertedRevenue := revenue * float64(normalizedGuaranteedRevenueRatioBP(ratioBasisPoints)) / float64(defaultGuaranteedRevenueRatioBP)
	return convertedRevenue - cost
}

func (s *Service) applyUpstreamCosts(summary *ProfitSummary, days int, todayKey string) error {
	var trendTodayCost float64
	if s.rates != nil {
		trend, err := s.rates.AggregateCostTrendAt(days, s.now())
		if err != nil {
			return err
		}
		for _, item := range trend {
			summary.SevenDayCost += item.Cost
			if item.Day.Format("2006-01-02") == todayKey {
				trendTodayCost = item.Cost
			}
		}
	}
	if s.channels == nil {
		summary.TodayCost = trendTodayCost
		return nil
	}
	channels, err := s.channels.List()
	if err != nil {
		return err
	}
	for _, channel := range channels {
		if channel.TodayCost != nil {
			summary.TodayCost += *channel.TodayCost
		}
	}
	summary.SevenDayCost += summary.TodayCost - trendTodayCost
	return nil
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
