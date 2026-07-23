package mainstation

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fausto2022/relaydeck/backend/connector"
	"github.com/fausto2022/relaydeck/backend/storage"
	"gorm.io/gorm"
)

const (
	maximumMarginBasisPoints     = int64(9900)
	autoExpansionMaxTestsPerPool = 3
	autoExpansionFailureCooldown = 6 * time.Hour
	autoExpansionErrorCooldown   = time.Hour
	autoExpansionRateFreshness   = 15 * time.Minute
)

var autoExpansionProviderPatterns = []struct {
	platform string
	pattern  *regexp.Regexp
}{
	{platform: "anthropic", pattern: regexp.MustCompile(`(?i)anthropic|claude|sonnet|opus|haiku|kiro|cc\s*max|ccmax|aws`)},
	{platform: "gemini", pattern: regexp.MustCompile(`(?i)gemini|google`)},
	{platform: "grok", pattern: regexp.MustCompile(`(?i)grok|xai`)},
	{platform: "image", pattern: regexp.MustCompile(`(?i)生图|绘图|画图|image|dall[ -]?e|midjourney|flux`)},
	{platform: "openai", pattern: regexp.MustCompile(`(?i)openai|gpt|codex|\bplus\b|\bpro\b|\bteam\b|快速稳定|散户|无限制|测试`)},
}

type autoExpansionCandidate struct {
	channel           storage.Channel
	rate              storage.RateSnapshot
	existingMember    *storage.MainAccountPoolMember
	costMicros        int64
	marginBasisPoints int64
}

func validateAutoExpandMarginBasisPoints(value int64) error {
	if value < 0 || value > maximumMarginBasisPoints {
		return errors.New("自动扩池最低利润率必须在 0% 到 99% 之间")
	}
	return nil
}

func (s *Service) RunAutoExpansion(ctx context.Context) {
	if !s.autoExpandMu.TryLock() {
		return
	}
	defer s.autoExpandMu.Unlock()
	config, err := s.store.GetConfig()
	if err != nil || !config.Enabled {
		return
	}
	pools, err := s.store.ListAllPools()
	if err != nil {
		if s.log != nil {
			s.log.Warn("list pools for automatic expansion", "err", err)
		}
		return
	}
	for i := range pools {
		if ctx.Err() != nil {
			return
		}
		pool := &pools[i]
		if !pool.Enabled || !pool.AutoExpandEnabled {
			continue
		}
		runAt := s.now()
		runErr := s.expandPoolFromRates(ctx, pool, runAt)
		errText := ""
		if runErr != nil {
			errText = sanitizeText(runErr.Error())
			if s.log != nil {
				s.log.Warn("automatic main station pool expansion failed", "err", runErr, "pool_id", pool.ID)
			}
		}
		if err := s.store.UpdatePoolAutoExpansionStatus(pool.ID, runAt, errText); err != nil && s.log != nil {
			s.log.Warn("update automatic expansion status", "err", err, "pool_id", pool.ID)
		}
	}
}

func (s *Service) expandPoolFromRates(ctx context.Context, pool *storage.MainAccountPool, now time.Time) error {
	groupIDs, err := s.store.ListPoolGroupIDs(pool.ID)
	if err != nil {
		return err
	}
	if len(groupIDs) != 1 {
		return errors.New("自动扩池要求主站分组与账号池保持一对一")
	}
	group, err := s.targetGroups.FindByID(groupIDs[0])
	if err != nil {
		return err
	}
	if group.Missing || !strings.EqualFold(group.Status, "active") {
		return errors.New("主站分组当前不可用，已跳过自动扩池")
	}
	if unsupportedPricing(group) {
		return errors.New("当前主站分组计费方式不支持自动利润判断")
	}
	platform := normalizeHealthPlatform(pool.Platform)
	mode, err := quickTestAPIMode(platform)
	if err != nil {
		return errors.New("当前主站分组类型不支持自动扩池测试")
	}
	model := strings.TrimSpace(s.configuredHealthModels()[platform])
	if model == "" {
		return fmt.Errorf("尚未配置 %s 类型的全局探活模型", platform)
	}
	saleMicros, _, reason := effectiveSaleMultiplier(group, now)
	if reason != "" {
		return errors.New(reason)
	}
	members, err := s.store.ListMembers(pool.ID)
	if err != nil {
		return err
	}
	candidates, err := s.autoExpansionCandidates(pool, group, members, platform, saleMicros, now)
	if err != nil {
		return err
	}
	tested := 0
	for i := range candidates {
		if tested >= autoExpansionMaxTestsPerPool || ctx.Err() != nil {
			break
		}
		candidate := &candidates[i]
		tested++
		evidence := autoExpansionEvidence(group, candidate, saleMicros, pool.AutoExpandMinMarginBasisPoints, platform, model)
		result, testErr := s.quickTestRate(ctx, candidate.channel.ID, candidate.rate.ID, RateQuickTestInput{
			Platform: platform,
			Model:    model,
		}, "scheduler")
		if testErr != nil {
			nextAttemptAt := now.Add(autoExpansionErrorCooldown)
			_ = s.saveAutoExpansionAttempt(pool.ID, group.ID, candidate, "error", sanitizeText(testErr.Error()), now, &nextAttemptAt)
			_ = s.appendAudit(&pool.ID, nil, nil, "auto_expand_test", "scheduler", false, nil, nil, evidence, "", sanitizeText(testErr.Error()))
			continue
		}
		if !result.Usable {
			nextAttemptAt := now.Add(autoExpansionFailureCooldown)
			_ = s.saveAutoExpansionAttempt(pool.ID, group.ID, candidate, "failed", result.Message, now, &nextAttemptAt)
			_ = s.appendAudit(&pool.ID, nil, nil, "auto_expand_test", "scheduler", false, nil, result, evidence, result.Message, "")
			continue
		}
		_ = s.saveAutoExpansionAttempt(pool.ID, group.ID, candidate, "usable", result.Message, now, nil)
		_ = s.appendAudit(&pool.ID, nil, nil, "auto_expand_test", "scheduler", true, nil, result, evidence, result.Message, "")
		var member *storage.MainAccountPoolMember
		var createErr error
		if candidate.existingMember != nil {
			member, createErr = s.SyncMember(ctx, pool.ID, candidate.existingMember.ID)
		} else {
			enabled := true
			member, createErr = s.CreateMember(ctx, pool.ID, MemberInput{
				AccountName:       candidate.rate.ModelName,
				OwnershipMode:     "managed",
				SourceChannelID:   candidate.channel.ID,
				SourceGroupID:     candidate.rate.RemoteGroupID,
				SourceGroupName:   candidate.rate.ModelName,
				AllowNameConflict: true,
				Enabled:           &enabled,
				Preferred:         boolPointer(false),
				Priority:          1,
				Concurrency:       0,
				RateConvertMode:   "raw",
				RateConvertValue:  1,
				CostAdjustment:    1,
				HealthEnabled:     &enabled,
				HealthModel:       model,
				HealthAPIMode:     mode,
			})
		}
		if createErr != nil {
			status := "create_error"
			next := now.Add(autoExpansionErrorCooldown)
			nextAttemptAt := &next
			if member != nil {
				status = "added_error"
			}
			_ = s.saveAutoExpansionAttempt(pool.ID, group.ID, candidate, status, sanitizeText(createErr.Error()), now, nextAttemptAt)
			var memberID *uint
			var remoteAccountID *int64
			if member != nil {
				memberID = &member.ID
				remoteAccountID = member.RemoteAccountID
			}
			_ = s.appendAudit(&pool.ID, memberID, remoteAccountID, "auto_expand_member_add", "scheduler", false, nil, member, evidence, "", sanitizeText(createErr.Error()))
			continue
		}
		_ = s.saveAutoExpansionAttempt(pool.ID, group.ID, candidate, "added", "已自动加入主站分组", now, nil)
		_ = s.appendAudit(&pool.ID, &member.ID, member.RemoteAccountID, "auto_expand_member_add", "scheduler", true, nil, member, evidence, "已通过利润筛选和连续三次测试，自动加入主站分组", "")
		break
	}
	return nil
}

func (s *Service) autoExpansionCandidates(
	pool *storage.MainAccountPool,
	group *storage.UpstreamSyncTargetGroup,
	members []storage.MainAccountPoolMember,
	platform string,
	saleMicros int64,
	now time.Time,
) ([]autoExpansionCandidate, error) {
	channels, err := s.channels.ListMonitorEnabled()
	if err != nil {
		return nil, err
	}
	candidates := make([]autoExpansionCandidate, 0)
	for i := range channels {
		channel := channels[i]
		rates, err := s.rates.ListByChannel(channel.ID)
		if err != nil {
			return nil, err
		}
		for j := range rates {
			rate := rates[j]
			if rate.LastSeenAt.IsZero() || rate.LastSeenAt.Before(now.Add(-autoExpansionRateFreshness)) || classifyAutoExpansionRate(rate) != platform {
				continue
			}
			effectiveRate := connector.ApplyRechargeMultiplier(rate.Ratio, channel.RechargeMultiplier, channel.RechargeMultiplierMode)
			costMicros := scaleFloat(effectiveRate)
			if costMicros <= 0 || costMicros >= saleMicros {
				continue
			}
			marginBasisPoints := profitBasisPoints(saleMicros, costMicros)
			if marginBasisPoints <= pool.AutoExpandMinMarginBasisPoints {
				continue
			}
			attempt, attemptErr := s.store.FindAutoExpansionAttempt(pool.ID, rate.ID)
			matchingMember := autoExpansionMatchingMember(members, channel.ID, &rate)
			if matchingMember != nil && (matchingMember.Status != "error" || attemptErr != nil || attempt.Status != "added_error") {
				continue
			}
			if attemptErr == nil && attempt.NextAttemptAt != nil && now.Before(*attempt.NextAttemptAt) && attempt.CostMultiplierMicros == costMicros {
				continue
			}
			if attemptErr != nil && !errors.Is(attemptErr, gorm.ErrRecordNotFound) {
				return nil, attemptErr
			}
			candidates = append(candidates, autoExpansionCandidate{
				channel: channel, rate: rate, existingMember: matchingMember,
				costMicros: costMicros, marginBasisPoints: marginBasisPoints,
			})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].marginBasisPoints != candidates[j].marginBasisPoints {
			return candidates[i].marginBasisPoints > candidates[j].marginBasisPoints
		}
		if candidates[i].costMicros != candidates[j].costMicros {
			return candidates[i].costMicros < candidates[j].costMicros
		}
		if candidates[i].channel.ID != candidates[j].channel.ID {
			return candidates[i].channel.ID < candidates[j].channel.ID
		}
		return candidates[i].rate.ID < candidates[j].rate.ID
	})
	return candidates, nil
}

func (s *Service) saveAutoExpansionAttempt(
	poolID, targetGroupID uint,
	candidate *autoExpansionCandidate,
	status, message string,
	at time.Time,
	nextAttemptAt *time.Time,
) error {
	return s.store.UpsertAutoExpansionAttempt(&storage.MainStationAutoExpansionAttempt{
		PoolID: poolID, TargetGroupID: targetGroupID, RateID: candidate.rate.ID, ChannelID: candidate.channel.ID,
		Status: status, CostMultiplierMicros: candidate.costMicros, MarginBasisPoints: candidate.marginBasisPoints,
		LastAttemptAt: at, NextAttemptAt: nextAttemptAt, Message: sanitizeText(message),
	})
}

func autoExpansionEvidence(
	group *storage.UpstreamSyncTargetGroup,
	candidate *autoExpansionCandidate,
	saleMicros, threshold int64,
	platform, model string,
) map[string]any {
	return map[string]any{
		"target_group_id":             group.ID,
		"target_group":                group.Name,
		"channel_id":                  candidate.channel.ID,
		"channel":                     candidate.channel.Name,
		"rate_id":                     candidate.rate.ID,
		"source_group":                candidate.rate.ModelName,
		"platform":                    platform,
		"model":                       model,
		"sale_multiplier_micros":      saleMicros,
		"cost_multiplier_micros":      candidate.costMicros,
		"margin_basis_points":         candidate.marginBasisPoints,
		"minimum_margin_basis_points": threshold,
	}
}

func autoExpansionMatchingMember(members []storage.MainAccountPoolMember, channelID uint, rate *storage.RateSnapshot) *storage.MainAccountPoolMember {
	for i := range members {
		member := &members[i]
		if member.SourceChannelID != channelID || member.Status == "orphaned" || member.BindingStatus == "orphaned" {
			continue
		}
		if member.SourceGroupID != nil && rate.RemoteGroupID != nil && *member.SourceGroupID == *rate.RemoteGroupID {
			return member
		}
		if strings.TrimSpace(member.SourceGroupName) != "" && strings.EqualFold(strings.TrimSpace(member.SourceGroupName), strings.TrimSpace(rate.ModelName)) {
			return member
		}
	}
	return nil
}

func classifyAutoExpansionRate(rate storage.RateSnapshot) string {
	if platform := normalizeHealthPlatform(rate.Platform); platform != "" {
		return platform
	}
	text := rate.ModelName + " " + rate.Description
	for i := range autoExpansionProviderPatterns {
		if autoExpansionProviderPatterns[i].pattern.MatchString(text) {
			return autoExpansionProviderPatterns[i].platform
		}
	}
	return "other"
}

func boolPointer(value bool) *bool {
	return &value
}
