package storage

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mysqlDriver "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestMainStationHealthStrategyMigrationUpdatesPreviousDefault(t *testing.T) {
	db, err := Open(DBConfig{
		Driver:       DBDriverSQLite,
		Path:         filepath.Join(t.TempDir(), "health-strategy.db"),
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql database: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Exec(`CREATE TABLE main_station_configs (
		id integer primary key,
		target_id integer not null,
		enabled numeric not null default 1,
		health_models_json text not null default '{}',
		health_interval_seconds integer not null default 300,
		created_at datetime,
		updated_at datetime
	)`).Error; err != nil {
		t.Fatalf("create previous config schema: %v", err)
	}
	if err := db.Exec(`INSERT INTO main_station_configs (id, target_id, enabled, health_interval_seconds) VALUES (1, 1, 1, 300)`).Error; err != nil {
		t.Fatalf("insert previous config: %v", err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("migrate health strategy: %v", err)
	}
	var config MainStationConfig
	if err := db.First(&config, MainStationSingletonID).Error; err != nil {
		t.Fatalf("load migrated config: %v", err)
	}
	if config.HealthIntervalSeconds != defaultMainStationHealthIntervalSeconds || config.HealthFailureThreshold != 10 || config.HealthRecoveryThreshold != 3 {
		t.Fatalf("migrated health strategy = %#v", config)
	}
}

func TestEmptyDatabaseCreatesMainStationSchemaWithoutConfiguration(t *testing.T) {
	db := openTestDB(t)
	models := []any{
		&MainStationConfig{},
		&MainStationAccountSnapshot{},
		&MainAccountPool{},
		&MainAccountPoolMember{},
		&MainAccountHealthCheck{},
		&MainAccountProfitCheck{},
		&MainAccountGuardLock{},
		&MainAccountAuditLog{},
	}
	for _, model := range models {
		if !db.Migrator().HasTable(model) {
			t.Fatalf("table for %T was not created", model)
		}
	}
	var count int64
	if err := db.Model(&MainStationConfig{}).Count(&count).Error; err != nil {
		t.Fatalf("count main station configs: %v", err)
	}
	if count != 0 {
		t.Fatalf("empty database created %d main station configs", count)
	}
}

func TestMainStationModelsUseMySQLCompatibleUpsertAndIndexes(t *testing.T) {
	db, err := gorm.Open(mysqlDriver.New(mysqlDriver.Config{
		DSN:                       "user:password@tcp(127.0.0.1:3306)/upstreamops?charset=utf8mb4&parseTime=True&loc=Local",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		DryRun:                 true,
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
	})
	if err != nil {
		t.Fatalf("open mysql dry-run database: %v", err)
	}

	statement := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"status", "detail", "updated_at"}),
	}).Create(&MainStationMigrationState{ID: MainStationSingletonID, Status: "completed"}).Statement
	if statement.Error != nil {
		t.Fatalf("build mysql upsert: %v", statement.Error)
	}
	if sql := statement.SQL.String(); !strings.Contains(sql, "ON DUPLICATE KEY UPDATE") {
		t.Fatalf("mysql upsert SQL = %q", sql)
	}

	lockStatement := &gorm.Statement{DB: db}
	if err := lockStatement.Parse(&MainAccountGuardLock{}); err != nil {
		t.Fatalf("parse guard lock schema: %v", err)
	}
	index := lockStatement.Schema.LookIndex("idx_main_account_lock")
	if index == nil || index.Class != "UNIQUE" || index.Where != "" || len(index.Fields) != 2 {
		t.Fatalf("guard lock unique index = %#v", index)
	}
}

func TestLegacyMainStationMigrationIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	now := time.Now()
	target := &UpstreamSyncTarget{
		Name:              "main",
		BaseURL:           "https://main.example.com",
		AdminAPIKeyCipher: "cipher",
		Enabled:           true,
	}
	if err := db.Create(target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}
	targetGroup := &UpstreamSyncTargetGroup{
		TargetID:      target.ID,
		RemoteGroupID: 101,
		Name:          "default",
		Ratio:         1,
		Status:        "active",
	}
	if err := db.Create(targetGroup).Error; err != nil {
		t.Fatalf("create target group: %v", err)
	}
	legacyGroup := &UpstreamSyncGroup{
		DisplayName:        "生产池",
		NameTemplate:       "managed-{channel}",
		Name:               "managed-main",
		TargetID:           target.ID,
		TargetGroupIDsJSON: mustUintArray([]uint{targetGroup.ID}),
		Platform:           "openai",
		ModelLimitsMode:    "sync_upstream",
		RateSortDirection:  "asc",
		Enabled:            true,
	}
	if err := db.Create(legacyGroup).Error; err != nil {
		t.Fatalf("create legacy group: %v", err)
	}
	sourceGroupID := int64(9)
	legacyAccount := &UpstreamSyncAccount{
		SyncGroupID:      legacyGroup.ID,
		SourceChannelID:  7,
		SourceGroupID:    &sourceGroupID,
		SourceGroupName:  "source-default",
		Concurrency:      12,
		Weight:           3,
		RateConvertMode:  "multiply",
		RateConvertValue: 0.8,
		Enabled:          true,
		TestEnabled:      true,
		TestModel:        "gpt-test",
	}
	if err := db.Create(legacyAccount).Error; err != nil {
		t.Fatalf("create legacy account: %v", err)
	}
	managed := &UpstreamSyncManagedAccount{
		SyncGroupID:        legacyGroup.ID,
		SyncAccountID:      legacyAccount.ID,
		SourceAPIKeyID:     88,
		SourceAPIKeyName:   "managed-key",
		TargetAccountID:    501,
		TargetAccountName:  "remote-account",
		TargetGroupIDsJSON: mustUintArray([]uint{targetGroup.ID}),
		LastAppliedAt:      &now,
	}
	if err := db.Create(managed).Error; err != nil {
		t.Fatalf("create managed mapping: %v", err)
	}

	for run := 1; run <= 2; run++ {
		if err := AutoMigrate(db); err != nil {
			t.Fatalf("auto migrate run %d: %v", run, err)
		}
	}

	store := NewMainStationStore(db)
	config, err := store.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if config.ID != MainStationSingletonID || config.TargetID != target.ID {
		t.Fatalf("config = %#v", config)
	}
	if config.AutoMarginProtection || config.AutoHealthProtection || config.AutoRecovery {
		t.Fatalf("automatic protection enabled after migration: %#v", config)
	}

	var pools []MainAccountPool
	if err := db.Find(&pools).Error; err != nil {
		t.Fatalf("list pools: %v", err)
	}
	if len(pools) != 1 || pools[0].LegacySyncGroupID == nil || *pools[0].LegacySyncGroupID != legacyGroup.ID {
		t.Fatalf("pools = %#v", pools)
	}
	groupIDs, err := store.ListPoolGroupIDs(pools[0].ID)
	if err != nil {
		t.Fatalf("list pool groups: %v", err)
	}
	if len(groupIDs) != 1 || groupIDs[0] != targetGroup.ID {
		t.Fatalf("pool group ids = %#v", groupIDs)
	}
	members, err := store.ListMembers(pools[0].ID)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("members = %#v", members)
	}
	member := members[0]
	if member.LegacySyncAccountID == nil || *member.LegacySyncAccountID != legacyAccount.ID {
		t.Fatalf("legacy account mapping = %#v", member.LegacySyncAccountID)
	}
	if member.RemoteAccountID == nil || *member.RemoteAccountID != managed.TargetAccountID {
		t.Fatalf("remote account mapping = %#v", member.RemoteAccountID)
	}
	if member.BindingStatus != "verified" || member.OwnershipMode != "managed" || member.Status != "active" {
		t.Fatalf("member state = %#v", member)
	}
	if member.RateConvertValueMicros != 800000 {
		t.Fatalf("rate convert micros = %d", member.RateConvertValueMicros)
	}

	state, err := store.GetMigrationState()
	if err != nil {
		t.Fatalf("get migration state: %v", err)
	}
	if state.Status != "completed" {
		t.Fatalf("migration state = %#v", state)
	}
}

func TestLegacyMainStationMigrationRequiresConfirmationForMultipleTargets(t *testing.T) {
	db := openTestDB(t)
	for i := 1; i <= 2; i++ {
		target := &UpstreamSyncTarget{
			Name:              "target-" + string(rune('0'+i)),
			BaseURL:           "https://example.com",
			AdminAPIKeyCipher: "cipher",
			Enabled:           true,
		}
		if err := db.Create(target).Error; err != nil {
			t.Fatalf("create target %d: %v", i, err)
		}
		group := &UpstreamSyncGroup{
			DisplayName:        target.Name,
			NameTemplate:       target.Name,
			Name:               target.Name,
			TargetID:           target.ID,
			TargetGroupIDsJSON: "[]",
			Platform:           "openai",
			ModelLimitsMode:    "sync_upstream",
			RateSortDirection:  "asc",
			Enabled:            true,
		}
		if err := db.Create(group).Error; err != nil {
			t.Fatalf("create group %d: %v", i, err)
		}
	}

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	store := NewMainStationStore(db)
	if _, err := store.GetConfig(); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("config error = %v, want record not found", err)
	}
	state, err := store.GetMigrationState()
	if err != nil {
		t.Fatalf("get migration state: %v", err)
	}
	if state.Status != "requires_confirmation" {
		t.Fatalf("migration state = %#v", state)
	}
}

func TestMainAccountMemberRemoteAccountIsUnique(t *testing.T) {
	db := openTestDB(t)
	store := NewMainStationStore(db)
	pool := &MainAccountPool{Name: "pool", Enabled: true}
	if err := store.CreatePool(pool, nil); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	remoteAccountID := int64(42)
	first := &MainAccountPoolMember{
		PoolID:          pool.ID,
		SourceChannelID: 1,
		RemoteAccountID: &remoteAccountID,
		OwnershipMode:   "bound",
		BindingStatus:   "manual_confirmed",
		Status:          "active",
		Enabled:         true,
	}
	if err := store.CreateMember(first); err != nil {
		t.Fatalf("create first member: %v", err)
	}
	second := *first
	second.ID = 0
	second.SourceChannelID = 2
	if err := store.CreateMember(&second); err == nil {
		t.Fatal("create second member succeeded, want unique constraint error")
	}
}
