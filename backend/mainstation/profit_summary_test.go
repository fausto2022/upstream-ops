package mainstation

import (
	"context"
	"testing"
	"time"

	"github.com/fausto2022/relaydeck/backend/connector/sub2api"
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
	service, _, baseAdmin, _ := newTestService(t)
	location := shanghaiLocation()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, location)
	service.now = func() time.Time { return now }
	admin := &profitAdminClient{fakeAdminClient: baseAdmin, stats: map[string][]sub2api.AdminGroupUsageStat{}}
	for offset := 0; offset < mainStationProfitDays; offset++ {
		day := now.AddDate(0, 0, -offset).Format("2006-01-02")
		admin.stats[day] = []sub2api.AdminGroupUsageStat{{
			GroupID: 1, ActualCost: profitFloat64(10), AccountCost: profitFloat64(6),
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
	if summary.TodayProfit != 4 || summary.SevenDayRevenue != 70 || summary.SevenDayCost != 42 || summary.SevenDayProfit != 28 {
		t.Fatalf("profit summary = %#v", summary)
	}
	if len(admin.calls) != 7 {
		t.Fatalf("initial profit calls = %v", admin.calls)
	}

	today := now.Format("2006-01-02")
	admin.stats[today] = []sub2api.AdminGroupUsageStat{{
		GroupID: 1, ActualCost: profitFloat64(12), AccountCost: profitFloat64(5),
	}}
	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatalf("refresh main station: %v", err)
	}
	summary, err = service.ProfitSummary(7)
	if err != nil {
		t.Fatalf("refreshed profit summary: %v", err)
	}
	if len(admin.calls) != 8 || summary.TodayProfit != 7 || summary.SevenDayProfit != 31 {
		t.Fatalf("refreshed summary = %#v, calls = %v", summary, admin.calls)
	}
}

func TestSyncDoesNotStoreProfitWithoutAccountCost(t *testing.T) {
	service, _, baseAdmin, _ := newTestService(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, shanghaiLocation())
	service.now = func() time.Time { return now }
	admin := &profitAdminClient{
		fakeAdminClient: baseAdmin,
		stats: map[string][]sub2api.AdminGroupUsageStat{
			now.Format("2006-01-02"): {{GroupID: 1, ActualCost: profitFloat64(10)}},
		},
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
	if summary.Available {
		t.Fatalf("profit should be unavailable without account cost: %#v", summary)
	}
}

func profitFloat64(value float64) *float64 { return &value }
