package mainstation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fausto2022/relaydeck/backend/connector"
	"github.com/fausto2022/relaydeck/backend/connector/sub2api"
	"github.com/fausto2022/relaydeck/backend/storage"
)

const (
	sourceBindingPageSize = 100
	sourceBindingMaxPages = 10000
)

type sourceBindingRefreshResult struct {
	Checked  int
	Updated  int
	Missing  int
	Renamed  int
	Cleaned  int
	Warnings []string
}

type sourceGroupResolution struct {
	ID    *int64
	Name  string
	State string
}

func (s *Service) refreshSourceAPIKeyGroups(
	ctx context.Context,
	client adminClient,
	adminTarget sub2api.AdminTarget,
	accounts []sub2api.AdminAccount,
	missingRemoteAccountIDs []int64,
	source string,
) (sourceBindingRefreshResult, error) {
	result := sourceBindingRefreshResult{Warnings: make([]string, 0)}
	if s.channelSvc == nil {
		return result, nil
	}
	members, err := s.store.ListAllMembers()
	if err != nil {
		return result, err
	}
	byChannel := make(map[uint][]storage.MainAccountPoolMember)
	for i := range members {
		if members[i].SourceChannelID == 0 || members[i].SourceAPIKeyID == nil || *members[i].SourceAPIKeyID <= 0 {
			continue
		}
		byChannel[members[i].SourceChannelID] = append(byChannel[members[i].SourceChannelID], members[i])
	}
	channelIDs := make([]uint, 0, len(byChannel))
	for channelID := range byChannel {
		channelIDs = append(channelIDs, channelID)
	}
	sort.Slice(channelIDs, func(i, j int) bool { return channelIDs[i] < channelIDs[j] })
	missingRemoteAccounts := make(map[int64]struct{}, len(missingRemoteAccountIDs))
	for _, remoteAccountID := range missingRemoteAccountIDs {
		missingRemoteAccounts[remoteAccountID] = struct{}{}
	}
	remoteAccounts := make(map[int64]sub2api.AdminAccount)
	for i := range accounts {
		remoteAccounts[accounts[i].ID] = accounts[i]
	}

	for _, channelID := range channelIDs {
		channelMembers := byChannel[channelID]
		result.Checked += len(channelMembers)
		wanted := make(map[int64]struct{}, len(channelMembers))
		for i := range channelMembers {
			wanted[*channelMembers[i].SourceAPIKeyID] = struct{}{}
		}
		keys, err := s.sourceAPIKeysByID(ctx, channelID, wanted)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s：读取上游 Key 失败，已保留原分组", s.sourceChannelLabel(channelID)))
			if s.log != nil {
				s.log.Warn("refresh source api key groups", "channel_id", channelID, "err", err)
			}
			continue
		}
		groups, groupErr := s.channelSvc.ListAPIKeyGroups(ctx, channelID)
		groupsAuthoritative := groupErr == nil
		if groupErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s：读取上游分组失败，已跳过删组清理", s.sourceChannelLabel(channelID)))
			if s.log != nil {
				s.log.Warn("refresh source api key groups: list groups", "channel_id", channelID, "err", groupErr)
			}
		}
		preservedMissing := 0
		for i := range channelMembers {
			member := &channelMembers[i]
			before := *member
			keyID := *member.SourceAPIKeyID
			key, ok := keys[keyID]
			if !ok {
				result.Missing++
				if member.OwnershipMode == "managed" && member.SourceAPIKeyManaged {
					if cleanupErr := s.cleanupManagedSourceBinding(ctx, client, adminTarget, member, source, "绑定的上游托管 Key 已不存在"); cleanupErr == nil {
						result.Cleaned++
						continue
					} else {
						result.Warnings = append(result.Warnings, fmt.Sprintf("%s：账号 %s 自动清理失败，将在下次同步重试", s.sourceChannelLabel(channelID), member.AccountName))
						if s.log != nil {
							s.log.Warn("cleanup managed member after source key disappeared", "member_id", member.ID, "err", cleanupErr)
						}
					}
				}
				preservedMissing++
				_ = s.appendAudit(&member.PoolID, &member.ID, member.RemoteAccountID, "member_source_group_sync", source, false,
					member, nil, map[string]any{"source_api_key_id": keyID}, "已保留原上游分组", "绑定的上游 Key 不存在")
				continue
			}
			pool, poolErr := s.store.FindPool(member.PoolID)
			if poolErr != nil {
				return result, fmt.Errorf("load member %d pool: %w", member.ID, poolErr)
			}
			if !member.SourceAPIKeyManaged && s.isLikelyManagedSourceAPIKey(pool, member, &key) {
				member.SourceAPIKeyManaged = true
			}
			resolution := resolveSourceAPIKeyGroup(&key, groups, groupsAuthoritative)
			wasExplicitlyGrouped := member.SourceGroupID != nil || strings.TrimSpace(member.SourceGroupName) != ""
			cleanupReason := ""
			if member.RemoteAccountID != nil {
				if _, missingRemote := missingRemoteAccounts[*member.RemoteAccountID]; missingRemote {
					cleanupReason = "主站托管账号已不存在"
				}
			}
			if cleanupReason == "" && wasExplicitlyGrouped {
				switch resolution.State {
				case "missing":
					cleanupReason = "上游分组已删除"
				case "unbound":
					cleanupReason = "上游托管 Key 已解除分组"
				}
			}
			if cleanupReason != "" && member.OwnershipMode == "managed" && member.SourceAPIKeyManaged {
				if cleanupErr := s.cleanupManagedSourceBinding(ctx, client, adminTarget, member, source, cleanupReason); cleanupErr == nil {
					result.Cleaned++
					continue
				} else {
					result.Warnings = append(result.Warnings, fmt.Sprintf("%s：账号 %s 自动清理失败，将在下次同步重试", s.sourceChannelLabel(channelID), member.AccountName))
					if s.log != nil {
						s.log.Warn("cleanup invalid managed source binding", "member_id", member.ID, "reason", cleanupReason, "err", cleanupErr)
					}
					continue
				}
			}
			if resolution.State == "missing" {
				result.Warnings = append(result.Warnings, fmt.Sprintf("%s：账号 %s 的上游分组不存在，因 Key 非系统托管已保留账号", s.sourceChannelLabel(channelID), member.AccountName))
				continue
			}
			groupChanged := !sameOptionalInt64(member.SourceGroupID, resolution.ID) || member.SourceGroupName != resolution.Name
			member.SourceGroupID = copyOptionalInt64(resolution.ID)
			member.SourceGroupName = resolution.Name
			if groupChanged {
				member.LastCostMicros = nil
				member.LastCostSource = ""
				member.LastCostAt = nil
				member.LastCostExpiresAt = nil
			}
			renamed := false
			if member.OwnershipMode == "managed" {
				if member.SourceAPIKeyManaged && !strings.EqualFold(strings.TrimSpace(key.Name), managedSourceAPIKeyName(member)) {
					name := managedSourceAPIKeyName(member)
					_, renameErr := s.channelSvc.UpdateAPIKey(ctx, channelID, keyID, connector.APIKeyUpdateRequest{
						Name: &name, IPWhitelist: append([]string(nil), key.IPWhitelist...), IPBlacklist: append([]string(nil), key.IPBlacklist...),
					})
					if renameErr != nil {
						result.Warnings = append(result.Warnings, fmt.Sprintf("%s：上游 Key %d 重命名失败，将在下次同步重试", s.sourceChannelLabel(channelID), keyID))
						if s.log != nil {
							s.log.Warn("rename managed source api key", "member_id", member.ID, "key_id", keyID, "err", renameErr)
						}
					} else {
						renamed = true
					}
				}
				remote, remoteExists := remoteAccountForMember(member, remoteAccounts)
				if remoteExists {
					mainRenamed, syncErr := s.syncManagedSourceBinding(ctx, client, adminTarget, pool, member, remote, groupChanged)
					if syncErr != nil {
						result.Warnings = append(result.Warnings, fmt.Sprintf("主站账号 %s 自动更正失败，将在下次同步重试", member.AccountName))
						if s.log != nil {
							s.log.Warn("sync managed source binding", "member_id", member.ID, "err", syncErr)
						}
					} else if mainRenamed {
						renamed = true
					}
				}
			}
			memberChanged := groupChanged || member.SourceAPIKeyManaged != before.SourceAPIKeyManaged || member.AccountName != before.AccountName || member.RemoteAccountName != before.RemoteAccountName
			if memberChanged {
				if err := s.store.UpdateMember(member); err != nil {
					return result, fmt.Errorf("update member %d source binding: %w", member.ID, err)
				}
				result.Updated++
			}
			if renamed {
				result.Renamed++
			}
			if memberChanged || renamed {
				_ = s.appendAudit(&member.PoolID, &member.ID, member.RemoteAccountID, "member_source_group_sync", source, true,
					before, member, map[string]any{"source_api_key_id": keyID}, "已按上游 Key 的实际所属分组和名称更新", "")
			}
		}
		if preservedMissing > 0 {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s：%d 个绑定的上游 Key 不存在，已保留原分组", s.sourceChannelLabel(channelID), preservedMissing))
		}
	}
	return result, nil
}

func resolveSourceAPIKeyGroup(key *connector.APIKey, groups []connector.APIKeyGroup, authoritative bool) sourceGroupResolution {
	groupName := strings.TrimSpace(firstNonEmpty(key.GroupName, key.Group))
	if !authoritative {
		if key.GroupID == nil && groupName == "" {
			return sourceGroupResolution{State: "unbound"}
		}
		return sourceGroupResolution{ID: copyOptionalInt64(key.GroupID), Name: groupName, State: "resolved"}
	}
	if key.GroupID != nil {
		for i := range groups {
			if groups[i].ID != nil && *groups[i].ID == *key.GroupID {
				return sourceGroupResolution{ID: copyOptionalInt64(groups[i].ID), Name: strings.TrimSpace(groups[i].Name), State: "resolved"}
			}
		}
		return sourceGroupResolution{ID: copyOptionalInt64(key.GroupID), Name: groupName, State: "missing"}
	}
	if groupName == "" {
		return sourceGroupResolution{State: "unbound"}
	}
	for i := range groups {
		if strings.EqualFold(strings.TrimSpace(groups[i].Name), groupName) {
			return sourceGroupResolution{ID: copyOptionalInt64(groups[i].ID), Name: strings.TrimSpace(groups[i].Name), State: "resolved"}
		}
	}
	return sourceGroupResolution{Name: groupName, State: "missing"}
}

func (s *Service) isLikelyManagedSourceAPIKey(pool *storage.MainAccountPool, member *storage.MainAccountPoolMember, key *connector.APIKey) bool {
	if member == nil || key == nil || member.OwnershipMode != "managed" {
		return false
	}
	if member.LegacySyncAccountID != nil {
		return true
	}
	keyName := strings.TrimSpace(key.Name)
	if keyName == "" {
		return false
	}
	if key.CreatedAt == nil || member.CreatedAt.IsZero() {
		return false
	}
	createdDelta := key.CreatedAt.Sub(member.CreatedAt)
	if createdDelta < 0 {
		createdDelta = -createdDelta
	}
	if createdDelta > 10*time.Minute {
		return false
	}
	for _, candidate := range []string{managedSourceAPIKeyName(member), s.managedAutomaticName(pool, member)} {
		if strings.EqualFold(keyName, strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func remoteAccountForMember(member *storage.MainAccountPoolMember, accounts map[int64]sub2api.AdminAccount) (*sub2api.AdminAccount, bool) {
	if member.RemoteAccountID == nil {
		return nil, false
	}
	account, ok := accounts[*member.RemoteAccountID]
	if !ok {
		return nil, false
	}
	return &account, true
}

func (s *Service) syncManagedSourceBinding(
	ctx context.Context,
	client adminClient,
	adminTarget sub2api.AdminTarget,
	pool *storage.MainAccountPool,
	member *storage.MainAccountPoolMember,
	remote *sub2api.AdminAccount,
	groupChanged bool,
) (bool, error) {
	desiredName := s.managedAutomaticName(pool, member)
	nameChanged := member.AccountName != desiredName || remote.Name != desiredName
	if !nameChanged && !groupChanged {
		return false, nil
	}
	member.AccountName = desiredName
	priority := remote.Priority
	if priority <= 0 {
		priority = member.Priority
	}
	request, secret, err := s.managedAccountRequest(ctx, pool, member, priority)
	if err != nil {
		return false, err
	}
	request.Schedulable = remote.Schedulable
	if strings.TrimSpace(remote.Status) != "" {
		request.Status = remote.Status
	}
	updated, err := client.UpdateAccount(ctx, adminTarget, remote.ID, request)
	if err != nil {
		return false, redactSecretError(err, secret)
	}
	if updated == nil {
		return false, errors.New("主站更新账号后返回空结果")
	}
	member.RemoteAccountName = updated.Name
	snapshot := accountSnapshot(*updated)
	snapshot.LastSyncAt = s.now()
	if err := s.store.UpsertAccountSnapshot(&snapshot); err != nil {
		return false, err
	}
	if groupChanged {
		if err := s.syncManagedAccountModels(ctx, client, adminTarget, remote.ID); err != nil && s.log != nil {
			s.log.Warn("sync models after source group changed", "member_id", member.ID, "err", err)
		}
	}
	return nameChanged, nil
}

func (s *Service) cleanupManagedSourceBinding(
	ctx context.Context,
	client adminClient,
	adminTarget sub2api.AdminTarget,
	member *storage.MainAccountPoolMember,
	source,
	reason string,
) error {
	if member.OwnershipMode != "managed" || !member.SourceAPIKeyManaged {
		return errors.New("source binding is not managed")
	}
	before := *member
	if member.RemoteAccountID != nil {
		if err := client.DeleteAccount(ctx, adminTarget, *member.RemoteAccountID); err != nil && !missingRemoteResource(err) {
			return fmt.Errorf("delete managed main station account: %w", err)
		}
		if err := s.store.MarkAccountSnapshotMissing(*member.RemoteAccountID, s.now()); err != nil {
			return fmt.Errorf("mark deleted main station account missing: %w", err)
		}
	}
	if member.SourceAPIKeyID != nil {
		if err := s.channelSvc.DeleteAPIKey(ctx, member.SourceChannelID, *member.SourceAPIKeyID); err != nil && !missingRemoteResource(err) {
			return fmt.Errorf("delete managed source api key: %w", err)
		}
	}
	if err := s.store.DeleteMember(member.PoolID, member.ID); err != nil {
		return err
	}
	if err := s.markPoolRankingDirty(member.PoolID); err != nil && s.log != nil {
		s.log.Warn("mark pool ranking dirty after managed cleanup", "pool_id", member.PoolID, "err", err)
	}
	_ = s.appendAudit(&member.PoolID, &member.ID, member.RemoteAccountID, "member_auto_cleanup", source, true,
		before, nil, map[string]any{"reason": reason}, reason, "")
	return nil
}

func (s *Service) sourceAPIKeysByID(ctx context.Context, channelID uint, wanted map[int64]struct{}) (map[int64]connector.APIKey, error) {
	found := make(map[int64]connector.APIKey, len(wanted))
	seen := make(map[int64]struct{})
	for pageNumber := 1; pageNumber <= sourceBindingMaxPages; pageNumber++ {
		page, err := s.channelSvc.ListAPIKeys(ctx, channelID, connector.APIKeyQuery{Page: pageNumber, PageSize: sourceBindingPageSize})
		if err != nil {
			return nil, err
		}
		if page == nil {
			return nil, fmt.Errorf("渠道 %d 的上游 Key 第 %d 页返回空结果", channelID, pageNumber)
		}
		added := 0
		for i := range page.Items {
			key := page.Items[i]
			if _, exists := seen[key.ID]; !exists {
				seen[key.ID] = struct{}{}
				added++
			}
			if _, needed := wanted[key.ID]; needed {
				found[key.ID] = key
			}
		}
		if len(found) == len(wanted) || !sourceAPIKeyPageHasNext(page, pageNumber) {
			return found, nil
		}
		if added == 0 {
			return nil, fmt.Errorf("渠道 %d 的上游 Key 分页没有前进", channelID)
		}
	}
	return nil, fmt.Errorf("渠道 %d 的上游 Key 超过最大分页限制", channelID)
}

func sourceAPIKeyPageHasNext(page *connector.APIKeyPage, requestedPage int) bool {
	currentPage := page.Page
	if currentPage <= 0 {
		currentPage = requestedPage
	}
	if page.Pages > 0 {
		return currentPage < page.Pages
	}
	pageSize := page.PageSize
	if pageSize <= 0 {
		pageSize = sourceBindingPageSize
	}
	if page.Total > 0 {
		return int64(currentPage*pageSize) < page.Total
	}
	return len(page.Items) >= pageSize
}

func (s *Service) sourceChannelLabel(channelID uint) string {
	if s.channels != nil {
		if channel, err := s.channels.FindByID(channelID); err == nil && strings.TrimSpace(channel.Name) != "" {
			return channel.Name
		}
	}
	return fmt.Sprintf("渠道 #%d", channelID)
}
