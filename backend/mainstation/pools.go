package mainstation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fausto2022/relaydeck/backend/connector"
	"github.com/fausto2022/relaydeck/backend/connector/sub2api"
	"github.com/fausto2022/relaydeck/backend/storage"
	"gorm.io/gorm"
)

const managedAccountPoolModeRetryCount = 10

func managedAccountPoolModeRetryStatusCodes() []int {
	return []int{400, 401, 403, 429, 502, 503, 524}
}

func (s *Service) ListRateConnections(channelID uint, rates []storage.RateSnapshot) (map[uint][]RateConnection, error) {
	connections := make(map[uint][]RateConnection, len(rates))
	if channelID == 0 || len(rates) == 0 {
		return connections, nil
	}
	members, err := s.store.ListAllMembers()
	if err != nil {
		return nil, err
	}
	poolConnections := make(map[uint][]RateConnection)
	seenByRate := make(map[uint]map[uint]struct{}, len(rates))
	for i := range members {
		member := &members[i]
		if member.SourceChannelID != channelID || member.RemoteAccountID == nil || *member.RemoteAccountID <= 0 ||
			member.BindingStatus == "invalid" || member.BindingStatus == "orphaned" || member.Status == "orphaned" {
			continue
		}
		groups, ok := poolConnections[member.PoolID]
		if !ok {
			groupIDs, listErr := s.store.ListPoolGroupIDs(member.PoolID)
			if listErr != nil {
				return nil, listErr
			}
			groups = make([]RateConnection, 0, len(groupIDs))
			for _, groupID := range groupIDs {
				group, findErr := s.targetGroups.FindByID(groupID)
				if findErr != nil {
					return nil, findErr
				}
				if !group.Missing {
					groups = append(groups, RateConnection{GroupID: group.ID, GroupName: group.Name})
				}
			}
			poolConnections[member.PoolID] = groups
		}
		for j := range rates {
			if !sourceMemberMatchesRate(member, &rates[j], len(rates)) {
				continue
			}
			if seenByRate[rates[j].ID] == nil {
				seenByRate[rates[j].ID] = make(map[uint]struct{})
			}
			for _, group := range groups {
				if _, exists := seenByRate[rates[j].ID][group.GroupID]; exists {
					continue
				}
				seenByRate[rates[j].ID][group.GroupID] = struct{}{}
				connections[rates[j].ID] = append(connections[rates[j].ID], group)
			}
		}
	}
	for rateID := range connections {
		sort.Slice(connections[rateID], func(i, j int) bool {
			return connections[rateID][i].GroupID < connections[rateID][j].GroupID
		})
	}
	return connections, nil
}

func sourceMemberMatchesRate(member *storage.MainAccountPoolMember, rate *storage.RateSnapshot, totalRates int) bool {
	if member.SourceGroupID != nil && rate.RemoteGroupID != nil && *member.SourceGroupID == *rate.RemoteGroupID {
		return true
	}
	if name := strings.TrimSpace(member.SourceGroupName); name != "" && strings.EqualFold(name, strings.TrimSpace(rate.ModelName)) {
		return true
	}
	return member.SourceGroupID == nil && strings.TrimSpace(member.SourceGroupName) == "" && totalRates == 1
}

func (s *Service) ListGroupWorkspaces(includeMissing bool) ([]GroupWorkspaceDTO, error) {
	groups, err := s.ListGroups(includeMissing)
	if err != nil {
		return nil, err
	}
	snapshots, err := s.store.ListAllAccountSnapshots(includeMissing)
	if err != nil {
		return nil, err
	}
	result := make([]GroupWorkspaceDTO, 0, len(groups))
	for i := range groups {
		pool, err := s.poolForGroup(&groups[i])
		if err != nil {
			return nil, err
		}
		members, err := s.store.ListMembers(pool.ID)
		if err != nil {
			return nil, err
		}
		accountCount := 0
		for j := range snapshots {
			if accountBelongsToRemoteGroup(&snapshots[j], groups[i].RemoteGroupID) {
				accountCount++
			}
		}
		result = append(result, GroupWorkspaceDTO{
			Group:                          groups[i],
			Enabled:                        pool.Enabled,
			MinimumHealthyAccounts:         pool.MinimumHealthyMembers,
			MinimumEffectiveConcurrency:    pool.MinimumEffectiveConcurrency,
			RateSortDirection:              pool.RateSortDirection,
			HealthPolicy:                   pool.HealthPolicyJSON,
			MarginPolicy:                   pool.MarginPolicyJSON,
			MinimumMarginBasisPoints:       poolMinimumMarginOverride(pool),
			LastStatus:                     pool.LastStatus,
			LastEvaluatedAt:                pool.LastEvaluatedAt,
			RankingIntervalSeconds:         pool.RankingIntervalSeconds,
			RankingDirtyAt:                 pool.RankingDirtyAt,
			LastRankingAt:                  pool.LastRankingAt,
			LastRankingError:               pool.LastRankingError,
			AutoExpandEnabled:              pool.AutoExpandEnabled,
			AutoExpandMinMarginBasisPoints: pool.AutoExpandMinMarginBasisPoints,
			LastAutoExpandAt:               pool.LastAutoExpandAt,
			LastAutoExpandError:            pool.LastAutoExpandError,
			AccountCount:                   accountCount,
			ManagedAccountCount:            len(members),
		})
	}
	return result, nil
}

func (s *Service) ListGroupAccounts(groupID uint, includeMissing bool) ([]AccountDTO, error) {
	group, err := s.mainStationGroup(groupID)
	if err != nil {
		return nil, err
	}
	pool, err := s.poolForGroup(group)
	if err != nil {
		return nil, err
	}
	config, err := s.store.GetConfig()
	if err != nil {
		return nil, err
	}
	policy := parseMarginPolicy(pool.MarginPolicyJSON)
	policy.MinimumMarginBasisPoints = effectiveMinimumMarginBasisPoints(config, pool)
	now := s.now()
	items, err := s.store.ListAllAccountSnapshots(includeMissing)
	if err != nil {
		return nil, err
	}
	result := make([]AccountDTO, 0)
	for i := range items {
		if accountBelongsToRemoteGroup(&items[i], group.RemoteGroupID) {
			dto := s.accountDTO(items[i])
			if dto.Member != nil {
				member, memberErr := s.store.FindMemberByRemoteAccountID(items[i].RemoteAccountID)
				if memberErr != nil {
					return nil, memberErr
				}
				current := s.buildProfitCheck(pool, member, group, s.resolveMemberCost(member, policy, now), policy, now)
				dto.Member.CurrentProfit = accountProfitDTO(current, policy.MinimumMarginBasisPoints)
				check, checkErr := s.store.LatestProfitCheck(dto.Member.ID, group.ID)
				switch {
				case checkErr == nil:
					dto.Member.LatestProfit = accountProfitDTO(*check, policy.MinimumMarginBasisPoints)
				case !errors.Is(checkErr, gorm.ErrRecordNotFound):
					return nil, checkErr
				}
			}
			result = append(result, dto)
		}
	}
	return result, nil
}

func accountProfitDTO(check storage.MainAccountProfitCheck, minimumMarginBasisPoints int64) *AccountProfitDTO {
	return &AccountProfitDTO{
		Status:                   check.Status,
		SaleMultiplierMicros:     check.SaleMultiplierMicros,
		CostMultiplierMicros:     check.CostMultiplierMicros,
		MarginBasisPoints:        check.MarginBasisPoints,
		MinimumMarginBasisPoints: minimumMarginBasisPoints,
		SaleSource:               check.SaleSource,
		CostSource:               check.CostSource,
		Reason:                   check.Reason,
		ObservedAt:               check.ObservedAt,
	}
}

func (s *Service) GroupPoolID(groupID uint) (uint, error) {
	group, err := s.mainStationGroup(groupID)
	if err != nil {
		return 0, err
	}
	pool, err := s.poolForGroup(group)
	if err != nil {
		return 0, err
	}
	return pool.ID, nil
}

func (s *Service) UpdateGroupSettings(ctx context.Context, groupID uint, in GroupSettingsInput) (*GroupWorkspaceDTO, error) {
	group, err := s.mainStationGroup(groupID)
	if err != nil {
		return nil, err
	}
	pool, err := s.poolForGroup(group)
	if err != nil {
		return nil, err
	}
	if err := validatePolicyJSON("health_policy", in.HealthPolicy); err != nil {
		return nil, err
	}
	if err := validatePolicyJSON("margin_policy", in.MarginPolicy); err != nil {
		return nil, err
	}
	marginPolicy, err := marginPolicyWithoutMinimum(in.MarginPolicy)
	if err != nil {
		return nil, err
	}
	before := *pool
	enabledChanged := false
	if in.Enabled != nil {
		enabledChanged = pool.Enabled != *in.Enabled
		pool.Enabled = *in.Enabled
	}
	pool.MinimumHealthyMembers = in.MinimumHealthyAccounts
	pool.MinimumEffectiveConcurrency = in.MinimumEffectiveConcurrency
	if strings.TrimSpace(in.RateSortDirection) != "" {
		pool.RateSortDirection = strings.TrimSpace(in.RateSortDirection)
	}
	if err := validatePoolRankingInterval(in.RankingIntervalSeconds); err != nil {
		return nil, err
	}
	if in.MinimumMarginBasisPoints != nil {
		if err := validateMinimumMarginBasisPoints(*in.MinimumMarginBasisPoints); err != nil {
			return nil, err
		}
	}
	if err := validateAutoExpandMarginBasisPoints(in.AutoExpandMinMarginBasisPoints); err != nil {
		return nil, err
	}
	if in.AutoExpandEnabled {
		platform := normalizeHealthPlatform(pool.Platform)
		if _, err := quickTestAPIMode(platform); err != nil {
			return nil, errors.New("当前主站分组类型不支持自动扩池测试")
		}
		if strings.TrimSpace(s.configuredHealthModels()[platform]) == "" {
			return nil, fmt.Errorf("请先在主站配置中设置 %s 类型的全局探活模型", platform)
		}
	}
	pool.RankingIntervalSeconds = in.RankingIntervalSeconds
	pool.MinimumMarginBasisPoints = copyOptionalInt64(in.MinimumMarginBasisPoints)
	pool.AutoExpandEnabled = in.AutoExpandEnabled
	pool.AutoExpandMinMarginBasisPoints = in.AutoExpandMinMarginBasisPoints
	pool.HealthPolicyJSON = strings.TrimSpace(in.HealthPolicy)
	pool.MarginPolicyJSON = marginPolicy
	if err := s.store.UpdatePool(pool, []uint{group.ID}); err != nil {
		return nil, err
	}
	if err := s.markPoolRankingDirty(pool.ID); err != nil {
		return nil, err
	}
	var schedulingErr error
	if enabledChanged {
		schedulingErr = s.reconcilePoolScheduling(ctx, pool.ID, "manual")
		if schedulingErr != nil {
			if s.log != nil {
				s.log.Warn("group settings saved; scheduling reconcile queued for retry", "err", schedulingErr, "pool_id", pool.ID)
			}
		}
	}
	detail := ""
	errText := ""
	if schedulingErr != nil {
		detail = "group settings saved; scheduling reconcile queued for retry"
		errText = sanitizeText(schedulingErr.Error())
	}
	_ = s.appendAudit(&pool.ID, nil, nil, "group_settings_update", "manual", schedulingErr == nil, before, pool, map[string]any{"group_id": group.ID}, detail, errText)
	items, err := s.ListGroupWorkspaces(false)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Group.ID == group.ID {
			return &items[i], nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (s *Service) mainStationGroup(groupID uint) (*storage.UpstreamSyncTargetGroup, error) {
	if groupID == 0 {
		return nil, errors.New("invalid main station group id")
	}
	config, err := s.store.GetConfig()
	if err != nil {
		return nil, ErrNotConfigured
	}
	group, err := s.targetGroups.FindByID(groupID)
	if err != nil {
		return nil, err
	}
	if group.TargetID != config.TargetID || group.Missing {
		return nil, errors.New("main station group does not belong to the active station")
	}
	return group, nil
}

func (s *Service) poolForGroup(group *storage.UpstreamSyncTargetGroup) (*storage.MainAccountPool, error) {
	pool, err := s.store.FindPoolByTargetGroupID(group.ID)
	if err == nil {
		groupIDs, listErr := s.store.ListPoolGroupIDs(pool.ID)
		if listErr != nil {
			return nil, listErr
		}
		if len(groupIDs) != 1 || groupIDs[0] != group.ID {
			return nil, errors.New("main station group has an invalid internal policy mapping")
		}
		return pool, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	platform := strings.TrimSpace(group.Platform)
	if platform == "" {
		platform = "openai"
	}
	pool = &storage.MainAccountPool{
		Name:        fmt.Sprintf("group-%d-%s", group.RemoteGroupID, compactName(group.Name, 120)),
		Description: "main station group policy",
		Platform:    platform,
		Enabled:     true,
	}
	if err := s.store.CreatePool(pool, []uint{group.ID}); err != nil {
		if existing, findErr := s.store.FindPoolByTargetGroupID(group.ID); findErr == nil {
			return existing, nil
		}
		return nil, err
	}
	return pool, nil
}

func accountBelongsToRemoteGroup(account *storage.MainStationAccountSnapshot, remoteGroupID int64) bool {
	var ids []int64
	if err := json.Unmarshal([]byte(account.GroupIDsJSON), &ids); err != nil {
		return false
	}
	for _, id := range ids {
		if id == remoteGroupID {
			return true
		}
	}
	return false
}

func (s *Service) ListPools(page, pageSize int) (*Page[PoolDTO], error) {
	items, total, err := s.store.ListPools(page, pageSize)
	if err != nil {
		return nil, err
	}
	out := make([]PoolDTO, 0, len(items))
	for i := range items {
		dto, err := s.poolDTO(&items[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *dto)
	}
	page, pageSize = normalizePage(page, pageSize)
	return &Page[PoolDTO]{Items: out, Total: total, Page: page, PageSize: pageSize, Pages: pageCount(total, pageSize)}, nil
}

func (s *Service) GetPool(id uint) (*PoolDTO, error) {
	item, err := s.store.FindPool(id)
	if err != nil {
		return nil, err
	}
	return s.poolDTO(item)
}

func (s *Service) CreatePool(in PoolInput) (*PoolDTO, error) {
	item, groupIDs, err := s.poolFromInput(nil, in)
	if err != nil {
		return nil, err
	}
	if err := s.store.CreatePool(item, groupIDs); err != nil {
		return nil, err
	}
	if err := s.markPoolRankingDirty(item.ID); err != nil {
		return nil, err
	}
	poolID := item.ID
	_ = s.appendAudit(&poolID, nil, nil, "pool_create", "manual", true, nil, item, nil, "", "")
	return s.GetPool(item.ID)
}

func (s *Service) UpdatePool(id uint, in PoolInput) (*PoolDTO, error) {
	existing, err := s.store.FindPool(id)
	if err != nil {
		return nil, err
	}
	before := *existing
	item, groupIDs, err := s.poolFromInput(existing, in)
	if err != nil {
		return nil, err
	}
	if err := s.store.UpdatePool(item, groupIDs); err != nil {
		return nil, err
	}
	if err := s.markPoolRankingDirty(item.ID); err != nil {
		return nil, err
	}
	poolID := item.ID
	_ = s.appendAudit(&poolID, nil, nil, "pool_update", "manual", true, before, item, nil, "", "")
	return s.GetPool(item.ID)
}

func (s *Service) DeletePool(id uint) error {
	item, err := s.store.FindPool(id)
	if err != nil {
		return err
	}
	if err := s.store.DeletePool(id); err != nil {
		return err
	}
	_ = s.appendAudit(&id, nil, nil, "pool_delete", "manual", true, item, nil, nil, "", "")
	return nil
}

func (s *Service) poolFromInput(existing *storage.MainAccountPool, in PoolInput) (*storage.MainAccountPool, []uint, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, nil, errors.New("account pool name is required")
	}
	if len(in.TargetGroupIDs) == 0 {
		return nil, nil, errors.New("account pool must serve at least one main station group")
	}
	if err := s.validateTargetGroups(in.TargetGroupIDs); err != nil {
		return nil, nil, err
	}
	if err := validatePolicyJSON("health_policy", in.HealthPolicy); err != nil {
		return nil, nil, err
	}
	if err := validatePolicyJSON("margin_policy", in.MarginPolicy); err != nil {
		return nil, nil, err
	}
	marginPolicy, err := marginPolicyWithoutMinimum(in.MarginPolicy)
	if err != nil {
		return nil, nil, err
	}
	item := &storage.MainAccountPool{}
	if existing != nil {
		*item = *existing
	}
	item.Name = name
	item.Description = strings.TrimSpace(in.Description)
	item.Platform = strings.TrimSpace(in.Platform)
	item.MinimumHealthyMembers = in.MinimumHealthyMembers
	item.MinimumEffectiveConcurrency = in.MinimumEffectiveConcurrency
	item.RateSortDirection = strings.TrimSpace(in.RateSortDirection)
	item.HealthPolicyJSON = strings.TrimSpace(in.HealthPolicy)
	item.MarginPolicyJSON = marginPolicy
	if in.MinimumMarginBasisPoints != nil {
		if err := validateMinimumMarginBasisPoints(*in.MinimumMarginBasisPoints); err != nil {
			return nil, nil, err
		}
	}
	item.MinimumMarginBasisPoints = copyOptionalInt64(in.MinimumMarginBasisPoints)
	if err := validatePoolRankingInterval(in.RankingIntervalSeconds); err != nil {
		return nil, nil, err
	}
	item.RankingIntervalSeconds = in.RankingIntervalSeconds
	if err := validateAutoExpandMarginBasisPoints(in.AutoExpandMinMarginBasisPoints); err != nil {
		return nil, nil, err
	}
	item.AutoExpandEnabled = in.AutoExpandEnabled
	item.AutoExpandMinMarginBasisPoints = in.AutoExpandMinMarginBasisPoints
	if item.AutoExpandEnabled {
		platform := normalizeHealthPlatform(item.Platform)
		if _, err := quickTestAPIMode(platform); err != nil {
			return nil, nil, errors.New("当前账号池类型不支持自动扩池测试")
		}
		if strings.TrimSpace(s.configuredHealthModels()[platform]) == "" {
			return nil, nil, fmt.Errorf("请先在主站配置中设置 %s 类型的全局探活模型", platform)
		}
	}
	if existing == nil {
		item.Enabled = true
	}
	if in.Enabled != nil {
		item.Enabled = *in.Enabled
	}
	return item, in.TargetGroupIDs, nil
}

func (s *Service) validateTargetGroups(ids []uint) error {
	config, err := s.store.GetConfig()
	if err != nil {
		return ErrNotConfigured
	}
	seen := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			return errors.New("invalid main station group id")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		group, err := s.targetGroups.FindByID(id)
		if err != nil {
			return fmt.Errorf("load main station group %d: %w", id, err)
		}
		if group.TargetID != config.TargetID || group.Missing {
			return fmt.Errorf("main station group %d does not belong to the active station", id)
		}
	}
	return nil
}

func validatePolicyJSON(field, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		return fmt.Errorf("%s must be a JSON object: %w", field, err)
	}
	return nil
}

func marginPolicyWithoutMinimum(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		return "", fmt.Errorf("margin_policy must be a JSON object: %w", err)
	}
	if _, exists := object["minimum_margin_basis_points"]; !exists {
		return raw, nil
	}
	delete(object, "minimum_margin_basis_points")
	encoded, err := json.Marshal(object)
	if err != nil {
		return "", fmt.Errorf("encode margin_policy: %w", err)
	}
	return string(encoded), nil
}

func copyOptionalInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func (s *Service) poolDTO(item *storage.MainAccountPool) (*PoolDTO, error) {
	groupIDs, err := s.store.ListPoolGroupIDs(item.ID)
	if err != nil {
		return nil, err
	}
	groups := make([]storage.UpstreamSyncTargetGroup, 0, len(groupIDs))
	for _, id := range groupIDs {
		group, err := s.targetGroups.FindByID(id)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		groups = append(groups, *group)
	}
	members, err := s.store.ListMembers(item.ID)
	if err != nil {
		return nil, err
	}
	return &PoolDTO{MainAccountPool: *item, TargetGroupIDs: groupIDs, Groups: groups, Members: members}, nil
}

func (s *Service) CreateMember(ctx context.Context, poolID uint, in MemberInput) (*storage.MainAccountPoolMember, error) {
	if _, err := s.store.FindPool(poolID); err != nil {
		return nil, err
	}
	prepared, err := s.prepareMemberInput(ctx, in)
	if err != nil {
		return nil, err
	}
	in = prepared
	mode := strings.ToLower(strings.TrimSpace(in.OwnershipMode))
	switch mode {
	case "managed":
		return s.createManagedMember(ctx, poolID, in)
	case "bound":
		member, err := s.createBoundMember(poolID, in)
		if err != nil {
			return nil, err
		}
		if rankingErr := s.markPoolRankingDirty(poolID); rankingErr != nil && s.log != nil {
			s.log.Warn("mark main station scheduling rank dirty", "err", rankingErr, "pool_id", poolID)
		}
		return s.store.FindMember(poolID, member.ID)
	default:
		return nil, errors.New("ownership_mode must be managed or bound")
	}
}

func (s *Service) prepareMemberInput(ctx context.Context, in MemberInput) (MemberInput, error) {
	if _, err := s.channels.FindByID(in.SourceChannelID); err != nil {
		return in, fmt.Errorf("load source channel: %w", err)
	}
	if in.Concurrency <= 0 {
		limits, err := s.channelSvc.GetAccountLimits(ctx, in.SourceChannelID)
		if err != nil {
			return in, fmt.Errorf("load source account concurrency: %w", err)
		}
		if limits == nil || limits.Concurrency <= 0 {
			return in, errors.New("source account concurrency is unavailable")
		}
		in.Concurrency = limits.Concurrency
	}
	if in.HealthIntervalSeconds != nil {
		if err := validateMemberHealthInterval(*in.HealthIntervalSeconds); err != nil {
			return in, err
		}
	}
	if in.HealthFailureThreshold != nil {
		if err := validateMemberHealthThreshold("member health failure threshold", *in.HealthFailureThreshold); err != nil {
			return in, err
		}
	}
	if in.HealthRecoveryThreshold != nil {
		if err := validateMemberHealthThreshold("member health recovery threshold", *in.HealthRecoveryThreshold); err != nil {
			return in, err
		}
	}
	in.Priority = normalizeSchedulingPriority(in.Priority)
	in.Weight = automaticLoadFactor(in.Concurrency)
	return in, nil
}

func (s *Service) UpdateMember(ctx context.Context, poolID, memberID uint, in MemberInput) (*storage.MainAccountPoolMember, error) {
	member, err := s.store.FindMember(poolID, memberID)
	if err != nil {
		return nil, err
	}
	before := *member
	if in.SourceChannelID != 0 {
		if _, err := s.channels.FindByID(in.SourceChannelID); err != nil {
			return nil, fmt.Errorf("load source channel: %w", err)
		}
		member.SourceChannelID = in.SourceChannelID
	}
	member.SourceGroupID = in.SourceGroupID
	member.SourceGroupName = strings.TrimSpace(in.SourceGroupName)
	if strings.TrimSpace(in.AccountName) != "" {
		member.AccountName = strings.TrimSpace(in.AccountName)
	}
	if in.SourceAPIKeyID != nil {
		if !sameOptionalInt64(member.SourceAPIKeyID, in.SourceAPIKeyID) {
			member.SourceAPIKeyManaged = false
		}
		member.SourceAPIKeyID = in.SourceAPIKeyID
	}
	if in.Enabled != nil {
		member.Enabled = *in.Enabled
	}
	if in.Preferred != nil {
		member.Preferred = *in.Preferred
	}
	if in.HealthEnabled != nil {
		member.HealthEnabled = *in.HealthEnabled
	}
	if in.HealthIntervalSeconds != nil {
		if err := validateMemberHealthInterval(*in.HealthIntervalSeconds); err != nil {
			return nil, err
		}
		member.HealthIntervalSeconds = *in.HealthIntervalSeconds
	}
	if in.HealthFailureThreshold != nil {
		if err := validateMemberHealthThreshold("member health failure threshold", *in.HealthFailureThreshold); err != nil {
			return nil, err
		}
		member.HealthFailureThreshold = *in.HealthFailureThreshold
	}
	if in.HealthRecoveryThreshold != nil {
		if err := validateMemberHealthThreshold("member health recovery threshold", *in.HealthRecoveryThreshold); err != nil {
			return nil, err
		}
		member.HealthRecoveryThreshold = *in.HealthRecoveryThreshold
	}
	if in.ProxyID != nil {
		member.ProxyID = in.ProxyID
	}
	if in.Priority > 0 {
		member.Priority = normalizeSchedulingPriority(in.Priority)
	}
	if in.Concurrency > 0 {
		member.Concurrency = in.Concurrency
		member.Weight = automaticLoadFactor(in.Concurrency)
	}
	if strings.TrimSpace(in.RateConvertMode) != "" {
		member.RateConvertMode = strings.TrimSpace(in.RateConvertMode)
		member.RateConvertValueMicros = scaleFloat(in.RateConvertValue)
	}
	if in.CostAdjustment > 0 {
		member.CostAdjustmentMicros = scaleFloat(in.CostAdjustment)
	}
	if in.ManualCostMultiplier != nil {
		value := scaleFloat(*in.ManualCostMultiplier)
		member.ManualCostMicros = &value
	}
	member.HealthModel = strings.TrimSpace(in.HealthModel)
	if strings.TrimSpace(in.HealthAPIMode) != "" {
		member.HealthAPIMode = strings.TrimSpace(in.HealthAPIMode)
	}
	if err := s.store.UpdateMember(member); err != nil {
		return nil, err
	}
	_ = s.appendAudit(&poolID, &memberID, member.RemoteAccountID, "member_update", "manual", true, before, member, nil, "", "")
	remoteConfigChanged := member.OwnershipMode == "managed" && managedRemoteConfigChanged(&before, member)
	if remoteConfigChanged {
		if before.Enabled != member.Enabled && member.RemoteAccountID != nil {
			if member.Enabled {
				if dirtyErr := s.store.MarkMemberSchedulingDirty(member.ID, s.now()); dirtyErr != nil {
					return nil, dirtyErr
				}
			} else if _, reconcileErr := s.ReconcileAccount(ctx, *member.RemoteAccountID, "manual"); reconcileErr != nil && s.log != nil {
				s.log.Warn("disable managed member before full sync", "err", reconcileErr, "member_id", member.ID)
			}
		}
		return s.SyncMember(ctx, poolID, memberID)
	}
	if before.Enabled != member.Enabled && member.RemoteAccountID != nil {
		if _, reconcileErr := s.ReconcileAccount(ctx, *member.RemoteAccountID, "manual"); reconcileErr != nil {
			if s.log != nil {
				s.log.Warn("member settings saved; scheduling reconcile queued for retry", "err", reconcileErr, "member_id", member.ID)
			}
		}
	}
	if rankingErr := s.markPoolRankingDirty(poolID); rankingErr != nil && s.log != nil {
		s.log.Warn("mark main station scheduling rank dirty", "err", rankingErr, "pool_id", poolID)
	}
	return s.store.FindMember(poolID, memberID)
}

func managedRemoteConfigChanged(before, after *storage.MainAccountPoolMember) bool {
	if before == nil || after == nil {
		return false
	}
	return before.AccountName != after.AccountName || before.SourceChannelID != after.SourceChannelID ||
		!sameOptionalInt64(before.SourceGroupID, after.SourceGroupID) || before.SourceGroupName != after.SourceGroupName ||
		!sameOptionalInt64(before.SourceAPIKeyID, after.SourceAPIKeyID) || !sameOptionalInt64(before.ProxyID, after.ProxyID) ||
		before.RateConvertMode != after.RateConvertMode || before.RateConvertValueMicros != after.RateConvertValueMicros
}

func sameOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *Service) createBoundMember(poolID uint, in MemberInput) (*storage.MainAccountPoolMember, error) {
	if in.RemoteAccountID == nil || *in.RemoteAccountID <= 0 {
		return nil, errors.New("remote_account_id is required for a bound member")
	}
	if _, err := s.store.FindMemberByRemoteAccountID(*in.RemoteAccountID); err == nil {
		return nil, ErrBindingConflict
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	snapshot, err := s.store.FindAccountSnapshot(*in.RemoteAccountID)
	if err != nil {
		return nil, fmt.Errorf("load remote account snapshot: %w", err)
	}
	if snapshot.Missing {
		return nil, errors.New("remote account is missing")
	}
	channel, err := s.channels.FindByID(in.SourceChannelID)
	if err != nil {
		return nil, err
	}
	baseURLMatches := sameEndpoint(snapshot.BaseURL, channel.SiteURL)
	if !in.ManualBindingConfirmed {
		if baseURLMatches {
			return nil, errors.New("api key identity cannot be verified; manual binding confirmation is required")
		}
		return nil, errors.New("remote account base url differs from the source channel; manual binding confirmation is required")
	}
	member := memberFromInput(poolID, in)
	member.OwnershipMode = "bound"
	member.BindingStatus = "manual_confirmed"
	member.RemoteAccountName = snapshot.Name
	if strings.EqualFold(snapshot.Status, "active") {
		member.Status = "active"
	} else {
		member.Status = "degraded"
	}
	if err := s.store.CreateMember(member); err != nil {
		if isUniqueError(err) {
			return nil, ErrBindingConflict
		}
		return nil, err
	}
	_ = s.appendAudit(&poolID, &member.ID, member.RemoteAccountID, "member_bind", "manual", true, nil, member, map[string]any{
		"base_url_matches": baseURLMatches,
		"manual_confirmed": true,
	}, "", "")
	return member, nil
}

func (s *Service) createManagedMember(ctx context.Context, poolID uint, in MemberInput) (*storage.MainAccountPoolMember, error) {
	if in.RemoteAccountID != nil {
		return nil, errors.New("新建托管账号时不能指定已有主站账号")
	}
	pool, err := s.store.FindPool(poolID)
	if err != nil {
		return nil, err
	}
	member := memberFromInput(poolID, in)
	member.OwnershipMode = "managed"
	member.BindingStatus = "pending"
	member.Status = "pending"
	if member.SourceAPIKeyID == nil {
		member.AccountName = s.managedAutomaticName(pool, member)
	}
	if !in.AllowNameConflict {
		conflict, conflictErr := s.managedAccountNameExists(ctx, pool, member)
		if conflictErr != nil {
			return nil, conflictErr
		}
		if conflict {
			return nil, fmt.Errorf("%w：%s", ErrManagedAccountNameConflict, managedAccountName(pool, member))
		}
	}
	if err := s.store.CreateMember(member); err != nil {
		return nil, err
	}
	if _, err := s.SyncMember(ctx, poolID, member.ID); err != nil {
		return member, err
	}
	return s.store.FindMember(poolID, member.ID)
}

func (s *Service) SyncMember(ctx context.Context, poolID, memberID uint) (*storage.MainAccountPoolMember, error) {
	pool, err := s.store.FindPool(poolID)
	if err != nil {
		return nil, err
	}
	member, err := s.store.FindMember(poolID, memberID)
	if err != nil {
		return nil, err
	}
	if member.OwnershipMode != "managed" {
		return nil, errors.New("bound member configuration is not managed")
	}
	if member.RemoteAccountID != nil {
		dirtyAt := s.now()
		if err := s.store.MarkMemberSchedulingDirty(member.ID, dirtyAt); err != nil {
			return nil, err
		}
		member.SchedulingDirtyAt = &dirtyAt
	}
	_, target, adminAPIKey, err := s.loadAdminTarget()
	if err != nil {
		return nil, s.failManagedMember(member, err)
	}
	client := s.adminFactory()
	adminTarget := sub2api.AdminTarget{BaseURL: target.BaseURL, APIKey: adminAPIKey}
	priority := normalizeSchedulingPriority(member.Priority)
	if member.RemoteAccountID != nil {
		if snapshot, snapshotErr := s.store.FindAccountSnapshot(*member.RemoteAccountID); snapshotErr == nil && snapshot.Priority > 0 {
			priority = snapshot.Priority
		}
	}
	request, secret, err := s.managedAccountRequest(ctx, pool, member, priority)
	if err != nil {
		return nil, s.failManagedMember(member, err)
	}
	accountName := request.Name
	var remote *sub2api.AdminAccount
	if member.RemoteAccountID != nil {
		remote, err = client.UpdateAccount(ctx, adminTarget, *member.RemoteAccountID, request)
		if err != nil && missingRemoteResource(err) {
			member.RemoteAccountID = nil
		} else if err != nil {
			if current, getErr := client.GetAccount(ctx, adminTarget, *member.RemoteAccountID); getErr == nil {
				remote = current
			}
		}
	}
	if member.RemoteAccountID == nil {
		existing, findErr := findManagedRemoteAccount(ctx, client, adminTarget, accountName, member.ID)
		if findErr != nil {
			return nil, s.failManagedMember(member, findErr)
		}
		if existing != nil {
			remote, err = client.UpdateAccount(ctx, adminTarget, existing.ID, request)
		} else {
			remote, err = client.CreateAccount(ctx, adminTarget, request)
			if err != nil {
				if recovered, findErr := findManagedRemoteAccount(ctx, client, adminTarget, accountName, member.ID); findErr == nil && recovered != nil {
					remote = recovered
					err = nil
				}
			}
		}
	}
	if err != nil {
		return nil, s.failManagedMember(member, redactSecretError(err, secret))
	}
	if remote == nil || remote.ID <= 0 {
		return nil, s.failManagedMember(member, errors.New("main station did not return the managed account id"))
	}
	remoteID := remote.ID
	member.RemoteAccountID = &remoteID
	member.RemoteAccountName = remote.Name
	member.BindingStatus = "verified"
	member.Status = "pending"
	if err := s.store.UpdateMember(member); err != nil {
		return nil, err
	}
	snapshot := accountSnapshot(*remote)
	snapshot.Schedulable = false
	snapshot.LastSyncAt = time.Now()
	if err := s.store.UpsertAccountSnapshot(&snapshot); err != nil && s.log != nil {
		s.log.Warn("save managed account snapshot", "err", err, "member_id", member.ID)
	}
	if _, err := s.ActivateGuardLock(ctx, remoteID, "sync", "managed member is awaiting model sync and initial health checks", map[string]any{
		"pool_id": poolID, "member_id": member.ID,
	}, "syncer"); err != nil {
		return nil, s.failManagedMember(member, err)
	}
	if err := s.syncManagedAccountModels(ctx, client, adminTarget, remoteID); err != nil {
		return nil, s.failManagedMember(member, err)
	}
	l0, err := s.CheckMember(ctx, poolID, member.ID, HealthCheckInput{Level: "L0", Force: true})
	if err != nil || l0.Check.Status != "success" {
		if err == nil {
			err = fmt.Errorf("initial L0 health check status: %s", l0.Check.Status)
		}
		return nil, s.failManagedMember(member, err)
	}
	if effectiveHealthModel(pool.Platform, member.HealthModel, s.configuredHealthModels()) != "" {
		l1, err := s.CheckMember(ctx, poolID, member.ID, HealthCheckInput{Level: "L1", Force: true})
		if err != nil || l1.Check.Status != "success" {
			if err == nil {
				err = fmt.Errorf("initial L1 health check status: %s", l1.Check.Status)
			}
			return nil, s.failManagedMember(member, err)
		}
	}
	if _, err := s.ClearGuardLock(ctx, remoteID, "sync", "syncer"); err != nil {
		return nil, s.failManagedMember(member, err)
	}
	member, err = s.store.FindMember(poolID, member.ID)
	if err != nil {
		return nil, err
	}
	_ = s.appendAudit(&poolID, &member.ID, &remoteID, "member_managed_sync", "manual", true, nil, member, nil, "initial health checks passed and scheduling was reconciled", "")
	if rankingErr := s.markPoolRankingDirty(poolID); rankingErr != nil && s.log != nil {
		s.log.Warn("mark main station scheduling rank dirty", "err", rankingErr, "pool_id", poolID)
	}
	return member, nil
}

func (s *Service) managedAccountNameExists(ctx context.Context, pool *storage.MainAccountPool, member *storage.MainAccountPoolMember) (bool, error) {
	_, target, adminAPIKey, err := s.loadAdminTarget()
	if err != nil {
		return false, err
	}
	accounts, err := s.adminFactory().ListAllAccounts(ctx, sub2api.AdminTarget{BaseURL: target.BaseURL, APIKey: adminAPIKey})
	if err != nil {
		return false, fmt.Errorf("检查主站同名账号失败：%w", redactSecretError(err, adminAPIKey))
	}
	name := managedAccountName(pool, member)
	for i := range accounts {
		if strings.EqualFold(accounts[i].Name, name) {
			return true, nil
		}
	}
	return false, nil
}

func findManagedRemoteAccount(ctx context.Context, client adminClient, target sub2api.AdminTarget, name string, memberID uint) (*sub2api.AdminAccount, error) {
	accounts, err := client.ListAllAccounts(ctx, target)
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		if strings.EqualFold(accounts[i].Name, name) && hasManagedMemberMarker(accounts[i].Notes, memberID) {
			item := accounts[i]
			return &item, nil
		}
	}
	return nil, nil
}

func hasManagedMemberMarker(notes string, memberID uint) bool {
	fields := strings.Fields(notes)
	marker := fmt.Sprintf("member:%d", memberID)
	for i := 1; i < len(fields); i++ {
		if strings.EqualFold(fields[i-1], "managed") && fields[i] == marker {
			return true
		}
	}
	return false
}

func (s *Service) syncManagedAccountModels(ctx context.Context, client adminClient, target sub2api.AdminTarget, remoteAccountID int64) error {
	models, err := client.SyncAccountModelsFromUpstream(ctx, target, remoteAccountID)
	if err != nil {
		return fmt.Errorf("sync managed account models from upstream: %w", err)
	}
	if len(models) == 0 {
		return errors.New("sync managed account models from upstream returned no models")
	}
	return nil
}

func (s *Service) managedAccountRequest(ctx context.Context, pool *storage.MainAccountPool, member *storage.MainAccountPoolMember, priority int) (sub2api.AdminAccount, string, error) {
	channel, err := s.channels.FindByID(member.SourceChannelID)
	if err != nil {
		return sub2api.AdminAccount{}, "", fmt.Errorf("load source channel: %w", err)
	}
	secret, err := s.ensureManagedSourceAPIKey(ctx, pool, member)
	if err != nil {
		return sub2api.AdminAccount{}, "", err
	}
	groupIDs, err := s.remoteGroupIDsForPool(pool.ID)
	if err != nil {
		return sub2api.AdminAccount{}, secret, err
	}
	rateMultiplier, err := s.sourceRateMultiplier(ctx, member)
	if err != nil {
		return sub2api.AdminAccount{}, secret, err
	}
	loadFactor := automaticLoadFactor(member.Concurrency)
	member.Weight = loadFactor
	member.Priority = normalizeSchedulingPriority(member.Priority)
	return sub2api.AdminAccount{
		Name:           managedAccountName(pool, member),
		Platform:       pool.Platform,
		Type:           "apikey",
		Status:         "active",
		Schedulable:    false,
		Notes:          fmt.Sprintf("RelayDeck managed member:%d", member.ID),
		ProxyID:        member.ProxyID,
		Concurrency:    member.Concurrency,
		Priority:       priority,
		Weight:         loadFactor,
		LoadFactor:     float64(loadFactor),
		RateMultiplier: rateMultiplier,
		GroupIDs:       groupIDs,
		Credentials: map[string]any{
			"api_key":                      secret,
			"base_url":                     strings.TrimRight(channel.SiteURL, "/"),
			"pool_mode":                    true,
			"pool_mode_retry_count":        managedAccountPoolModeRetryCount,
			"pool_mode_retry_status_codes": managedAccountPoolModeRetryStatusCodes(),
		},
	}, secret, nil
}

func (s *Service) ensureManagedSourceAPIKey(ctx context.Context, pool *storage.MainAccountPool, member *storage.MainAccountPoolMember) (string, error) {
	if member.SourceAPIKeyID != nil {
		secret, err := s.channelSvc.RevealAPIKey(ctx, member.SourceChannelID, *member.SourceAPIKeyID)
		if err != nil && !missingRemoteResource(err) {
			return "", fmt.Errorf("reveal managed source api key: %w", err)
		}
		if err == nil && strings.TrimSpace(secret) == "" {
			return "", errors.New("managed source api key is empty")
		}
		if err == nil {
			return secret, nil
		}
		if !member.SourceAPIKeyManaged {
			return "", errors.New("selected source api key no longer exists")
		}
		member.SourceAPIKeyID = nil
	}
	name := managedSourceAPIKeyName(member)
	key, err := s.channelSvc.CreateAPIKey(ctx, member.SourceChannelID, connector.APIKeyCreateRequest{
		Name:    name,
		Group:   member.SourceGroupName,
		GroupID: member.SourceGroupID,
	})
	if err != nil {
		return "", fmt.Errorf("create managed source api key: %w", err)
	}
	keyID := key.ID
	member.AccountName = s.managedAutomaticName(pool, member)
	member.SourceAPIKeyID = &keyID
	member.SourceAPIKeyManaged = true
	if err := s.store.UpdateMember(member); err != nil {
		cleanupErr := s.channelSvc.DeleteAPIKey(context.Background(), member.SourceChannelID, keyID)
		if cleanupErr != nil && !missingRemoteResource(cleanupErr) {
			return "", errors.Join(err, fmt.Errorf("cleanup untracked managed source api key: %w", cleanupErr))
		}
		return "", err
	}
	secret := strings.TrimSpace(key.Key)
	if secret == "" || isMaskedCredential(secret) {
		secret, err = s.channelSvc.RevealAPIKey(ctx, member.SourceChannelID, key.ID)
		if err != nil {
			return "", fmt.Errorf("reveal created source api key: %w", err)
		}
	}
	if strings.TrimSpace(secret) == "" {
		return "", errors.New("created source api key is empty")
	}
	return secret, nil
}

func (s *Service) sourceRateMultiplier(ctx context.Context, member *storage.MainAccountPoolMember) (float64, error) {
	if member.RateConvertMode == "custom" {
		return float64(member.RateConvertValueMicros) / float64(storage.MainStationScale), nil
	}
	groups, err := s.channelSvc.ListAPIKeyGroups(ctx, member.SourceChannelID)
	if err != nil {
		return 0, fmt.Errorf("list source groups: %w", err)
	}
	ratio := 1.0
	matched := member.SourceGroupID == nil && strings.TrimSpace(member.SourceGroupName) == ""
	for _, group := range groups {
		if (member.SourceGroupID != nil && group.ID != nil && *member.SourceGroupID == *group.ID) ||
			(member.SourceGroupName != "" && strings.EqualFold(member.SourceGroupName, group.Name)) {
			ratio = group.Ratio
			matched = true
			break
		}
	}
	if !matched {
		return 0, errors.New("selected source group no longer exists")
	}
	switch member.RateConvertMode {
	case "multiply_100":
		ratio *= 100
	case "divide_100":
		ratio /= 100
	}
	return s.applySourceRechargeMultiplier(member.SourceChannelID, ratio), nil
}

func (s *Service) remoteGroupIDsForPool(poolID uint) ([]int64, error) {
	ids, err := s.store.ListPoolGroupIDs(poolID)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		group, err := s.targetGroups.FindByID(id)
		if err != nil {
			return nil, err
		}
		if group.Missing {
			return nil, fmt.Errorf("main station group %d is missing", group.RemoteGroupID)
		}
		out = append(out, group.RemoteGroupID)
	}
	if len(out) == 0 {
		return nil, errors.New("account pool has no main station groups")
	}
	return out, nil
}

func (s *Service) failManagedMember(member *storage.MainAccountPoolMember, err error) error {
	member.Status = "error"
	if updateErr := s.store.UpdateMember(member); updateErr != nil {
		return errors.Join(err, updateErr)
	}
	_ = s.appendAudit(&member.PoolID, &member.ID, member.RemoteAccountID, "member_managed_sync", "manual", false, nil, member, nil, "", err.Error())
	return err
}

func (s *Service) DeleteMember(ctx context.Context, poolID, memberID uint, in DeleteMemberInput) error {
	if !in.Confirm {
		return errors.New("member deletion requires explicit confirmation")
	}
	member, err := s.store.FindMember(poolID, memberID)
	if err != nil {
		return err
	}
	if member.OwnershipMode == "bound" {
		if in.DeleteRemoteAccount || in.DeleteSourceAPIKey {
			return errors.New("bound member deletion cannot delete remote resources")
		}
		if err := s.store.DeleteMember(poolID, memberID); err != nil {
			return err
		}
		if err := s.markPoolRankingDirty(poolID); err != nil {
			return err
		}
		_ = s.appendAudit(&poolID, &memberID, member.RemoteAccountID, "member_unbind", "manual", true, member, nil, nil, "bound remote resources were preserved", "")
		return nil
	}
	_, target, adminAPIKey, err := s.loadAdminTarget()
	if err != nil {
		return err
	}
	client := s.adminFactory()
	adminTarget := sub2api.AdminTarget{BaseURL: target.BaseURL, APIKey: adminAPIKey}
	if member.RemoteAccountID != nil {
		remoteAccountID := *member.RemoteAccountID
		_, pauseErr := s.ActivateGuardLock(ctx, remoteAccountID, "manual", "managed member is being removed", map[string]any{
			"pool_id": poolID, "member_id": memberID,
		}, "admin")
		if pauseErr != nil && !(in.DeleteRemoteAccount && missingRemoteResource(pauseErr)) {
			return fmt.Errorf("pause managed remote account before removal: %w", pauseErr)
		}
		if in.DeleteRemoteAccount {
			if err := client.DeleteAccount(ctx, adminTarget, remoteAccountID); err != nil && !missingRemoteResource(err) {
				return fmt.Errorf("delete managed remote account: %w", err)
			}
			if err := s.ClearSchedulingLock(ctx, remoteAccountID, "manual", "admin"); err != nil {
				return fmt.Errorf("clear temporary removal lock: %w", err)
			}
		} else {
			if _, err := s.ClearGuardLock(ctx, remoteAccountID, "manual", "admin"); err != nil {
				return fmt.Errorf("restore managed remote account after local removal: %w", err)
			}
		}
	}
	if in.DeleteSourceAPIKey && member.SourceAPIKeyID != nil {
		if err := s.channelSvc.DeleteAPIKey(ctx, member.SourceChannelID, *member.SourceAPIKeyID); err != nil && !missingRemoteResource(err) {
			return fmt.Errorf("delete managed source api key: %w", err)
		}
	}
	if err := s.store.DeleteMember(poolID, memberID); err != nil {
		return err
	}
	if err := s.markPoolRankingDirty(poolID); err != nil {
		return err
	}
	_ = s.appendAudit(&poolID, &memberID, member.RemoteAccountID, "member_delete", "manual", true, member, nil, map[string]any{
		"remote_deleted":     in.DeleteRemoteAccount,
		"source_key_deleted": in.DeleteSourceAPIKey,
	}, "managed remote account was paused before local removal", "")
	return nil
}

func memberFromInput(poolID uint, in MemberInput) *storage.MainAccountPoolMember {
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	healthEnabled := true
	if in.HealthEnabled != nil {
		healthEnabled = *in.HealthEnabled
	}
	preferred := false
	if in.Preferred != nil {
		preferred = *in.Preferred
	}
	healthIntervalSeconds := 0
	if in.HealthIntervalSeconds != nil {
		healthIntervalSeconds = *in.HealthIntervalSeconds
	}
	healthFailureThreshold := 0
	if in.HealthFailureThreshold != nil {
		healthFailureThreshold = *in.HealthFailureThreshold
	}
	healthRecoveryThreshold := 0
	if in.HealthRecoveryThreshold != nil {
		healthRecoveryThreshold = *in.HealthRecoveryThreshold
	}
	costAdjustment := in.CostAdjustment
	if costAdjustment == 0 {
		costAdjustment = 1
	}
	var manualCost *int64
	if in.ManualCostMultiplier != nil {
		value := scaleFloat(*in.ManualCostMultiplier)
		manualCost = &value
	}
	return &storage.MainAccountPoolMember{
		PoolID:                  poolID,
		AccountName:             strings.TrimSpace(in.AccountName),
		SourceChannelID:         in.SourceChannelID,
		SourceGroupID:           in.SourceGroupID,
		SourceGroupName:         strings.TrimSpace(in.SourceGroupName),
		SourceAPIKeyID:          in.SourceAPIKeyID,
		RemoteAccountID:         in.RemoteAccountID,
		Enabled:                 enabled,
		Preferred:               preferred,
		ProxyID:                 in.ProxyID,
		Weight:                  automaticLoadFactor(in.Concurrency),
		Priority:                normalizeSchedulingPriority(in.Priority),
		Concurrency:             in.Concurrency,
		RateConvertMode:         strings.TrimSpace(in.RateConvertMode),
		RateConvertValueMicros:  scaleFloat(in.RateConvertValue),
		CostAdjustmentMicros:    scaleFloat(costAdjustment),
		ManualCostMicros:        manualCost,
		HealthEnabled:           healthEnabled,
		HealthModel:             strings.TrimSpace(in.HealthModel),
		HealthIntervalSeconds:   healthIntervalSeconds,
		HealthFailureThreshold:  healthFailureThreshold,
		HealthRecoveryThreshold: healthRecoveryThreshold,
		HealthAPIMode:           strings.TrimSpace(in.HealthAPIMode),
		LastHealthStatus:        "unknown",
	}
}

func sameEndpoint(left, right string) bool {
	l, err := normalizeMainStationURL(left)
	if err != nil {
		return false
	}
	r, err := normalizeMainStationURL(right)
	if err != nil {
		return false
	}
	return strings.EqualFold(l, r)
}

func managedAccountName(pool *storage.MainAccountPool, member *storage.MainAccountPoolMember) string {
	if name := strings.TrimSpace(member.AccountName); name != "" {
		return compactName(name, 120)
	}
	if name := strings.TrimSpace(member.SourceGroupName); name != "" {
		return compactName(name, 120)
	}
	return compactName(pool.Name, 120)
}

func (s *Service) managedAutomaticName(pool *storage.MainAccountPool, member *storage.MainAccountPoolMember) string {
	channelName := ""
	if member != nil && s.channels != nil {
		if channel, err := s.channels.FindByID(member.SourceChannelID); err == nil {
			channelName = strings.TrimSpace(channel.Name)
		}
	}
	groupName := ""
	if member != nil {
		groupName = strings.TrimSpace(member.SourceGroupName)
		if groupName == "" && member.SourceGroupID != nil {
			groupName = fmt.Sprintf("分组-%d", *member.SourceGroupID)
		}
	}
	if groupName == "" {
		groupName = "默认分组"
	}
	if channelName != "" {
		return compactName(channelName+"-"+groupName, 120)
	}
	if member != nil {
		if name := strings.TrimSpace(member.AccountName); name != "" {
			return compactName(name, 120)
		}
	}
	return compactName(pool.Name+"-"+groupName, 120)
}

func managedSourceAPIKeyName(member *storage.MainAccountPoolMember) string {
	if member != nil {
		if groupName := strings.TrimSpace(member.SourceGroupName); groupName != "" {
			return compactName(groupName, 120)
		}
		if member.SourceGroupID != nil {
			return compactName(fmt.Sprintf("分组-%d", *member.SourceGroupID), 120)
		}
	}
	return "默认分组"
}

func missingRemoteResource(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound) || statusCodeFromError(err) == 404
}

func compactName(value string, max int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), "-")
	if value == "" {
		value = "pool"
	}
	runes := []rune(value)
	if len(runes) > max {
		return string(runes[:max])
	}
	return value
}

func isUniqueError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique") || strings.Contains(text, "duplicate")
}
