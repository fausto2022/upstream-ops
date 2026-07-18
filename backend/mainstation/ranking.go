package mainstation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/bejix/upstream-ops/backend/storage"
)

type schedulingRankSignal struct {
	MemberID      uint
	HealthBand    int
	Preferred     bool
	Priority      int
	CostKnown     bool
	CostMicros    int64
	SuccessBucket int
	LatencyBucket int
}

func normalizeSchedulingPriority(priority int) int {
	if priority > 0 {
		return priority
	}
	return 1
}

func automaticLoadFactor(concurrency int) int {
	if concurrency <= 0 {
		return 1
	}
	return concurrency
}

func (s *Service) poolSchedulingPriorities(poolID uint) (map[uint]int, error) {
	members, err := s.store.ListMembers(poolID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	signals := make([]schedulingRankSignal, 0, len(members))
	for i := range members {
		member := &members[i]
		stats, statsErr := s.MemberHealthStats(member.ID)
		if statsErr != nil {
			stats = HealthStats{}
		}
		costKnown := validCostSnapshot(member, now)
		costMicros := int64(0)
		if costKnown {
			costMicros = *member.LastCostMicros
		}
		signals = append(signals, schedulingRankSignal{
			MemberID:      member.ID,
			HealthBand:    schedulingHealthBand(member),
			Preferred:     member.Preferred,
			Priority:      normalizeSchedulingPriority(member.Priority),
			CostKnown:     costKnown,
			CostMicros:    costMicros,
			SuccessBucket: schedulingSuccessBucket(stats.Recent20SuccessRate),
			LatencyBucket: schedulingLatencyBucket(stats.P95LatencyMS),
		})
	}
	return rankSchedulingSignals(signals), nil
}

func rankSchedulingSignals(signals []schedulingRankSignal) map[uint]int {
	sort.SliceStable(signals, func(i, j int) bool {
		left, right := signals[i], signals[j]
		switch {
		case left.HealthBand != right.HealthBand:
			return left.HealthBand < right.HealthBand
		case left.Preferred != right.Preferred:
			return left.Preferred
		case left.Priority != right.Priority:
			return left.Priority < right.Priority
		case left.CostKnown != right.CostKnown:
			return left.CostKnown
		case left.CostKnown && left.CostMicros != right.CostMicros:
			return left.CostMicros < right.CostMicros
		case left.SuccessBucket != right.SuccessBucket:
			return left.SuccessBucket < right.SuccessBucket
		case left.LatencyBucket != right.LatencyBucket:
			return left.LatencyBucket < right.LatencyBucket
		default:
			return left.MemberID < right.MemberID
		}
	})
	priorities := make(map[uint]int, len(signals))
	priority := 0
	var previous *schedulingRankSignal
	for i := range signals {
		signal := signals[i]
		if previous == nil || !sameSchedulingRank(*previous, signal) {
			priority++
		}
		priorities[signal.MemberID] = priority
		previous = &signals[i]
	}
	return priorities
}

func sameSchedulingRank(left, right schedulingRankSignal) bool {
	return left.HealthBand == right.HealthBand &&
		left.Preferred == right.Preferred &&
		left.Priority == right.Priority &&
		left.CostKnown == right.CostKnown &&
		(!left.CostKnown || left.CostMicros == right.CostMicros) &&
		left.SuccessBucket == right.SuccessBucket &&
		left.LatencyBucket == right.LatencyBucket
}

func schedulingHealthBand(member *storage.MainAccountPoolMember) int {
	if member == nil || !member.Enabled || member.BindingStatus == "invalid" || member.BindingStatus == "orphaned" {
		return 4
	}
	switch strings.ToLower(strings.TrimSpace(member.LastHealthStatus)) {
	case "healthy":
		return 0
	case "", "unknown", "pending":
		return 1
	case "degraded", "rate_limited":
		return 2
	case "unhealthy", "quarantined":
		return 3
	default:
		return 4
	}
}

func schedulingSuccessBucket(rate *float64) int {
	if rate == nil {
		return 4
	}
	switch {
	case *rate >= 99:
		return 0
	case *rate >= 95:
		return 1
	case *rate >= 80:
		return 2
	default:
		return 3
	}
}

func schedulingLatencyBucket(p95 *int64) int {
	if p95 == nil {
		return 4
	}
	switch {
	case *p95 <= 2_000:
		return 0
	case *p95 <= 5_000:
		return 1
	case *p95 <= 10_000:
		return 2
	default:
		return 3
	}
}

func (s *Service) ReconcilePoolRanking(ctx context.Context, poolID uint, source string) error {
	pool, err := s.store.FindPool(poolID)
	if err != nil {
		return err
	}
	members, err := s.store.ListMembers(poolID)
	if err != nil {
		return err
	}
	priorities, err := s.poolSchedulingPriorities(poolID)
	if err != nil {
		return err
	}
	_, target, adminAPIKey, err := s.loadAdminTarget()
	if err != nil {
		return err
	}
	client := s.adminFactory()
	adminTarget := sub2api.AdminTarget{BaseURL: target.BaseURL, APIKey: adminAPIKey}
	var reconcileErrors []error
	for i := range members {
		member := &members[i]
		if member.RemoteAccountID == nil {
			continue
		}
		desiredPriority := priorities[member.ID]
		if desiredPriority <= 0 {
			desiredPriority = 1
		}
		desiredLoadFactor := automaticLoadFactor(member.Concurrency)
		if member.Weight != desiredLoadFactor || member.Priority != normalizeSchedulingPriority(member.Priority) {
			member.Weight = desiredLoadFactor
			member.Priority = normalizeSchedulingPriority(member.Priority)
			if updateErr := s.store.UpdateMember(member); updateErr != nil {
				reconcileErrors = append(reconcileErrors, fmt.Errorf("update member %d automatic scheduling fields: %w", member.ID, updateErr))
				continue
			}
		}
		snapshot, snapshotErr := s.store.FindAccountSnapshot(*member.RemoteAccountID)
		if snapshotErr == nil && snapshot.Priority == desiredPriority && snapshot.Concurrency == member.Concurrency && snapshot.Weight == desiredLoadFactor {
			continue
		}
		updated, updateErr := client.UpdateAccountScheduling(ctx, adminTarget, *member.RemoteAccountID, sub2api.AdminAccountSchedulingUpdate{
			Concurrency: member.Concurrency,
			Priority:    desiredPriority,
			LoadFactor:  desiredLoadFactor,
		})
		if updateErr != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("update member %d scheduling: %w", member.ID, redactSecretError(updateErr, adminAPIKey)))
			continue
		}
		if refreshed, refreshErr := client.GetAccount(ctx, adminTarget, *member.RemoteAccountID); refreshErr == nil {
			updated = refreshed
		}
		if updated != nil {
			s.saveRemoteSchedulingSnapshot(updated, *member.RemoteAccountID)
		}
		_ = s.appendAudit(&pool.ID, &member.ID, member.RemoteAccountID, "member_scheduling_rank", source, true, snapshot, updated, map[string]any{
			"automatic_priority": desiredPriority,
			"load_factor":        desiredLoadFactor,
		}, "automatic scheduling fields applied", "")
	}
	return errors.Join(reconcileErrors...)
}

func validCostSnapshot(member *storage.MainAccountPoolMember, now time.Time) bool {
	return member != nil && member.LastCostMicros != nil && *member.LastCostMicros > 0 &&
		member.LastCostSource != "remote_account_estimate" &&
		(member.LastCostExpiresAt == nil || now.Before(*member.LastCostExpiresAt))
}
