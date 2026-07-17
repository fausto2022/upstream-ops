package mainstation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/bejix/upstream-ops/backend/notify"
	"github.com/bejix/upstream-ops/backend/storage"
	"gorm.io/gorm"
)

const (
	probeResponseBodyLimit    int64 = 64 << 10
	healthSchedulerBatchLimit       = 20
)

type healthPolicy struct {
	Mode                      string `json:"mode"`
	L0IntervalMinutes         int    `json:"l0_interval_minutes"`
	L1IntervalMinutes         int    `json:"l1_interval_minutes"`
	L2IntervalMinutes         int    `json:"l2_interval_minutes"`
	JitterPercent             int    `json:"jitter_percent"`
	TransientFailureThreshold int    `json:"transient_failure_threshold"`
	EmptyFailureThreshold     int    `json:"empty_failure_threshold"`
	RecoverySuccessThreshold  int    `json:"recovery_success_threshold"`
	WindowSize                int    `json:"window_size"`
	DailyL1Limit              int    `json:"daily_l1_limit"`
	DailyL2Limit              int    `json:"daily_l2_limit"`
	DailyTokenLimit           int64  `json:"daily_token_limit"`
}

type probeExecution struct {
	Protocol     string
	Model        string
	Endpoint     string
	Status       string
	ErrorClass   string
	HTTPStatus   int
	LatencyMS    int64
	InputTokens  *int64
	OutputTokens *int64
	TotalTokens  *int64
	Message      string
}

type probeRequest struct {
	Method   string
	Path     string
	Protocol string
	Model    string
	Headers  map[string]string
	Body     any
}

func (s *Service) CheckMember(ctx context.Context, poolID, memberID uint, in HealthCheckInput) (*HealthCheckResult, error) {
	level := strings.ToUpper(strings.TrimSpace(in.Level))
	if level != "L0" && level != "L1" && level != "L2" {
		return nil, errors.New("health check level must be L0, L1 or L2")
	}
	pool, err := s.store.FindPool(poolID)
	if err != nil {
		return nil, err
	}
	member, err := s.store.FindMember(poolID, memberID)
	if err != nil {
		return nil, err
	}
	if !member.HealthEnabled && !in.Force {
		return nil, errors.New("health checks are disabled for this member")
	}
	if member.BindingStatus == "orphaned" || member.BindingStatus == "invalid" {
		return nil, errors.New("member binding is invalid")
	}
	policy := parseHealthPolicy(pool.HealthPolicyJSON)
	release, err := s.acquireHealthSlot(ctx, member.ID, member.SourceChannelID, level)
	if err != nil {
		return nil, err
	}
	defer release()

	now := s.now()
	if !in.Force {
		if last, lastErr := s.store.LastHealthCheck(member.ID, level); lastErr == nil {
			interval := healthLevelInterval(policy, level)
			if interval > 0 && now.Sub(last.CreatedAt) < interval {
				return nil, fmt.Errorf("health check minimum interval has not elapsed; retry after %s", interval-now.Sub(last.CreatedAt))
			}
		} else if !errors.Is(lastErr, gorm.ErrRecordNotFound) {
			return nil, lastErr
		}
	}

	budget, err := s.healthBudget(member.ID, policy, now)
	if err != nil {
		return nil, err
	}
	if !in.Force && healthBudgetExceeded(level, budget) {
		check := storage.MainAccountHealthCheck{
			PoolID: pool.ID, MemberID: member.ID, RemoteAccountID: memberRemoteID(member), Level: level,
			Status: "skipped_budget", ErrorClass: "budget_exceeded", Message: "daily health probe budget exceeded",
			StartedAt: now, FinishedAt: now, CreatedAt: now,
		}
		if err := s.store.AppendHealthCheck(&check); err != nil {
			return nil, err
		}
		s.notifyHealthBudgetExceeded(ctx, pool, member, level, budget)
		stats, _ := s.MemberHealthStats(member.ID)
		return &HealthCheckResult{Check: check, Member: *member, Stats: stats, Budget: budget}, nil
	}

	startedAt := s.now()
	execution := s.executeHealthProbe(ctx, level, member)
	finishedAt := s.now()
	check := storage.MainAccountHealthCheck{
		PoolID:          pool.ID,
		MemberID:        member.ID,
		RemoteAccountID: memberRemoteID(member),
		Level:           level,
		Protocol:        execution.Protocol,
		Model:           execution.Model,
		Endpoint:        execution.Endpoint,
		Status:          execution.Status,
		ErrorClass:      execution.ErrorClass,
		HTTPStatus:      execution.HTTPStatus,
		LatencyMS:       execution.LatencyMS,
		InputTokens:     execution.InputTokens,
		OutputTokens:    execution.OutputTokens,
		TotalTokens:     execution.TotalTokens,
		Message:         sanitizeProbeSummary(execution.Message),
		StartedAt:       startedAt,
		FinishedAt:      finishedAt,
		CreatedAt:       finishedAt,
	}
	fields, action, oldHealth, newHealth := applyHealthOutcome(member, &check, policy, finishedAt)
	check.TriggeredAction = action
	if err := s.store.UpdateMemberHealth(member.ID, fields); err != nil {
		return nil, err
	}
	if err := s.store.AppendHealthCheck(&check); err != nil {
		return nil, err
	}
	if config, configErr := s.store.GetConfig(); configErr == nil && config.HealthObservedAt == nil {
		config.HealthObservedAt = &finishedAt
		if config.ObservationEvaluatedAt == nil {
			config.ObservationEvaluatedAt = &finishedAt
		}
		_ = s.store.SaveConfig(config)
	}
	automationAction, automationErr := s.applyHealthAutomation(ctx, pool, member, &check, newHealth)
	if automationAction != "" {
		if check.TriggeredAction != "" {
			check.TriggeredAction += ","
		}
		check.TriggeredAction += automationAction
	}
	if automationErr != nil {
		check.Message = sanitizeProbeSummary(check.Message + "; automation failed: " + automationErr.Error())
		if check.TriggeredAction != "" {
			check.TriggeredAction += ","
		}
		check.TriggeredAction += "automation_failed"
	}
	if automationAction != "" || automationErr != nil {
		_ = s.store.UpdateHealthCheckOutcome(check.ID, check.TriggeredAction, check.Message)
	}
	updated, err := s.store.FindMember(pool.ID, member.ID)
	if err != nil {
		return nil, err
	}
	if newHealth == "unhealthy" || (newHealth == "healthy" && (oldHealth == "unhealthy" || oldHealth == "quarantined" || oldHealth == "degraded")) {
		s.notifyHealthTransition(ctx, pool, updated, &check, oldHealth, newHealth)
	}
	_, _ = s.EvaluatePoolCapacity(ctx, pool.ID)
	stats, err := s.MemberHealthStats(member.ID)
	if err != nil {
		return nil, err
	}
	budget, err = s.healthBudget(member.ID, policy, finishedAt)
	if err != nil {
		return nil, err
	}
	return &HealthCheckResult{Check: check, Member: *updated, Stats: stats, Budget: budget}, nil
}

func (s *Service) applyHealthAutomation(ctx context.Context, pool *storage.MainAccountPool, member *storage.MainAccountPoolMember, check *storage.MainAccountHealthCheck, newHealth string) (string, error) {
	if member.RemoteAccountID == nil {
		return "", nil
	}
	config, err := s.store.GetConfig()
	if err != nil {
		return "", err
	}
	if newHealth == "unhealthy" && config.AutoHealthProtection && config.HealthObservedAt != nil {
		_, err := s.ActivateGuardLock(ctx, *member.RemoteAccountID, "health", "member health checks reached quarantine threshold", map[string]any{
			"pool_id": pool.ID, "member_id": member.ID, "health_check_id": check.ID,
			"level": check.Level, "error_class": check.ErrorClass,
		}, "health")
		if err != nil {
			return "health_lock_failed", err
		}
		return "health_lock_applied", nil
	}
	if newHealth == "healthy" && config.AutoRecovery {
		_, err := s.ClearGuardLock(ctx, *member.RemoteAccountID, "health", "health")
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		if err != nil {
			return "health_lock_clear_failed", err
		}
		return "health_lock_cleared", nil
	}
	return "", nil
}

func (s *Service) executeHealthProbe(ctx context.Context, level string, member *storage.MainAccountPoolMember) probeExecution {
	if member.RemoteAccountID == nil {
		return probeExecution{Status: "config_error", ErrorClass: "binding_missing", Message: "member has no remote account binding"}
	}
	switch level {
	case "L0":
		return s.executeL0(ctx, member)
	case "L1":
		return s.executeL1(ctx, member)
	case "L2":
		return s.executeL2(ctx, member)
	default:
		return probeExecution{Status: "config_error", ErrorClass: "invalid_level", Message: "invalid health check level"}
	}
}

func (s *Service) executeL0(ctx context.Context, member *storage.MainAccountPoolMember) probeExecution {
	channel, secret, err := s.healthSourceCredentials(ctx, member)
	if err != nil {
		return probeExecution{Status: "config_error", ErrorClass: "credential_missing", Message: err.Error()}
	}
	if channel.Type == storage.ChannelTypeSub2API {
		result := s.performProbeRequest(ctx, channel, secret, probeRequest{
			Method: http.MethodGet, Path: "/v1/sub2api/billing", Protocol: "sub2api_billing",
		})
		if result.HTTPStatus != http.StatusNotFound && result.HTTPStatus != http.StatusMethodNotAllowed {
			return result
		}
	}
	return s.performProbeRequest(ctx, channel, secret, probeRequest{
		Method: http.MethodGet, Path: "/v1/models", Protocol: "models",
	})
}

func (s *Service) executeL1(ctx context.Context, member *storage.MainAccountPoolMember) probeExecution {
	model := strings.TrimSpace(member.HealthModel)
	if model == "" {
		return probeExecution{Status: "config_error", ErrorClass: "test_model_missing", Message: "low-cost health model is not configured"}
	}
	channel, secret, err := s.healthSourceCredentials(ctx, member)
	if err != nil {
		return probeExecution{Status: "config_error", ErrorClass: "credential_missing", Model: model, Message: err.Error()}
	}
	mode := strings.ToLower(strings.TrimSpace(member.HealthAPIMode))
	request := probeRequest{Method: http.MethodPost, Model: model, Headers: map[string]string{"Content-Type": "application/json"}}
	switch mode {
	case "", "openai_chat":
		request.Path = "/v1/chat/completions"
		request.Protocol = "openai_chat"
		request.Body = map[string]any{
			"model": model, "messages": []map[string]string{{"role": "user", "content": "Reply OK"}},
			"max_tokens": 4, "stream": false,
		}
	case "openai_responses":
		request.Path = "/v1/responses"
		request.Protocol = "openai_responses"
		request.Body = map[string]any{"model": model, "input": "Reply OK", "max_output_tokens": 8, "stream": false}
	case "anthropic":
		request.Path = "/v1/messages"
		request.Protocol = "anthropic"
		request.Headers["anthropic-version"] = "2023-06-01"
		request.Body = map[string]any{
			"model": model, "messages": []map[string]string{{"role": "user", "content": "Reply OK"}},
			"max_tokens": 4, "stream": false,
		}
	case "gemini":
		request.Path = "/v1beta/models/" + url.PathEscape(model) + ":generateContent"
		request.Protocol = "gemini"
		request.Body = map[string]any{
			"contents":         []map[string]any{{"parts": []map[string]string{{"text": "Reply OK"}}}},
			"generationConfig": map[string]any{"maxOutputTokens": 8},
		}
	default:
		return probeExecution{Status: "config_error", ErrorClass: "protocol_unsupported", Model: model, Message: "unsupported health api mode"}
	}
	return s.performProbeRequest(ctx, channel, secret, request)
}

func (s *Service) executeL2(ctx context.Context, member *storage.MainAccountPoolMember) probeExecution {
	model := strings.TrimSpace(member.HealthModel)
	if model == "" {
		return probeExecution{Status: "config_error", ErrorClass: "test_model_missing", Protocol: "sub2api_account_test", Message: "account test model is not configured"}
	}
	_, target, apiKey, err := s.loadAdminTarget()
	if err != nil {
		return probeExecution{Status: "config_error", ErrorClass: "main_station_unavailable", Protocol: "sub2api_account_test", Model: model, Message: err.Error()}
	}
	started := s.now()
	_, err = s.adminFactory().TestAccount(ctx, sub2api.AdminTarget{BaseURL: target.BaseURL, APIKey: apiKey}, *member.RemoteAccountID, model)
	latency := s.now().Sub(started).Milliseconds()
	if err == nil {
		return probeExecution{Status: "success", Protocol: "sub2api_account_test", Model: model, Endpoint: "/api/v1/admin/accounts/:id/test", LatencyMS: latency, Message: "account test succeeded"}
	}
	status := statusCodeFromError(err)
	resultStatus, class := classifyProbeFailure(status, err)
	return probeExecution{
		Status: resultStatus, ErrorClass: class, HTTPStatus: status, Protocol: "sub2api_account_test",
		Model: model, Endpoint: "/api/v1/admin/accounts/:id/test", LatencyMS: latency,
		Message: redactSecretError(err, apiKey).Error(),
	}
}

func (s *Service) healthSourceCredentials(ctx context.Context, member *storage.MainAccountPoolMember) (*storage.Channel, string, error) {
	channel, err := s.channels.FindByID(member.SourceChannelID)
	if err != nil {
		return nil, "", fmt.Errorf("load source channel: %w", err)
	}
	if member.SourceAPIKeyID == nil {
		return channel, "", errors.New("source api key is not bound")
	}
	secret, err := s.channelSvc.RevealAPIKey(ctx, member.SourceChannelID, *member.SourceAPIKeyID)
	if err != nil {
		return channel, "", fmt.Errorf("reveal source api key: %w", err)
	}
	if strings.TrimSpace(secret) == "" {
		return channel, "", errors.New("source api key is empty")
	}
	return channel, secret, nil
}

func (s *Service) performProbeRequest(ctx context.Context, channel *storage.Channel, secret string, request probeRequest) probeExecution {
	endpoint, err := joinProbeURL(channel.SiteURL, request.Path)
	if err != nil {
		return probeExecution{Status: "config_error", ErrorClass: "url_invalid", Protocol: request.Protocol, Model: request.Model, Message: err.Error()}
	}
	var body io.Reader
	if request.Body != nil {
		encoded, err := json.Marshal(request.Body)
		if err != nil {
			return probeExecution{Status: "config_error", ErrorClass: "request_encode", Protocol: request.Protocol, Model: request.Model, Endpoint: request.Path, Message: err.Error()}
		}
		body = bytes.NewReader(encoded)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, request.Method, endpoint, body)
	if err != nil {
		return probeExecution{Status: "config_error", ErrorClass: "request_create", Protocol: request.Protocol, Model: request.Model, Endpoint: request.Path, Message: err.Error()}
	}
	for key, value := range request.Headers {
		httpRequest.Header.Set(key, value)
	}
	s.probeConfigMu.RLock()
	userAgent := s.probeUserAgent
	s.probeConfigMu.RUnlock()
	httpRequest.Header.Set("User-Agent", userAgent)
	switch request.Protocol {
	case "anthropic":
		httpRequest.Header.Set("x-api-key", secret)
	case "gemini":
		httpRequest.Header.Set("x-goog-api-key", secret)
	default:
		httpRequest.Header.Set("Authorization", "Bearer "+secret)
	}
	started := s.now()
	response, err := s.probeHTTPClient(channel).Do(httpRequest)
	latency := s.now().Sub(started).Milliseconds()
	if err != nil {
		_, class := classifyProbeFailure(0, err)
		return probeExecution{
			Status: "failure", ErrorClass: class, Protocol: request.Protocol, Model: request.Model,
			Endpoint: request.Path, LatencyMS: latency, Message: redactSecretError(err, secret).Error(),
		}
	}
	defer response.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, probeResponseBodyLimit+1))
	if readErr != nil {
		return probeExecution{Status: "failure", ErrorClass: "response_read", HTTPStatus: response.StatusCode, Protocol: request.Protocol, Model: request.Model, Endpoint: request.Path, LatencyMS: latency, Message: readErr.Error()}
	}
	if int64(len(raw)) > probeResponseBodyLimit {
		return probeExecution{Status: "failure", ErrorClass: "response_too_large", HTTPStatus: response.StatusCode, Protocol: request.Protocol, Model: request.Model, Endpoint: request.Path, LatencyMS: latency, Message: "response exceeded 64 KiB limit"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := redactSecretError(connector.HTTPStatusError(response.StatusCode, raw), secret).Error()
		status, class := classifyHTTPFailure(response.StatusCode, message)
		return probeExecution{Status: status, ErrorClass: class, HTTPStatus: response.StatusCode, Protocol: request.Protocol, Model: request.Model, Endpoint: request.Path, LatencyMS: latency, Message: message}
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return probeExecution{Status: "failure", ErrorClass: "empty_response", HTTPStatus: response.StatusCode, Protocol: request.Protocol, Model: request.Model, Endpoint: request.Path, LatencyMS: latency, Message: "upstream returned an empty response"}
	}
	input, output, total := parseProbeUsage(raw)
	return probeExecution{
		Status: "success", HTTPStatus: response.StatusCode, Protocol: request.Protocol, Model: request.Model,
		Endpoint: request.Path, LatencyMS: latency, InputTokens: input, OutputTokens: output, TotalTokens: total,
		Message: "probe succeeded",
	}
}

func (s *Service) probeHTTPClient(channel *storage.Channel) *http.Client {
	s.probeConfigMu.RLock()
	proxyConfig := s.proxyConfig
	timeout := s.probeTimeout
	s.probeConfigMu.RUnlock()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = timeout
	transport.TLSHandshakeTimeout = minDuration(timeout, 10*time.Second)
	transport.DialContext = (&net.Dialer{Timeout: minDuration(timeout, 10*time.Second), KeepAlive: 30 * time.Second}).DialContext
	if channel.ProxyEnabled {
		if rawProxy, err := proxyConfig.ActiveURL(); err == nil && rawProxy != "" {
			if parsed, parseErr := url.Parse(rawProxy); parseErr == nil {
				transport.Proxy = http.ProxyURL(parsed)
			}
		}
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func joinProbeURL(baseURL, path string) (string, error) {
	normalized, err := normalizeMainStationURL(baseURL)
	if err != nil {
		return "", err
	}
	base, err := url.Parse(normalized + "/")
	if err != nil {
		return "", err
	}
	reference, err := url.Parse(strings.TrimPrefix(path, "/"))
	if err != nil {
		return "", err
	}
	resolved := base.ResolveReference(reference)
	if !strings.EqualFold(base.Host, resolved.Host) {
		return "", errors.New("probe endpoint escaped the configured upstream host")
	}
	return resolved.String(), nil
}

func classifyHTTPFailure(status int, message string) (string, string) {
	lower := strings.ToLower(message)
	if (status == http.StatusBadRequest || status == http.StatusUnprocessableEntity) &&
		(strings.Contains(lower, "max_tokens") || strings.Contains(lower, "max_output_tokens") || strings.Contains(lower, "maxoutputtokens")) &&
		(strings.Contains(lower, "minimum") || strings.Contains(lower, "at least") || strings.Contains(lower, "too small")) {
		return "config_error", "output_limit_incompatible"
	}
	if (status == http.StatusBadRequest || status == http.StatusNotFound || status == http.StatusUnprocessableEntity) &&
		strings.Contains(lower, "model") &&
		(strings.Contains(lower, "not found") || strings.Contains(lower, "unsupported") || strings.Contains(lower, "does not exist")) {
		return "config_error", "model_incompatible"
	}
	_, class := classifyProbeFailure(status, errors.New(message))
	return "failure", class
}

func classifyProbeFailure(status int, err error) (string, string) {
	switch status {
	case http.StatusUnauthorized:
		return "failure", "auth_invalid"
	case http.StatusForbidden:
		return "failure", "permission_denied"
	case http.StatusPaymentRequired:
		return "failure", "balance_exhausted"
	case http.StatusTooManyRequests:
		return "failure", "rate_limited"
	}
	if status >= 500 {
		return "failure", "server_error"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "failure", "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "failure", "timeout"
	}
	if status >= 400 {
		return "failure", "http_error"
	}
	return "failure", "connection_error"
}

func parseProbeUsage(raw []byte) (*int64, *int64, *int64) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, nil, nil
	}
	usage := objectValue(root, "usage")
	if usage != nil {
		input := firstInt64(usage, "prompt_tokens", "input_tokens")
		output := firstInt64(usage, "completion_tokens", "output_tokens")
		total := firstInt64(usage, "total_tokens")
		if total == nil && (input != nil || output != nil) {
			value := valueOrZero(input) + valueOrZero(output)
			total = &value
		}
		return input, output, total
	}
	usage = objectValue(root, "usageMetadata")
	if usage != nil {
		input := firstInt64(usage, "promptTokenCount")
		output := firstInt64(usage, "candidatesTokenCount")
		total := firstInt64(usage, "totalTokenCount")
		return input, output, total
	}
	return nil, nil, nil
}

func objectValue(root map[string]any, key string) map[string]any {
	value, ok := root[key].(map[string]any)
	if !ok {
		return nil
	}
	return value
}

func firstInt64(root map[string]any, keys ...string) *int64 {
	for _, key := range keys {
		value, ok := root[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case float64:
			result := int64(typed)
			return &result
		case json.Number:
			if result, err := typed.Int64(); err == nil {
				return &result
			}
		}
	}
	return nil
}

func applyHealthOutcome(member *storage.MainAccountPoolMember, check *storage.MainAccountHealthCheck, policy healthPolicy, now time.Time) (map[string]any, string, string, string) {
	oldHealth := member.LastHealthStatus
	newHealth := oldHealth
	newMemberStatus := member.Status
	successes := member.ConsecutiveHealthSuccess
	failures := member.ConsecutiveHealthFailure
	var cooldown *time.Time
	action := ""

	switch check.Status {
	case "success":
		failures = 0
		successes++
		recovering := oldHealth == "unhealthy" || oldHealth == "quarantined" || member.Status == "quarantined" ||
			(oldHealth == "degraded" && member.ConsecutiveHealthSuccess > 0)
		if recovering {
			if successes >= policy.RecoverySuccessThreshold {
				newHealth = "healthy"
				newMemberStatus = "active"
				action = "health_recovered"
			} else {
				newHealth = "degraded"
				newMemberStatus = "degraded"
			}
		} else {
			newHealth = "healthy"
			newMemberStatus = "active"
		}
	case "config_error":
		newHealth = "config_error"
		newMemberStatus = "error"
	case "skipped_budget":
		return map[string]any{}, "", oldHealth, oldHealth
	default:
		successes = 0
		if check.ErrorClass == "rate_limited" {
			until := now.Add(15 * time.Minute)
			cooldown = &until
			newHealth = "degraded"
			newMemberStatus = "degraded"
			break
		}
		failures++
		threshold := policy.TransientFailureThreshold
		if check.ErrorClass == "empty_response" {
			threshold = policy.EmptyFailureThreshold
		}
		immediate := check.ErrorClass == "auth_invalid" || check.ErrorClass == "permission_denied" || check.ErrorClass == "balance_exhausted"
		if immediate || failures >= threshold {
			newHealth = "unhealthy"
			newMemberStatus = "quarantined"
			action = "health_quarantined"
		} else {
			newHealth = "degraded"
			newMemberStatus = "degraded"
		}
	}
	fields := map[string]any{
		"last_health_status":         newHealth,
		"last_health_at":             now,
		"consecutive_health_success": successes,
		"consecutive_health_failure": failures,
		"cooldown_until":             cooldown,
		"status":                     newMemberStatus,
	}
	return fields, action, oldHealth, newHealth
}

func (s *Service) MemberHealthStats(memberID uint) (HealthStats, error) {
	member, err := s.findMemberByID(memberID)
	if err != nil {
		return HealthStats{}, err
	}
	now := s.now()
	checks, err := s.store.ListMemberHealthChecksSince(memberID, now.Add(-7*24*time.Hour), 10000)
	if err != nil {
		return HealthStats{}, err
	}
	stats := HealthStats{
		MemberID: memberID, LastStatus: member.LastHealthStatus,
		ConsecutiveSuccess: member.ConsecutiveHealthSuccess, ConsecutiveFailure: member.ConsecutiveHealthFailure,
	}
	effective := make([]storage.MainAccountHealthCheck, 0, len(checks))
	latencies := make([]int64, 0, len(checks))
	for _, check := range checks {
		if check.Status == "success" {
			if stats.LastSuccessAt == nil {
				value := check.FinishedAt
				stats.LastSuccessAt = &value
			}
			latencies = append(latencies, check.LatencyMS)
		} else if check.Status == "failure" {
			if stats.LastFailureAt == nil {
				value := check.FinishedAt
				stats.LastFailureAt = &value
				stats.LastErrorClass = check.ErrorClass
				stats.LastErrorMessage = check.Message
			}
		}
		if check.Status == "success" || check.Status == "failure" {
			effective = append(effective, check)
		}
	}
	stats.Recent20SuccessRate = successRate(limitChecks(effective, 20), time.Time{})
	stats.OneHourSuccessRate = successRate(effective, now.Add(-time.Hour))
	stats.TwentyFourHourSuccessRate = successRate(effective, now.Add(-24*time.Hour))
	stats.SevenDaySuccessRate = successRate(effective, now.Add(-7*24*time.Hour))
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		var total int64
		for _, latency := range latencies {
			total += latency
		}
		average := float64(total) / float64(len(latencies))
		stats.AverageLatencyMS = &average
		p50 := percentile(latencies, 0.50)
		p95 := percentile(latencies, 0.95)
		stats.P50LatencyMS = &p50
		stats.P95LatencyMS = &p95
	}
	dayStart := healthDayStart(now)
	for _, check := range checks {
		if !check.CreatedAt.Before(dayStart) {
			stats.DailyChecks++
			stats.DailyTokens += valueOrZero(check.TotalTokens)
		}
	}
	return stats, nil
}

func (s *Service) ListHealthChecks(poolID, memberID uint, level string, page, pageSize int) (*Page[storage.MainAccountHealthCheck], error) {
	if _, err := s.store.FindPool(poolID); err != nil {
		return nil, err
	}
	items, total, err := s.store.ListHealthChecks(poolID, memberID, level, page, pageSize)
	if err != nil {
		return nil, err
	}
	page, pageSize = normalizePage(page, pageSize)
	return &Page[storage.MainAccountHealthCheck]{Items: items, Total: total, Page: page, PageSize: pageSize, Pages: pageCount(total, pageSize)}, nil
}

func (s *Service) PoolHealthSummary(poolID uint) ([]MemberHealthSummary, error) {
	pool, err := s.store.FindPool(poolID)
	if err != nil {
		return nil, err
	}
	policy := parseHealthPolicy(pool.HealthPolicyJSON)
	members, err := s.store.ListMembers(poolID)
	if err != nil {
		return nil, err
	}
	out := make([]MemberHealthSummary, 0, len(members))
	for i := range members {
		stats, err := s.MemberHealthStats(members[i].ID)
		if err != nil {
			return nil, err
		}
		budget, err := s.healthBudget(members[i].ID, policy, s.now())
		if err != nil {
			return nil, err
		}
		out = append(out, MemberHealthSummary{Member: members[i], Stats: stats, Budget: budget})
	}
	return out, nil
}

func (s *Service) healthBudget(memberID uint, policy healthPolicy, now time.Time) (HealthBudget, error) {
	since := healthDayStart(now)
	l1, err := s.store.CountDailyHealthChecks(memberID, "L1", since)
	if err != nil {
		return HealthBudget{}, err
	}
	l2, err := s.store.CountDailyHealthChecks(memberID, "L2", since)
	if err != nil {
		return HealthBudget{}, err
	}
	tokens, err := s.store.SumDailyHealthTokens(memberID, since)
	if err != nil {
		return HealthBudget{}, err
	}
	return HealthBudget{
		DailyL1Used: l1, DailyL1Limit: policy.DailyL1Limit,
		DailyL2Used: l2, DailyL2Limit: policy.DailyL2Limit,
		DailyTokens: tokens, TokenLimit: policy.DailyTokenLimit,
	}, nil
}

func healthBudgetExceeded(level string, budget HealthBudget) bool {
	if budget.TokenLimit > 0 && budget.DailyTokens >= budget.TokenLimit {
		return level == "L1" || level == "L2"
	}
	if level == "L1" && budget.DailyL1Limit > 0 && budget.DailyL1Used >= int64(budget.DailyL1Limit) {
		return true
	}
	return level == "L2" && budget.DailyL2Limit > 0 && budget.DailyL2Used >= int64(budget.DailyL2Limit)
}

func (s *Service) RunDueHealthChecks(ctx context.Context) {
	members, err := s.store.ListAllMembers()
	if err != nil {
		if s.log != nil {
			s.log.Warn("list due main station health checks", "err", err)
		}
		return
	}
	type task struct {
		poolID   uint
		memberID uint
		level    string
	}
	tasks := make([]task, 0, healthSchedulerBatchLimit)
	poolCache := make(map[uint]*storage.MainAccountPool)
	for _, member := range members {
		if len(tasks) >= healthSchedulerBatchLimit {
			break
		}
		if !member.Enabled || !member.HealthEnabled || member.BindingStatus == "orphaned" || member.BindingStatus == "invalid" || member.Status == "disabled" {
			continue
		}
		pool := poolCache[member.PoolID]
		if pool == nil {
			pool, err = s.store.FindPool(member.PoolID)
			if err != nil {
				continue
			}
			poolCache[member.PoolID] = pool
		}
		if !pool.Enabled {
			continue
		}
		policy := parseHealthPolicy(pool.HealthPolicyJSON)
		for _, level := range []string{"L0", "L1", "L2"} {
			if level != "L0" && strings.TrimSpace(member.HealthModel) == "" {
				continue
			}
			if s.healthLevelDue(&member, policy, level, s.now()) {
				tasks = append(tasks, task{poolID: member.PoolID, memberID: member.ID, level: level})
				if len(tasks) >= healthSchedulerBatchLimit {
					break
				}
			}
		}
	}
	var wait sync.WaitGroup
	for _, item := range tasks {
		item := item
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := s.CheckMember(ctx, item.poolID, item.memberID, HealthCheckInput{Level: item.level}); err != nil && s.log != nil && !strings.Contains(err.Error(), "minimum interval") {
				s.log.Warn("scheduled main station health check", "err", err, "member_id", item.memberID, "level", item.level)
			}
		}()
	}
	wait.Wait()
}

func (s *Service) healthLevelDue(member *storage.MainAccountPoolMember, policy healthPolicy, level string, now time.Time) bool {
	interval := healthLevelInterval(policy, level)
	if interval <= 0 {
		return false
	}
	last, err := s.store.LastHealthCheck(member.ID, level)
	base := member.CreatedAt
	if err == nil {
		base = last.CreatedAt
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false
	}
	due := base.Add(interval + deterministicJitter(member.ID, level, interval, policy.JitterPercent))
	return !now.Before(due)
}

func (s *Service) acquireHealthSlot(ctx context.Context, memberID, channelID uint, level string) (func(), error) {
	key := fmt.Sprintf("%d:%s", memberID, level)
	s.healthMu.Lock()
	if _, running := s.healthRunning[key]; running {
		s.healthMu.Unlock()
		return nil, errors.New("the same member health check is already running")
	}
	s.healthRunning[key] = struct{}{}
	channelSemaphore := s.healthChannels[channelID]
	if channelSemaphore == nil {
		channelSemaphore = make(chan struct{}, 1)
		s.healthChannels[channelID] = channelSemaphore
	}
	s.healthMu.Unlock()

	cleanupRunning := func() {
		s.healthMu.Lock()
		delete(s.healthRunning, key)
		s.healthMu.Unlock()
	}
	select {
	case s.healthGlobal <- struct{}{}:
	case <-ctx.Done():
		cleanupRunning()
		return nil, ctx.Err()
	}
	select {
	case channelSemaphore <- struct{}{}:
	case <-ctx.Done():
		<-s.healthGlobal
		cleanupRunning()
		return nil, ctx.Err()
	}
	return func() {
		<-channelSemaphore
		<-s.healthGlobal
		cleanupRunning()
	}, nil
}

func (s *Service) findMemberByID(memberID uint) (*storage.MainAccountPoolMember, error) {
	members, err := s.store.ListAllMembers()
	if err != nil {
		return nil, err
	}
	for i := range members {
		if members[i].ID == memberID {
			return &members[i], nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (s *Service) notifyHealthBudgetExceeded(ctx context.Context, pool *storage.MainAccountPool, member *storage.MainAccountPoolMember, level string, budget HealthBudget) {
	if s.dispatcher == nil {
		return
	}
	dedupKey := fmt.Sprintf("%s:%d:%d:0", storage.EventHealthProbeBudgetExceeded, pool.ID, member.ID)
	claimed, err := s.store.TryClaimNotificationCooldown(dedupKey, string(storage.EventHealthProbeBudgetExceeded), pool.ID, member.ID, 0, 24*time.Hour)
	if err != nil || !claimed {
		return
	}
	_ = s.dispatcher.Dispatch(ctx, notify.Message{
		Event: storage.EventHealthProbeBudgetExceeded, ChannelID: member.SourceChannelID,
		Subject: fmt.Sprintf("[测活预算已用尽] %s · 成员 #%d", pool.Name, member.ID),
		Body: fmt.Sprintf("账号池：%s\n成员：#%d\n层级：%s\n今日 L1：%d/%d\n今日 L2：%d/%d\n今日 Token：%d/%d\n动作：暂停非必要 L1/L2，继续 L0。",
			pool.Name, member.ID, level, budget.DailyL1Used, budget.DailyL1Limit, budget.DailyL2Used, budget.DailyL2Limit, budget.DailyTokens, budget.TokenLimit),
	})
}

func (s *Service) notifyHealthTransition(ctx context.Context, pool *storage.MainAccountPool, member *storage.MainAccountPoolMember, check *storage.MainAccountHealthCheck, oldHealth, newHealth string) {
	if s.dispatcher == nil {
		return
	}
	event := storage.EventMainMemberHealthFailed
	subject := "主站成员测活失败"
	if newHealth == "healthy" {
		event = storage.EventMainMemberHealthRecovered
		subject = "主站成员健康恢复"
	}
	dedupKey := fmt.Sprintf("%s:%d:%d:0", event, pool.ID, member.ID)
	claimed, err := s.store.TryClaimNotificationCooldown(dedupKey, string(event), pool.ID, member.ID, 0, 30*time.Minute)
	if err != nil || !claimed {
		return
	}
	_ = s.dispatcher.Dispatch(ctx, notify.Message{
		Event: event, ChannelID: member.SourceChannelID,
		Subject: fmt.Sprintf("[%s] %s · 成员 #%d", subject, pool.Name, member.ID),
		Body: fmt.Sprintf("账号池：%s\n成员：#%d\n主站 Account：%s\n状态：%s -> %s\n层级：%s\n模型：%s\n错误：%s\n连续失败：%d\n延迟：%dms",
			pool.Name, member.ID, member.RemoteAccountName, oldHealth, newHealth, check.Level, check.Model, check.ErrorClass, member.ConsecutiveHealthFailure, check.LatencyMS),
	})
}

func parseHealthPolicy(raw string) healthPolicy {
	policy := healthPolicy{
		Mode: "observe", L0IntervalMinutes: 5, L1IntervalMinutes: 30, L2IntervalMinutes: 720,
		JitterPercent: 10, TransientFailureThreshold: 3, EmptyFailureThreshold: 2,
		RecoverySuccessThreshold: 3, WindowSize: 20, DailyL1Limit: 48, DailyL2Limit: 2,
	}
	_ = json.Unmarshal([]byte(raw), &policy)
	if policy.L0IntervalMinutes <= 0 {
		policy.L0IntervalMinutes = 5
	}
	if policy.L1IntervalMinutes <= 0 {
		policy.L1IntervalMinutes = 30
	}
	if policy.L2IntervalMinutes <= 0 {
		policy.L2IntervalMinutes = 720
	}
	if policy.JitterPercent < 0 {
		policy.JitterPercent = 0
	}
	if policy.JitterPercent > 50 {
		policy.JitterPercent = 50
	}
	if policy.TransientFailureThreshold <= 0 {
		policy.TransientFailureThreshold = 3
	}
	if policy.EmptyFailureThreshold <= 0 {
		policy.EmptyFailureThreshold = 2
	}
	if policy.RecoverySuccessThreshold <= 0 {
		policy.RecoverySuccessThreshold = 3
	}
	if policy.WindowSize <= 0 {
		policy.WindowSize = 20
	}
	return policy
}

func healthLevelInterval(policy healthPolicy, level string) time.Duration {
	switch level {
	case "L0":
		return time.Duration(policy.L0IntervalMinutes) * time.Minute
	case "L1":
		return time.Duration(policy.L1IntervalMinutes) * time.Minute
	case "L2":
		return time.Duration(policy.L2IntervalMinutes) * time.Minute
	default:
		return 0
	}
}

func deterministicJitter(memberID uint, level string, interval time.Duration, percent int) time.Duration {
	if percent <= 0 || interval <= 0 {
		return 0
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(fmt.Sprintf("%d:%s", memberID, level)))
	fraction := float64(hash.Sum32()%2001)/1000 - 1
	return time.Duration(float64(interval) * float64(percent) / 100 * fraction)
}

func successRate(checks []storage.MainAccountHealthCheck, since time.Time) *float64 {
	total := 0
	success := 0
	for _, check := range checks {
		if !since.IsZero() && check.CreatedAt.Before(since) {
			continue
		}
		total++
		if check.Status == "success" {
			success++
		}
	}
	if total == 0 {
		return nil
	}
	value := float64(success) / float64(total) * 100
	return &value
}

func limitChecks(checks []storage.MainAccountHealthCheck, limit int) []storage.MainAccountHealthCheck {
	if len(checks) <= limit {
		return checks
	}
	return checks[:limit]
}

func percentile(sorted []int64, percentile float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(math.Ceil(percentile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func healthDayStart(now time.Time) time.Time {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		location = time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	local := now.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
}

func statusCodeFromError(err error) int {
	text := err.Error()
	index := strings.Index(text, "status ")
	if index < 0 {
		return 0
	}
	fields := strings.Fields(text[index:])
	if len(fields) < 2 {
		return 0
	}
	value, _ := strconv.Atoi(strings.TrimSuffix(fields[1], ":"))
	return value
}

func sanitizeProbeSummary(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	runes := []rune(value)
	if len(runes) > 500 {
		return string(runes[:500]) + "..."
	}
	return value
}

func memberRemoteID(member *storage.MainAccountPoolMember) int64 {
	if member.RemoteAccountID == nil {
		return 0
	}
	return *member.RemoteAccountID
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
