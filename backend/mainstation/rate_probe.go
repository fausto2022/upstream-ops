package mainstation

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fausto2022/relaydeck/backend/connector"
	"github.com/fausto2022/relaydeck/backend/storage"
)

const (
	temporaryAPIKeyLifetime      = 10 * time.Minute
	temporaryAPIKeyRetryInterval = time.Minute
	temporaryAPIKeyCleanupLimit  = 50
)

func (s *Service) QuickTestRate(ctx context.Context, channelID, rateID uint, in RateQuickTestInput) (*RateQuickTestResult, error) {
	if channelID == 0 || rateID == 0 {
		return nil, errors.New("渠道或倍率分组参数不正确")
	}
	testKey := fmt.Sprintf("%d:%d", channelID, rateID)
	if !s.beginRateTest(testKey) {
		return nil, errors.New("该分组正在测试，请等待当前测试完成")
	}
	defer s.finishRateTest(testKey)

	channel, err := s.channels.FindByID(channelID)
	if err != nil {
		return nil, fmt.Errorf("读取上游渠道失败：%w", err)
	}
	rate, err := s.rates.FindByID(channelID, rateID)
	if err != nil {
		return nil, fmt.Errorf("读取倍率分组失败：%w", err)
	}
	platform := normalizeHealthPlatform(in.Platform)
	mode, err := quickTestAPIMode(platform)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(in.Model)
	if model == "" {
		model = strings.TrimSpace(s.configuredHealthModels()[platform])
	}
	if model == "" {
		return nil, errors.New("请选择快速测试模型")
	}
	request, err := buildL1ProbeRequest(mode, model)
	if err != nil {
		return nil, err
	}

	keyName, err := temporaryAPIKeyName()
	if err != nil {
		return nil, fmt.Errorf("生成临时 Key 名称失败：%w", err)
	}
	expiresAt := s.now().Add(temporaryAPIKeyLifetime)
	createRequest, err := temporaryAPIKeyRequest(channel, rate, keyName, expiresAt)
	if err != nil {
		return nil, err
	}
	created, err := s.channelSvc.CreateAPIKey(ctx, channelID, createRequest)
	if err != nil {
		return nil, fmt.Errorf("创建临时测试 Key 失败：%w", err)
	}
	if created == nil || created.ID <= 0 {
		return nil, errors.New("上游创建临时测试 Key 后没有返回 Key ID")
	}
	record := &storage.MainStationTemporaryAPIKey{
		ChannelID: channelID, RemoteKeyID: created.ID, KeyName: keyName, ExpiresAt: expiresAt,
	}
	if err := s.store.CreateTemporaryAPIKey(record); err != nil {
		cleanupErr := s.channelSvc.DeleteAPIKey(context.Background(), channelID, created.ID)
		return nil, errors.Join(fmt.Errorf("保存临时测试 Key 清理记录失败：%w", err), cleanupErr)
	}

	secret := strings.TrimSpace(created.Key)
	if secret == "" || isMaskedCredential(secret) {
		secret, err = s.channelSvc.RevealAPIKey(ctx, channelID, created.ID)
		if err != nil {
			cleanupErr := s.cleanupTemporaryAPIKey(record)
			return nil, errors.Join(fmt.Errorf("读取临时测试 Key 失败：%w", err), cleanupErr)
		}
	}
	if strings.TrimSpace(secret) == "" {
		cleanupErr := s.cleanupTemporaryAPIKey(record)
		return nil, errors.Join(errors.New("临时测试 Key 内容为空"), cleanupErr)
	}

	execution := s.performProbeRequest(ctx, channel, secret, request)
	cleanupErr := s.cleanupTemporaryAPIKey(record)
	result := quickTestResult(execution, keyName, cleanupErr, s.now())
	_ = s.appendAudit(nil, nil, nil, "rate_quick_test", "manual", result.Usable, nil, result, map[string]any{
		"channel_id": channelID,
		"rate_id":    rateID,
		"group":      rate.ModelName,
		"platform":   platform,
		"model":      model,
	}, result.Message, result.CleanupError)
	return result, nil
}

func (s *Service) CleanupTemporaryAPIKeys(ctx context.Context) {
	if !s.tempCleanupMu.TryLock() {
		return
	}
	defer s.tempCleanupMu.Unlock()
	now := s.now()
	if !s.tempCleanupAt.IsZero() && now.Sub(s.tempCleanupAt) < temporaryAPIKeyRetryInterval {
		return
	}
	s.tempCleanupAt = now
	items, err := s.store.ListTemporaryAPIKeysForCleanup(now, now.Add(-temporaryAPIKeyRetryInterval), temporaryAPIKeyCleanupLimit)
	if err != nil {
		if s.log != nil {
			s.log.Warn("list temporary api keys for cleanup", "err", err)
		}
		return
	}
	for i := range items {
		if err := s.cleanupTemporaryAPIKeyWithContext(ctx, &items[i]); err != nil && s.log != nil {
			s.log.Warn("cleanup temporary api key", "err", err, "channel_id", items[i].ChannelID, "remote_key_id", items[i].RemoteKeyID)
		}
	}
}

func (s *Service) beginRateTest(key string) bool {
	s.rateTestMu.Lock()
	defer s.rateTestMu.Unlock()
	if _, running := s.rateTests[key]; running {
		return false
	}
	s.rateTests[key] = struct{}{}
	return true
}

func (s *Service) finishRateTest(key string) {
	s.rateTestMu.Lock()
	delete(s.rateTests, key)
	s.rateTestMu.Unlock()
}

func (s *Service) cleanupTemporaryAPIKey(item *storage.MainStationTemporaryAPIKey) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.cleanupTemporaryAPIKeyWithContext(ctx, item)
}

func (s *Service) cleanupTemporaryAPIKeyWithContext(ctx context.Context, item *storage.MainStationTemporaryAPIKey) error {
	err := s.channelSvc.DeleteAPIKey(ctx, item.ChannelID, item.RemoteKeyID)
	if err == nil || missingRemoteResource(err) {
		if deleteErr := s.store.DeleteTemporaryAPIKey(item.ID); deleteErr != nil {
			return fmt.Errorf("删除临时 Key 清理记录失败：%w", deleteErr)
		}
		return nil
	}
	now := s.now()
	if updateErr := s.store.MarkTemporaryAPIKeyCleanupFailure(item.ID, now, sanitizeText(err.Error())); updateErr != nil {
		return errors.Join(fmt.Errorf("删除临时测试 Key 失败：%w", err), updateErr)
	}
	return fmt.Errorf("删除临时测试 Key 失败，后台将自动重试：%w", err)
}

func temporaryAPIKeyRequest(channel *storage.Channel, rate *storage.RateSnapshot, name string, expiresAt time.Time) (connector.APIKeyCreateRequest, error) {
	crossGroupRetry := false
	request := connector.APIKeyCreateRequest{
		Name: name, Group: strings.TrimSpace(rate.ModelName), GroupID: rate.RemoteGroupID, CrossGroupRetry: &crossGroupRetry,
	}
	switch channel.Type {
	case storage.ChannelTypeNewAPI:
		remainQuota := 100000
		unlimitedQuota := false
		expiredTime := expiresAt.Unix()
		request.GroupID = nil
		request.RemainQuota = &remainQuota
		request.UnlimitedQuota = &unlimitedQuota
		request.ExpiredTime = &expiredTime
	case storage.ChannelTypeSub2API:
		if rate.RemoteGroupID == nil || *rate.RemoteGroupID <= 0 {
			return connector.APIKeyCreateRequest{}, errors.New("该 Sub2API 分组缺少远端分组 ID，请先重新同步倍率")
		}
		quota := 1.0
		expiresInDays := 1
		request.Quota = &quota
		request.ExpiresInDays = &expiresInDays
	default:
		return connector.APIKeyCreateRequest{}, errors.New("当前渠道类型不支持快速测试")
	}
	return request, nil
}

func temporaryAPIKeyName() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	for i := range raw {
		raw[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return "测试key-" + string(raw), nil
}

func quickTestAPIMode(platform string) (string, error) {
	switch platform {
	case "openai", "grok":
		return "openai_chat", nil
	case "anthropic":
		return "anthropic", nil
	case "gemini":
		return "gemini", nil
	default:
		return "", errors.New("当前分组类型暂不支持快速测试")
	}
}

func quickTestResult(execution probeExecution, keyName string, cleanupErr error, testedAt time.Time) *RateQuickTestResult {
	usable := execution.Status == "success"
	reachable := usable || execution.HTTPStatus > 0
	status := "unreachable"
	if usable {
		status = "usable"
	} else if reachable {
		status = "reachable"
	}
	cleanupStatus := "deleted"
	cleanupMessage := ""
	if cleanupErr != nil {
		cleanupStatus = "pending"
		cleanupMessage = sanitizeText(cleanupErr.Error())
	}
	return &RateQuickTestResult{
		Status: status, Usable: usable, Reachable: reachable, Protocol: execution.Protocol, Model: execution.Model,
		Endpoint: execution.Endpoint, HTTPStatus: execution.HTTPStatus, LatencyMS: execution.LatencyMS, TTFBMS: execution.TTFBMS,
		ErrorClass: execution.ErrorClass, Message: quickTestMessage(execution), InputTokens: execution.InputTokens,
		OutputTokens: execution.OutputTokens, TotalTokens: execution.TotalTokens, TemporaryKeyName: keyName,
		TemporaryKeyStatus: cleanupStatus, CleanupError: cleanupMessage, TestedAt: testedAt,
	}
}

func quickTestMessage(execution probeExecution) string {
	if execution.Status == "success" {
		return "请求成功，当前上游分组可以使用"
	}
	messages := map[string]string{
		"auth_invalid":              "临时测试 Key 无法通过上游认证",
		"permission_denied":         "当前分组或测试模型没有调用权限",
		"balance_exhausted":         "上游账号余额或额度不足",
		"rate_limited":              "上游可以连接，但当前触发了限流",
		"model_incompatible":        "当前分组不支持所选测试模型",
		"output_limit_incompatible": "上游不接受当前最小输出参数",
		"server_error":              "上游服务返回服务器错误",
		"timeout":                   "连接上游或等待响应超时",
		"connection_error":          "无法连接上游服务",
		"empty_response":            "上游返回了空响应",
		"response_read":             "读取上游响应失败",
	}
	if message := messages[execution.ErrorClass]; message != "" {
		return message
	}
	if message := sanitizeProbeSummary(execution.Message); message != "" {
		return message
	}
	return "快速测试失败"
}
