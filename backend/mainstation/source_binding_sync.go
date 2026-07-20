package mainstation

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fausto2022/relaydeck/backend/connector"
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
	Warnings []string
}

func (s *Service) refreshSourceAPIKeyGroups(ctx context.Context, source string) (sourceBindingRefreshResult, error) {
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
		missing := 0
		for i := range channelMembers {
			member := &channelMembers[i]
			keyID := *member.SourceAPIKeyID
			key, ok := keys[keyID]
			if !ok {
				missing++
				result.Missing++
				_ = s.appendAudit(&member.PoolID, &member.ID, member.RemoteAccountID, "member_source_group_sync", source, false,
					member, nil, map[string]any{"source_api_key_id": keyID}, "已保留原上游分组", "绑定的上游 Key 不存在")
				continue
			}
			groupName := strings.TrimSpace(firstNonEmpty(key.GroupName, key.Group))
			if groupName == "" && sameOptionalInt64(member.SourceGroupID, key.GroupID) {
				groupName = member.SourceGroupName
			}
			if sameOptionalInt64(member.SourceGroupID, key.GroupID) && member.SourceGroupName == groupName {
				continue
			}
			before := *member
			if err := s.store.UpdateMemberSourceGroup(member.ID, key.GroupID, groupName); err != nil {
				return result, fmt.Errorf("update member %d source group: %w", member.ID, err)
			}
			member.SourceGroupID = copyOptionalInt64(key.GroupID)
			member.SourceGroupName = groupName
			member.LastCostMicros = nil
			member.LastCostSource = ""
			member.LastCostAt = nil
			member.LastCostExpiresAt = nil
			result.Updated++
			_ = s.appendAudit(&member.PoolID, &member.ID, member.RemoteAccountID, "member_source_group_sync", source, true,
				before, member, map[string]any{"source_api_key_id": keyID}, "已按上游 Key 的实际所属分组更新", "")
		}
		if missing > 0 {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s：%d 个绑定的上游 Key 不存在，已保留原分组", s.sourceChannelLabel(channelID), missing))
		}
	}
	return result, nil
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
