package mainstation

import (
	"context"
	"testing"
	"time"

	"github.com/fausto2022/relaydeck/backend/connector/sub2api"
	"github.com/fausto2022/relaydeck/backend/storage"
	"gorm.io/gorm"
)

type profitAdminClient struct {
	*fakeAdminClient
	stats map[string][]sub2api.AdminGroupUsageStat
	calls []string
}

func (f *profitAdminClient) ListGroupUsageStats(_ context.Context, _ sub2api.AdminTarget, startDate, endDate string) ([]sub2api.AdminGroupUsageStat, error) {
	f.calls = append(f.calls, startDate+":"+endDate)
	return append([]sub2api.AdminGroupUsageStat(nil), f.stats[startDate]...), nil
}

func TestSyncBackfillsAndUpdatesProfitSummary(t *testing.T) {
	service, db, baseAdmin, _ := newTestService(t)
	location := shanghaiLocation()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, location)
	service.now = func() time.Time { return now }
	channel := seedProfitCosts(t, db, now, 3)
	admin := &profitAdminClient{fakeAdminClient: baseAdmin, stats: map[string][]sub2api.AdminGroupUsageStat{}}
	for offset := 0; offset < mainStationProfitDays; offset++ {
		day := now.AddDate(0, 0, -offset).Format("2006-01-02")
		admin.stats[day] = []sub2api.AdminGroupUsageStat{{
			GroupID: 1, ActualCost: profitFloat64(10), AccountCost: profitFloat64(600),
		}}
	}
	service.adminFactory = func() adminClient { return admin }
	configureTestStation(t, service)

	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("sync main station: %v", err)
	}
	summary, err := service.ProfitSummary(7)
	if err != nil {
		t.Fatalf("profit summary: %v", err)
	}
	if !summary.Available || !summary.TodayAvailable || !summary.Complete || summary.SampledDays != 7 {
		t.Fatalf("profit availability = %#v", summary)
	}
	if summary.TodayProfit != 7 || summary.SevenDayRevenue != 70 || summary.SevenDayCost != 21 || summary.SevenDayProfit != 49 {
		t.Fatalf("profit summary = %#v", summary)
	}
	if len(admin.calls) != 7 {
		t.Fatalf("initial profit calls = %v", admin.calls)
	}

	today := now.Format("2006-01-02")
	admin.stats[today] = []sub2api.AdminGroupUsageStat{{
		GroupID: 1, ActualCost: profitFloat64(12), AccountCost: profitFloat64(500),
	}}
	if err := storage.NewChannels(db).UpdateCosts(channel.ID, 4, 4); err != nil {
		t.Fatalf("update current upstream cost: %v", err)
	}
	if err := storage.NewRates(db).AppendCost(&storage.CostSnapshot{ChannelID: channel.ID, TodayCost: 4, SampledAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("append refreshed cost: %v", err)
	}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("refresh main station: %v", err)
	}
	summary, err = service.ProfitSummary(7)
	if err != nil {
		t.Fatalf("refreshed profit summary: %v", err)
	}
	if len(admin.calls) != 8 || summary.TodayProfit != 8 || summary.SevenDayProfit != 50 {
		t.Fatalf("refreshed summary = %#v, calls = %v", summary, admin.calls)
	}
}

func TestSyncUsesActualCostWithoutAccountCost(t *testing.T) {
	service, db, baseAdmin, _ := newTestService(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, shanghaiLocation())
	service.now = func() time.Time { return now }
	seedProfitCosts(t, db, now, 3)
	admin := &profitAdminClient{
		fakeAdminClient: baseAdmin,
		stats: map[string][]sub2api.AdminGroupUsageStat{
			now.Format("2006-01-02"): {{GroupID: 1, ActualCost: profitFloat64(10)}},
		},
	}
	service.adminFactory = func() adminClient { return admin }
	configureTestStation(t, service)
	guaranteedRevenueRatio := int64(8000)
	if _, err := service.UpdateConfig(context.Background(), ConfigInput{GuaranteedRevenueRatioBP: &guaranteedRevenueRatio}); err != nil {
		t.Fatalf("update guaranteed revenue ratio: %v", err)
	}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("sync main station: %v", err)
	}
	summary, err := service.ProfitSummary(7)
	if err != nil {
		t.Fatalf("profit summary: %v", err)
	}
	if !summary.Available || summary.TodayRevenue != 10 || summary.TodayGuaranteedRevenue != 5 || summary.GuaranteedRevenueRatioBP != guaranteedRevenueRatio || summary.TodayCost != 3 || summary.TodayProfit != 7 {
		t.Fatalf("profit should use actual revenue and converted upstream cost: %#v", summary)
	}
}

func profitFloat64(value float64) *float64 { return &value }

func seedProfitCosts(t *testing.T, db *gorm.DB, now time.Time, dailyCost float64) *storage.Channel {
	t.Helper()
	channel := &storage.Channel{
		Name: "profit-source", Type: storage.ChannelTypeSub2API, SiteURL: "https://source.example.com",
		Username: "user", PasswordCipher: "cipher", MonitorEnabled: true, TodayCost: &dailyCost,
	}
	if err := storage.NewChannels(db).Create(channel); err != nil {
		t.Fatalf("create profit source channel: %v", err)
	}
	rates := storage.NewRates(db)
	for offset := 0; offset < mainStationProfitDays; offset++ {
		if err := rates.AppendCost(&storage.CostSnapshot{
			ChannelID: channel.ID, TodayCost: dailyCost, SampledAt: now.AddDate(0, 0, -offset),
		}); err != nil {
			t.Fatalf("append profit cost snapshot: %v", err)
		}
	}
	return channel
}
