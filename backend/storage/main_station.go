package storage

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultMainHealthPolicy                 = `{"mode":"observe","l0_interval_minutes":5,"l1_interval_minutes":30,"l2_interval_minutes":720,"jitter_percent":10,"transient_failure_threshold":3,"empty_failure_threshold":2,"recovery_success_threshold":3,"window_size":20,"global_concurrency":4,"channel_concurrency":1,"daily_l1_limit":48,"daily_l2_limit":2}`
	defaultMainMarginPolicy                 = `{"mode":"observe","minimum_margin_basis_points":0,"risk_confirmations":2,"cost_max_age_minutes":60}`
	defaultMainStationHealthIntervalSeconds = 30
)

type MainStationStore struct{ db *gorm.DB }

func NewMainStationStore(db *gorm.DB) *MainStationStore { return &MainStationStore{db: db} }

func (r *MainStationStore) CreateConfigWithTarget(target *UpstreamSyncTarget, config *MainStationConfig) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&MainStationConfig{}).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errors.New("main station already configured")
		}
		if err := tx.Select("*").Create(target).Error; err != nil {
			return err
		}
		config.ID = MainStationSingletonID
		config.TargetID = target.ID
		return tx.Select("*").Create(config).Error
	})
}

func (r *MainStationStore) AttachConfigToTarget(target *UpstreamSyncTarget, config *MainStationConfig) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&MainStationConfig{}).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errors.New("main station already configured")
		}
		if err := tx.Save(target).Error; err != nil {
			return err
		}
		config.ID = MainStationSingletonID
		config.TargetID = target.ID
		return tx.Select("*").Create(config).Error
	})
}

func (r *MainStationStore) UpdateConfigWithTarget(target *UpstreamSyncTarget, config *MainStationConfig) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var existing MainStationConfig
		if err := tx.First(&existing, MainStationSingletonID).Error; err != nil {
			return err
		}
		if existing.TargetID != target.ID {
			return errors.New("main station target cannot be replaced by update")
		}
		if err := tx.Save(target).Error; err != nil {
			return err
		}
		config.ID = MainStationSingletonID
		config.TargetID = target.ID
		config.CreatedAt = existing.CreatedAt
		return tx.Save(config).Error
	})
}

func (r *MainStationStore) GetConfig() (*MainStationConfig, error) {
	var item MainStationConfig
	if err := r.db.First(&item, MainStationSingletonID).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *MainStationStore) SaveConfig(item *MainStationConfig) error {
	item.ID = MainStationSingletonID
	return r.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&MainStationConfig{}).Where("id <> ?", MainStationSingletonID).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return errors.New("main station singleton constraint violated")
		}
		var existing MainStationConfig
		err := tx.First(&existing, MainStationSingletonID).Error
		switch {
		case err == nil:
			item.CreatedAt = existing.CreatedAt
			return tx.Save(item).Error
		case errors.Is(err, gorm.ErrRecordNotFound):
			return tx.Select("*").Create(item).Error
		default:
			return err
		}
	})
}

func (r *MainStationStore) UpdateSyncStatus(status string, at *time.Time, errText string) error {
	return r.db.Model(&MainStationConfig{}).Where("id = ?", MainStationSingletonID).Updates(map[string]any{
		"last_sync_status": status,
		"last_sync_at":     at,
		"last_sync_error":  errText,
	}).Error
}

func (r *MainStationStore) MarkPoolRankingDirty(poolID uint, at time.Time) error {
	return r.db.Model(&MainAccountPool{}).Where("id = ?", poolID).Updates(map[string]any{
		"ranking_dirty_at": at,
	}).Error
}

func (r *MainStationStore) MarkAllPoolRankingsDirty(at time.Time) error {
	return r.db.Model(&MainAccountPool{}).Where("1 = 1").Updates(map[string]any{
		"ranking_dirty_at": at,
	}).Error
}

func (r *MainStationStore) CompletePoolRanking(poolID uint, startedAt, finishedAt time.Time, errText string) error {
	updates := map[string]any{
		"last_ranking_at":    finishedAt,
		"last_ranking_error": strings.TrimSpace(errText),
	}
	if strings.TrimSpace(errText) == "" {
		updates["ranking_dirty_at"] = gorm.Expr("CASE WHEN ranking_dirty_at IS NULL OR ranking_dirty_at <= ? THEN NULL ELSE ranking_dirty_at END", startedAt)
	}
	return r.db.Model(&MainAccountPool{}).Where("id = ?", poolID).Updates(updates).Error
}

func (r *MainStationStore) MarkMemberSchedulingDirty(memberID uint, at time.Time) error {
	return r.db.Model(&MainAccountPoolMember{}).Where("id = ?", memberID).Updates(map[string]any{
		"scheduling_dirty_at": at,
	}).Error
}

func (r *MainStationStore) CompleteMemberScheduling(memberID uint, startedAt, finishedAt time.Time, errText string) error {
	updates := map[string]any{
		"last_scheduling_at":    finishedAt,
		"last_scheduling_error": strings.TrimSpace(errText),
	}
	if strings.TrimSpace(errText) == "" {
		updates["scheduling_dirty_at"] = gorm.Expr("CASE WHEN scheduling_dirty_at IS NULL OR scheduling_dirty_at <= ? THEN NULL ELSE scheduling_dirty_at END", startedAt)
	}
	return r.db.Model(&MainAccountPoolMember{}).Where("id = ?", memberID).Updates(updates).Error
}

func (r *MainStationStore) ListSchedulingDirtyMembers() ([]MainAccountPoolMember, error) {
	var list []MainAccountPoolMember
	err := r.db.Where("scheduling_dirty_at IS NOT NULL AND remote_account_id IS NOT NULL").
		Where("binding_status NOT IN ?", []string{"invalid", "orphaned"}).
		Order("scheduling_dirty_at ASC, id ASC").Find(&list).Error
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (r *MainStationStore) GetMigrationState() (*MainStationMigrationState, error) {
	var item MainStationMigrationState
	if err := r.db.First(&item, MainStationSingletonID).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *MainStationStore) ListAccountSnapshots(page, pageSize int, includeMissing, unboundOnly bool) ([]MainStationAccountSnapshot, int64, error) {
	page, pageSize = normalizeStoragePage(page, pageSize)
	q := r.db.Model(&MainStationAccountSnapshot{}).Where("main_station_id = ?", MainStationSingletonID)
	if !includeMissing {
		q = q.Where("missing = ?", false)
	}
	if unboundOnly {
		q = q.Where("NOT EXISTS (SELECT 1 FROM main_account_pool_members AS members WHERE members.remote_account_id = main_station_account_snapshots.remote_account_id)")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []MainStationAccountSnapshot
	if err := q.Order("remote_account_id ASC, id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (r *MainStationStore) ListAllAccountSnapshots(includeMissing bool) ([]MainStationAccountSnapshot, error) {
	q := r.db.Where("main_station_id = ?", MainStationSingletonID)
	if !includeMissing {
		q = q.Where("missing = ?", false)
	}
	var list []MainStationAccountSnapshot
	if err := q.Order("remote_account_id ASC, id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (r *MainStationStore) MigrateLegacyData() error {
	return migrateLegacyMainStationData(r.db)
}

func (r *MainStationStore) FindAccountSnapshot(remoteAccountID int64) (*MainStationAccountSnapshot, error) {
	var item MainStationAccountSnapshot
	if err := r.db.First(&item, "main_station_id = ? AND remote_account_id = ?", MainStationSingletonID, remoteAccountID).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *MainStationStore) UpsertProfitSnapshot(item *MainStationProfitSnapshot) error {
	item.MainStationID = MainStationSingletonID
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "main_station_id"}, {Name: "day"}},
		DoUpdates: clause.AssignmentColumns([]string{"revenue", "cost", "sampled_at", "updated_at"}),
	}).Create(item).Error
}

func (r *MainStationStore) ListProfitSnapshotsSince(day string) ([]MainStationProfitSnapshot, error) {
	var list []MainStationProfitSnapshot
	if err := r.db.Where("main_station_id = ? AND day >= ?", MainStationSingletonID, day).
		Order("day ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (r *MainStationStore) ReplaceAccountSnapshots(items []MainStationAccountSnapshot, syncedAt time.Time) ([]int64, error) {
	missing := make([]int64, 0)
	err := r.db.Transaction(func(tx *gorm.DB) error {
		remoteIDs := make([]int64, 0, len(items))
		for i := range items {
			items[i].MainStationID = MainStationSingletonID
			items[i].LastSyncAt = syncedAt
			items[i].Missing = false
			remoteIDs = append(remoteIDs, items[i].RemoteAccountID)
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "main_station_id"}, {Name: "remote_account_id"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"name", "notes", "platform", "type", "status", "schedulable", "concurrency",
					"priority", "weight", "rate_multiplier_micros", "group_ids_json", "base_url",
					"credentials_present", "billing_probe_json", "last_used_at", "remote_updated_at",
					"last_sync_at", "missing", "updated_at",
				}),
			}).Create(&items[i]).Error; err != nil {
				return err
			}
		}

		q := tx.Model(&MainStationAccountSnapshot{}).
			Where("main_station_id = ? AND missing = ?", MainStationSingletonID, false)
		if len(remoteIDs) > 0 {
			q = q.Where("remote_account_id NOT IN ?", remoteIDs)
		}
		if err := q.Pluck("remote_account_id", &missing).Error; err != nil {
			return err
		}
		if len(missing) == 0 {
			return nil
		}
		return tx.Model(&MainStationAccountSnapshot{}).
			Where("main_station_id = ? AND remote_account_id IN ?", MainStationSingletonID, missing).
			Updates(map[string]any{"missing": true, "last_sync_at": syncedAt}).Error
	})
	return missing, err
}

func (r *MainStationStore) UpsertAccountSnapshot(item *MainStationAccountSnapshot) error {
	item.MainStationID = MainStationSingletonID
	item.Missing = false
	if item.LastSyncAt.IsZero() {
		item.LastSyncAt = time.Now()
	}
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "main_station_id"}, {Name: "remote_account_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"name", "notes", "platform", "type", "status", "schedulable", "concurrency",
			"priority", "weight", "rate_multiplier_micros", "group_ids_json", "base_url",
			"credentials_present", "billing_probe_json", "last_used_at", "remote_updated_at",
			"last_sync_at", "missing", "updated_at",
		}),
	}).Create(item).Error
}

func (r *MainStationStore) ListPools(page, pageSize int) ([]MainAccountPool, int64, error) {
	page, pageSize = normalizeStoragePage(page, pageSize)
	var total int64
	if err := r.db.Model(&MainAccountPool{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []MainAccountPool
	if err := r.db.Order("id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (r *MainStationStore) ListAllPools() ([]MainAccountPool, error) {
	var list []MainAccountPool
	if err := r.db.Order("id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (r *MainStationStore) FindPool(id uint) (*MainAccountPool, error) {
	var item MainAccountPool
	if err := r.db.First(&item, id).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *MainStationStore) FindPoolByTargetGroupID(targetGroupID uint) (*MainAccountPool, error) {
	var item MainAccountPool
	err := r.db.Table("main_account_pools AS pools").
		Select("pools.*").
		Joins("JOIN main_account_pool_groups AS pool_groups ON pool_groups.pool_id = pools.id").
		Where("pool_groups.target_group_id = ?", targetGroupID).
		Order("pools.id ASC").
		First(&item).Error
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *MainStationStore) CreatePool(item *MainAccountPool, targetGroupIDs []uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		applyPoolDefaults(item)
		if err := tx.Select("*").Create(item).Error; err != nil {
			return err
		}
		return replaceMainPoolGroups(tx, item.ID, targetGroupIDs)
	})
}

func (r *MainStationStore) UpdatePool(item *MainAccountPool, targetGroupIDs []uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		applyPoolDefaults(item)
		if err := tx.Save(item).Error; err != nil {
			return err
		}
		return replaceMainPoolGroups(tx, item.ID, targetGroupIDs)
	})
}

func (r *MainStationStore) DeletePool(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&MainAccountPoolMember{}).Where("pool_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("account pool still has %d members", count)
		}
		if err := tx.Where("pool_id = ?", id).Delete(&MainAccountPoolGroup{}).Error; err != nil {
			return err
		}
		return tx.Delete(&MainAccountPool{}, id).Error
	})
}

func (r *MainStationStore) ListPoolGroupIDs(poolID uint) ([]uint, error) {
	var ids []uint
	if err := r.db.Model(&MainAccountPoolGroup{}).Where("pool_id = ?", poolID).Order("target_group_id ASC").Pluck("target_group_id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *MainStationStore) ListMembers(poolID uint) ([]MainAccountPoolMember, error) {
	var list []MainAccountPoolMember
	if err := r.db.Where("pool_id = ?", poolID).Order("priority ASC, id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (r *MainStationStore) FindMember(poolID, memberID uint) (*MainAccountPoolMember, error) {
	var item MainAccountPoolMember
	if err := r.db.First(&item, "id = ? AND pool_id = ?", memberID, poolID).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *MainStationStore) FindMemberByRemoteAccountID(remoteAccountID int64) (*MainAccountPoolMember, error) {
	var item MainAccountPoolMember
	if err := r.db.First(&item, "remote_account_id = ?", remoteAccountID).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *MainStationStore) CreateMember(item *MainAccountPoolMember) error {
	applyMemberDefaults(item)
	return r.db.Select("*").Create(item).Error
}

func (r *MainStationStore) UpdateMember(item *MainAccountPoolMember) error {
	applyMemberDefaults(item)
	return r.db.Save(item).Error
}

func (r *MainStationStore) DeleteMember(poolID, memberID uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("member_id = ?", memberID).Delete(&MainAccountGuardLock{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ? AND pool_id = ?", memberID, poolID).Delete(&MainAccountPoolMember{}).Error
	})
}

func (r *MainStationStore) AppendAudit(item *MainAccountAuditLog) error {
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	return r.db.Create(item).Error
}

func (r *MainStationStore) AppendHealthCheck(item *MainAccountHealthCheck) error {
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	return r.db.Create(item).Error
}

func (r *MainStationStore) LastHealthCheck(memberID uint, level string) (*MainAccountHealthCheck, error) {
	var item MainAccountHealthCheck
	if err := r.db.Where("member_id = ? AND level = ?", memberID, level).
		Order("created_at DESC, id DESC").First(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *MainStationStore) ListHealthChecks(poolID, memberID uint, level string, page, pageSize int) ([]MainAccountHealthCheck, int64, error) {
	page, pageSize = normalizeStoragePage(page, pageSize)
	q := r.db.Model(&MainAccountHealthCheck{}).Where("pool_id = ?", poolID)
	if memberID != 0 {
		q = q.Where("member_id = ?", memberID)
	}
	if strings.TrimSpace(level) != "" {
		q = q.Where("level = ?", strings.ToUpper(strings.TrimSpace(level)))
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []MainAccountHealthCheck
	if err := q.Order("created_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (r *MainStationStore) ListMemberHealthChecksSince(memberID uint, since time.Time, limit int) ([]MainAccountHealthCheck, error) {
	if limit <= 0 {
		limit = 10000
	}
	var list []MainAccountHealthCheck
	if err := r.db.Where("member_id = ? AND created_at >= ?", memberID, since).
		Order("created_at DESC, id DESC").Limit(limit).Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (r *MainStationStore) ListRecentMemberHealthChecks(memberID uint, limit int) ([]MainAccountHealthCheck, error) {
	if limit <= 0 {
		limit = 20
	}
	var list []MainAccountHealthCheck
	if err := r.db.Where("member_id = ? AND status IN ?", memberID, []string{"success", "failure"}).
		Order("created_at DESC, id DESC").Limit(limit).Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (r *MainStationStore) CountDailyHealthChecks(memberID uint, level string, since time.Time) (int64, error) {
	var count int64
	err := r.db.Model(&MainAccountHealthCheck{}).
		Where("member_id = ? AND level = ? AND created_at >= ? AND status <> ?", memberID, strings.ToUpper(level), since, "skipped_budget").
		Count(&count).Error
	return count, err
}

func (r *MainStationStore) SumDailyHealthTokens(memberID uint, since time.Time) (int64, error) {
	var total int64
	err := r.db.Model(&MainAccountHealthCheck{}).
		Select("COALESCE(SUM(total_tokens), 0)").
		Where("member_id = ? AND created_at >= ?", memberID, since).
		Scan(&total).Error
	return total, err
}

func (r *MainStationStore) UpdateMemberHealth(memberID uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.Model(&MainAccountPoolMember{}).Where("id = ?", memberID).Updates(fields).Error
}

func (r *MainStationStore) UpdateHealthCheckOutcome(id uint, action, message string) error {
	return r.db.Model(&MainAccountHealthCheck{}).Where("id = ?", id).Updates(map[string]any{
		"triggered_action": action,
		"message":          message,
	}).Error
}

func (r *MainStationStore) ListAllMembers() ([]MainAccountPoolMember, error) {
	var list []MainAccountPoolMember
	if err := r.db.Order("pool_id ASC, id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (r *MainStationStore) AppendProfitCheck(item *MainAccountProfitCheck) error {
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	if item.ObservedAt.IsZero() {
		item.ObservedAt = item.CreatedAt
	}
	return r.db.Create(item).Error
}

func (r *MainStationStore) ListProfitChecks(poolID, memberID, targetGroupID uint, page, pageSize int) ([]MainAccountProfitCheck, int64, error) {
	page, pageSize = normalizeStoragePage(page, pageSize)
	q := r.db.Model(&MainAccountProfitCheck{}).Where("pool_id = ?", poolID)
	if memberID != 0 {
		q = q.Where("member_id = ?", memberID)
	}
	if targetGroupID != 0 {
		q = q.Where("target_group_id = ?", targetGroupID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []MainAccountProfitCheck
	if err := q.Order("observed_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (r *MainStationStore) ListProfitChecksSince(memberID, targetGroupID uint, since time.Time, limit int) ([]MainAccountProfitCheck, error) {
	if limit <= 0 {
		limit = 100
	}
	q := r.db.Where("member_id = ? AND observed_at >= ?", memberID, since)
	if targetGroupID != 0 {
		q = q.Where("target_group_id = ?", targetGroupID)
	}
	var list []MainAccountProfitCheck
	if err := q.Order("observed_at DESC, id DESC").Limit(limit).Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (r *MainStationStore) LatestProfitCheck(memberID, targetGroupID uint) (*MainAccountProfitCheck, error) {
	var item MainAccountProfitCheck
	if err := r.db.Where("member_id = ? AND target_group_id = ?", memberID, targetGroupID).
		Order("observed_at DESC, id DESC").First(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *MainStationStore) UpsertGuardLock(item *MainAccountGuardLock) error {
	now := time.Now()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	return r.db.Transaction(func(tx *gorm.DB) error {
		var existing MainAccountGuardLock
		err := tx.First(&existing, "remote_account_id = ? AND lock_type = ?", item.RemoteAccountID, item.LockType).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return tx.Select("*").Create(item).Error
		case err != nil:
			return err
		default:
			item.ID = existing.ID
			item.CreatedAt = existing.CreatedAt
			return tx.Model(&existing).Updates(map[string]any{
				"member_id":     item.MemberID,
				"active":        item.Active,
				"reason":        item.Reason,
				"evidence_json": item.EvidenceJSON,
				"created_by":    item.CreatedBy,
				"updated_at":    item.UpdatedAt,
				"cleared_at":    item.ClearedAt,
				"cleared_by":    item.ClearedBy,
			}).Error
		}
	})
}

func (r *MainStationStore) ListActiveGuardLocks(remoteAccountID int64) ([]MainAccountGuardLock, error) {
	var list []MainAccountGuardLock
	if err := r.db.Where("remote_account_id = ? AND active = ?", remoteAccountID, true).
		Order("lock_type ASC, id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (r *MainStationStore) ListAllActiveGuardLocks() ([]MainAccountGuardLock, error) {
	var list []MainAccountGuardLock
	if err := r.db.Where("active = ?", true).Order("remote_account_id ASC, lock_type ASC, id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (r *MainStationStore) ClearGuardLock(remoteAccountID int64, lockType, clearedBy string) (*MainAccountGuardLock, error) {
	now := time.Now()
	var item MainAccountGuardLock
	if err := r.db.First(&item, "remote_account_id = ? AND lock_type = ?", remoteAccountID, lockType).Error; err != nil {
		return nil, err
	}
	if err := r.db.Model(&item).Updates(map[string]any{
		"active": false, "cleared_at": now, "cleared_by": clearedBy, "updated_at": now,
	}).Error; err != nil {
		return nil, err
	}
	item.Active = false
	item.ClearedAt = &now
	item.ClearedBy = clearedBy
	return &item, nil
}

func (r *MainStationStore) ListAuditLogs(poolID, memberID uint, page, pageSize int) ([]MainAccountAuditLog, int64, error) {
	page, pageSize = normalizeStoragePage(page, pageSize)
	q := r.db.Model(&MainAccountAuditLog{})
	if poolID != 0 {
		q = q.Where("pool_id = ?", poolID)
	}
	if memberID != 0 {
		q = q.Where("member_id = ?", memberID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []MainAccountAuditLog
	if err := q.Order("created_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (r *MainStationStore) MarkMembersOrphaned(remoteAccountIDs []int64) ([]MainAccountPoolMember, error) {
	if len(remoteAccountIDs) == 0 {
		return []MainAccountPoolMember{}, nil
	}
	var members []MainAccountPoolMember
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("remote_account_id IN ? AND binding_status <> ?", remoteAccountIDs, "orphaned").Order("id ASC").Find(&members).Error; err != nil {
			return err
		}
		if len(members) == 0 {
			return nil
		}
		ids := make([]uint, 0, len(members))
		for _, member := range members {
			ids = append(ids, member.ID)
		}
		return tx.Model(&MainAccountPoolMember{}).Where("id IN ?", ids).Updates(map[string]any{
			"binding_status": "orphaned",
			"status":         "orphaned",
		}).Error
	})
	return members, err
}

func (r *MainStationStore) TryClaimNotificationCooldown(dedupKey, event string, poolID, memberID, groupID uint, cooldown time.Duration) (bool, error) {
	if cooldown <= 0 {
		return true, nil
	}
	now := time.Now()
	threshold := now.Add(-cooldown)
	res := r.db.Model(&MainStationNotificationCooldown{}).
		Where("dedup_key = ? AND last_sent_at < ?", dedupKey, threshold).
		Updates(map[string]any{"last_sent_at": now})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected > 0 {
		return true, nil
	}
	res = r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&MainStationNotificationCooldown{
		DedupKey: dedupKey, Event: event, PoolID: poolID, MemberID: memberID, GroupID: groupID, LastSentAt: now,
	})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

func normalizeStoragePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func applyPoolDefaults(item *MainAccountPool) {
	item.Name = strings.TrimSpace(item.Name)
	item.Description = strings.TrimSpace(item.Description)
	item.Platform = strings.TrimSpace(item.Platform)
	if item.MinimumHealthyMembers < 0 {
		item.MinimumHealthyMembers = 0
	}
	if item.MinimumEffectiveConcurrency < 0 {
		item.MinimumEffectiveConcurrency = 0
	}
	if item.RankingIntervalSeconds < 0 {
		item.RankingIntervalSeconds = 0
	}
	if item.RateSortDirection != "desc" && item.RateSortDirection != "stability" {
		item.RateSortDirection = "asc"
	}
	if strings.TrimSpace(item.HealthPolicyJSON) == "" {
		item.HealthPolicyJSON = defaultMainHealthPolicy
	}
	if strings.TrimSpace(item.MarginPolicyJSON) == "" {
		item.MarginPolicyJSON = defaultMainMarginPolicy
	}
	if strings.TrimSpace(item.LastStatus) == "" {
		item.LastStatus = "unknown"
	}
}

func applyMemberDefaults(item *MainAccountPoolMember) {
	item.SourceGroupName = strings.TrimSpace(item.SourceGroupName)
	item.RemoteAccountName = strings.TrimSpace(item.RemoteAccountName)
	item.HealthModel = strings.TrimSpace(item.HealthModel)
	if item.Weight <= 0 {
		item.Weight = 1
	}
	if item.Priority <= 0 {
		item.Priority = 1
	}
	if item.Concurrency <= 0 {
		item.Concurrency = 10
	}
	if strings.TrimSpace(item.RateConvertMode) == "" {
		item.RateConvertMode = "raw"
	}
	if item.RateConvertValueMicros == 0 {
		item.RateConvertValueMicros = MainStationScale
	}
	if item.CostAdjustmentMicros == 0 {
		item.CostAdjustmentMicros = MainStationScale
	}
	if strings.TrimSpace(item.HealthAPIMode) == "" {
		item.HealthAPIMode = "openai_chat"
	}
	if strings.TrimSpace(item.LastHealthStatus) == "" {
		item.LastHealthStatus = "unknown"
	}
	if strings.TrimSpace(item.OwnershipMode) == "" {
		item.OwnershipMode = "bound"
	}
	if strings.TrimSpace(item.BindingStatus) == "" {
		item.BindingStatus = "manual_confirmed"
	}
	if strings.TrimSpace(item.Status) == "" {
		item.Status = "pending"
	}
}

func replaceMainPoolGroups(tx *gorm.DB, poolID uint, targetGroupIDs []uint) error {
	if err := tx.Where("pool_id = ?", poolID).Delete(&MainAccountPoolGroup{}).Error; err != nil {
		return err
	}
	ids := uniqueSortedUints(targetGroupIDs)
	for _, id := range ids {
		if err := tx.Create(&MainAccountPoolGroup{PoolID: poolID, TargetGroupID: id}).Error; err != nil {
			return err
		}
	}
	return nil
}

func uniqueSortedUints(values []uint) []uint {
	seen := make(map[uint]struct{}, len(values))
	out := make([]uint, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func migrateLegacyMainStationData(db *gorm.DB) error {
	if !db.Migrator().HasTable(&UpstreamSyncGroup{}) || !db.Migrator().HasTable(&UpstreamSyncTarget{}) {
		return nil
	}

	var targetIDs []uint
	if err := db.Model(&UpstreamSyncGroup{}).
		Distinct("target_id").
		Where("target_id > 0").
		Order("target_id ASC").
		Pluck("target_id", &targetIDs).Error; err != nil {
		return fmt.Errorf("inspect legacy main station targets: %w", err)
	}
	if len(targetIDs) == 0 {
		return saveMainMigrationState(db, "not_needed", "no legacy upstream sync groups")
	}
	targetID := targetIDs[0]
	if len(targetIDs) > 1 {
		var config MainStationConfig
		if err := db.First(&config, MainStationSingletonID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return saveMainMigrationState(db, "requires_confirmation", fmt.Sprintf("legacy sync groups reference multiple targets: %v", targetIDs))
		} else if err != nil {
			return err
		}
		targetID = config.TargetID
		found := false
		for _, id := range targetIDs {
			if id == targetID {
				found = true
				break
			}
		}
		if !found {
			return saveMainMigrationState(db, "completed", fmt.Sprintf("configured target %d has no legacy sync groups; skipped targets %v", targetID, targetIDs))
		}
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var target UpstreamSyncTarget
		if err := tx.First(&target, targetID).Error; err != nil {
			return fmt.Errorf("load legacy main station target %d: %w", targetID, err)
		}

		var config MainStationConfig
		err := tx.First(&config, MainStationSingletonID).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			config = MainStationConfig{ID: MainStationSingletonID, TargetID: targetID, Enabled: target.Enabled}
			if err := tx.Select("*").Create(&config).Error; err != nil {
				return fmt.Errorf("create main station config: %w", err)
			}
		case err != nil:
			return err
		case config.TargetID != targetID:
			return saveMainMigrationState(tx, "requires_confirmation", fmt.Sprintf("configured main station target %d differs from legacy target %d", config.TargetID, targetID))
		}

		var groups []UpstreamSyncGroup
		if err := tx.Where("target_id = ?", targetID).Order("id ASC").Find(&groups).Error; err != nil {
			return err
		}
		for i := range groups {
			if err := migrateLegacyMainPool(tx, &groups[i]); err != nil {
				return err
			}
		}
		detail := fmt.Sprintf("migrated %d legacy sync groups for target %d", len(groups), targetID)
		if len(targetIDs) > 1 {
			detail += fmt.Sprintf("; skipped unselected targets %v", targetIDs)
		}
		return saveMainMigrationState(tx, "completed", detail)
	})
}

func migrateLegacyMainPool(tx *gorm.DB, legacy *UpstreamSyncGroup) error {
	var pool MainAccountPool
	err := tx.First(&pool, "legacy_sync_group_id = ?", legacy.ID).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		displayName := strings.TrimSpace(legacy.DisplayName)
		if displayName == "" {
			displayName = strings.TrimSpace(legacy.Name)
		}
		legacyID := legacy.ID
		pool = MainAccountPool{
			LegacySyncGroupID:           &legacyID,
			Name:                        uniqueLegacyPoolName(tx, displayName, legacy.ID),
			Description:                 "由旧版上游动态同步分组迁移",
			Platform:                    legacy.Platform,
			Enabled:                     legacy.Enabled,
			MinimumHealthyMembers:       1,
			MinimumEffectiveConcurrency: 1,
			RateSortDirection:           legacy.RateSortDirection,
			HealthPolicyJSON:            defaultMainHealthPolicy,
			MarginPolicyJSON:            defaultMainMarginPolicy,
			LastStatus:                  "unknown",
		}
		applyPoolDefaults(&pool)
		if err := tx.Select("*").Create(&pool).Error; err != nil {
			return fmt.Errorf("migrate legacy sync group %d: %w", legacy.ID, err)
		}
	case err != nil:
		return err
	}

	targetGroupIDs, err := parseUintArray(legacy.TargetGroupIDsJSON)
	if err != nil {
		return fmt.Errorf("parse legacy sync group %d target groups: %w", legacy.ID, err)
	}
	if err := replaceMainPoolGroups(tx, pool.ID, targetGroupIDs); err != nil {
		return fmt.Errorf("migrate legacy sync group %d target groups: %w", legacy.ID, err)
	}

	var accounts []UpstreamSyncAccount
	if err := tx.Where("sync_group_id = ?", legacy.ID).Order("position ASC, id ASC").Find(&accounts).Error; err != nil {
		return err
	}
	for i := range accounts {
		if err := migrateLegacyMainMember(tx, pool.ID, &accounts[i]); err != nil {
			return err
		}
	}
	return nil
}

func migrateLegacyMainMember(tx *gorm.DB, poolID uint, legacy *UpstreamSyncAccount) error {
	var member MainAccountPoolMember
	err := tx.First(&member, "legacy_sync_account_id = ?", legacy.ID).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	legacyID := legacy.ID
	member = MainAccountPoolMember{
		PoolID:                 poolID,
		LegacySyncAccountID:    &legacyID,
		SourceChannelID:        legacy.SourceChannelID,
		SourceGroupID:          legacy.SourceGroupID,
		SourceGroupName:        legacy.SourceGroupName,
		OwnershipMode:          "managed",
		BindingStatus:          "pending",
		Status:                 "pending",
		Enabled:                legacy.Enabled,
		ProxyID:                legacy.ProxyID,
		Weight:                 legacy.Weight,
		Priority:               legacy.Position + 1,
		Concurrency:            legacy.Concurrency,
		RateConvertMode:        legacy.RateConvertMode,
		RateConvertValueMicros: scaledFromFloat(legacy.RateConvertValue),
		CostAdjustmentMicros:   MainStationScale,
		HealthEnabled:          legacy.TestEnabled,
		HealthModel:            legacy.TestModel,
		HealthAPIMode:          "openai_chat",
		LastHealthStatus:       "unknown",
	}

	var managed UpstreamSyncManagedAccount
	if err := tx.First(&managed, "sync_account_id = ?", legacy.ID).Error; err == nil {
		keyID := managed.SourceAPIKeyID
		remoteID := managed.TargetAccountID
		member.SourceAPIKeyID = &keyID
		member.RemoteAccountID = &remoteID
		member.RemoteAccountName = managed.TargetAccountName
		member.BindingStatus = "verified"
		member.Status = "active"
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	if member.RemoteAccountID != nil {
		var count int64
		if err := tx.Model(&MainAccountPoolMember{}).Where("remote_account_id = ?", *member.RemoteAccountID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			member.RemoteAccountID = nil
			member.BindingStatus = "invalid"
			member.Status = "error"
		}
	}
	applyMemberDefaults(&member)
	if err := tx.Select("*").Create(&member).Error; err != nil {
		return fmt.Errorf("migrate legacy sync account %d: %w", legacy.ID, err)
	}
	return nil
}

func uniqueLegacyPoolName(tx *gorm.DB, base string, legacyID uint) string {
	if base == "" {
		base = fmt.Sprintf("账号池 %d", legacyID)
	}
	name := base
	for suffix := 0; ; suffix++ {
		var count int64
		_ = tx.Model(&MainAccountPool{}).Where("name = ?", name).Count(&count).Error
		if count == 0 {
			return name
		}
		name = fmt.Sprintf("%s（迁移 %d-%d）", base, legacyID, suffix+1)
	}
}

func saveMainMigrationState(db *gorm.DB, status, detail string) error {
	item := MainStationMigrationState{
		ID:        MainStationSingletonID,
		Status:    status,
		Detail:    detail,
		UpdatedAt: time.Now(),
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"status", "detail", "updated_at"}),
	}).Create(&item).Error
}

func scaledFromFloat(value float64) int64 {
	if value == 0 {
		return MainStationScale
	}
	if value > 0 {
		return int64(value*float64(MainStationScale) + 0.5)
	}
	return int64(value*float64(MainStationScale) - 0.5)
}
