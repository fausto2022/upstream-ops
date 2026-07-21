package scheduler

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fausto2022/relaydeck/backend/config"
	"github.com/fausto2022/relaydeck/backend/monitor"
	"github.com/fausto2022/relaydeck/backend/storage"
	"gorm.io/gorm"
)

type blockingMainStation struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

type pricingSyncMainStation struct {
	pricingChanged bool
	profitCalls    atomic.Int32
}

func (f *pricingSyncMainStation) RunDueHealthChecks(context.Context)         {}
func (f *pricingSyncMainStation) CleanupTemporaryAPIKeys(context.Context)    {}
func (f *pricingSyncMainStation) SyncForScheduler(context.Context) bool      { return f.pricingChanged }
func (f *pricingSyncMainStation) RunDueSchedulingReconciles(context.Context) {}
func (f *pricingSyncMainStation) RunDueRankings(context.Context)             {}
func (f *pricingSyncMainStation) RunProfitEvaluation(context.Context)        { f.profitCalls.Add(1) }
func (f *pricingSyncMainStation) RunAutoExpansion(context.Context)           {}

func (f *blockingMainStation) RunDueHealthChecks(context.Context) {
	if f.calls.Add(1) == 1 {
		close(f.started)
	}
	<-f.release
}

func (f *blockingMainStation) CleanupTemporaryAPIKeys(context.Context)    {}
func (f *blockingMainStation) SyncForScheduler(context.Context) bool      { return false }
func (f *blockingMainStation) RunDueSchedulingReconciles(context.Context) {}
func (f *blockingMainStation) RunDueRankings(context.Context)             {}
func (f *blockingMainStation) RunProfitEvaluation(context.Context)        {}
func (f *blockingMainStation) RunAutoExpansion(context.Context)           {}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := storage.Open(storage.DBConfig{
		Driver: storage.DBDriverSQLite,
		Path:   filepath.Join(t.TempDir(), "scheduler-test.db"),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := storage.AutoMigrate(db); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestRunRetentionDeletesAnnouncements(t *testing.T) {
	db := openTestDB(t)
	announcements := storage.NewUpstreamAnnouncements(db)
	notifies := storage.NewNotifications(db)
	monLogs := storage.NewMonitorLogs(db)
	rates := storage.NewRates(db)
	syncLogs := storage.NewUpstreamSyncLogs(db)

	oldTime := time.Now().AddDate(0, 0, -10)
	if _, err := announcements.Sync(1, []storage.UpstreamAnnouncement{{
		ChannelID:   1,
		SourceKey:   "old",
		Content:     "old",
		FirstSeenAt: oldTime,
	}}); err != nil {
		t.Fatalf("sync announcement: %v", err)
	}

	s := New(
		config.SchedulerConfig{
			Retention: config.RetentionConfig{
				AnnouncementsDays: 1,
			},
		},
		&monitor.Service{},
		monLogs,
		syncLogs,
		rates,
		notifies,
		announcements,
		nil,
		nil,
		nil,
		nil,
		config.ProxyConfig{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	s.runRetention()

	list, total, err := announcements.ListPage(1, 10)
	if err != nil {
		t.Fatalf("list announcements: %v", err)
	}
	if total != 0 || len(list) != 0 {
		t.Fatalf("announcements not cleaned: total=%d list=%#v", total, list)
	}
}

func TestRunRetentionDeletesUpstreamSyncLogsWithMonitorLogDays(t *testing.T) {
	db := openTestDB(t)
	monLogs := storage.NewMonitorLogs(db)
	syncLogs := storage.NewUpstreamSyncLogs(db)
	rates := storage.NewRates(db)
	notifies := storage.NewNotifications(db)

	if err := syncLogs.Append(&storage.UpstreamSyncLog{
		SyncGroupID: 1,
		TargetID:    1,
		Action:      "apply",
		Success:     true,
		Message:     "old",
		CreatedAt:   time.Now().AddDate(0, 0, -10),
	}); err != nil {
		t.Fatalf("append old sync log: %v", err)
	}
	if err := syncLogs.Append(&storage.UpstreamSyncLog{
		SyncGroupID: 1,
		TargetID:    1,
		Action:      "apply",
		Success:     true,
		Message:     "new",
		CreatedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("append new sync log: %v", err)
	}

	s := New(
		config.SchedulerConfig{
			Retention: config.RetentionConfig{
				MonitorLogsDays: 1,
			},
		},
		&monitor.Service{},
		monLogs,
		syncLogs,
		rates,
		notifies,
		nil,
		nil,
		nil,
		nil,
		nil,
		config.ProxyConfig{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	s.runRetention()

	list, total, err := syncLogs.ListPage(1, 10)
	if err != nil {
		t.Fatalf("list sync logs: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Message != "new" {
		t.Fatalf("sync logs not cleaned: total=%d list=%#v", total, list)
	}
}

func TestRunMainStationHealthSkipsOverlappingTick(t *testing.T) {
	mainStation := &blockingMainStation{started: make(chan struct{}), release: make(chan struct{})}
	s := New(
		config.SchedulerConfig{}, nil, nil, nil, nil, nil, nil, nil, nil,
		mainStation, nil, config.ProxyConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	done := make(chan struct{})
	go func() {
		s.runMainStationHealth()
		close(done)
	}()
	<-mainStation.started
	s.runMainStationHealth()
	close(mainStation.release)
	<-done
	if calls := mainStation.calls.Load(); calls != 1 {
		t.Fatalf("overlapping main station calls = %d, want 1", calls)
	}
}

func TestRunMainStationHealthEvaluatesProfitOnlyAfterPricingChange(t *testing.T) {
	mainStation := &pricingSyncMainStation{}
	s := New(
		config.SchedulerConfig{}, nil, nil, nil, nil, nil, nil, nil, nil,
		mainStation, nil, config.ProxyConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	s.runMainStationHealth()
	if calls := mainStation.profitCalls.Load(); calls != 0 {
		t.Fatalf("unchanged pricing profit evaluations = %d, want 0", calls)
	}
	mainStation.pricingChanged = true
	s.runMainStationHealth()
	if calls := mainStation.profitCalls.Load(); calls != 1 {
		t.Fatalf("changed pricing profit evaluations = %d, want 1", calls)
	}
}
