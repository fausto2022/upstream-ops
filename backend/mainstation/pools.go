package mainstation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fausto2022/relaydeck/backend/connector"
	"github.com/fausto2022/relaydeck/backend/connector/sub2api"
	"github.com/fausto2022/relaydeck/backend/storage"
	"gorm.io/gorm"
)

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
			Group:                       groups[i],
			Enabled:                     pool.Enabled,
			MinimumHealthyAccounts:      pool.MinimumHealthyMembers,
			MinimumEffectiveConcurrency: pool.MinimumEffectiveConcurrency,
			RateSortDirection:           pool.RateSortDirection,
			HealthPolicy:                pool.HealthPolicyJSON,
			MarginPolicy:                pool.MarginPolicyJSON,
			LastStatus:                  pool.LastStatus,
			LastEvaluatedAt:             pool.LastEvaluatedAt,
			AccountCount:                accountCount,
			ManagedAccountCount:         len(members),
		})
	}
	return result, nil
}

func (s *Service) ListGroupAccounts(groupID uint, includeMissing bool) ([]AccountDTO, error) {
	group, err := s.mainStationGroup(groupID)
	if err != nil {
		return nil, err
	}
	items, err := s.store.ListAllAccountSnapshots(includeMissing)
	if err != nil {
		return nil, err
	}
	result := make([]AccountDTO, 0)
	for i := range items {
		if accountBelongsToRemoteGroup(&items[i], group.RemoteGroupID) {
			result = append(result, s.accountDTO(items[i]))
		}
	}
	return result, nil
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

func (s *Service) UpdateGroupSettings(groupID uint, in GroupSettingsInput) (*GroupWorkspaceDTO, error) {
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
	before := *pool
	if in.Enabled != nil {
		pool.Enabled = *in.Enabled
	}
	pool.MinimumHealthyMembers = in.MinimumHealthyAccounts
	pool.MinimumEffectiveConcurrency = in.MinimumEffectiveConcurrency
	if strings.TrimSpace(in.RateSortDirection) != "" {
		pool.RateSortDirection = strings.TrimSpace(in.RateSortDirection)
	}
	pool.HealthPolicyJSON = strings.TrimSpace(in.HealthPolicy)
	pool.MarginPolicyJSON = strings.TrimSpace(in.MarginPolicy)
	if err := s.store.UpdatePool(pool, []uint{group.ID}); err != nil {
		return nil, err
	}
	_ = s.appendAudit(&pool.ID, nil, nil, "group_settings_update", "manual", true, before, pool, map[string]any{"group_id": group.ID}, "", "")
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
	item.MarginPolicyJSON = strings.TrimSpace(in.MarginPolicy)
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
	if _, err := s.channels.FindByID(in.SourceChannelID); err != nil {
		return nil, fmt.Errorf("load source channel: %w", err)
	}
	if in.Concurrency <= 0 {
		limits, err := s.channelSvc.GetAccountLimits(ctx, in.SourceChannelID)
		if err != nil {
			return nil, fmt.Errorf("load source account concurrency: %w", err)
		}
		in.Concurrency = limits.Concurrency
	}
	if in.HealthIntervalSeconds != nil {
		if err := validateMemberHealthInterval(*in.HealthIntervalSeconds); err != nil {
			return nil, err
		}
	}
	if in.HealthFailureThreshold != nil {
		if err := validateMemberHealthThreshold("member health failure threshold", *in.HealthFailureThreshold); err != nil {
			return nil, err
		}
	}
	if in.HealthRecoveryThreshold != nil {
		if err := validateMemberHealthThreshold("member health recovery threshold", *in.HealthRecoveryThreshold); err != nil {
			return nil, err
		}
	}
	in.Priority = normalizeSchedulingPriority(in.Priority)
	in.Weight = automaticLoadFactor(in.Concurrency)
	mode := strings.ToLower(strings.TrimSpace(in.OwnershipMode))
	switch mode {
	case "managed":
		return s.createManagedMember(ctx, poolID, in)
	case "bound":
		member, err := s.createBoundMember(poolID, in)
		if err != nil {
			return nil, err
		}
		if rankingErr := s.ReconcilePoolRanking(ctx, poolID, "member_bind"); rankingErr != nil && s.log != nil {
			s.log.Warn("reconcile main station scheduling rank", "err", rankingErr, "pool_id", poolID)
		}
		return s.store.FindMember(poolID, member.ID)
	default:
		return nil, errors.New("ownership_mode must be managed or bound")
	}
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
	if member.OwnershipMode == "managed" {
		return s.SyncMember(ctx, poolID, memberID)
	}
	if rankingErr := s.ReconcilePoolRanking(ctx, poolID, "member_update"); rankingErr != nil && s.log != nil {
		s.log.Warn("reconcile main station scheduling rank", "err", rankingErr, "pool_id", poolID)
	}
	return s.store.FindMember(poolID, memberID)
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
		return nil, errors.New("managed member cannot specify remote_account_id")
	}
	member := memberFromInput(poolID, in)
	member.OwnershipMode = "managed"
	member.BindingStatus = "pending"
	member.Status = "pending"
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
	_, target, adminAPIKey, err := s.loadAdminTarget()
	if err != nil {
		return nil, s.failManagedMember(member, err)
	}
	client := s.adminFactory()
	adminTarget := sub2api.AdminTarget{BaseURL: target.BaseURL, APIKey: adminAPIKey}
	priorities, err := s.poolSchedulingPriorities(poolID)
	if err != nil {
		return nil, s.failManagedMember(member, err)
	}
	priority := priorities[member.ID]
	if priority <= 0 {
		priority = 1
	}
	request, secret, err := s.managedAccountRequest(ctx, pool, member, priority)
	if err != nil {
		return nil, s.failManagedMember(member, err)
	}
	accountName := request.Name
	var remote *sub2api.AdminAccount
	if member.RemoteAccountID != nil {
		remote, err = client.UpdateAccount(ctx, adminTarget, *member.RemoteAccountID, request)
		if err != nil {
			if current, getErr := client.GetAccount(ctx, adminTarget, *member.RemoteAccountID); getErr == nil {
				remote = current
			}
		}
	} else {
		existing, findErr := client.FindAccountByName(ctx, adminTarget, accountName)
		if findErr != nil {
			return nil, s.failManagedMember(member, findErr)
		}
		if existing != nil {
			if !strings.Contains(existing.Notes, fmt.Sprintf("managed member:%d", member.ID)) {
				return nil, s.failManagedMember(member, errors.New("managed account name is already used by an unrelated remote account"))
			}
			remote, err = client.UpdateAccount(ctx, adminTarget, existing.ID, request)
		} else {
			remote, err = client.CreateAccount(ctx, adminTarget, request)
			if err != nil {
				if recovered, findErr := client.FindAccountByName(ctx, adminTarget, accountName); findErr == nil && recovered != nil && strings.Contains(recovered.Notes, fmt.Sprintf("managed member:%d", member.ID)) {
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
	if _, err := s.ActivateGuardLock(ctx, remoteID, "sync", "new managed member is awaiting initial health checks", map[string]any{
		"pool_id": poolID, "member_id": member.ID,
	}, "syncer"); err != nil {
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
	if rankingErr := s.ReconcilePoolRanking(ctx, poolID, "member_sync"); rankingErr != nil && s.log != nil {
		s.log.Warn("reconcile main station scheduling rank", "err", rankingErr, "pool_id", poolID)
	}
	return member, nil
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
			"api_key":  secret,
			"base_url": strings.TrimRight(channel.SiteURL, "/"),
		},
	}, secret, nil
}

func (s *Service) ensureManagedSourceAPIKey(ctx context.Context, pool *storage.MainAccountPool, member *storage.MainAccountPoolMember) (string, error) {
	if member.SourceAPIKeyID != nil {
		secret, err := s.channelSvc.RevealAPIKey(ctx, member.SourceChannelID, *member.SourceAPIKeyID)
		if err != nil {
			return "", fmt.Errorf("reveal managed source api key: %w", err)
		}
		if strings.TrimSpace(secret) == "" {
			return "", errors.New("managed source api key is empty")
		}
		return secret, nil
	}
	name := fmt.Sprintf("RelayDeck-%s-%d", compactName(pool.Name, 64), member.ID)
	key, err := s.channelSvc.CreateAPIKey(ctx, member.SourceChannelID, connector.APIKeyCreateRequest{
		Name:    name,
		Group:   member.SourceGroupName,
		GroupID: member.SourceGroupID,
	})
	if err != nil {
		return "", fmt.Errorf("create managed source api key: %w", err)
	}
	keyID := key.ID
	member.SourceAPIKeyID = &keyID
	if err := s.store.UpdateMember(member); err != nil {
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
		if _, err := s.ActivateGuardLock(ctx, *member.RemoteAccountID, "manual", "managed member is being removed", map[string]any{
			"pool_id": poolID, "member_id": memberID,
		}, "admin"); err != nil {
			return fmt.Errorf("pause managed remote account before removal: %w", err)
		}
		if in.DeleteRemoteAccount {
			if err := client.DeleteAccount(ctx, adminTarget, *member.RemoteAccountID); err != nil {
				return fmt.Errorf("delete managed remote account: %w", err)
			}
		}
		if err := s.ClearSchedulingLock(ctx, *member.RemoteAccountID, "manual", "admin"); err != nil {
			return fmt.Errorf("clear temporary removal lock: %w", err)
		}
	}
	if in.DeleteSourceAPIKey && member.SourceAPIKeyID != nil {
		if err := s.channelSvc.DeleteAPIKey(ctx, member.SourceChannelID, *member.SourceAPIKeyID); err != nil {
			return fmt.Errorf("delete managed source api key: %w", err)
		}
	}
	if err := s.store.DeleteMember(poolID, memberID); err != nil {
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
	return fmt.Sprintf("RelayDeck-%s-%d", compactName(pool.Name, 80), member.ID)
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
