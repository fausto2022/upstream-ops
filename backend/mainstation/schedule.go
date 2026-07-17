package mainstation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/bejix/upstream-ops/backend/storage"
	"gorm.io/gorm"
)

var validGuardLockTypes = map[string]struct{}{
	"manual": {}, "margin": {}, "health": {}, "sync": {}, "credential": {}, "binding": {},
}

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
	if err != nil {
		return nil, err
	}
	_ = s.appendAudit(&member.PoolID, &member.ID, &remoteAccountID, "guard_lock_clear", clearedBy, true, nil, cleared, nil, "", "")
	return s.ReconcileAccount(ctx, remoteAccountID, clearedBy)
}

func (s *Service) ListGuardLocks(remoteAccountID int64) ([]storage.MainAccountGuardLock, error) {
	return s.store.ListActiveGuardLocks(remoteAccountID)
}

func (s *Service) ReconcileAccount(ctx context.Context, remoteAccountID int64, source string) (*SchedulingDecision, error) {
	value, _ := s.scheduleLocks.LoadOrStore(remoteAccountID, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	defer mutex.Unlock()

	member, err := s.store.FindMemberByRemoteAccountID(remoteAccountID)
	if err != nil {
		return nil, err
	}
	pool, err := s.store.FindPool(member.PoolID)
	if err != nil {
		return nil, err
	}
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
			_ = s.store.UpdateMember(member)
			bindingLock := &storage.MainAccountGuardLock{
				RemoteAccountID: remoteAccountID, MemberID: member.ID, LockType: "binding", Active: true,
				Reason: "remote account no longer exists", CreatedBy: "system",
			}
			_ = s.store.UpsertGuardLock(bindingLock)
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
	decision := &SchedulingDecision{
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
	return decision, nil
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
