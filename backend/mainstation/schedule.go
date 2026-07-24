package mainstation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fausto2022/relaydeck/backend/connector/sub2api"
	"github.com/fausto2022/relaydeck/backend/notify"
	"github.com/fausto2022/relaydeck/backend/storage"
	"gorm.io/gorm"
)

var validGuardLockTypes = map[string]struct{}{
	"manual": {}, "margin": {}, "health": {}, "sync": {}, "credential": {}, "binding": {},
}

const schedulingRetryInterval = 10 * time.Second

func (s *Service) IsManagedAccount(remoteAccountID int64) bool {
	if remoteAccountID <= 0 {
		return false
	}
	_, err := s.store.FindMemberByRemoteAccountID(remoteAccountID)
	return err == nil
}

func (s *Service) ActivateSchedulingLock(ctx context.Context, remoteAccountID int64, lockType, reason string, evidence any, source string) error {
	_, err := s.ActivateGuardLock(ctx, remoteAccountID, lockType, reason, evidence, source)
	return err
}

func (s *Service) ClearSchedulingLock(_ context.Context, remoteAccountID int64, lockType, clearedBy string) error {
	member, err := s.store.FindMemberByRemoteAccountID(remoteAccountID)
	if err != nil {
		return err
	}
	cleared, err := s.store.ClearGuardLock(remoteAccountID, lockType, clearedBy)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err == nil {
		_ = s.appendAudit(&member.PoolID, &member.ID, &remoteAccountID, "guard_lock_clear", clearedBy, true, nil, cleared, nil, "", "")
	}
	return err
}

func (s *Service) ReconcileScheduling(ctx context.Context, remoteAccountID int64, source string) error {
	_, err := s.ReconcileAccount(ctx, remoteAccountID, source)
	return err
}

func (s *Service) ActivateGuardLock(ctx context.Context, remoteAccountID int64, lockType, reason string, evidence any, source string) (*storage.MainAccountGuardLock, error) {
	lockType = strings.ToLower(strings.TrimSpace(lockType))
	if _, ok := validGuardLockTypes[lockType]; !ok {
		return nil, errors.New("invalid guard lock type")
	}
	member, err := s.store.FindMemberByRemoteAccountID(remoteAccountID)
	if err != nil {
		return nil, err
	}
	locks, err := s.store.ListActiveGuardLocks(remoteAccountID)
	if err != nil {
		return nil, err
	}
	for i := range locks {
		if locks[i].LockType == lockType {
			return &locks[i], nil
		}
	}
	item := &storage.MainAccountGuardLock{
		RemoteAccountID: remoteAccountID,
		MemberID:        member.ID,
		LockType:        lockType,
		Active:          true,
		Reason:          sanitizeText(reason),
		EvidenceJSON:    safeJSON(evidence),
		CreatedBy:       source,
	}
	if item.CreatedBy == "" {
		item.CreatedBy = "system"
	}
	if err := s.store.UpsertGuardLock(item); err != nil {
		return nil, err
	}
	_ = s.appendAudit(&member.PoolID, &member.ID, &remoteAccountID, "guard_lock_activate", source, true, nil, item, evidence, item.Reason, "")
	if _, err := s.ReconcileAccount(ctx, remoteAccountID, source); err != nil {
		return item, err
	}
	return item, nil
}

func (s *Service) ClearGuardLock(ctx context.Context, remoteAccountID int64, lockType, clearedBy string) (*SchedulingDecision, error) {
	lockType = strings.ToLower(strings.TrimSpace(lockType))
	if _, ok := validGuardLockTypes[lockType]; !ok {
		return nil, errors.New("invalid guard lock type")
	}
	member, err := s.store.FindMemberByRemoteAccountID(remoteAccountID)
	if err != nil {
		return nil, err
	}
	cleared, err := s.store.ClearGuardLock(remoteAccountID, lockType, clearedBy)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.ReconcileAccount(ctx, remoteAccountID, clearedBy)
	}
	if err != nil {
		return nil, err
	}
	_ = s.appendAudit(&member.PoolID, &member.ID, &remoteAccountID, "guard_lock_clear", clearedBy, true, nil, cleared, nil, "", "")
	return s.ReconcileAccount(ctx, remoteAccountID, clearedBy)
}

func (s *Service) ListGuardLocks(remoteAccountID int64) ([]storage.MainAccountGuardLock, error) {
	return s.store.ListActiveGuardLocks(remoteAccountID)
}

func (s *Service) ReconcileAccount(ctx context.Context, remoteAccountID int64, source string) (decision *SchedulingDecision, reconcileErr error) {
	value, _ := s.scheduleLocks.LoadOrStore(remoteAccountID, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	defer mutex.Unlock()

	member, err := s.store.FindMemberByRemoteAccountID(remoteAccountID)
	if err != nil {
		return nil, err
	}
	startedAt := s.now()
	if err := s.store.MarkMemberSchedulingDirty(member.ID, startedAt); err != nil {
		return nil, err
	}
	defer func() {
		finishedAt := s.now()
		errText := ""
		if reconcileErr != nil {
			errText = sanitizeText(reconcileErr.Error())
		}
		if completeErr := s.store.CompleteMemberScheduling(member.ID, startedAt, finishedAt, errText); completeErr != nil {
			reconcileErr = errors.Join(reconcileErr, completeErr)
		}
	}()
	pool, err := s.store.FindPool(member.PoolID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if dirtyErr := s.markPoolRankingDirty(pool.ID); dirtyErr != nil && s.log != nil {
			s.log.Warn("mark main station scheduling rank dirty", "err", dirtyErr, "pool_id", pool.ID)
		}
	}()
	config, target, apiKey, err := s.loadAdminTarget()
	if err != nil {
		return nil, err
	}
	client := s.adminFactory()
	adminTarget := sub2api.AdminTarget{BaseURL: target.BaseURL, APIKey: apiKey}
	remote, err := client.GetAccount(ctx, adminTarget, remoteAccountID)
	if err != nil {
		if statusCodeFromError(err) == 404 || errors.Is(err, gorm.ErrRecordNotFound) {
			member.BindingStatus = "orphaned"
			member.Status = "orphaned"
			if updateErr := s.store.UpdateMember(member); updateErr != nil {
				return nil, updateErr
			}
			_ = s.store.MarkAccountSnapshotMissing(remoteAccountID, s.now())
			bindingLock := &storage.MainAccountGuardLock{
				RemoteAccountID: remoteAccountID, MemberID: member.ID, LockType: "binding", Active: true,
				Reason: "remote account no longer exists", CreatedBy: "system",
			}
			if lockErr := s.store.UpsertGuardLock(bindingLock); lockErr != nil {
				return nil, lockErr
			}
			decision = &SchedulingDecision{
				RemoteAccountID: remoteAccountID, DesiredSchedulable: false, RemoteSchedulable: false,
				Reason: "member binding is invalid", Locks: []storage.MainAccountGuardLock{*bindingLock},
			}
			_ = s.appendAudit(&pool.ID, &member.ID, &remoteAccountID, "schedulable_reconcile", source, true, nil, nil, decision, "remote account no longer exists; member marked orphaned", "")
			return decision, nil
		}
		_ = s.appendAudit(&pool.ID, &member.ID, &remoteAccountID, "schedulable_reconcile", source, false, nil, nil, nil, "", redactSecretError(err, apiKey).Error())
		return nil, fmt.Errorf("read remote account before scheduling decision: %w", redactSecretError(err, apiKey))
	}
	locks, err := s.store.ListActiveGuardLocks(remoteAccountID)
	if err != nil {
		return nil, err
	}
	bindingValid := member.BindingStatus == "verified" || member.BindingStatus == "manual_confirmed"
	desired := config.Enabled && pool.Enabled && member.Enabled && strings.EqualFold(remote.Status, "active") && bindingValid && len(locks) == 0
	reason := schedulingReason(config.Enabled, pool.Enabled, member.Enabled, remote.Status, bindingValid, locks)
	decision = &SchedulingDecision{
		RemoteAccountID: remoteAccountID, DesiredSchedulable: desired, RemoteSchedulable: remote.Schedulable,
		Reason: reason, Locks: locks,
	}
	before := *remote
	if remote.Schedulable == desired {
		s.saveRemoteSchedulingSnapshot(remote, remoteAccountID)
		_ = s.appendAudit(&pool.ID, &member.ID, &remoteAccountID, "schedulable_reconcile", source, true, before, remote, decision, "remote state already matches decision", "")
		return decision, nil
	}
	updated, writeErr := client.SetAccountSchedulable(ctx, adminTarget, remoteAccountID, desired)
	if writeErr != nil {
		verified, verifyErr := client.GetAccount(ctx, adminTarget, remoteAccountID)
		if verifyErr == nil && verified.Schedulable == desired {
			updated = verified
			writeErr = nil
		}
	}
	if writeErr != nil {
		safeErr := redactSecretError(writeErr, apiKey)
		_ = s.appendAudit(&pool.ID, &member.ID, &remoteAccountID, "schedulable_reconcile", source, false, before, nil, decision, "remote schedulable write failed", safeErr.Error())
		return decision, fmt.Errorf("set remote account schedulable: %w", safeErr)
	}
	decision.Changed = true
	decision.RemoteSchedulable = updated.Schedulable
	s.saveRemoteSchedulingSnapshot(updated, remoteAccountID)
	_ = s.appendAudit(&pool.ID, &member.ID, &remoteAccountID, "schedulable_reconcile", source, true, before, updated, decision, "remote schedulable updated", "")
	s.notifySchedulingTransition(ctx, pool, member, updated, decision, source)
	return decision, nil
}

func (s *Service) notifySchedulingTransition(ctx context.Context, pool *storage.MainAccountPool, member *storage.MainAccountPoolMember, remote *sub2api.AdminAccount, decision *SchedulingDecision, source string) {
	if s.dispatcher == nil || pool == nil || member == nil || remote == nil || decision == nil || !decision.Changed {
		return
	}
	event := storage.EventMainMemberDisabled
	state := "已停用"
	action := "账号已退出主站调度，后台仍会按现有策略检查恢复条件。"
	if decision.DesiredSchedulable {
		event = storage.EventMainMemberReenabled
		state = "已启用"
		action = "账号已恢复参与主站调度。"
	}
	dedupKey := fmt.Sprintf("%s:%d:%d:0", event, pool.ID, member.ID)
	claimed, err := s.store.TryClaimNotificationCooldown(dedupKey, string(event), pool.ID, member.ID, 0, 5*time.Minute)
	if err != nil || !claimed {
		return
	}
	message := notify.Message{
		Event: event, ChannelID: member.SourceChannelID,
		Subject: fmt.Sprintf("主站账号%s · %s · %s", state, pool.Name, remote.Name),
		Body: notify.MarkdownDetails(
			"主站账号调度状态发生变化。",
			notify.Detail("主站分组", pool.Name),
			notify.Detail("主站账号", remote.Name),
			notify.Detail("远端 ID", remote.ID),
			notify.Detail("状态变化", fmt.Sprintf("%s -> %s", schedulingStateLabel(!decision.DesiredSchedulable), schedulingStateLabel(decision.DesiredSchedulable))),
			notify.Detail("触发来源", schedulingSourceLabel(source)),
			notify.Detail("原因", schedulingDecisionLabel(decision)),
		) + notify.MarkdownNote("系统动作", action),
	}
	if err := s.dispatcher.Dispatch(ctx, message); err != nil && s.log != nil {
		s.log.Warn("dispatch main station scheduling transition", "err", err, "member_id", member.ID, "event", event)
	}
}

func schedulingStateLabel(schedulable bool) string {
	if schedulable {
		return "启用"
	}
	return "停用"
}

func schedulingSourceLabel(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "manual", "admin":
		return "人工操作"
	case "health":
		return "健康保护"
	case "margin", "profit":
		return "利润保护"
	case "syncer":
		return "同步流程"
	case "scheduler":
		return "后台重试"
	case "system":
		return "系统策略"
	default:
		if strings.TrimSpace(source) == "" {
			return "系统策略"
		}
		return source
	}
}

func schedulingDecisionLabel(decision *SchedulingDecision) string {
	if decision == nil {
		return "未记录"
	}
	if decision.DesiredSchedulable {
		return "所有调度条件已恢复"
	}
	if len(decision.Locks) > 0 {
		labels := make([]string, 0, len(decision.Locks))
		for _, lock := range decision.Locks {
			labels = append(labels, schedulingLockLabel(lock.LockType))
		}
		return "停用保护：" + strings.Join(labels, "、")
	}
	switch decision.Reason {
	case "main station management is disabled":
		return "主站管理已停用"
	case "account pool is disabled":
		return "主站分组已停用"
	case "pool member is disabled":
		return "账号已在设置中关闭"
	case "remote account status is not active":
		return "远端账号状态不是启用"
	case "member binding is invalid":
		return "账号绑定关系无效"
	default:
		return decision.Reason
	}
}

func schedulingLockLabel(lockType string) string {
	switch lockType {
	case "manual":
		return "人工停用"
	case "health":
		return "健康异常"
	case "margin":
		return "利润不足"
	case "sync":
		return "同步保护"
	case "credential":
		return "凭据异常"
	case "binding":
		return "绑定异常"
	default:
		return lockType
	}
}

func (s *Service) RunDueSchedulingReconciles(ctx context.Context) {
	members, err := s.store.ListSchedulingDirtyMembers()
	if err != nil {
		if s.log != nil {
			s.log.Warn("list due main station scheduling reconciles", "err", err)
		}
		return
	}
	now := s.now()
	for i := range members {
		member := &members[i]
		if member.LastSchedulingAt != nil && now.Before(member.LastSchedulingAt.Add(schedulingRetryInterval)) {
			continue
		}
		if member.RemoteAccountID == nil {
			continue
		}
		if _, err := s.ReconcileAccount(ctx, *member.RemoteAccountID, "scheduler"); err != nil && s.log != nil {
			s.log.Warn("scheduled main station scheduling reconcile", "err", err, "member_id", member.ID)
		}
	}
}

func (s *Service) reconcilePoolScheduling(ctx context.Context, poolID uint, source string) error {
	members, err := s.store.ListMembers(poolID)
	if err != nil {
		return err
	}
	var reconcileErrors []error
	for i := range members {
		member := &members[i]
		if member.RemoteAccountID == nil || member.BindingStatus == "invalid" || member.BindingStatus == "orphaned" {
			continue
		}
		if _, reconcileErr := s.ReconcileAccount(ctx, *member.RemoteAccountID, source); reconcileErr != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("member %d: %w", member.ID, reconcileErr))
		}
	}
	return errors.Join(reconcileErrors...)
}

func (s *Service) reconcileAllScheduling(ctx context.Context, source string) error {
	pools, err := s.store.ListAllPools()
	if err != nil {
		return err
	}
	var reconcileErrors []error
	for i := range pools {
		if reconcileErr := s.reconcilePoolScheduling(ctx, pools[i].ID, source); reconcileErr != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("pool %d: %w", pools[i].ID, reconcileErr))
		}
	}
	return errors.Join(reconcileErrors...)
}

func (s *Service) saveRemoteSchedulingSnapshot(remote *sub2api.AdminAccount, remoteAccountID int64) {
	if remote == nil {
		return
	}
	snapshot := accountSnapshot(*remote)
	snapshot.RemoteAccountID = remoteAccountID
	snapshot.LastSyncAt = time.Now()
	if existing, err := s.store.FindAccountSnapshot(remoteAccountID); err == nil {
		if snapshot.BaseURL == "" {
			snapshot.BaseURL = existing.BaseURL
		}
		if !snapshot.CredentialsPresent {
			snapshot.CredentialsPresent = existing.CredentialsPresent
		}
		if snapshot.BillingProbeJSON == "" {
			snapshot.BillingProbeJSON = existing.BillingProbeJSON
		}
	}
	_ = s.store.UpsertAccountSnapshot(&snapshot)
}

func schedulingReason(stationEnabled, poolEnabled, memberEnabled bool, remoteStatus string, bindingValid bool, locks []storage.MainAccountGuardLock) string {
	switch {
	case !stationEnabled:
		return "main station management is disabled"
	case !poolEnabled:
		return "account pool is disabled"
	case !memberEnabled:
		return "pool member is disabled"
	case !strings.EqualFold(remoteStatus, "active"):
		return "remote account status is not active"
	case !bindingValid:
		return "member binding is invalid"
	case len(locks) > 0:
		types := make([]string, 0, len(locks))
		for _, item := range locks {
			types = append(types, item.LockType)
		}
		return "active guard locks: " + strings.Join(types, ",")
	default:
		return "all scheduling conditions are satisfied"
	}
}
