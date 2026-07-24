package mainstation

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/fausto2022/relaydeck/backend/connector"
	"github.com/fausto2022/relaydeck/backend/connector/sub2api"
	"github.com/fausto2022/relaydeck/backend/notify"
	"github.com/fausto2022/relaydeck/backend/storage"
)

func (s *Service) Sync(ctx context.Context) (*SyncResult, error) {
	return s.sync(ctx, "manual")
}

func (s *Service) SyncForScheduler(ctx context.Context) bool {
	s.syncScheduleMu.Lock()
	defer s.syncScheduleMu.Unlock()
	config, err := s.store.GetConfig()
	if err != nil || !config.Enabled {
		return false
	}
	if config.LastSyncAt != nil && s.now().Sub(*config.LastSyncAt) < time.Duration(normalizedSyncInterval(config.SyncIntervalSeconds))*time.Second {
		return false
	}
	result, err := s.sync(ctx, "scheduler")
	if err != nil {
		if s.log != nil {
			s.log.Warn("scheduled main station sync", "err", err)
		}
		return false
	}
	return result.PricingChanged
}

func (s *Service) sync(ctx context.Context, source string) (*SyncResult, error) {
	_, target, apiKey, err := s.loadAdminTarget()
	if err != nil {
		return nil, err
	}
	adminTarget := sub2api.AdminTarget{BaseURL: target.BaseURL, APIKey: apiKey}
	client := s.adminFactory()
	syncedAt := s.now()
	groups, err := client.ListGroups(ctx, adminTarget, true)
	if err != nil {
		return nil, s.recordSyncFailure(target, apiKey, source, fmt.Errorf("sync main station groups: %w", err))
	}
	accounts, err := client.ListAllAccounts(ctx, adminTarget)
	if err != nil {
		return nil, s.recordSyncFailure(target, apiKey, source, fmt.Errorf("sync main station accounts: %w", err))
	}
	existingGroups, err := s.targetGroups.ListByTarget(target.ID, true)
	if err != nil {
		return nil, s.recordSyncFailure(target, apiKey, source, fmt.Errorf("load existing main station groups: %w", err))
	}
	existingByRemoteID := make(map[int64]storage.UpstreamSyncTargetGroup, len(existingGroups))
	for i := range existingGroups {
		existingByRemoteID[existingGroups[i].RemoteGroupID] = existingGroups[i]
	}
	pricingChanged := false

	remoteGroupIDs := make([]int64, 0, len(groups))
	for _, group := range groups {
		remoteGroupIDs = append(remoteGroupIDs, group.ID)
		ratio := group.Ratio
		if ratio == 0 && group.RateMultiplier != 0 {
			ratio = group.RateMultiplier
		}
		var userMinRateMicros *int64
		userRatesComplete := false
		if multipliers, rateErr := client.ListGroupRateMultipliers(ctx, adminTarget, group.ID); rateErr == nil {
			userRatesComplete = true
			if len(multipliers) > 0 {
				value := scaleFloat(multipliers[0])
				userMinRateMicros = &value
			}
		}
		pricingMetadata := ""
		if len(group.PricingMetadata) > 0 && string(group.PricingMetadata) != "null" {
			pricingMetadata = string(group.PricingMetadata)
		}
		item := &storage.UpstreamSyncTargetGroup{
			TargetID:             target.ID,
			RemoteGroupID:        group.ID,
			Name:                 strings.TrimSpace(group.Name),
			Platform:             strings.TrimSpace(group.Platform),
			Ratio:                ratio,
			RateMultiplierMicros: scaleFloat(ratio),
			Status:               strings.TrimSpace(group.Status),
			Sort:                 group.Sort,
			Description:          strings.TrimSpace(group.Description),
			PeakEnabled:          group.PeakEnabled,
			PeakStart:            strings.TrimSpace(group.PeakStart),
			PeakEnd:              strings.TrimSpace(group.PeakEnd),
			PeakMultiplierMicros: scaleFloat(group.PeakMultiplier),
			SubscriptionType:     strings.TrimSpace(group.SubscriptionType),
			ImageSeparateRate:    group.ImageSeparateRate,
			VideoSeparateRate:    group.VideoSeparateRate,
			PricingMetadataJSON:  pricingMetadata,
			UserMinRateMicros:    userMinRateMicros,
			UserRatesComplete:    userRatesComplete,
			Missing:              false,
			LastSyncAt:           &syncedAt,
		}
		previous, exists := existingByRemoteID[group.ID]
		if !exists || mainStationGroupPricingChanged(&previous, item) {
			pricingChanged = true
		}
		if err := s.targetGroups.Upsert(item); err != nil {
			return nil, s.recordSyncFailure(target, apiKey, source, fmt.Errorf("save main station group %d: %w", group.ID, err))
		}
	}
	missingGroups, err := s.targetGroups.MarkMissing(target.ID, remoteGroupIDs, syncedAt)
	if err != nil {
		return nil, s.recordSyncFailure(target, apiKey, source, fmt.Errorf("mark missing main station groups: %w", err))
	}
	if len(missingGroups) > 0 {
		pricingChanged = true
	}

	snapshots := make([]storage.MainStationAccountSnapshot, 0, len(accounts))
	for _, account := range accounts {
		snapshots = append(snapshots, accountSnapshot(account))
	}
	existingSnapshots, err := s.store.ListAllAccountSnapshots(true)
	if err != nil {
		return nil, s.recordSyncFailure(target, apiKey, source, fmt.Errorf("load existing main station account snapshots: %w", err))
	}
	accountSchedulingChanged := mainStationAccountSchedulingChanged(existingSnapshots, snapshots)
	missingAccounts, err := s.store.ReplaceAccountSnapshots(snapshots, syncedAt)
	if err != nil {
		return nil, s.recordSyncFailure(target, apiKey, source, fmt.Errorf("save main station account snapshots: %w", err))
	}
	sourceBindings, err := s.refreshSourceAPIKeyGroups(ctx, client, adminTarget, accounts, missingAccounts, source)
	if err != nil {
		return nil, s.recordSyncFailure(target, apiKey, source, fmt.Errorf("refresh source api key groups: %w", err))
	}
	if sourceBindings.Updated > 0 || sourceBindings.Missing > 0 ||
		sourceBindings.Renamed > 0 || sourceBindings.Cleaned > 0 {
		pricingChanged = true
	}
	s.syncProfitSnapshots(ctx, client, adminTarget, syncedAt)
	orphanedMembers, err := s.store.MarkMembersOrphaned(missingAccounts)
	if err != nil {
		return nil, s.recordSyncFailure(target, apiKey, source, fmt.Errorf("mark orphaned main station members: %w", err))
	}
	if err := s.store.UpdateSyncStatus("success", &syncedAt, ""); err != nil {
		return nil, err
	}
	_ = s.targets.UpdateCheck(target.ID, "success", &syncedAt, "")
	result := &SyncResult{
		Groups:                len(groups),
		Accounts:              len(accounts),
		PricingChanged:        pricingChanged,
		MissingGroups:         missingGroups,
		MissingAccounts:       missingAccounts,
		SourceBindingsChecked: sourceBindings.Checked,
		SourceBindingsUpdated: sourceBindings.Updated,
		SourceBindingsMissing: sourceBindings.Missing,
		SourceBindingsRenamed: sourceBindings.Renamed,
		SourceBindingsCleaned: sourceBindings.Cleaned,
		SourceBindingWarnings: sourceBindings.Warnings,
		SyncedAt:              syncedAt,
	}
	if len(orphanedMembers) > 0 {
		result.OrphanedMembers = len(orphanedMembers)
		s.notifyOrphanedMembers(ctx, orphanedMembers)
	}
	if pricingChanged || accountSchedulingChanged {
		if err := s.store.MarkAllPoolRankingsDirty(syncedAt); err != nil && s.log != nil {
			s.log.Warn("mark main station rankings dirty after sync", "err", err)
		}
	}
	_ = s.appendAudit(nil, nil, nil, "main_station_sync", source, true, nil, result, nil, "", "")
	return result, nil
}

func mainStationAccountSchedulingChanged(existing, current []storage.MainStationAccountSnapshot) bool {
	existingByRemoteID := make(map[int64]storage.MainStationAccountSnapshot, len(existing))
	for i := range existing {
		existingByRemoteID[existing[i].RemoteAccountID] = existing[i]
	}
	currentRemoteIDs := make(map[int64]struct{}, len(current))
	for i := range current {
		item := current[i]
		currentRemoteIDs[item.RemoteAccountID] = struct{}{}
		previous, ok := existingByRemoteID[item.RemoteAccountID]
		if !ok {
			continue
		}
		if previous.Status != item.Status || previous.Schedulable != item.Schedulable ||
			previous.Concurrency != item.Concurrency || previous.Priority != item.Priority ||
			previous.Weight != item.Weight || !sameRemoteGroupIDs(previous.GroupIDsJSON, item.GroupIDsJSON) || previous.Missing {
			return true
		}
	}
	for i := range existing {
		if existing[i].Missing {
			continue
		}
		if _, ok := currentRemoteIDs[existing[i].RemoteAccountID]; !ok {
			return true
		}
	}
	return false
}

func sameRemoteGroupIDs(leftJSON, rightJSON string) bool {
	var left, right []int64
	if err := json.Unmarshal([]byte(leftJSON), &left); err != nil {
		return leftJSON == rightJSON
	}
	if err := json.Unmarshal([]byte(rightJSON), &right); err != nil {
		return false
	}
	if len(left) != len(right) {
		return false
	}
	counts := make(map[int64]int, len(left))
	for _, id := range left {
		counts[id]++
	}
	for _, id := range right {
		counts[id]--
		if counts[id] < 0 {
			return false
		}
	}
	return true
}

func mainStationGroupPricingChanged(previous, current *storage.UpstreamSyncTargetGroup) bool {
	if previous == nil || current == nil {
		return true
	}
	return previous.RateMultiplierMicros != current.RateMultiplierMicros ||
		!optionalInt64Equal(previous.UserMinRateMicros, current.UserMinRateMicros) ||
		previous.UserRatesComplete != current.UserRatesComplete ||
		previous.PeakEnabled != current.PeakEnabled ||
		previous.PeakStart != current.PeakStart ||
		previous.PeakEnd != current.PeakEnd ||
		previous.PeakMultiplierMicros != current.PeakMultiplierMicros ||
		previous.SubscriptionType != current.SubscriptionType ||
		previous.ImageSeparateRate != current.ImageSeparateRate ||
		previous.VideoSeparateRate != current.VideoSeparateRate ||
		previous.Status != current.Status ||
		previous.Missing != current.Missing
}

func optionalInt64Equal(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *Service) recordSyncFailure(target *storage.UpstreamSyncTarget, apiKey, source string, syncErr error) error {
	safeErr := redactSecretError(syncErr, apiKey)
	now := time.Now()
	_ = s.store.UpdateSyncStatus("failed", &now, safeErr.Error())
	_ = s.targets.UpdateCheck(target.ID, "failed", &now, safeErr.Error())
	_ = s.appendAudit(nil, nil, nil, "main_station_sync", source, false, nil, nil, nil, "", safeErr.Error())
	return safeErr
}

func (s *Service) notifyOrphanedMembers(ctx context.Context, members []storage.MainAccountPoolMember) {
	if s.dispatcher == nil {
		return
	}
	for _, member := range members {
		remoteID := int64(0)
		if member.RemoteAccountID != nil {
			remoteID = *member.RemoteAccountID
		}
		dedupKey := fmt.Sprintf("%s:%d:%d:0", storage.EventMainMemberBindingLost, member.PoolID, member.ID)
		claimed, err := s.store.TryClaimNotificationCooldown(
			dedupKey, string(storage.EventMainMemberBindingLost), member.PoolID, member.ID, 0, 100*365*24*time.Hour,
		)
		if err != nil {
			if s.log != nil {
				s.log.Warn("claim main station orphan notification", "err", err, "member_id", member.ID)
			}
			continue
		}
		if !claimed {
			continue
		}
		pool, _ := s.store.FindPool(member.PoolID)
		poolName := fmt.Sprintf("#%d", member.PoolID)
		if pool != nil {
			poolName = pool.Name
		}
		message := notify.Message{
			Event:     storage.EventMainMemberBindingLost,
			ChannelID: member.SourceChannelID,
			Subject:   fmt.Sprintf("主站绑定失效 · %s · 成员 #%d", poolName, member.ID),
			Body: notify.MarkdownDetails(
				"同步结果中已找不到该主站账号，原绑定关系失效。",
				notify.Detail("账号池", poolName),
				notify.Detail("成员", fmt.Sprintf("#%d", member.ID)),
				notify.Detail("主站账号", fmt.Sprintf("%s (ID %d)", member.RemoteAccountName, remoteID)),
				notify.Detail("上游渠道 ID", member.SourceChannelID),
				notify.Detail("上游分组", member.SourceGroupName),
			) + notify.MarkdownNote("系统动作", "成员已标记为绑定失效，并停止向远端自动写入。请重新确认并绑定对应账号。"),
			Extra: map[string]any{"pool_id": member.PoolID, "member_id": member.ID, "remote_account_id": remoteID},
		}
		if err := s.dispatcher.Dispatch(ctx, message); err != nil && s.log != nil {
			s.log.Warn("dispatch main station orphan notification", "err", err, "member_id", member.ID)
		}
	}
}

func (s *Service) ListGroups(includeMissing bool) ([]storage.UpstreamSyncTargetGroup, error) {
	config, err := s.store.GetConfig()
	if err != nil {
		return nil, err
	}
	return s.targetGroups.ListByTarget(config.TargetID, includeMissing)
}

func (s *Service) ListAccounts(page, pageSize int, includeMissing, unboundOnly bool) (*Page[AccountDTO], error) {
	items, total, err := s.store.ListAccountSnapshots(page, pageSize, includeMissing, unboundOnly)
	if err != nil {
		return nil, err
	}
	out := make([]AccountDTO, 0, len(items))
	for _, item := range items {
		out = append(out, s.accountDTO(item))
	}
	page, pageSize = normalizePage(page, pageSize)
	return &Page[AccountDTO]{
		Items: out, Total: total, Page: page, PageSize: pageSize, Pages: pageCount(total, pageSize),
	}, nil
}

func (s *Service) accountDTO(item storage.MainStationAccountSnapshot) AccountDTO {
	dto := AccountDTO{MainStationAccountSnapshot: item}
	if member, err := s.store.FindMemberByRemoteAccountID(item.RemoteAccountID); err == nil {
		recent20SuccessRate := s.recent20SuccessRate(member.ID)
		sourceGroupRate, sourceGroupRateObservedAt := s.sourceGroupRate(member)
		dto.Member = &AccountMemberDTO{
			ID:                        member.ID,
			AccountName:               member.AccountName,
			OwnershipMode:             member.OwnershipMode,
			BindingStatus:             member.BindingStatus,
			Status:                    member.Status,
			Enabled:                   member.Enabled,
			Preferred:                 member.Preferred,
			SourceChannelID:           member.SourceChannelID,
			SourceGroupID:             member.SourceGroupID,
			SourceGroupName:           member.SourceGroupName,
			SourceGroupRateMultiplier: sourceGroupRate,
			SourceGroupRateObservedAt: sourceGroupRateObservedAt,
			SourceAPIKeyID:            member.SourceAPIKeyID,
			Weight:                    member.Weight,
			Priority:                  member.Priority,
			Concurrency:               member.Concurrency,
			HealthEnabled:             member.HealthEnabled,
			HealthModel:               member.HealthModel,
			HealthIntervalSeconds:     member.HealthIntervalSeconds,
			HealthFailureThreshold:    member.HealthFailureThreshold,
			HealthRecoveryThreshold:   member.HealthRecoveryThreshold,
			Recent20SuccessRate:       recent20SuccessRate,
			LastHealthStatus:          member.LastHealthStatus,
			LastHealthAt:              member.LastHealthAt,
			ConsecutiveHealthSuccess:  member.ConsecutiveHealthSuccess,
			ConsecutiveHealthFailure:  member.ConsecutiveHealthFailure,
			SchedulingDirtyAt:         member.SchedulingDirtyAt,
			LastSchedulingAt:          member.LastSchedulingAt,
			LastSchedulingError:       member.LastSchedulingError,
		}
	}
	return dto
}

func (s *Service) sourceGroupRate(member *storage.MainAccountPoolMember) (*float64, *time.Time) {
	if s.rates == nil || member == nil {
		return nil, nil
	}
	if member.SourceGroupID == nil && strings.TrimSpace(member.SourceGroupName) == "" {
		ratio := s.applySourceRechargeMultiplier(member.SourceChannelID, 1)
		return &ratio, nil
	}
	snapshots, err := s.rates.ListByChannel(member.SourceChannelID)
	if err != nil {
		return nil, nil
	}
	for i := range snapshots {
		matchedByID := member.SourceGroupID != nil && snapshots[i].RemoteGroupID != nil &&
			*member.SourceGroupID == *snapshots[i].RemoteGroupID
		matchedByName := strings.TrimSpace(member.SourceGroupName) != "" &&
			strings.EqualFold(strings.TrimSpace(member.SourceGroupName), strings.TrimSpace(snapshots[i].ModelName))
		if matchedByID || matchedByName {
			ratio := s.applySourceRechargeMultiplier(member.SourceChannelID, snapshots[i].Ratio)
			observedAt := snapshots[i].LastSeenAt
			return &ratio, &observedAt
		}
	}
	return nil, nil
}

func (s *Service) applySourceRechargeMultiplier(channelID uint, ratio float64) float64 {
	if s.channels == nil {
		return ratio
	}
	channelItem, err := s.channels.FindByID(channelID)
	if err != nil {
		return ratio
	}
	return connector.ApplyRechargeMultiplier(ratio, channelItem.RechargeMultiplier, channelItem.RechargeMultiplierMode)
}

func accountSnapshot(account sub2api.AdminAccount) storage.MainStationAccountSnapshot {
	weight := account.Weight
	if weight <= 0 && account.LoadFactor > 0 {
		weight = int(math.Round(account.LoadFactor))
	}
	if weight <= 0 {
		weight = 1
	}
	baseURL := credentialString(account.Credentials, "base_url")
	credentialsPresent := false
	for _, key := range []string{"api_key", "access_token", "token"} {
		if value := credentialString(account.Credentials, key); value != "" {
			credentialsPresent = true
			break
		}
	}
	billingProbe := billingProbeSnapshot(account.Extra)
	return storage.MainStationAccountSnapshot{
		MainStationID:        storage.MainStationSingletonID,
		RemoteAccountID:      account.ID,
		Name:                 strings.TrimSpace(account.Name),
		Notes:                strings.TrimSpace(account.Notes),
		Platform:             strings.TrimSpace(account.Platform),
		Type:                 strings.TrimSpace(account.Type),
		Status:               strings.TrimSpace(account.Status),
		Schedulable:          account.Schedulable,
		Concurrency:          account.Concurrency,
		Priority:             account.Priority,
		Weight:               weight,
		RateMultiplierMicros: scaleFloat(account.RateMultiplier),
		GroupIDsJSON:         safeJSON(account.GroupIDs),
		BaseURL:              strings.TrimRight(baseURL, "/"),
		CredentialsPresent:   credentialsPresent,
		BillingProbeJSON:     billingProbe,
		LastUsedAt:           account.LastUsedAt,
		RemoteUpdatedAt:      account.UpdatedAt,
	}
}

func credentialString(credentials map[string]any, key string) string {
	if credentials == nil {
		return ""
	}
	value, ok := credentials[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func isMaskedCredential(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.Contains(lower, "***") || strings.Contains(lower, "redacted") || strings.Contains(lower, "masked")
}

func billingProbeSnapshot(extra json.RawMessage) string {
	if len(extra) == 0 || string(extra) == "null" {
		return ""
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(extra, &object); err != nil {
		return ""
	}
	probe, ok := object["upstream_billing_probe"]
	if !ok || len(probe) == 0 || string(probe) == "null" {
		return ""
	}
	return sanitizeText(string(probe))
}

func scaleFloat(value float64) int64 {
	return int64(math.Round(value * float64(storage.MainStationScale)))
}

func normalizePage(page, pageSize int) (int, int) {
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

func pageCount(total int64, pageSize int) int {
	if pageSize <= 0 {
		return 0
	}
	pages := int((total + int64(pageSize) - 1) / int64(pageSize))
	if pages < 1 && total > 0 {
		return 1
	}
	return pages
}
