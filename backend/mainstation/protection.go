package mainstation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fausto2022/relaydeck/backend/notify"
	"github.com/fausto2022/relaydeck/backend/storage"
	"gorm.io/gorm"
)

func (s *Service) UpdateProtectionPolicy(ctx context.Context, in ProtectionPolicyInput) (*ConfigDTO, error) {
	config, err := s.store.GetConfig()
	if err != nil {
		return nil, err
	}
	before := *config
	if in.AutoMarginProtection != nil {
		if *in.AutoMarginProtection && !config.AutoMarginProtection && config.MarginObservedAt == nil {
			return nil, errors.New("run a read-only margin evaluation before enabling automatic margin protection")
		}
		config.AutoMarginProtection = *in.AutoMarginProtection
	}
	if in.AutoHealthProtection != nil {
		if *in.AutoHealthProtection && !config.AutoHealthProtection && config.HealthObservedAt == nil {
			return nil, errors.New("run a health check before enabling automatic health protection")
		}
		config.AutoHealthProtection = *in.AutoHealthProtection
	}
	if in.AutoRecovery != nil {
		config.AutoRecovery = *in.AutoRecovery
	}
	if err := s.store.SaveConfig(config); err != nil {
		return nil, err
	}
	if err := s.reconcileHealthProtectionPolicy(ctx, &before, config, "admin"); err != nil {
		_ = s.appendAudit(nil, nil, nil, "protection_policy_update", "admin", false, before, config, nil, "policy saved but health state reconciliation failed", sanitizeText(err.Error()))
		return nil, fmt.Errorf("reconcile health protection policy: %w", err)
	}
	_ = s.appendAudit(nil, nil, nil, "protection_policy_update", "admin", true, before, config, nil, "current health locks reconciled; disabling still preserves existing locks", "")
	return s.GetConfig()
}

func (s *Service) reconcileHealthProtectionPolicy(ctx context.Context, before, after *storage.MainStationConfig, source string) error {
	if before == nil || after == nil {
		return nil
	}
	protectionEnabled := !before.AutoHealthProtection && after.AutoHealthProtection
	recoveryEnabled := !before.AutoRecovery && after.AutoRecovery
	if !protectionEnabled && !recoveryEnabled {
		return nil
	}
	members, err := s.store.ListAllMembers()
	if err != nil {
		return err
	}
	var reconcileErrors []error
	for i := range members {
		member := &members[i]
		if member.RemoteAccountID == nil {
			continue
		}
		remoteAccountID := *member.RemoteAccountID
		if protectionEnabled && member.Enabled && (member.LastHealthStatus == "unhealthy" || member.Status == "quarantined") {
			if _, lockErr := s.ActivateGuardLock(ctx, remoteAccountID, "health", "member was already unhealthy when automatic health protection was enabled", map[string]any{
				"pool_id": member.PoolID, "member_id": member.ID, "last_health_status": member.LastHealthStatus,
			}, source); lockErr != nil {
				reconcileErrors = append(reconcileErrors, fmt.Errorf("activate member %d health lock: %w", member.ID, lockErr))
			}
		}
		if recoveryEnabled && member.LastHealthStatus == "healthy" {
			locks, lockErr := s.store.ListActiveGuardLocks(remoteAccountID)
			if lockErr != nil {
				reconcileErrors = append(reconcileErrors, fmt.Errorf("list member %d health locks: %w", member.ID, lockErr))
				continue
			}
			if !guardLockActive(locks, "health") {
				continue
			}
			if _, clearErr := s.ClearGuardLock(ctx, remoteAccountID, "health", source); clearErr != nil {
				reconcileErrors = append(reconcileErrors, fmt.Errorf("clear member %d health lock: %w", member.ID, clearErr))
			}
		}
	}
	return errors.Join(reconcileErrors...)
}

func guardLockActive(locks []storage.MainAccountGuardLock, lockType string) bool {
	for _, item := range locks {
		if item.Active && item.LockType == lockType {
			return true
		}
	}
	return false
}

func (s *Service) ProtectionPreview() (*ProtectionPreview, error) {
	config, err := s.store.GetConfig()
	if err != nil {
		return nil, err
	}
	members, err := s.store.ListAllMembers()
	if err != nil {
		return nil, err
	}
	locks, err := s.store.ListAllActiveGuardLocks()
	if err != nil {
		return nil, err
	}
	preview := &ProtectionPreview{
		HealthReady: config.HealthObservedAt != nil,
		MarginReady: config.MarginObservedAt != nil,
		ActiveLocks: locks,
	}
	healthIDs := make(map[uint]struct{})
	marginIDs := make(map[uint]struct{})
	schedulableIDs := make(map[int64]struct{})
	for _, member := range members {
		if member.LastHealthStatus == "unhealthy" || member.Status == "quarantined" {
			healthIDs[member.ID] = struct{}{}
		}
		groupIDs, _ := s.store.ListPoolGroupIDs(member.PoolID)
		for _, groupID := range groupIDs {
			if check, checkErr := s.store.LatestProfitCheck(member.ID, groupID); checkErr == nil && check.Status == "risk" {
				marginIDs[member.ID] = struct{}{}
				break
			}
		}
		if member.RemoteAccountID != nil {
			if snapshot, snapshotErr := s.store.FindAccountSnapshot(*member.RemoteAccountID); snapshotErr == nil && snapshot.Schedulable && !snapshot.Missing {
				schedulableIDs[*member.RemoteAccountID] = struct{}{}
			}
		}
	}
	preview.UnhealthyMemberIDs = sortedUintKeys(healthIDs)
	preview.MarginRiskMemberIDs = sortedUintKeys(marginIDs)
	for id := range schedulableIDs {
		preview.SchedulableAccountIDs = append(preview.SchedulableAccountIDs, id)
	}
	sort.Slice(preview.SchedulableAccountIDs, func(i, j int) bool {
		return preview.SchedulableAccountIDs[i] < preview.SchedulableAccountIDs[j]
	})
	return preview, nil
}

func (s *Service) ClearAutomaticLocks(ctx context.Context, remoteAccountID int64, clearedBy string) (*SchedulingDecision, error) {
	member, err := s.store.FindMemberByRemoteAccountID(remoteAccountID)
	if err != nil {
		return nil, err
	}
	for _, lockType := range []string{"margin", "health", "sync", "credential", "binding"} {
		cleared, clearErr := s.store.ClearGuardLock(remoteAccountID, lockType, clearedBy)
		if errors.Is(clearErr, gorm.ErrRecordNotFound) {
			continue
		}
		if clearErr != nil {
			return nil, clearErr
		}
		_ = s.appendAudit(&member.PoolID, &member.ID, &remoteAccountID, "guard_lock_clear", clearedBy, true, nil, cleared, nil, "clear all automatic locks", "")
	}
	return s.ReconcileAccount(ctx, remoteAccountID, clearedBy)
}

func (s *Service) BulkCheckPool(ctx context.Context, poolID uint, level string, force bool) (*BulkOperationResult, error) {
	members, err := s.store.ListMembers(poolID)
	if err != nil {
		return nil, err
	}
	result := &BulkOperationResult{}
	var mutex sync.Mutex
	var wait sync.WaitGroup
	for _, member := range members {
		if !member.HealthEnabled || member.RemoteAccountID == nil {
			result.Skipped++
			continue
		}
		result.Attempted++
		memberID := member.ID
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, checkErr := s.CheckMember(ctx, poolID, memberID, HealthCheckInput{Level: level, Force: force})
			mutex.Lock()
			defer mutex.Unlock()
			if checkErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("member %d: %s", memberID, sanitizeText(checkErr.Error())))
				return
			}
			result.Succeeded++
		}()
	}
	wait.Wait()
	return result, nil
}

func (s *Service) BulkRecoverPool(ctx context.Context, poolID uint) (*BulkOperationResult, error) {
	members, err := s.store.ListMembers(poolID)
	if err != nil {
		return nil, err
	}
	groupIDs, err := s.store.ListPoolGroupIDs(poolID)
	if err != nil {
		return nil, err
	}
	result := &BulkOperationResult{}
	for _, member := range members {
		if member.RemoteAccountID == nil || member.LastHealthStatus != "healthy" {
			result.Skipped++
			continue
		}
		allProfitable := len(groupIDs) > 0
		for _, groupID := range groupIDs {
			check, checkErr := s.store.LatestProfitCheck(member.ID, groupID)
			if checkErr != nil || check.Status != "healthy" {
				allProfitable = false
				break
			}
		}
		if !allProfitable {
			result.Skipped++
			continue
		}
		result.Attempted++
		memberFailed := false
		for _, lockType := range []string{"health", "margin"} {
			if err := s.ClearSchedulingLock(ctx, *member.RemoteAccountID, lockType, "admin"); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("member %d: %s", member.ID, sanitizeText(err.Error())))
				memberFailed = true
				break
			}
		}
		if memberFailed {
			continue
		}
		if _, err := s.ReconcileAccount(ctx, *member.RemoteAccountID, "admin"); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("member %d: %s", member.ID, sanitizeText(err.Error())))
			continue
		}
		result.Succeeded++
	}
	return result, nil
}

func (s *Service) EvaluatePoolCapacity(ctx context.Context, poolID uint) (*PoolCapacityResult, error) {
	pool, err := s.store.FindPool(poolID)
	if err != nil {
		return nil, err
	}
	members, err := s.store.ListMembers(poolID)
	if err != nil {
		return nil, err
	}
	groupIDs, err := s.store.ListPoolGroupIDs(poolID)
	if err != nil {
		return nil, err
	}
	result := &PoolCapacityResult{PoolID: poolID, TotalMembers: len(members)}
	hasProfitEvidence := false
	for _, member := range members {
		healthy := member.LastHealthStatus == "healthy"
		if healthy {
			result.HealthyMembers++
		}
		profitable := len(groupIDs) > 0
		for _, groupID := range groupIDs {
			check, checkErr := s.store.LatestProfitCheck(member.ID, groupID)
			if checkErr != nil || check.Status != "healthy" {
				profitable = false
				if checkErr == nil {
					hasProfitEvidence = true
				}
				break
			}
			hasProfitEvidence = true
		}
		if profitable {
			result.ProfitableMembers++
		}
		if member.RemoteAccountID == nil {
			continue
		}
		locks, lockErr := s.store.ListActiveGuardLocks(*member.RemoteAccountID)
		snapshot, snapshotErr := s.store.FindAccountSnapshot(*member.RemoteAccountID)
		bindingValid := member.BindingStatus == "verified" || member.BindingStatus == "manual_confirmed"
		qualified := member.Enabled && healthy && profitable && bindingValid && lockErr == nil && len(locks) == 0 &&
			snapshotErr == nil && !snapshot.Missing && strings.EqualFold(snapshot.Status, "active")
		if qualified {
			result.QualifiedMembers++
			result.EffectiveConcurrency += member.Concurrency
		}
		if snapshotErr == nil && snapshot.Schedulable && !snapshot.Missing {
			result.SchedulableMembers++
		}
	}
	switch {
	case !hasProfitEvidence:
		result.Status = "unknown"
	case result.QualifiedMembers == 0:
		result.Status = "critical"
	case result.QualifiedMembers < pool.MinimumHealthyMembers || result.EffectiveConcurrency < pool.MinimumEffectiveConcurrency:
		result.Status = "degraded"
	default:
		result.Status = "healthy"
	}
	oldStatus := pool.LastStatus
	now := s.now()
	pool.LastStatus = result.Status
	pool.LastEvaluatedAt = &now
	if err := s.store.UpdatePool(pool, groupIDs); err != nil {
		return nil, err
	}
	if oldStatus != result.Status {
		s.notifyPoolCapacityTransition(ctx, pool, result, oldStatus)
	}
	return result, nil
}

func (s *Service) notifyPoolCapacityTransition(ctx context.Context, pool *storage.MainAccountPool, result *PoolCapacityResult, oldStatus string) {
	if s.dispatcher == nil || (result.Status != "degraded" && result.Status != "critical") {
		return
	}
	event := storage.EventMainPoolDegraded
	if result.Status == "critical" {
		event = storage.EventMainPoolCritical
	}
	dedupKey := fmt.Sprintf("%s:%d:0:0", event, pool.ID)
	claimed, err := s.store.TryClaimNotificationCooldown(dedupKey, string(event), pool.ID, 0, 0, 30*time.Minute)
	if err != nil || !claimed {
		return
	}
	_ = s.dispatcher.Dispatch(ctx, notify.Message{
		Event:   event,
		Subject: fmt.Sprintf("账号池容量告警 · %s · %s", notificationStatusLabel(result.Status), pool.Name),
		Body: notify.MarkdownDetails(
			"账号池容量已触发风险保护。",
			notify.Detail("账号池", pool.Name),
			notify.Detail("状态变化", fmt.Sprintf("%s -> %s", notificationStatusLabel(oldStatus), notificationStatusLabel(result.Status))),
			notify.Detail("健康成员", result.HealthyMembers),
			notify.Detail("盈利成员", result.ProfitableMembers),
			notify.Detail("合格成员", result.QualifiedMembers),
			notify.Detail("可调度成员", result.SchedulableMembers),
			notify.Detail("有效并发", result.EffectiveConcurrency),
		) + notify.MarkdownNote("处理建议", "请检查异常成员、利润风险和账号池最低容量配置。"),
	})
}

func (s *Service) ListAuditLogs(poolID, memberID uint, page, pageSize int) (*Page[storage.MainAccountAuditLog], error) {
	items, total, err := s.store.ListAuditLogs(poolID, memberID, page, pageSize)
	if err != nil {
		return nil, err
	}
	page, pageSize = normalizePage(page, pageSize)
	return &Page[storage.MainAccountAuditLog]{Items: items, Total: total, Page: page, PageSize: pageSize, Pages: pageCount(total, pageSize)}, nil
}
