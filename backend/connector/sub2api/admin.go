package sub2api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fausto2022/relaydeck/backend/connector"
)

// AdminClient 只封装 Sub2API 管理员接口，使用 x-api-key 鉴权。
type AdminClient struct {
	client *Client
}

func NewAdminClient() *AdminClient {
	return &AdminClient{client: New()}
}

type AdminTarget struct {
	BaseURL string
	APIKey  string
}

type AdminGroup struct {
	ID                int64           `json:"id"`
	Name              string          `json:"name"`
	Platform          string          `json:"platform"`
	Ratio             float64         `json:"ratio"`
	RateMultiplier    float64         `json:"rate_multiplier"`
	Status            string          `json:"status"`
	Sort              int             `json:"sort"`
	Description       string          `json:"description"`
	PeakEnabled       bool            `json:"peak_enabled"`
	PeakStart         string          `json:"peak_start"`
	PeakEnd           string          `json:"peak_end"`
	PeakMultiplier    float64         `json:"peak_multiplier"`
	SubscriptionType  string          `json:"subscription_type"`
	ImageSeparateRate bool            `json:"image_separate_rate"`
	VideoSeparateRate bool            `json:"video_separate_rate"`
	PricingMetadata   json.RawMessage `json:"pricing_metadata"`
}

type AdminProxy struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Status   string `json:"status"`
}

type AdminAccount struct {
	ID             int64           `json:"id"`
	Name           string          `json:"name"`
	Platform       string          `json:"platform"`
	Type           string          `json:"type"`
	Status         string          `json:"status"`
	Schedulable    bool            `json:"schedulable,omitempty"`
	Notes          string          `json:"notes"`
	ProxyID        *int64          `json:"proxy_id,omitempty"`
	Concurrency    int             `json:"concurrency"`
	Priority       int             `json:"priority"`
	Weight         int             `json:"weight"`
	RateMultiplier float64         `json:"rate_multiplier"`
	LoadFactor     float64         `json:"load_factor"`
	GroupIDs       []int64         `json:"group_ids"`
	Credentials    map[string]any  `json:"credentials"`
	Extra          json.RawMessage `json:"extra"`
	LastUsedAt     *time.Time      `json:"last_used_at"`
	UpdatedAt      *time.Time      `json:"updated_at"`
}

type AdminAccountPage struct {
	Items    []AdminAccount `json:"items"`
	Total    int64          `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Pages    int            `json:"pages"`
}

type AdminAccountTestResult struct {
	Model        string
	ResponseText string
}

type AdminAccountSchedulingUpdate struct {
	Concurrency int `json:"concurrency"`
	Priority    int `json:"priority"`
	LoadFactor  int `json:"load_factor"`
}

func (a *AdminClient) Ping(ctx context.Context, t AdminTarget) error {
	if _, err := a.ListGroups(ctx, t, true); err != nil {
		return err
	}
	_, err := a.ListAccounts(ctx, t, 1, 1)
	return err
}

func (a *AdminClient) ListGroups(ctx context.Context, t AdminTarget, includeInactive bool) ([]AdminGroup, error) {
	const pageSize = 100
	out := make([]AdminGroup, 0)
	seen := make(map[int64]struct{})
	for page := 1; page <= 10000; page++ {
		items, meta, paginated, err := a.listGroupsPage(ctx, t, includeInactive, page, pageSize)
		if err != nil {
			return nil, err
		}
		added := 0
		for _, item := range items {
			if _, ok := seen[item.ID]; ok {
				continue
			}
			seen[item.ID] = struct{}{}
			out = append(out, item)
			added++
		}
		if !paginated || added == 0 || !adminPageHasNext(meta.Page, meta.PageSize, meta.Pages, meta.Total, len(items)) {
			break
		}
	}
	normalizeAdminGroupRatios(out)
	return out, nil
}

func (a *AdminClient) listGroupsPage(ctx context.Context, t AdminTarget, includeInactive bool, page, pageSize int) ([]AdminGroup, AdminAccountPage, bool, error) {
	params := url.Values{}
	params.Set("include_inactive", strconv.FormatBool(includeInactive))
	params.Set("page", strconv.Itoa(page))
	params.Set("page_size", strconv.Itoa(pageSize))
	body, err := a.getJSON(ctx, t, "/api/v1/admin/groups/all?"+params.Encode())
	if err != nil {
		return nil, AdminAccountPage{}, false, err
	}
	var list []AdminGroup
	if err := json.Unmarshal(body, &list); err == nil {
		return list, AdminAccountPage{Page: page, PageSize: pageSize, Total: int64(len(list)), Pages: 1}, false, nil
	}
	var wrapped struct {
		Items    []AdminGroup `json:"items"`
		Total    int64        `json:"total"`
		Page     int          `json:"page"`
		PageSize int          `json:"page_size"`
		Pages    int          `json:"pages"`
	}
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return nil, AdminAccountPage{}, false, fmt.Errorf("decode admin groups: %w", err)
	}
	if wrapped.Page <= 0 {
		wrapped.Page = page
	}
	if wrapped.PageSize <= 0 {
		wrapped.PageSize = pageSize
	}
	return wrapped.Items, AdminAccountPage{
		Total: wrapped.Total, Page: wrapped.Page, PageSize: wrapped.PageSize, Pages: wrapped.Pages,
	}, true, nil
}

func (a *AdminClient) ListProxies(ctx context.Context, t AdminTarget) ([]AdminProxy, error) {
	body, err := a.getJSON(ctx, t, "/api/v1/admin/proxies/all")
	if err != nil {
		return nil, err
	}
	var list []AdminProxy
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("decode admin proxies: %w", err)
	}
	return list, nil
}

func (a *AdminClient) ListGroupRateMultipliers(ctx context.Context, t AdminTarget, groupID int64) ([]float64, error) {
	body, err := a.getJSON(ctx, t, "/api/v1/admin/groups/"+strconv.FormatInt(groupID, 10)+"/rate-multipliers")
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, fmt.Errorf("decode admin group rate multipliers: %w", err)
	}
	return compactPositiveMultipliers(collectRateMultipliers(value)), nil
}

func (a *AdminClient) ListAccounts(ctx context.Context, t AdminTarget, page, pageSize int) ([]AdminAccount, error) {
	result, err := a.ListAccountsPage(ctx, t, page, pageSize)
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (a *AdminClient) ListAccountsPage(ctx context.Context, t AdminTarget, page, pageSize int) (*AdminAccountPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 100
	}
	if pageSize > 1000 {
		pageSize = 1000
	}
	body, err := a.getJSON(ctx, t, "/api/v1/admin/accounts?page="+strconv.Itoa(page)+"&page_size="+strconv.Itoa(pageSize))
	if err != nil {
		return nil, err
	}
	var direct []AdminAccount
	if err := json.Unmarshal(body, &direct); err == nil {
		return &AdminAccountPage{Items: direct, Total: int64(len(direct)), Page: page, PageSize: pageSize, Pages: 1}, nil
	}
	var raw AdminAccountPage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode admin accounts: %w", err)
	}
	if raw.Page <= 0 {
		raw.Page = page
	}
	if raw.PageSize <= 0 {
		raw.PageSize = pageSize
	}
	if raw.Total == 0 && len(raw.Items) > 0 && raw.Pages <= 1 {
		raw.Total = int64(len(raw.Items))
	}
	return &raw, nil
}

func (a *AdminClient) ListAllAccounts(ctx context.Context, t AdminTarget) ([]AdminAccount, error) {
	const pageSize = 100
	out := make([]AdminAccount, 0)
	seen := make(map[int64]struct{})
	for page := 1; page <= 10000; page++ {
		result, err := a.ListAccountsPage(ctx, t, page, pageSize)
		if err != nil {
			return nil, err
		}
		added := 0
		for _, item := range result.Items {
			if _, ok := seen[item.ID]; ok {
				continue
			}
			seen[item.ID] = struct{}{}
			out = append(out, item)
			added++
		}
		if added == 0 || !adminPageHasNext(result.Page, result.PageSize, result.Pages, result.Total, len(result.Items)) {
			break
		}
	}
	return out, nil
}

func (a *AdminClient) FindGroupByName(ctx context.Context, t AdminTarget, name string) (*AdminGroup, error) {
	groups, err := a.ListGroups(ctx, t, true)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if strings.EqualFold(groups[i].Name, name) {
			return &groups[i], nil
		}
	}
	return nil, nil
}

func (a *AdminClient) FindAccountByName(ctx context.Context, t AdminTarget, name string) (*AdminAccount, error) {
	items, err := a.ListAllAccounts(ctx, t)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if strings.EqualFold(items[i].Name, name) {
			return &items[i], nil
		}
	}
	return nil, nil
}

func (a *AdminClient) GetAccount(ctx context.Context, t AdminTarget, id int64) (*AdminAccount, error) {
	body, err := a.getJSON(ctx, t, "/api/v1/admin/accounts/"+strconv.FormatInt(id, 10))
	if err != nil {
		return nil, err
	}
	var out AdminAccount
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode admin account: %w", err)
	}
	return &out, nil
}

func (a *AdminClient) CreateAccount(ctx context.Context, t AdminTarget, req AdminAccount) (*AdminAccount, error) {
	req.Status = adminAccountStatusForUpdate(req.Status)
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetHeader("x-api-key", t.APIKey).
		SetBody(body).
		Post(strings.TrimRight(t.BaseURL, "/") + "/api/v1/admin/accounts")
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		return nil, fmt.Errorf("create admin account: %w", connector.HTTPStatusError(resp.StatusCode(), resp.Body()))
	}
	out, err := decodeAdminAccount(resp.Body())
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (a *AdminClient) UpdateAccount(ctx context.Context, t AdminTarget, id int64, req AdminAccount) (*AdminAccount, error) {
	req.Status = adminAccountStatusForUpdate(req.Status)
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetHeader("x-api-key", t.APIKey).
		SetBody(body).
		Put(strings.TrimRight(t.BaseURL, "/") + "/api/v1/admin/accounts/" + strconv.FormatInt(id, 10))
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		return nil, fmt.Errorf("update admin account: %w", connector.HTTPStatusError(resp.StatusCode(), resp.Body()))
	}
	out, err := decodeAdminAccount(resp.Body())
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (a *AdminClient) UpdateAccountScheduling(ctx context.Context, t AdminTarget, id int64, req AdminAccountSchedulingUpdate) (*AdminAccount, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetHeader("x-api-key", t.APIKey).
		SetBody(body).
		Put(strings.TrimRight(t.BaseURL, "/") + "/api/v1/admin/accounts/" + strconv.FormatInt(id, 10))
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		return nil, fmt.Errorf("update admin account scheduling: %w", connector.HTTPStatusError(resp.StatusCode(), resp.Body()))
	}
	return decodeAdminAccount(resp.Body())
}

func (a *AdminClient) SetAccountSchedulable(ctx context.Context, t AdminTarget, id int64, schedulable bool) (*AdminAccount, error) {
	body, err := json.Marshal(map[string]bool{"schedulable": schedulable})
	if err != nil {
		return nil, err
	}
	resp, err := a.client.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetHeader("x-api-key", t.APIKey).
		SetBody(body).
		Post(strings.TrimRight(t.BaseURL, "/") + "/api/v1/admin/accounts/" + strconv.FormatInt(id, 10) + "/schedulable")
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		return nil, fmt.Errorf("set admin account schedulable: %w", connector.HTTPStatusError(resp.StatusCode(), resp.Body()))
	}
	out, err := decodeAdminAccount(resp.Body())
	if err != nil {
		return nil, err
	}
	return out, nil
}

func adminAccountStatusForUpdate(status string) string {
	if strings.TrimSpace(status) == "disabled" {
		return "inactive"
	}
	return status
}

func (a *AdminClient) DeleteAccount(ctx context.Context, t AdminTarget, id int64) error {
	resp, err := a.client.http.R().
		SetContext(ctx).
		SetHeader("x-api-key", t.APIKey).
		Delete(strings.TrimRight(t.BaseURL, "/") + "/api/v1/admin/accounts/" + strconv.FormatInt(id, 10))
	if err != nil {
		return err
	}
	if resp.IsError() {
		return fmt.Errorf("delete admin account: %w", connector.HTTPStatusError(resp.StatusCode(), resp.Body()))
	}
	return nil
}

func (a *AdminClient) SyncAccountModelsFromUpstream(ctx context.Context, t AdminTarget, id int64) ([]string, error) {
	resp, err := a.client.http.R().
		SetContext(ctx).
		SetHeader("x-api-key", t.APIKey).
		Post(strings.TrimRight(t.BaseURL, "/") + "/api/v1/admin/accounts/" + strconv.FormatInt(id, 10) + "/models/sync-upstream")
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		return nil, fmt.Errorf("sync account models from upstream: %w", connector.HTTPStatusError(resp.StatusCode(), resp.Body()))
	}
	return decodeAdminModels(resp.Body())
}

func (a *AdminClient) ListAccountModels(ctx context.Context, t AdminTarget, id int64) ([]string, error) {
	body, err := a.getJSON(ctx, t, "/api/v1/admin/accounts/"+strconv.FormatInt(id, 10)+"/models")
	if err != nil {
		return nil, err
	}
	return decodeAdminModelList(body)
}

func (a *AdminClient) TestAccount(ctx context.Context, t AdminTarget, id int64, modelID string) (*AdminAccountTestResult, error) {
	payload := map[string]string{}
	if strings.TrimSpace(modelID) != "" {
		payload["model_id"] = strings.TrimSpace(modelID)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetHeader("Accept", "text/event-stream").
		SetHeader("x-api-key", t.APIKey).
		SetBody(body).
		Post(strings.TrimRight(t.BaseURL, "/") + "/api/v1/admin/accounts/" + strconv.FormatInt(id, 10) + "/test")
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		return nil, fmt.Errorf("test admin account: %w", connector.HTTPStatusError(resp.StatusCode(), resp.Body()))
	}
	return decodeAdminAccountTest(resp.Body())
}

func decodeAdminAccountTest(body []byte) (*AdminAccountTestResult, error) {
	var result AdminAccountTestResult
	var completed bool
	var texts []string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var event struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Model   string `json:"model"`
			Success *bool  `json:"success"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			continue
		}
		switch event.Type {
		case "test_start":
			result.Model = strings.TrimSpace(event.Model)
		case "content", "status":
			if event.Text != "" {
				texts = append(texts, event.Text)
			}
		case "error":
			if strings.TrimSpace(event.Error) != "" {
				result.ResponseText = strings.Join(texts, "")
				return &result, errors.New(strings.TrimSpace(event.Error))
			}
		case "test_complete":
			if event.Success != nil && !*event.Success {
				result.ResponseText = strings.Join(texts, "")
				return &result, errors.New("test failed")
			}
			completed = true
		}
	}
	result.ResponseText = strings.Join(texts, "")
	if !completed {
		return &result, errors.New("test did not complete")
	}
	return &result, nil
}

func decodeAdminModels(body []byte) ([]string, error) {
	var wrapped struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return nil, fmt.Errorf("decode admin models response: %w", err)
	}
	if wrapped.Code != 0 {
		return nil, errors.New(strings.TrimSpace(wrapped.Message))
	}
	if len(wrapped.Data) == 0 || string(wrapped.Data) == "null" {
		return []string{}, nil
	}
	var object struct {
		Models json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(wrapped.Data, &object); err == nil && len(object.Models) > 0 {
		return decodeAdminModelList(object.Models)
	}
	return decodeAdminModelList(wrapped.Data)
}

func decodeAdminModelList(raw json.RawMessage) ([]string, error) {
	var stringsList []string
	if err := json.Unmarshal(raw, &stringsList); err == nil {
		return compactUniqueStrings(stringsList), nil
	}
	var objects []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &objects); err != nil {
		return nil, fmt.Errorf("decode admin models: %w", err)
	}
	out := make([]string, 0, len(objects))
	for _, item := range objects {
		if strings.TrimSpace(item.ID) != "" {
			out = append(out, item.ID)
			continue
		}
		out = append(out, item.Name)
	}
	return compactUniqueStrings(out), nil
}

func compactUniqueStrings(list []string) []string {
	out := make([]string, 0, len(list))
	seen := map[string]struct{}{}
	for _, item := range list {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func (a *AdminClient) DeleteGroup(ctx context.Context, t AdminTarget, id int64) error {
	resp, err := a.client.http.R().
		SetContext(ctx).
		SetHeader("x-api-key", t.APIKey).
		Delete(strings.TrimRight(t.BaseURL, "/") + "/api/v1/admin/groups/" + strconv.FormatInt(id, 10))
	if err != nil {
		return err
	}
	if resp.IsError() {
		return fmt.Errorf("delete admin group: %w", connector.HTTPStatusError(resp.StatusCode(), resp.Body()))
	}
	return nil
}

func (a *AdminClient) getJSON(ctx context.Context, t AdminTarget, path string) ([]byte, error) {
	resp, err := a.client.http.R().
		SetContext(ctx).
		SetHeader("x-api-key", t.APIKey).
		Get(strings.TrimRight(t.BaseURL, "/") + path)
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		return nil, connector.HTTPStatusError(resp.StatusCode(), resp.Body())
	}
	var wrapped struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(resp.Body(), &wrapped); err != nil {
		return nil, fmt.Errorf("decode admin response: %w", err)
	}
	if wrapped.Code != 0 {
		return nil, errors.New(strings.TrimSpace(wrapped.Message))
	}
	return wrapped.Data, nil
}

func decodeAdminAccount(body []byte) (*AdminAccount, error) {
	var wrapped struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil && wrapped.Data != nil {
		if wrapped.Code != 0 {
			return nil, errors.New(strings.TrimSpace(wrapped.Message))
		}
		var out AdminAccount
		if err := json.Unmarshal(wrapped.Data, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
	var out AdminAccount
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func normalizeAdminGroupRatios(groups []AdminGroup) {
	for i := range groups {
		if groups[i].Ratio == 0 && groups[i].RateMultiplier != 0 {
			groups[i].Ratio = groups[i].RateMultiplier
		}
	}
}

func adminPageHasNext(page, pageSize, pages int, total int64, itemCount int) bool {
	if itemCount == 0 {
		return false
	}
	if pages > 0 {
		return page < pages
	}
	if total > 0 && pageSize > 0 {
		return int64(page*pageSize) < total
	}
	return pageSize > 0 && itemCount >= pageSize
}

func collectRateMultipliers(value any) []float64 {
	switch typed := value.(type) {
	case []any:
		out := make([]float64, 0, len(typed))
		for _, item := range typed {
			out = append(out, collectRateMultipliers(item)...)
		}
		return out
	case map[string]any:
		out := make([]float64, 0)
		for key, item := range typed {
			lowerKey := strings.ToLower(strings.TrimSpace(key))
			if number, ok := item.(float64); ok {
				if lowerKey == "rate_multiplier" || lowerKey == "multiplier" || lowerKey == "effective_rate_multiplier" || isNumericKey(lowerKey) {
					out = append(out, number)
				}
				continue
			}
			if lowerKey == "items" || lowerKey == "data" || lowerKey == "multipliers" || lowerKey == "users" {
				out = append(out, collectRateMultipliers(item)...)
			}
		}
		return out
	default:
		return nil
	}
}

func compactPositiveMultipliers(values []float64) []float64 {
	out := make([]float64, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		key := strconv.FormatFloat(value, 'g', -1, 64)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	sort.Float64s(out)
	return out
}

func isNumericKey(value string) bool {
	if value == "" {
		return false
	}
	_, err := strconv.ParseInt(value, 10, 64)
	return err == nil
}
