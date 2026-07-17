package mainstation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bejix/upstream-ops/backend/notify"
	"github.com/bejix/upstream-ops/backend/storage"
	"gorm.io/gorm"
)

type marginPolicy struct {
	Mode                     string `json:"mode"`
	MinimumMarginBasisPoints int64  `json:"minimum_margin_basis_points"`
	RiskConfirmations        int    `json:"risk_confirmations"`
	CostMaxAgeMinutes        int    `json:"cost_max_age_minutes"`
}

type resolvedCost struct {
	Micros    int64
	Source    string
	Observed  time.Time
	ExpiresAt *time.Time
	Reliable  bool
	Reason    string
}

func (s *Service) EvaluatePool(ctx context.Context, poolID uint, source string) (*PoolEvaluationResult, error) {
	pool, err := s.store.FindPool(poolID)
	if err != nil {
		return nil, err
	}
	if source == "" {
		source = "manual"
	}
	policy := parseMarginPolicy(pool.MarginPolicyJSON)
	groupIDs, err := s.store.ListPoolGroupIDs(pool.ID)
	if err != nil {
		return nil, err
	}
	groups := make([]storage.UpstreamSyncTargetGroup, 0, len(groupIDs))
	for _, id := range groupIDs {
		group, err := s.targetGroups.FindByID(id)
		if err != nil {
			return nil, err
		}
		groups = append(groups, *group)
	}
	members, err := s.store.ListMembers(pool.ID)
	if err != nil {
		return nil, err
	}
	config, err := s.store.GetConfig()
	if err != nil {
		return nil, err
	}
	now := s.now()
	result := &PoolEvaluationResult{PoolID: pool.ID, EvaluatedAt: now}
	wouldDisable := make(map[uint]struct{})
	applied := make(map[uint]struct{})
	for i := range members {
		member := &members[i]
		cost := s.resolveMemberCost(member, policy, now)
		if cost.Micros > 0 {
			member.LastCostMicros = &cost.Micros
			member.LastCostSource = cost.Source
			member.LastCostAt = &cost.Observed
			member.LastCostExpiresAt = cost.ExpiresAt
			if err := s.store.UpdateMember(member); err != nil {
				return nil, err
			}
		}
		memberConfirmedRisk := false
		memberAllHealthy := len(groups) > 0
		for j := range groups {
			previousChecks, _ := s.store.ListProfitChecksSince(member.ID, groups[j].ID, now.Add(-7*24*time.Hour), 1)
			check := s.buildProfitCheck(pool, member, &groups[j], cost, policy, now)
			if err := s.store.AppendProfitCheck(&check); err != nil {
				return nil, err
			}
			result.Checks = append(result.Checks, check)
			s.notifyProfitTransition(ctx, pool, member, &groups[j], &check, previousChecks)
			switch check.Status {
			case "healthy":
				result.Healthy++
			case "risk":
				result.Risk++
				wouldDisable[member.ID] = struct{}{}
				confirmed, err := s.marginRiskConfirmed(member.ID, groups[j].ID, policy, now)
				if err != nil {
					return nil, err
				}
				memberConfirmedRisk = memberConfirmedRisk || confirmed
			case "unsupported":
				result.Unsupported++
			default:
				result.Unknown++
			}
			if check.Status != "healthy" {
				memberAllHealthy = false
			}
		}
		if memberConfirmedRisk && member.RemoteAccountID != nil && config.AutoMarginProtection && config.MarginObservedAt != nil {
			if _, err := s.ActivateGuardLock(ctx, *member.RemoteAccountID, "margin", "profit margin remained below threshold", map[string]any{
				"pool_id": pool.ID, "member_id": member.ID, "minimum_margin_basis_points": policy.MinimumMarginBasisPoints,
			}, "margin"); err != nil {
				return nil, err
			}
			applied[member.ID] = struct{}{}
		} else if memberAllHealthy && member.RemoteAccountID != nil && config.AutoRecovery {
			confirmed, err := s.marginRecoveryConfirmed(member.ID, groups, policy, now)
			if err != nil {
				return nil, err
			}
			if confirmed {
				if _, err := s.ClearGuardLock(ctx, *member.RemoteAccountID, "margin", "margin"); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, err
				}
			}
		}
	}
	result.WouldDisable = sortedUintKeys(wouldDisable)
	result.ProtectionApplied = sortedUintKeys(applied)
	pool.LastEvaluatedAt = &now
	if err := s.store.UpdatePool(pool, groupIDs); err != nil {
		return nil, err
	}
	if config.MarginObservedAt == nil {
		config.MarginObservedAt = &now
	}
	if config.ObservationEvaluatedAt == nil {
		config.ObservationEvaluatedAt = &now
	}
	if err := s.store.SaveConfig(config); err != nil {
		return nil, err
	}
	if _, err := s.EvaluatePoolCapacity(ctx, pool.ID); err != nil {
		return nil, err
	}
	_ = s.appendAudit(&pool.ID, nil, nil, "pool_profit_evaluate", source, true, nil, result, nil, "", "")
	return result, nil
}

func (s *Service) notifyProfitTransition(ctx context.Context, pool *storage.MainAccountPool, member *storage.MainAccountPoolMember, group *storage.UpstreamSyncTargetGroup, check *storage.MainAccountProfitCheck, previous []storage.MainAccountProfitCheck) {
	if s.dispatcher == nil {
		return
	}
	previousStatus := ""
	if len(previous) > 0 {
		previousStatus = previous[0].Status
	}
	event := storage.NotificationEvent("")
	subject := ""
	if check.Status == "risk" && previousStatus != "risk" {
		event = storage.EventMainMemberMarginRisk
		subject = "主站成员利润风险"
	} else if check.Status == "healthy" && previousStatus == "risk" {
		event = storage.EventMainMemberMarginRecovered
		subject = "主站成员利润恢复"
	}
	if event == "" {
		return
	}
	dedupKey := fmt.Sprintf("%s:%d:%d:%d", event, pool.ID, member.ID, group.ID)
	claimed, err := s.store.TryClaimNotificationCooldown(dedupKey, string(event), pool.ID, member.ID, group.ID, 30*time.Minute)
	if err != nil || !claimed {
		return
	}
	_ = s.dispatcher.Dispatch(ctx, notify.Message{
		Event: event, ChannelID: member.SourceChannelID, ModelName: group.Name,
		Subject: fmt.Sprintf("[%s] %s · 成员 #%d", subject, pool.Name, member.ID),
		Body: fmt.Sprintf("账号池：%s\n成员：#%d\n主站 Account：%s\n主站分组：%s\n销售倍率：%s\n成本倍率：%s\n利润率：%.2f%%\n成本来源：%s\n状态：%s\n原因：%s",
			pool.Name, member.ID, member.RemoteAccountName, group.Name,
			formatScaled(check.SaleMultiplierMicros), formatScaled(check.CostMultiplierMicros), float64(check.MarginBasisPoints)/100,
			check.CostSource, check.Status, check.Reason),
	})
}

func (s *Service) notifyPoolProfitTransition(ctx context.Context, pool *storage.MainAccountPool, result *PoolEvaluationResult, oldStatus string) {
	if s.dispatcher == nil || (pool.LastStatus != "degraded" && pool.LastStatus != "critical") {
		return
	}
	event := storage.EventMainPoolDegraded
	if pool.LastStatus == "critical" {
		event = storage.EventMainPoolCritical
	}
	dedupKey := fmt.Sprintf("%s:%d:0:0", event, pool.ID)
	claimed, err := s.store.TryClaimNotificationCooldown(dedupKey, string(event), pool.ID, 0, 0, 30*time.Minute)
	if err != nil || !claimed {
		return
	}
	_ = s.dispatcher.Dispatch(ctx, notify.Message{
		Event:   event,
		Subject: fmt.Sprintf("[主站账号池%s] %s", pool.LastStatus, pool.Name),
		Body: fmt.Sprintf("账号池：%s\n状态：%s -> %s\n健康利润项：%d\n风险项：%d\n未知项：%d\n不支持项：%d",
			pool.Name, oldStatus, pool.LastStatus, result.Healthy, result.Risk, result.Unknown, result.Unsupported),
	})
}

func formatScaled(value int64) string {
	return strconv.FormatFloat(float64(value)/float64(storage.MainStationScale), 'f', 6, 64)
}

func (s *Service) RunProfitEvaluation(ctx context.Context) {
	page := 1
	for {
		pools, total, err := s.store.ListPools(page, 100)
		if err != nil {
			if s.log != nil {
				s.log.Warn("list pools for profit evaluation", "err", err)
			}
			return
		}
		for i := range pools {
			if !pools[i].Enabled {
				continue
			}
			if _, err := s.EvaluatePool(ctx, pools[i].ID, "scheduler"); err != nil && s.log != nil {
				s.log.Warn("evaluate main station pool profit", "err", err, "pool_id", pools[i].ID)
			}
		}
		if int64(page*100) >= total {
			return
		}
		page++
	}
}

func (s *Service) ListProfitChecks(poolID, memberID, targetGroupID uint, page, pageSize int) (*Page[storage.MainAccountProfitCheck], error) {
	if _, err := s.store.FindPool(poolID); err != nil {
		return nil, err
	}
	items, total, err := s.store.ListProfitChecks(poolID, memberID, targetGroupID, page, pageSize)
	if err != nil {
		return nil, err
	}
	page, pageSize = normalizePage(page, pageSize)
	return &Page[storage.MainAccountProfitCheck]{Items: items, Total: total, Page: page, PageSize: pageSize, Pages: pageCount(total, pageSize)}, nil
}

func (s *Service) resolveMemberCost(member *storage.MainAccountPoolMember, policy marginPolicy, now time.Time) resolvedCost {
	maxAge := time.Duration(policy.CostMaxAgeMinutes) * time.Minute
	if maxAge <= 0 {
		maxAge = time.Hour
	}
	if member.RemoteAccountID != nil {
		if snapshot, err := s.store.FindAccountSnapshot(*member.RemoteAccountID); err == nil {
			if value, observed, expiresAt, ok := billingProbeRate(snapshot.BillingProbeJSON, snapshot.LastSyncAt, maxAge); ok {
				if expiresAt == nil || now.Before(*expiresAt) {
					return resolvedCost{Micros: value, Source: "sub2api_billing_probe", Observed: observed, ExpiresAt: expiresAt, Reliable: true}
				}
			}
		}
	}
	if s.rates != nil {
		if snapshots, err := s.rates.ListByChannel(member.SourceChannelID); err == nil {
			if snapshot := selectSourceRateSnapshot(snapshots, member); snapshot != nil {
				expiresAt := snapshot.LastSeenAt.Add(maxAge)
				if now.Before(expiresAt) {
					source := "source_rate_snapshot"
					if member.OwnershipMode == "managed" {
						source = "managed_binding"
					}
					return resolvedCost{Micros: scaleFloat(snapshot.Ratio), Source: source, Observed: snapshot.LastSeenAt, ExpiresAt: &expiresAt, Reliable: true}
				}
			}
		}
	}
	if member.ManualCostMicros != nil && *member.ManualCostMicros > 0 {
		return resolvedCost{Micros: *member.ManualCostMicros, Source: "manual_override", Observed: member.UpdatedAt, Reliable: true}
	}
	if member.RemoteAccountID != nil {
		if snapshot, err := s.store.FindAccountSnapshot(*member.RemoteAccountID); err == nil && snapshot.RateMultiplierMicros > 0 {
			return resolvedCost{
				Micros: snapshot.RateMultiplierMicros, Source: "remote_account_estimate", Observed: snapshot.LastSyncAt,
				Reliable: false, Reason: "remote account rate multiplier is an unconfirmed estimate",
			}
		}
	}
	return resolvedCost{Reason: "no usable cost multiplier source"}
}

func (s *Service) buildProfitCheck(pool *storage.MainAccountPool, member *storage.MainAccountPoolMember, group *storage.UpstreamSyncTargetGroup, cost resolvedCost, policy marginPolicy, now time.Time) storage.MainAccountProfitCheck {
	costAdjustment := member.CostAdjustmentMicros
	if costAdjustment == 0 {
		costAdjustment = storage.MainStationScale
	}
	check := storage.MainAccountProfitCheck{
		PoolID: pool.ID, MemberID: member.ID, TargetGroupID: group.ID,
		CostMultiplierMicros: cost.Micros, CostAdjustmentMicros: costAdjustment,
		CostSource: cost.Source, ObservedAt: now, CreatedAt: now,
	}
	if !pool.Enabled || !member.Enabled || group.Missing || !strings.EqualFold(group.Status, "active") {
		check.Status = "unknown"
		check.Reason = "pool, member or main station group is not active"
		return check
	}
	if unsupportedPricing(group) {
		check.Status = "unsupported"
		check.Reason = "subscription, image, video or non-linear pricing is not eligible for automatic margin protection"
		return check
	}
	saleMicros, saleSource, saleReason := effectiveSaleMultiplier(group, now)
	check.SaleMultiplierMicros = saleMicros
	check.SaleSource = saleSource
	if saleReason != "" {
		check.Status = "unknown"
		check.Reason = saleReason
		return check
	}
	if cost.Micros <= 0 {
		check.Status = "unknown"
		check.Reason = cost.Reason
		return check
	}
	if !cost.Reliable {
		check.Status = "unknown"
		check.Reason = cost.Reason
		return check
	}
	if cost.ExpiresAt != nil && !now.Before(*cost.ExpiresAt) {
		check.Status = "unknown"
		check.Reason = "cost multiplier snapshot is expired"
		return check
	}
	effectiveCost := fixedMul(cost.Micros, costAdjustment, storage.MainStationScale)
	check.CostMultiplierMicros = effectiveCost
	check.MarginValueMicros = saleMicros - effectiveCost
	check.MarginBasisPoints = fixedMul(check.MarginValueMicros, 10000, saleMicros)
	if check.MarginBasisPoints < policy.MinimumMarginBasisPoints {
		check.Status = "risk"
		check.Reason = fmt.Sprintf("margin %d bps is below threshold %d bps", check.MarginBasisPoints, policy.MinimumMarginBasisPoints)
	} else {
		check.Status = "healthy"
	}
	return check
}

func effectiveSaleMultiplier(group *storage.UpstreamSyncTargetGroup, now time.Time) (int64, string, string) {
	if !group.UserRatesComplete {
		return 0, "", "user-specific rate multipliers are not confirmed"
	}
	value := group.RateMultiplierMicros
	source := "main_group_rate"
	if group.UserMinRateMicros != nil && *group.UserMinRateMicros > 0 && (*group.UserMinRateMicros < value || value == 0) {
		value = *group.UserMinRateMicros
		source = "main_group_user_min_rate"
	}
	if value <= 0 {
		return 0, source, "main station sale multiplier is missing"
	}
	if group.PeakEnabled {
		active, err := peakWindowActive(now, group.PeakStart, group.PeakEnd)
		if err != nil {
			return 0, source, "main station peak window is invalid"
		}
		if active {
			if group.PeakMultiplierMicros <= 0 {
				return 0, source, "main station peak multiplier is missing"
			}
			value = fixedMul(value, group.PeakMultiplierMicros, storage.MainStationScale)
			source += "+peak"
		}
	}
	return value, source, ""
}

func unsupportedPricing(group *storage.UpstreamSyncTargetGroup) bool {
	if group.ImageSeparateRate || group.VideoSeparateRate {
		return true
	}
	typeName := strings.ToLower(strings.TrimSpace(group.SubscriptionType))
	return typeName != "" && typeName != "usage" && typeName != "token" && typeName != "payg"
}

func selectSourceRateSnapshot(items []storage.RateSnapshot, member *storage.MainAccountPoolMember) *storage.RateSnapshot {
	for i := range items {
		if member.SourceGroupID != nil && items[i].RemoteGroupID != nil && *member.SourceGroupID == *items[i].RemoteGroupID {
			return &items[i]
		}
		if member.SourceGroupName != "" && strings.EqualFold(member.SourceGroupName, items[i].ModelName) {
			return &items[i]
		}
	}
	if member.SourceGroupID == nil && strings.TrimSpace(member.SourceGroupName) == "" && len(items) == 1 {
		return &items[0]
	}
	return nil
}

func billingProbeRate(raw string, fallbackObserved time.Time, maxAge time.Duration) (int64, time.Time, *time.Time, bool) {
	if strings.TrimSpace(raw) == "" {
		return 0, time.Time{}, nil, false
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return 0, time.Time{}, nil, false
	}
	data := root
	if nested, ok := root["data"].(map[string]any); ok {
		data = nested
	}
	value, ok := numericValue(data["effective_rate_multiplier"])
	if !ok || value <= 0 {
		return 0, time.Time{}, nil, false
	}
	observed := parsedTimeFromMaps(root, data, "observed_at", "sampled_at", "checked_at", "updated_at")
	if observed.IsZero() {
		observed = fallbackObserved
	}
	expires := parsedTimeFromMaps(root, data, "expires_at")
	if expires.IsZero() && !observed.IsZero() {
		expires = observed.Add(maxAge)
	}
	var expiresAt *time.Time
	if !expires.IsZero() {
		expiresAt = &expires
	}
	return scaleFloat(value), observed, expiresAt, true
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func parsedTimeFromMaps(root, data map[string]any, keys ...string) time.Time {
	for _, key := range keys {
		for _, object := range []map[string]any{data, root} {
			raw, ok := object[key]
			if !ok {
				continue
			}
			switch typed := raw.(type) {
			case string:
				for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02 15:04:05"} {
					if parsed, err := time.Parse(layout, typed); err == nil {
						return parsed
					}
				}
			case float64:
				if typed > 1e12 {
					return time.UnixMilli(int64(typed))
				}
				if typed > 1e9 {
					return time.Unix(int64(typed), 0)
				}
			}
		}
	}
	return time.Time{}
}

func peakWindowActive(now time.Time, startRaw, endRaw string) (bool, error) {
	parse := func(raw string) (int, error) {
		parts := strings.Split(strings.TrimSpace(raw), ":")
		if len(parts) < 2 {
			return 0, errors.New("invalid peak time")
		}
		hour, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, err
		}
		minute, err := strconv.Atoi(parts[1])
		if err != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
			return 0, errors.New("invalid peak time")
		}
		return hour*60 + minute, nil
	}
	start, err := parse(startRaw)
	if err != nil {
		return false, err
	}
	end, err := parse(endRaw)
	if err != nil {
		return false, err
	}
	current := now.Hour()*60 + now.Minute()
	if start <= end {
		return current >= start && current < end, nil
	}
	return current >= start || current < end, nil
}

func fixedMul(left, right, divisor int64) int64 {
	if divisor == 0 {
		return 0
	}
	numerator := new(big.Int).Mul(big.NewInt(left), big.NewInt(right))
	numerator.Quo(numerator, big.NewInt(divisor))
	if !numerator.IsInt64() {
		if numerator.Sign() < 0 {
			return -1 << 63
		}
		return 1<<63 - 1
	}
	return numerator.Int64()
}

func (s *Service) marginRiskConfirmed(memberID, targetGroupID uint, policy marginPolicy, now time.Time) (bool, error) {
	checks, err := s.store.ListProfitChecksSince(memberID, targetGroupID, now.Add(-24*time.Hour), policy.RiskConfirmations)
	if err != nil {
		return false, err
	}
	if len(checks) < policy.RiskConfirmations {
		return false, nil
	}
	for _, check := range checks {
		if check.Status != "risk" {
			return false, nil
		}
	}
	return true, nil
}

func (s *Service) marginRecoveryConfirmed(memberID uint, groups []storage.UpstreamSyncTargetGroup, policy marginPolicy, now time.Time) (bool, error) {
	for _, group := range groups {
		checks, err := s.store.ListProfitChecksSince(memberID, group.ID, now.Add(-24*time.Hour), policy.RiskConfirmations)
		if err != nil {
			return false, err
		}
		if len(checks) < policy.RiskConfirmations {
			return false, nil
		}
		for _, check := range checks {
			if check.Status != "healthy" {
				return false, nil
			}
		}
	}
	return len(groups) > 0, nil
}

func parseMarginPolicy(raw string) marginPolicy {
	policy := marginPolicy{Mode: "observe", RiskConfirmations: 2, CostMaxAgeMinutes: 60}
	_ = json.Unmarshal([]byte(raw), &policy)
	if policy.RiskConfirmations <= 0 {
		policy.RiskConfirmations = 2
	}
	if policy.CostMaxAgeMinutes <= 0 {
		policy.CostMaxAgeMinutes = 60
	}
	return policy
}

func evaluatedPoolStatus(result *PoolEvaluationResult, memberCount int) string {
	if memberCount == 0 || len(result.Checks) == 0 {
		return "unknown"
	}
	if result.Healthy == 0 && result.Risk > 0 {
		return "critical"
	}
	if result.Risk > 0 || result.Unknown > 0 || result.Unsupported > 0 {
		return "degraded"
	}
	return "healthy"
}

func sortedUintKeys(values map[uint]struct{}) []uint {
	out := make([]uint, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
