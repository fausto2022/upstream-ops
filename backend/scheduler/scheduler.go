// Package scheduler 用 robfig/cron 触发周期性扫描。
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/fausto2022/relaydeck/backend/captcha"
	"github.com/fausto2022/relaydeck/backend/config"
	"github.com/fausto2022/relaydeck/backend/crypto"
	appLogger "github.com/fausto2022/relaydeck/backend/logger"
	"github.com/fausto2022/relaydeck/backend/monitor"
	"github.com/fausto2022/relaydeck/backend/storage"
	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	cfg           config.SchedulerConfig
	log           *slog.Logger
	cron          *cron.Cron
	monitor       *monitor.Service
	monLogs       *storage.MonitorLogs
	syncLogs      *storage.UpstreamSyncLogs
	rates         *storage.Rates
	notifies      *storage.Notifications
	announcements *storage.UpstreamAnnouncements
	captchas      *storage.Captchas
	cipher        *crypto.Cipher
	mainStation   mainStationHealthService
	backup        databaseBackupService
	proxy         config.ProxyConfig
	balanceMu     sync.Mutex
	ratesMu       sync.Mutex
	retentionMu   sync.Mutex
	mainHealthMu  sync.Mutex
	mainOpsMu     sync.Mutex
}

type mainStationHealthService interface {
	RunDueHealthChecks(ctx context.Context)
	CleanupTemporaryAPIKeys(ctx context.Context)
	SyncForScheduler(ctx context.Context) bool
	RunDueSchedulingReconciles(ctx context.Context)
	RunDueRankings(ctx context.Context)
	RunProfitEvaluation(ctx context.Context)
	RunAutoExpansion(ctx context.Context)
}

type mainStationRetentionService interface {
	DeleteHistoryBefore(cutoff time.Time) (storage.MainStationRetentionResult, error)
}

type databaseBackupService interface {
	Backup() (string, error)
}

const mainStationTaskTimeout = 2 * time.Minute

func New(
	cfg config.SchedulerConfig,
	m *monitor.Service,
	monLogs *storage.MonitorLogs,
	syncLogs *storage.UpstreamSyncLogs,
	rates *storage.Rates,
	notifies *storage.Notifications,
	announcements *storage.UpstreamAnnouncements,
	captchas *storage.Captchas,
	cipher *crypto.Cipher,
	mainStation mainStationHealthService,
	backup databaseBackupService,
	proxy config.ProxyConfig,
	log *slog.Logger,
) *Scheduler {
	return &Scheduler{
		cfg:           cfg,
		log:           log,
		cron:          cron.New(cron.WithSeconds()),
		monitor:       m,
		monLogs:       monLogs,
		syncLogs:      syncLogs,
		rates:         rates,
		notifies:      notifies,
		announcements: announcements,
		captchas:      captchas,
		cipher:        cipher,
		mainStation:   mainStation,
		backup:        backup,
		proxy:         proxy,
	}
}

func (s *Scheduler) Start() error {
	if s.cfg.BalanceCron != "" {
		if _, err := s.cron.AddFunc(s.cfg.BalanceCron, s.runBalance); err != nil {
			return err
		}
	}
	if s.cfg.RateCron != "" {
		if _, err := s.cron.AddFunc(s.cfg.RateCron, s.runRates); err != nil {
			return err
		}
	}
	if s.cfg.Retention.Cron != "" && s.hasRetention() {
		if _, err := s.cron.AddFunc(s.cfg.Retention.Cron, s.runRetention); err != nil {
			return err
		}
	}
	if s.mainStation != nil {
		if _, err := s.cron.AddFunc("@every 1s", s.runMainStationHealth); err != nil {
			return err
		}
		if _, err := s.cron.AddFunc("@every 1s", s.runMainStationMaintenance); err != nil {
			return err
		}
	}
	s.cron.Start()
	s.log.Info("scheduler started",
		"balanceCron", s.cfg.BalanceCron,
		"rateCron", s.cfg.RateCron,
		"retentionCron", s.cfg.Retention.Cron,
		"concurrency", s.cfg.Concurrency,
	)
	return nil
}

func (s *Scheduler) runMainStationHealth() {
	if !s.mainHealthMu.TryLock() {
		return
	}
	defer s.mainHealthMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s.mainStation.RunDueHealthChecks(ctx)
}

func (s *Scheduler) runMainStationMaintenance() {
	if !s.mainOpsMu.TryLock() {
		return
	}
	defer s.mainOpsMu.Unlock()
	s.runMainStationTask(s.mainStation.CleanupTemporaryAPIKeys)
	pricingChanged := false
	s.runMainStationTask(func(ctx context.Context) {
		pricingChanged = s.mainStation.SyncForScheduler(ctx)
	})
	if pricingChanged {
		s.runMainStationTask(s.mainStation.RunProfitEvaluation)
	}
	s.runMainStationTask(s.mainStation.RunDueSchedulingReconciles)
	s.runMainStationTask(s.mainStation.RunDueRankings)
}

func (s *Scheduler) runMainStationTask(run func(context.Context)) {
	ctx, cancel := context.WithTimeout(context.Background(), mainStationTaskTimeout)
	defer cancel()
	run(ctx)
}

func (s *Scheduler) Stop() {
	if s.cron != nil {
		<-s.cron.Stop().Done()
	}
}

func (s *Scheduler) runBalance() {
	if !s.balanceMu.TryLock() {
		return
	}
	defer s.balanceMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	s.monitor.ScanAllBalances(ctx)
	if s.captchas != nil && s.cipher != nil {
		if _, err := captcha.RefreshAllBalancesWithProxy(ctx, s.captchas, s.cipher, s.log, s.proxy); err != nil {
			s.log.Warn("refresh captcha balances failed", "err", err)
		}
	}
}

func (s *Scheduler) runRates() {
	if !s.ratesMu.TryLock() {
		return
	}
	defer s.ratesMu.Unlock()
	if s.monitor != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		s.monitor.ScanAllRates(ctx)
		cancel()
	}
	if s.mainStation != nil {
		s.mainOpsMu.Lock()
		defer s.mainOpsMu.Unlock()
		s.runMainStationTask(func(ctx context.Context) { s.mainStation.SyncForScheduler(ctx) })
		s.runMainStationTask(s.mainStation.RunProfitEvaluation)
		s.runMainStationTask(s.mainStation.RunAutoExpansion)
	}
}

func (s *Scheduler) hasRetention() bool {
	r := s.cfg.Retention
	return r.RuntimeLogsDays > 0 ||
		r.MonitorLogsDays > 0 ||
		r.BalanceSnapshotsDays > 0 ||
		r.NotificationLogsDays > 0 ||
		r.AnnouncementsDays > 0 ||
		r.MainStationLogsDays > 0 ||
		s.backup != nil
}

// runRetention 按配置删除过期历史。任一表失败不影响其它，全部错误写日志。
func (s *Scheduler) runRetention() {
	if !s.retentionMu.TryLock() {
		return
	}
	defer s.retentionMu.Unlock()
	r := s.cfg.Retention
	now := time.Now()
	if r.RuntimeLogsDays > 0 {
		cutoff := now.AddDate(0, 0, -r.RuntimeLogsDays)
		n, err := appLogger.DeleteRuntimeLogsBefore(cutoff)
		if err != nil {
			s.log.Warn("retention runtime logs failed", "err", err)
		} else if n > 0 {
			s.log.Info("retention runtime logs deleted", "files", n, "before", cutoff)
		}
	}

	if r.MonitorLogsDays > 0 {
		cutoff := now.AddDate(0, 0, -r.MonitorLogsDays)
		n, err := s.monLogs.DeleteBefore(cutoff)
		if err != nil {
			s.log.Warn("retention monitor_logs failed", "err", err)
		} else if n > 0 {
			s.log.Info("retention monitor_logs deleted", "rows", n, "before", cutoff)
		}
		if s.syncLogs != nil {
			n, err = s.syncLogs.DeleteBefore(cutoff)
			if err != nil {
				s.log.Warn("retention upstream_sync_logs failed", "err", err)
			} else if n > 0 {
				s.log.Info("retention upstream_sync_logs deleted", "rows", n, "before", cutoff)
			}
		}
	}

	if r.BalanceSnapshotsDays > 0 {
		cutoff := now.AddDate(0, 0, -r.BalanceSnapshotsDays)
		n, err := s.rates.DeleteBalanceSnapshotsBefore(cutoff)
		if err != nil {
			s.log.Warn("retention balance_snapshots failed", "err", err)
		} else if n > 0 {
			s.log.Info("retention balance_snapshots deleted", "rows", n, "before", cutoff)
		}

		n, err = s.rates.DeleteCostSnapshotsBefore(cutoff)
		if err != nil {
			s.log.Warn("retention cost_snapshots failed", "err", err)
		} else if n > 0 {
			s.log.Info("retention cost_snapshots deleted", "rows", n, "before", cutoff)
		}
	}

	if r.NotificationLogsDays > 0 {
		cutoff := now.AddDate(0, 0, -r.NotificationLogsDays)
		n, err := s.notifies.DeleteLogsBefore(cutoff)
		if err != nil {
			s.log.Warn("retention notification_logs failed", "err", err)
		} else if n > 0 {
			s.log.Info("retention notification_logs deleted", "rows", n, "before", cutoff)
		}
		n, err = s.notifies.DeleteEventsBefore(cutoff)
		if err != nil {
			s.log.Warn("retention alert_events failed", "err", err)
		} else if n > 0 {
			s.log.Info("retention alert_events deleted", "rows", n, "before", cutoff)
		}
	}

	if r.AnnouncementsDays > 0 && s.announcements != nil {
		cutoff := now.AddDate(0, 0, -r.AnnouncementsDays)
		n, err := s.announcements.DeleteBefore(cutoff)
		if err != nil {
			s.log.Warn("retention announcements failed", "err", err)
		} else if n > 0 {
			s.log.Info("retention announcements deleted", "rows", n, "before", cutoff)
		}
	}

	if r.MainStationLogsDays > 0 && s.mainStation != nil {
		if retention, ok := s.mainStation.(mainStationRetentionService); ok {
			cutoff := now.AddDate(0, 0, -r.MainStationLogsDays)
			result, err := retention.DeleteHistoryBefore(cutoff)
			if err != nil {
				s.log.Warn("retention main station history failed", "err", err)
			} else {
				deleted := result.HealthChecks + result.ProfitChecks + result.ProfitSnapshots + result.AuditLogs
				if deleted > 0 {
					s.log.Info("retention main station history deleted", "rows", deleted, "before", cutoff)
				}
			}
		}
	}

	if s.backup != nil {
		path, err := s.backup.Backup()
		if err != nil {
			s.log.Warn("sqlite backup failed", "err", err)
		} else if path != "" {
			s.log.Info("sqlite backup completed", "path", path)
		}
	}
}
