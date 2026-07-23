package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fausto2022/relaydeck/backend/api"
	"github.com/fausto2022/relaydeck/backend/auth"
	"github.com/fausto2022/relaydeck/backend/channel"
	"github.com/fausto2022/relaydeck/backend/config"
	"github.com/fausto2022/relaydeck/backend/crypto"
	"github.com/fausto2022/relaydeck/backend/logger"
	"github.com/fausto2022/relaydeck/backend/mainstation"
	"github.com/fausto2022/relaydeck/backend/monitor"
	"github.com/fausto2022/relaydeck/backend/notify"
	"github.com/fausto2022/relaydeck/backend/rateranking"
	"github.com/fausto2022/relaydeck/backend/runtimeconfig"
	"github.com/fausto2022/relaydeck/backend/scheduler"
	"github.com/fausto2022/relaydeck/backend/storage"
	"github.com/fausto2022/relaydeck/backend/syncer"
	"github.com/fausto2022/relaydeck/web"
	"github.com/gin-gonic/gin"

	// 注册 connector 实现。
	_ "github.com/fausto2022/relaydeck/backend/connector/newapi"
	_ "github.com/fausto2022/relaydeck/backend/connector/sub2api"
)

func main() {
	configPath := flag.String("config", "", "path to config.yaml (optional; env vars also supported)")
	flag.Parse()

	cfg, usedConfigPath, err := config.LoadWithPath(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config failed: %v\n", err)
		os.Exit(1)
	}
	resolvedConfigPath := config.ResolvePath(*configPath, usedConfigPath)

	logDir := filepath.Join(filepath.Dir(cfg.Database.Path), "logs")
	if cfg.Database.Driver != "sqlite" {
		logDir = filepath.Join("data", "logs")
	}
	log := logger.New(cfg.Log.Level, cfg.Log.Format, logDir)
	log.Info("starting RelayDeck", "port", cfg.Server.Port, "mode", cfg.Server.Mode)

	if _, err := os.Stat(resolvedConfigPath); errors.Is(err, os.ErrNotExist) {
		if err := config.Save(resolvedConfigPath, cfg); err != nil {
			log.Error("create config failed", "path", resolvedConfigPath, "err", err)
			os.Exit(1)
		}
		log.Info("config created", "path", resolvedConfigPath)
	}

	cipher, err := crypto.NewCipher(cfg.Security.AppSecret)
	if err != nil {
		log.Error("init cipher failed (set APP_SECRET)", "err", err)
		os.Exit(1)
	}

	// Auth：默认禁用（AUTH_ENABLED=false），所有 /api/* 免 token；
	// 显式开启时账号/密码必填，token secret 缺省回退到 AppSecret。
	var authSvc *auth.Service
	if cfg.Auth.Enabled {
		tokenSecret := cfg.Auth.TokenSecret
		if tokenSecret == "" {
			tokenSecret = cfg.Security.AppSecret
		}
		authSvc, err = auth.New(
			cfg.Auth.Username,
			cfg.Auth.Password,
			tokenSecret,
			time.Duration(cfg.Auth.SessionTTLHours)*time.Hour,
		)
		if err != nil {
			log.Error("init auth failed (set ADMIN_USERNAME / ADMIN_PASSWORD or AUTH_ENABLED=false)", "err", err)
			os.Exit(1)
		}
		log.Info("auth enabled", "username", cfg.Auth.Username)
	} else {
		log.Warn("auth disabled — all /api/* endpoints are open; set AUTH_ENABLED=true for production exposure")
	}

	db, err := storage.Open(cfg.Database.ToStorageConfig())
	if err != nil {
		log.Error("open database failed", "err", err)
		os.Exit(1)
	}
	if err := storage.AutoMigrate(db); err != nil {
		log.Error("auto migrate failed", "err", err)
		os.Exit(1)
	}

	channels := storage.NewChannels(db)
	authSessions := storage.NewAuthSessions(db)
	captchas := storage.NewCaptchas(db)
	notifies := storage.NewNotifications(db)
	announcements := storage.NewUpstreamAnnouncements(db)
	rates := storage.NewRates(db)
	rateRankingSvc := rateranking.New(storage.NewRateRankingConfigs(db))
	monLogs := storage.NewMonitorLogs(db)
	syncTargets := storage.NewUpstreamSyncTargets(db)
	syncGroups := storage.NewUpstreamSyncTargetGroups(db)
	upstreamSyncGroups := storage.NewUpstreamSyncGroups(db)
	upstreamSyncAccounts := storage.NewUpstreamSyncAccounts(db)
	managedSyncAccounts := storage.NewUpstreamSyncManagedAccounts(db)
	syncLogs := storage.NewUpstreamSyncLogs(db)
	mainStationStore := storage.NewMainStationStore(db)

	channelSvc := channel.NewService(channels, authSessions, captchas, rates, monLogs, cipher)
	channelSvc.UpdateProxyConfig(cfg.Proxy)
	channelSvc.UpdateUpstreamConfig(cfg.Upstream)
	dispatcher := notify.NewDispatcher(notifies, cipher, log, notify.Policy{
		DisabledEvents:                           append([]storage.NotificationEvent(nil), cfg.Notifications.DisabledEvents...),
		BatchRateChanges:                         cfg.Notifications.BatchRateChanges,
		MinChangePct:                             cfg.Notifications.MinChangePct,
		BalanceLowCooldown:                       time.Duration(cfg.Notifications.BalanceLowCooldownMinutes) * time.Minute,
		SubscriptionDailyRemainingThresholdPct:   cfg.Notifications.SubscriptionDailyRemainingThresholdPct,
		SubscriptionWeeklyRemainingThresholdPct:  cfg.Notifications.SubscriptionWeeklyRemainingThresholdPct,
		SubscriptionMonthlyRemainingThresholdPct: cfg.Notifications.SubscriptionMonthlyRemainingThresholdPct,
		SubscriptionExpiryThreshold:              time.Duration(cfg.Notifications.SubscriptionExpiryThresholdHours) * time.Hour,
		SubscriptionAlertCooldown:                time.Duration(cfg.Notifications.SubscriptionAlertCooldownMinutes) * time.Minute,
		SendMaxAttempts:                          cfg.Notifications.SendMaxAttempts,
	})
	dispatcher.UpdateProxyConfig(cfg.Proxy)
	monitorSvc := monitor.NewService(channels, announcements, rates, monLogs, channelSvc, dispatcher, log)
	syncSvc := syncer.New(channels, rates, cipher, channelSvc, log, syncTargets, syncGroups, upstreamSyncGroups, upstreamSyncAccounts, managedSyncAccounts, syncLogs)
	syncSvc.SetDispatcher(dispatcher)
	mainStationSvc := mainstation.New(mainStationStore, syncTargets, syncGroups, channels, rates, managedSyncAccounts, cipher, channelSvc, log)
	mainStationSvc.SetDispatcher(dispatcher)
	mainStationSvc.UpdateProbeConfig(cfg.Proxy, time.Duration(cfg.Upstream.TimeoutSeconds)*time.Second, cfg.Upstream.UserAgent)
	syncSvc.SetSchedulingGuard(mainStationSvc)
	sqliteBackups := storage.NewSQLiteBackups(db, cfg.Database.ToStorageConfig(), storage.DefaultSQLiteBackupKeep)
	if sqliteBackups != nil {
		if path, backupErr := sqliteBackups.Backup(); backupErr != nil {
			log.Warn("startup sqlite backup failed", "err", backupErr)
		} else {
			log.Info("startup sqlite backup completed", "path", path)
		}
	}

	schedulerFactory := func(scfg config.SchedulerConfig, pcfg config.ProxyConfig) *scheduler.Scheduler {
		return scheduler.New(scfg, monitorSvc, monLogs, syncLogs, rates, notifies, announcements, captchas, cipher, mainStationSvc, sqliteBackups, pcfg, log)
	}
	sch := schedulerFactory(cfg.Scheduler, cfg.Proxy)
	if err := sch.Start(); err != nil {
		log.Error("start scheduler failed", "err", err)
		os.Exit(1)
	}
	defer sch.Stop()

	runtimeMgr := runtimeconfig.New(
		resolvedConfigPath,
		cfg.Security.AppSecret,
		log,
		dispatcher,
		channelSvc,
		authSvc,
		sch,
		cfg.Proxy,
		cfg.Upstream,
		schedulerFactory,
	)
	runtimeMgr.SetProbeConfigUpdater(mainStationSvc)

	gin.SetMode(cfg.Server.Mode)
	router := gin.New()
	router.Use(gin.Recovery())
	if len(cfg.Server.TrustedProxies) > 0 {
		_ = router.SetTrustedProxies(cfg.Server.TrustedProxies)
	}

	// 仅在嵌入了真实前端产物时挂载静态 handler。
	// 本地 `go run` 跑出来的二进制 dist 是空占位，此时由 vite dev server 接管 :3010。
	var frontendFS fs.FS
	if web.HasFrontend() {
		frontendFS = web.DistFS()
		log.Info("frontend embedded, serving SPA on /")
	} else {
		log.Info("no embedded frontend, run vite dev server separately for UI")
	}

	api.Register(router, &api.Deps{
		DB:            db,
		Cipher:        cipher,
		Runtime:       runtimeMgr,
		Channels:      channels,
		Sessions:      authSessions,
		Captchas:      captchas,
		Notifies:      notifies,
		Announcements: announcements,
		Rates:         rates,
		RateRanking:   rateRankingSvc,
		MonLogs:       monLogs,
		ChannelSvc:    channelSvc,
		Monitor:       monitorSvc,
		Dispatcher:    dispatcher,
		UpstreamSync:  syncSvc,
		MainStation:   mainStationSvc,
		Log:           log,
		Frontend:      frontendFS,
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server error", "err", err)
			os.Exit(1)
		}
	}()
	log.Info("http server listening", "addr", srv.Addr)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Info("shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("http shutdown error", "err", err)
	}
	log.Info("bye")
}
