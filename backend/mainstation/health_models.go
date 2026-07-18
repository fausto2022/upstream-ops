package mainstation

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/bejix/upstream-ops/backend/storage"
)

func decodeHealthModels(raw string) map[string]string {
	models := make(map[string]string)
	_ = json.Unmarshal([]byte(raw), &models)
	return normalizeHealthModels(models)
}

func encodeHealthModels(models map[string]string) (string, error) {
	normalized := normalizeHealthModels(models)
	for platform, model := range normalized {
		if len(platform) > 64 {
			return "", errors.New("health model platform is too long")
		}
		if len(model) > 256 {
			return "", errors.New("health model name is too long")
		}
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func normalizeHealthModels(models map[string]string) map[string]string {
	normalized := make(map[string]string)
	for platform, model := range models {
		platform = normalizeHealthPlatform(platform)
		model = strings.TrimSpace(model)
		if platform != "" && model != "" {
			normalized[platform] = model
		}
	}
	return normalized
}

func normalizeHealthPlatform(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "claude":
		return "anthropic"
	case "google":
		return "gemini"
	default:
		return strings.ToLower(strings.TrimSpace(platform))
	}
}

func effectiveHealthModel(platform, memberModel string, globalModels map[string]string) string {
	if model := strings.TrimSpace(memberModel); model != "" {
		return model
	}
	return strings.TrimSpace(globalModels[normalizeHealthPlatform(platform)])
}

func (s *Service) configuredHealthModels() map[string]string {
	config, err := s.store.GetConfig()
	if err != nil {
		return map[string]string{}
	}
	return decodeHealthModels(config.HealthModelsJSON)
}

func (s *Service) ListHealthModelCatalogs(ctx context.Context) ([]HealthModelCatalog, error) {
	pools, _, err := s.store.ListPools(1, 1000)
	if err != nil {
		return nil, err
	}
	candidates := make(map[string][]storage.MainAccountPoolMember)
	platforms := map[string]struct{}{
		"openai": {}, "anthropic": {}, "gemini": {},
	}
	for platform := range s.configuredHealthModels() {
		platforms[platform] = struct{}{}
	}
	snapshots, err := s.store.ListAllAccountSnapshots(false)
	if err != nil {
		return nil, err
	}
	_, adminTarget, adminAPIKey, adminErr := s.loadAdminTarget()
	admin := s.adminFactory()
	for i := range pools {
		platform := normalizeHealthPlatform(pools[i].Platform)
		if platform == "" {
			continue
		}
		platforms[platform] = struct{}{}
		members, listErr := s.store.ListMembers(pools[i].ID)
		if listErr != nil {
			return nil, listErr
		}
		candidates[platform] = append(candidates[platform], members...)
	}
	keys := make([]string, 0, len(platforms))
	for platform := range platforms {
		keys = append(keys, platform)
	}
	sort.Strings(keys)
	catalogs := make([]HealthModelCatalog, 0, len(keys))
	for _, platform := range keys {
		catalog := HealthModelCatalog{Platform: platform, Models: []string{}}
		var lastErr error
		seen := make(map[string]struct{})
		for i := range candidates[platform] {
			member := &candidates[platform][i]
			if member.SourceAPIKeyID == nil {
				continue
			}
			candidateKey := memberKey(member)
			if _, ok := seen[candidateKey]; ok {
				continue
			}
			seen[candidateKey] = struct{}{}
			channel, secret, credentialErr := s.healthSourceCredentials(ctx, member)
			if credentialErr != nil {
				lastErr = credentialErr
				continue
			}
			catalog.Models, lastErr = connector.FetchModels(ctx, s.probeHTTPClient(channel), channel.SiteURL, platform, secret)
			if lastErr == nil {
				break
			}
		}
		if len(catalog.Models) == 0 && adminErr == nil {
			attempts := 0
			for i := range snapshots {
				if normalizeHealthPlatform(snapshots[i].Platform) != platform {
					continue
				}
				attempts++
				catalog.Models, lastErr = admin.SyncAccountModelsFromUpstream(ctx, sub2api.AdminTarget{
					BaseURL: adminTarget.BaseURL, APIKey: adminAPIKey,
				}, snapshots[i].RemoteAccountID)
				if lastErr != nil {
					catalog.Models, lastErr = admin.ListAccountModels(ctx, sub2api.AdminTarget{
						BaseURL: adminTarget.BaseURL, APIKey: adminAPIKey,
					}, snapshots[i].RemoteAccountID)
				}
				if lastErr == nil && len(catalog.Models) > 0 {
					sort.Strings(catalog.Models)
					break
				}
				if attempts >= 3 {
					break
				}
			}
		}
		if len(catalog.Models) == 0 {
			if lastErr != nil {
				catalog.Error = sanitizeText(redactSecretError(lastErr, adminAPIKey).Error())
			} else {
				catalog.Error = "该平台没有可用于获取模型的账号 API Key"
			}
		}
		catalogs = append(catalogs, catalog)
	}
	return catalogs, nil
}

func memberKey(member *storage.MainAccountPoolMember) string {
	if member == nil || member.SourceAPIKeyID == nil {
		return ""
	}
	return strings.Join([]string{
		strconv.FormatUint(uint64(member.SourceChannelID), 10),
		strconv.FormatInt(*member.SourceAPIKeyID, 10),
	}, ":")
}
