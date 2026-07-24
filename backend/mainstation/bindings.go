package mainstation

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/fausto2022/relaydeck/backend/connector"
	"github.com/fausto2022/relaydeck/backend/storage"
)

const (
	maximumBindingCandidates = 5
	maximumBatchBindings     = 100
	bindingSourceConcurrency = 4
)

type bindingSource struct {
	channel     storage.Channel
	groupID     *int64
	groupName   string
	apiKeyID    *int64
	apiKeyName  string
	concurrency int
}

type bindingSourceResult struct {
	sources  []bindingSource
	warnings []string
}

func (s *Service) RecommendBindings(ctx context.Context, groupID uint) (*BindingRecommendationResult, error) {
	group, err := s.mainStationGroup(groupID)
	if err != nil {
		return nil, err
	}
	accounts, boundRemoteIDs, err := s.groupBindingAccounts(group.RemoteGroupID)
	if err != nil {
		return nil, err
	}
	channels, err := s.channels.List()
	if err != nil {
		return nil, err
	}

	sources, warnings := s.bindingSources(ctx, channels)
	result := &BindingRecommendationResult{
		Items:       make([]BindingRecommendation, 0, len(accounts)),
		Warnings:    warnings,
		GeneratedAt: s.now(),
	}
	for i := range accounts {
		if _, bound := boundRemoteIDs[accounts[i].RemoteAccountID]; bound {
			continue
		}
		result.Items = append(result.Items, recommendAccountBinding(accounts[i], group, sources))
	}
	sort.SliceStable(result.Items, func(i, j int) bool {
		if result.Items[i].Score != result.Items[j].Score {
			return result.Items[i].Score > result.Items[j].Score
		}
		return result.Items[i].RemoteAccountID < result.Items[j].RemoteAccountID
	})
	return result, nil
}

func (s *Service) BindMembersBatch(ctx context.Context, groupID uint, in BindingBatchInput) (*BindingBatchResult, error) {
	if len(in.Items) == 0 {
		return nil, fmt.Errorf("binding items are required")
	}
	if len(in.Items) > maximumBatchBindings {
		return nil, fmt.Errorf("binding items must not exceed %d", maximumBatchBindings)
	}
	poolID, err := s.GroupPoolID(groupID)
	if err != nil {
		return nil, err
	}
	group, err := s.mainStationGroup(groupID)
	if err != nil {
		return nil, err
	}
	accounts, boundRemoteIDs, err := s.groupBindingAccounts(group.RemoteGroupID)
	if err != nil {
		return nil, err
	}
	allowed := make(map[int64]storage.MainStationAccountSnapshot, len(accounts))
	for i := range accounts {
		allowed[accounts[i].RemoteAccountID] = accounts[i]
	}

	result := &BindingBatchResult{Items: make([]BindingBatchItemResult, 0, len(in.Items))}
	seen := make(map[int64]struct{}, len(in.Items))
	for i := range in.Items {
		item := in.Items[i]
		row := BindingBatchItemResult{RemoteAccountID: memberInputRemoteID(item)}
		account, exists := allowed[row.RemoteAccountID]
		switch {
		case row.RemoteAccountID <= 0:
			row.Error = "remote_account_id is required"
		case !exists:
			row.Error = "remote account does not belong to the selected group"
		case isBoundRemoteAccount(boundRemoteIDs, row.RemoteAccountID):
			row.Error = "remote account is already bound"
		case account.Missing:
			row.Error = "remote account is missing"
		default:
			if _, duplicate := seen[row.RemoteAccountID]; duplicate {
				row.Error = "remote account is duplicated in this request"
			}
		}
		seen[row.RemoteAccountID] = struct{}{}
		if row.Error != "" {
			result.Failed++
			result.Items = append(result.Items, row)
			continue
		}

		item.OwnershipMode = "bound"
		item.ManualBindingConfirmed = true
		item.RemoteAccountID = &row.RemoteAccountID
		prepared, prepareErr := s.prepareMemberInput(ctx, item)
		if prepareErr != nil {
			row.Error = prepareErr.Error()
		} else {
			member, createErr := s.createBoundMember(poolID, prepared)
			if createErr != nil {
				row.Error = createErr.Error()
			} else {
				row.Success = true
				row.Member = member
				result.Succeeded++
			}
		}
		if !row.Success {
			result.Failed++
		}
		result.Items = append(result.Items, row)
	}

	if result.Succeeded > 0 {
		if rankingErr := s.markPoolRankingDirty(poolID); rankingErr != nil {
			result.RankingError = rankingErr.Error()
			if s.log != nil {
				s.log.Warn("mark main station scheduling rank dirty", "err", rankingErr, "pool_id", poolID)
			}
		}
		_ = s.appendAudit(&poolID, nil, nil, "member_bind_batch", "manual", true, nil, nil, map[string]any{
			"succeeded": result.Succeeded,
			"failed":    result.Failed,
		}, "", result.RankingError)
	}
	return result, nil
}

func (s *Service) groupBindingAccounts(remoteGroupID int64) ([]storage.MainStationAccountSnapshot, map[int64]struct{}, error) {
	snapshots, err := s.store.ListAllAccountSnapshots(false)
	if err != nil {
		return nil, nil, err
	}
	members, err := s.store.ListAllMembers()
	if err != nil {
		return nil, nil, err
	}
	boundRemoteIDs := make(map[int64]struct{}, len(members))
	for i := range members {
		if members[i].RemoteAccountID != nil {
			boundRemoteIDs[*members[i].RemoteAccountID] = struct{}{}
		}
	}
	accounts := make([]storage.MainStationAccountSnapshot, 0)
	for i := range snapshots {
		if accountBelongsToRemoteGroup(&snapshots[i], remoteGroupID) {
			accounts = append(accounts, snapshots[i])
		}
	}
	return accounts, boundRemoteIDs, nil
}

func isBoundRemoteAccount(boundRemoteIDs map[int64]struct{}, remoteAccountID int64) bool {
	_, ok := boundRemoteIDs[remoteAccountID]
	return ok
}

func (s *Service) bindingSources(ctx context.Context, channels []storage.Channel) ([]bindingSource, []string) {
	if s.channelSvc == nil {
		return nil, []string{"channel service is unavailable"}
	}
	results := make([]bindingSourceResult, len(channels))
	semaphore := make(chan struct{}, bindingSourceConcurrency)
	var wait sync.WaitGroup
	for i := range channels {
		i := i
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results[i].warnings = []string{fmt.Sprintf("%s：读取已取消", channels[i].Name)}
				return
			}
			results[i] = s.bindingSourcesForChannel(ctx, channels[i])
		}()
	}
	wait.Wait()

	sources := make([]bindingSource, 0)
	warnings := make([]string, 0)
	for i := range results {
		sources = append(sources, results[i].sources...)
		warnings = append(warnings, results[i].warnings...)
	}
	return sources, warnings
}

func (s *Service) bindingSourcesForChannel(ctx context.Context, channel storage.Channel) bindingSourceResult {
	sources := make([]bindingSource, 0)
	warnings := make([]string, 0)
	channelWarnings := make([]string, 0, 3)
	concurrency := 0
	if limits, limitErr := s.channelSvc.GetAccountLimits(ctx, channel.ID); limitErr == nil && limits != nil {
		concurrency = limits.Concurrency
	} else {
		channelWarnings = append(channelWarnings, "最高并发")
		if limitErr != nil {
			s.logBindingSourceError(channel, "load concurrency", limitErr)
		}
	}
	keys, err := s.listBindingAPIKeys(ctx, channel.ID)
	if err != nil {
		channelWarnings = append(channelWarnings, "API Key")
		s.logBindingSourceError(channel, "load api keys", err)
	}
	usableKeys := 0
	for j := range keys {
		key := keys[j]
		if !usableBindingKey(key.Status) {
			continue
		}
		keyID := key.ID
		sources = append(sources, bindingSource{
			channel: channel, groupID: key.GroupID, groupName: firstNonEmpty(key.GroupName, key.Group),
			apiKeyID: &keyID, apiKeyName: key.Name, concurrency: concurrency,
		})
		usableKeys++
	}
	if usableKeys > 0 {
		return bindingSourceResult{sources: sources, warnings: appendBindingSourceWarning(warnings, channel.Name, channelWarnings)}
	}
	groups, groupErr := s.channelSvc.ListAPIKeyGroups(ctx, channel.ID)
	if groupErr != nil {
		channelWarnings = append(channelWarnings, "来源分组")
		s.logBindingSourceError(channel, "load api key groups", groupErr)
	}
	if len(groups) == 0 {
		sources = append(sources, bindingSource{channel: channel, concurrency: concurrency})
		return bindingSourceResult{sources: sources, warnings: appendBindingSourceWarning(warnings, channel.Name, channelWarnings)}
	}
	for j := range groups {
		sources = append(sources, bindingSource{
			channel: channel, groupID: groups[j].ID, groupName: groups[j].Name, concurrency: concurrency,
		})
	}
	warnings = appendBindingSourceWarning(warnings, channel.Name, channelWarnings)
	return bindingSourceResult{sources: sources, warnings: warnings}
}

func (s *Service) logBindingSourceError(channel storage.Channel, operation string, err error) {
	if s.log != nil {
		s.log.Warn("load binding recommendation source", "channel_id", channel.ID, "channel", channel.Name, "operation", operation, "err", err)
	}
}

func appendBindingSourceWarning(warnings []string, channelName string, fields []string) []string {
	if len(fields) == 0 {
		return warnings
	}
	return append(warnings, fmt.Sprintf("%s：无法读取%s", channelName, strings.Join(fields, "、")))
}

func (s *Service) listBindingAPIKeys(ctx context.Context, channelID uint) ([]connector.APIKey, error) {
	const pageSize = 100
	items := make([]connector.APIKey, 0, pageSize)
	for pageNumber := 1; pageNumber <= 5; pageNumber++ {
		page, err := s.channelSvc.ListAPIKeys(ctx, channelID, connector.APIKeyQuery{Page: pageNumber, PageSize: pageSize})
		if err != nil {
			return items, err
		}
		if page == nil {
			break
		}
		items = append(items, page.Items...)
		if len(page.Items) < pageSize || (page.Pages > 0 && pageNumber >= page.Pages) {
			break
		}
	}
	return items, nil
}

func recommendAccountBinding(account storage.MainStationAccountSnapshot, group *storage.UpstreamSyncTargetGroup, sources []bindingSource) BindingRecommendation {
	result := BindingRecommendation{
		RemoteAccountID: account.RemoteAccountID, RemoteAccountName: account.Name, Platform: account.Platform,
		Status: account.Status, Confidence: "none", Candidates: make([]BindingRecommendationCandidate, 0),
	}
	for i := range sources {
		candidate := scoreBindingSource(account, group, sources[i])
		if candidate.Score < 25 {
			continue
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	sort.SliceStable(result.Candidates, func(i, j int) bool {
		if result.Candidates[i].Score != result.Candidates[j].Score {
			return result.Candidates[i].Score > result.Candidates[j].Score
		}
		return result.Candidates[i].ID < result.Candidates[j].ID
	})
	if len(result.Candidates) > maximumBindingCandidates {
		result.Candidates = result.Candidates[:maximumBindingCandidates]
	}
	if len(result.Candidates) == 0 {
		return result
	}
	top := result.Candidates[0]
	result.SuggestedCandidateID = top.ID
	result.Score = top.Score
	result.Confidence = top.Confidence
	result.Reasons = append([]string(nil), top.Reasons...)
	if len(result.Candidates) > 1 && top.Score-result.Candidates[1].Score <= 10 {
		result.Conflict = true
	}
	return result
}

func scoreBindingSource(account storage.MainStationAccountSnapshot, group *storage.UpstreamSyncTargetGroup, source bindingSource) BindingRecommendationCandidate {
	candidate := BindingRecommendationCandidate{
		ID: bindingCandidateID(source), SourceChannelID: source.channel.ID, SourceChannelName: source.channel.Name,
		SourceChannelType: string(source.channel.Type), SourceGroupID: source.groupID, SourceGroupName: source.groupName,
		SourceAPIKeyID: source.apiKeyID, SourceAPIKeyName: source.apiKeyName, Concurrency: source.concurrency,
		Reasons: make([]string, 0, 5),
	}
	accountName := normalizeBindingText(account.Name)
	channelName := normalizeBindingText(source.channel.Name)
	keyName := normalizeBindingText(source.apiKeyName)
	groupName := normalizeBindingText(source.groupName)
	remoteGroupName := normalizeBindingText(group.Name)
	platform := normalizeBindingText(firstNonEmpty(account.Platform, group.Platform))

	remoteHost := bindingHost(account.BaseURL)
	channelHost := bindingHost(source.channel.SiteURL)
	if remoteHost != "" && channelHost != "" {
		if remoteHost == channelHost {
			candidate.Score += 55
			candidate.Reasons = append(candidate.Reasons, "上游地址一致")
		} else {
			candidate.Score -= 40
		}
	}
	addBindingNameScore(&candidate, accountName, channelName, 40, 20, "账号名称与渠道名称")
	addBindingNameScore(&candidate, accountName, keyName, 45, 25, "账号名称与 API Key 名称")
	addBindingNameScore(&candidate, remoteGroupName, groupName, 20, 10, "主站分组与来源分组")
	if platform != "" && bindingTextContains(strings.Join([]string{channelName, keyName, groupName}, ""), platform) {
		candidate.Score += 10
		candidate.Reasons = append(candidate.Reasons, "平台类型一致")
	}
	if source.apiKeyID == nil {
		candidate.Score -= 10
	} else {
		candidate.Score += 5
		candidate.Reasons = append(candidate.Reasons, "来源 API Key 可用")
	}
	candidate.Confidence = bindingConfidence(candidate.Score)
	return candidate
}

func addBindingNameScore(candidate *BindingRecommendationCandidate, left, right string, exactScore, containsScore int, label string) {
	if left == "" || right == "" {
		return
	}
	if left == right {
		candidate.Score += exactScore
		candidate.Reasons = append(candidate.Reasons, label+"完全匹配")
		return
	}
	if bindingTextContains(left, right) || bindingTextContains(right, left) {
		candidate.Score += containsScore
		candidate.Reasons = append(candidate.Reasons, label+"相似")
	}
}

func bindingCandidateID(source bindingSource) string {
	keyID := int64(0)
	if source.apiKeyID != nil {
		keyID = *source.apiKeyID
	}
	groupID := int64(0)
	if source.groupID != nil {
		groupID = *source.groupID
	}
	return fmt.Sprintf("channel:%d:key:%d:group:%d:%s", source.channel.ID, keyID, groupID, normalizeBindingText(source.groupName))
}

func bindingConfidence(score int) string {
	switch {
	case score >= 80:
		return "high"
	case score >= 50:
		return "medium"
	case score >= 25:
		return "low"
	default:
		return "none"
	}
}

func normalizeBindingText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, strings.TrimSpace(value))
}

func bindingTextContains(value, part string) bool {
	return utf8.RuneCountInString(part) >= 2 && strings.Contains(value, part)
}

func bindingHost(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Host)
}

func usableBindingKey(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "active", "enabled", "unknown":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func memberInputRemoteID(in MemberInput) int64 {
	if in.RemoteAccountID == nil {
		return 0
	}
	return *in.RemoteAccountID
}
