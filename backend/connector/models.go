package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

const modelListBodyLimit int64 = 2 << 20

// FetchModels 从兼容官方协议的模型接口读取当前 API Key 可用的完整模型列表。
func FetchModels(ctx context.Context, client *http.Client, baseURL, platform, apiKey string) ([]string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	endpoint := ModelsURL(baseURL, platform)
	cursor := ""
	models := make([]string, 0)
	for page := 0; page < 20; page++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		query := request.URL.Query()
		switch platform {
		case "anthropic", "antigravity":
			query.Set("limit", "1000")
			if cursor != "" {
				query.Set("after_id", cursor)
			}
		case "gemini":
			query.Set("pageSize", "1000")
			if cursor != "" {
				query.Set("pageToken", cursor)
			}
		}
		request.URL.RawQuery = query.Encode()
		request.Header.Set("Accept", "application/json")
		SetModelAuthHeaders(request, platform, strings.TrimSpace(apiKey))
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, modelListBodyLimit+1))
		_ = response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if int64(len(body)) > modelListBodyLimit {
			return nil, errors.New("model list response is too large")
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return nil, fmt.Errorf("model list request failed with HTTP %d", response.StatusCode)
		}
		pageModels, decodeErr := DecodeModels(body)
		if decodeErr != nil {
			return nil, decodeErr
		}
		models = append(models, pageModels...)
		nextCursor, hasMore := modelPageCursor(body, platform)
		if !hasMore {
			models = uniqueSortedModels(models)
			break
		}
		if nextCursor == "" || nextCursor == cursor {
			return nil, errors.New("model list pagination cursor is invalid")
		}
		cursor = nextCursor
		if page == 19 {
			return nil, errors.New("model list exceeded pagination limit")
		}
	}
	if len(models) == 0 {
		return nil, errors.New("model list is empty")
	}
	return models, nil
}

func ModelsURL(baseURL, platform string) string {
	normalized := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.EqualFold(strings.TrimSpace(platform), "gemini") {
		if strings.HasSuffix(normalized, "/v1beta/models") {
			return normalized
		}
		if strings.HasSuffix(normalized, "/v1beta") {
			return normalized + "/models"
		}
		return normalized + "/v1beta/models"
	}
	if strings.HasSuffix(normalized, "/v1/models") {
		return normalized
	}
	if strings.HasSuffix(normalized, "/v1") {
		return normalized + "/models"
	}
	return normalized + "/v1/models"
}

func SetModelAuthHeaders(request *http.Request, platform, apiKey string) {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "gemini":
		request.Header.Set("x-goog-api-key", apiKey)
	case "anthropic", "antigravity":
		request.Header.Set("x-api-key", apiKey)
		request.Header.Set("anthropic-version", "2023-06-01")
		request.Header.Set("anthropic-beta", "oauth-2025-04-20")
	default:
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

func DecodeModels(body []byte) ([]string, error) {
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	return uniqueSortedModels(collectModelIDs(raw)), nil
}

func uniqueSortedModels(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	models := make([]string, 0, len(values))
	for _, value := range values {
		model := strings.TrimSpace(value)
		model = strings.TrimPrefix(model, "models/")
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

func modelPageCursor(body []byte, platform string) (string, bool) {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return "", false
	}
	switch platform {
	case "anthropic", "antigravity":
		hasMore, _ := root["has_more"].(bool)
		lastID, _ := root["last_id"].(string)
		return strings.TrimSpace(lastID), hasMore
	case "gemini":
		token, _ := root["nextPageToken"].(string)
		token = strings.TrimSpace(token)
		return token, token != ""
	default:
		return "", false
	}
}

func collectModelIDs(raw any) []string {
	switch value := raw.(type) {
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			out = append(out, collectModelIDs(item)...)
		}
		return out
	case map[string]any:
		out := []string(nil)
		if models, ok := value["models"]; ok {
			if modelMap, ok := models.(map[string]any); ok {
				for id := range modelMap {
					out = append(out, id)
				}
			} else {
				out = append(out, collectModelIDs(models)...)
			}
		}
		if data, ok := value["data"]; ok {
			out = append(out, collectModelIDs(data)...)
		}
		for _, key := range []string{"id", "name", "model"} {
			if text, ok := value[key].(string); ok {
				out = append(out, text)
				break
			}
		}
		return out
	case string:
		return []string{value}
	default:
		return nil
	}
}
