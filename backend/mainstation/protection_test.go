package mainstation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fausto2022/relaydeck/backend/storage"
)

func TestProtectionPolicyRequiresObservationAndDisablingPreservesLocks(t *testing.T) {
	unobservedService, _, _, _ := newTestService(t)
	configureTestStation(t, unobservedService)

	enabled := true
	if _, err := unobservedService.UpdateProtectionPolicy(context.Background(), ProtectionPolicyInput{AutoMarginProtection: &enabled}); err == nil {
		t.Fatal("margin protection enabled without observation evidence")
	}
	if _, err := unobservedService.UpdateProtectionPolicy(context.Background(), ProtectionPolicyInput{AutoHealthProtection: &enabled}); err == nil {
		t.Fatal("health protection enabled without observation evidence")
	}

	service, db, admin, _ := newTestService(t)
	current := time.Date(2026, 7, 17, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	service.now = func() time.Time { return current }
	pool, member, _ := createProfitMember(
		t,
		service,
		db,
		admin,
		current,
		1.2,
		`{"mode":"observe","minimum_margin_basis_points":0,"risk_confirmations":2,"cost_max_age_minutes":60}`,
	)
	if _, err := service.EvaluatePool(context.Background(), pool.ID, "manual"); err != nil {
		t.Fatalf("evaluate pool: %v", err)
	}
	if _, err := service.ActivateGuardLock(context.Background(), *member.RemoteAccountID, "margin", "risk", nil, "margin"); err != nil {
		t.Fatalf("activate margin lock: %v", err)
	}
	if _, err := service.UpdateProtectionPolicy(context.Background(), ProtectionPolicyInput{AutoMarginProtection: &enabled}); err != nil {
		t.Fatalf("enable observed margin protection: %v", err)
	}
	disabled := false
	if _, err := service.UpdateProtectionPolicy(context.Background(), ProtectionPolicyInput{AutoMarginProtection: &disabled}); err != nil {
		t.Fatalf("disable margin protection: %v", err)
	}
	locks, err := service.ListGuardLocks(*member.RemoteAccountID)
	if err != nil || len(locks) != 1 || locks[0].LockType != "margin" {
		t.Fatalf("locks after disabling protection = %#v, err=%v", locks, err)
	}
}

func TestBulkRecoveryPreservesNonRecoveryLocks(t *testing.T) {
	service, db, admin, _ := newTestService(t)
	current := time.Date(2026, 7, 17, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	service.now = func() time.Time { return current }
	pool, member, _ := createProfitMember(
		t,
		service,
		db,
		admin,
		current,
		0.8,
		`{"mode":"observe","minimum_margin_basis_points":0,"risk_confirmations":2,"cost_max_age_minutes":60}`,
	)
	if _, err := service.EvaluatePool(context.Background(), pool.ID, "manual"); err != nil {
		t.Fatalf("evaluate pool: %v", err)
	}
	if err := db.Model(&storage.MainAccountPoolMember{}).
		Where("id = ?", member.ID).
		Updates(map[string]any{"last_health_status": "healthy", "status": "active"}).Error; err != nil {
		t.Fatalf("mark member healthy: %v", err)
	}
	for _, lockType := range []string{"health", "margin", "manual", "credential", "binding"} {
		if _, err := service.ActivateGuardLock(context.Background(), *member.RemoteAccountID, lockType, lockType+" lock", nil, "test"); err != nil {
			t.Fatalf("activate %s lock: %v", lockType, err)
		}
	}

	result, err := service.BulkRecoverPool(context.Background(), pool.ID)
	if err != nil {
		t.Fatalf("bulk recover: %v", err)
	}
	if result.Attempted != 1 || result.Succeeded != 1 || len(result.Errors) != 0 {
		t.Fatalf("bulk recover result = %#v", result)
	}
	locks, err := service.ListGuardLocks(*member.RemoteAccountID)
	if err != nil {
		t.Fatalf("list remaining locks: %v", err)
	}
	remaining := make(map[string]bool, len(locks))
	for _, item := range locks {
		remaining[item.LockType] = true
	}
	for _, lockType := range []string{"manual", "credential", "binding"} {
		if !remaining[lockType] {
			t.Fatalf("%s lock was cleared: %#v", lockType, locks)
		}
	}
	if remaining["health"] || remaining["margin"] {
		t.Fatalf("recovery locks remain active: %#v", locks)
	}
	if admin.accounts[0].Schedulable {
		t.Fatal("account became schedulable while non-recovery locks remained")
	}
}

func TestActivateGuardLockIsIdempotent(t *testing.T) {
	service, db, admin, _ := newTestService(t)
	current := time.Date(2026, 7, 17, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	service.now = func() time.Time { return current }
	_, member, _ := createProfitMember(
		t, service, db, admin, current, 0.8,
		`{"mode":"observe","minimum_margin_basis_points":0,"risk_confirmations":1,"cost_max_age_minutes":60}`,
	)
	if _, err := service.ActivateGuardLock(context.Background(), *member.RemoteAccountID, "manual", "maintenance", nil, "admin"); err != nil {
		t.Fatalf("activate guard lock: %v", err)
	}
	var auditCount int64
	if err := db.Model(&storage.MainAccountAuditLog{}).
		Where("action IN ?", []string{"guard_lock_activate", "schedulable_reconcile"}).
		Count(&auditCount).Error; err != nil {
		t.Fatalf("count first activation audits: %v", err)
	}
	beforeCalls := len(admin.schedulableCalls)
	if _, err := service.ActivateGuardLock(context.Background(), *member.RemoteAccountID, "manual", "duplicate", nil, "admin"); err != nil {
		t.Fatalf("repeat guard lock: %v", err)
	}
	var repeatedAuditCount int64
	if err := db.Model(&storage.MainAccountAuditLog{}).
		Where("action IN ?", []string{"guard_lock_activate", "schedulable_reconcile"}).
		Count(&repeatedAuditCount).Error; err != nil {
		t.Fatalf("count repeated activation audits: %v", err)
	}
	if repeatedAuditCount != auditCount || len(admin.schedulableCalls) != beforeCalls {
		t.Fatalf("duplicate activation wrote state: audits %d -> %d, calls=%#v", auditCount, repeatedAuditCount, admin.schedulableCalls)
	}
}

func TestPoolCapacityThresholds(t *testing.T) {
	service, db, admin, _ := newTestService(t)
	current := time.Date(2026, 7, 17, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	service.now = func() time.Time { return current }
	pool, member, _ := createProfitMember(
		t,
		service,
		db,
		admin,
		current,
		0.8,
		`{"mode":"observe","minimum_margin_basis_points":0,"risk_confirmations":2,"cost_max_age_minutes":60}`,
	)
	if _, err := service.EvaluatePool(context.Background(), pool.ID, "manual"); err != nil {
		t.Fatalf("evaluate pool: %v", err)
	}
	if err := db.Model(&storage.MainAccountPoolMember{}).
		Where("id = ?", member.ID).
		Updates(map[string]any{"last_health_status": "healthy", "status": "active"}).Error; err != nil {
		t.Fatalf("mark member healthy: %v", err)
	}

	healthy, err := service.EvaluatePoolCapacity(context.Background(), pool.ID)
	if err != nil {
		t.Fatalf("healthy capacity: %v", err)
	}
	if healthy.Status != "healthy" || healthy.QualifiedMembers != 1 || healthy.EffectiveConcurrency != member.Concurrency {
		t.Fatalf("healthy capacity = %#v", healthy)
	}
	if err := db.Model(&storage.MainAccountPool{}).
		Where("id = ?", pool.ID).
		Updates(map[string]any{"minimum_healthy_members": 2, "minimum_effective_concurrency": member.Concurrency + 1}).Error; err != nil {
		t.Fatalf("raise capacity thresholds: %v", err)
	}
	degraded, err := service.EvaluatePoolCapacity(context.Background(), pool.ID)
	if err != nil {
		t.Fatalf("degraded capacity: %v", err)
	}
	if degraded.Status != "degraded" {
		t.Fatalf("degraded capacity = %#v", degraded)
	}
	if _, err := service.ActivateGuardLock(context.Background(), *member.RemoteAccountID, "manual", "maintenance", nil, "admin"); err != nil {
		t.Fatalf("activate manual lock: %v", err)
	}
	critical, err := service.EvaluatePoolCapacity(context.Background(), pool.ID)
	if err != nil {
		t.Fatalf("critical capacity: %v", err)
	}
	if critical.Status != "critical" || critical.QualifiedMembers != 0 {
		t.Fatalf("critical capacity = %#v", critical)
	}
}

func TestAuditJSONRedactsNestedSecrets(t *testing.T) {
	service, _, _, _ := newTestService(t)
	secret := "known-secret-value"
	value := map[string]any{
		"api_key": secret,
		"nested": []any{
			map[string]any{"password": secret, "safe": "visible"},
			map[string]any{"admin_api_key_cipher": secret, "token": secret},
		},
	}
	if err := service.appendAudit(nil, nil, nil, "redaction_test", "test", true, value, value, value, "", ""); err != nil {
		t.Fatalf("append audit: %v", err)
	}
	page, err := service.ListAuditLogs(0, 0, 1, 10)
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("audit count = %d", len(page.Items))
	}
	item := page.Items[0]
	combined := item.BeforeJSON + item.AfterJSON + item.EvidenceJSON
	if strings.Contains(combined, secret) {
		t.Fatalf("audit contains secret: %s", combined)
	}
	if !strings.Contains(combined, "visible") || !strings.Contains(combined, "[redacted]") {
		t.Fatalf("audit redaction output = %s", combined)
	}
}
