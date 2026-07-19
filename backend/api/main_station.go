package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/fausto2022/relaydeck/backend/mainstation"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func registerMainStation(g *gin.RouterGroup, d *Deps) {
	if d.MainStation == nil {
		return
	}
	group := g.Group("/main-station")
	group.GET("", func(c *gin.Context) {
		item, err := d.MainStation.GetConfig()
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, item)
	})
	group.POST("", func(c *gin.Context) {
		var in mainstation.ConfigInput
		if err := c.ShouldBindJSON(&in); err != nil {
			failMainStationRequest(c)
			return
		}
		item, err := d.MainStation.CreateConfig(c.Request.Context(), in)
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusCreated, item)
	})
	group.PUT("", func(c *gin.Context) {
		var in mainstation.ConfigInput
		if err := c.ShouldBindJSON(&in); err != nil {
			failMainStationRequest(c)
			return
		}
		item, err := d.MainStation.UpdateConfig(c.Request.Context(), in)
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, item)
	})
	group.POST("/test", func(c *gin.Context) {
		var in *mainstation.ConfigInput
		if c.Request.ContentLength != 0 {
			var body mainstation.ConfigInput
			if err := c.ShouldBindJSON(&body); err != nil {
				failMainStationRequest(c)
				return
			}
			in = &body
		}
		if err := d.MainStation.TestConnection(c.Request.Context(), in); err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	group.POST("/sync", func(c *gin.Context) {
		result, err := d.MainStation.Sync(c.Request.Context())
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.GET("/health-models", func(c *gin.Context) {
		result, err := d.MainStation.ListHealthModelCatalogs(c.Request.Context())
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.GET("/protection-preview", func(c *gin.Context) {
		result, err := d.MainStation.ProtectionPreview()
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.PUT("/protection", func(c *gin.Context) {
		var in mainstation.ProtectionPolicyInput
		if err := c.ShouldBindJSON(&in); err != nil {
			failMainStationRequest(c)
			return
		}
		result, err := d.MainStation.UpdateProtectionPolicy(c.Request.Context(), in)
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.GET("/audit-logs", func(c *gin.Context) {
		poolID := uint(0)
		if groupID := uint(queryIntDefault(c, "group_id", 0)); groupID != 0 {
			var err error
			poolID, err = d.MainStation.GroupPoolID(groupID)
			if err != nil {
				failMainStation(c, err)
				return
			}
		}
		result, err := d.MainStation.ListAuditLogs(
			poolID,
			uint(queryIntDefault(c, "member_id", 0)),
			queryIntDefault(c, "page", 1),
			queryIntDefault(c, "page_size", 20),
		)
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.GET("/groups", func(c *gin.Context) {
		items, err := d.MainStation.ListGroupWorkspaces(queryBool(c, "include_missing"))
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	})
	group.GET("/accounts", func(c *gin.Context) {
		items, err := d.MainStation.ListAccounts(
			queryIntDefault(c, "page", 1),
			queryIntDefault(c, "page_size", 20),
			queryBool(c, "include_missing"),
			queryBool(c, "unbound_only"),
		)
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, items)
	})
	group.GET("/groups/:id/accounts", func(c *gin.Context) {
		groupID, ok := mainStationUintParam(c, "id")
		if !ok {
			return
		}
		items, err := d.MainStation.ListGroupAccounts(groupID, queryBool(c, "include_missing"))
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	})
	group.GET("/groups/:id/binding-recommendations", func(c *gin.Context) {
		groupID, ok := mainStationUintParam(c, "id")
		if !ok {
			return
		}
		result, err := d.MainStation.RecommendBindings(c.Request.Context(), groupID)
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.POST("/groups/:id/accounts/bind-batch", func(c *gin.Context) {
		groupID, ok := mainStationUintParam(c, "id")
		if !ok {
			return
		}
		var in mainstation.BindingBatchInput
		if err := c.ShouldBindJSON(&in); err != nil {
			failMainStationRequest(c)
			return
		}
		result, err := d.MainStation.BindMembersBatch(c.Request.Context(), groupID, in)
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.PUT("/groups/:id/settings", func(c *gin.Context) {
		groupID, ok := mainStationUintParam(c, "id")
		if !ok {
			return
		}
		var in mainstation.GroupSettingsInput
		if err := c.ShouldBindJSON(&in); err != nil {
			failMainStationRequest(c)
			return
		}
		item, err := d.MainStation.UpdateGroupSettings(c.Request.Context(), groupID, in)
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, item)
	})
	group.POST("/groups/:id/recalculate-ranking", func(c *gin.Context) {
		groupID, ok := mainStationUintParam(c, "id")
		if !ok {
			return
		}
		if err := d.MainStation.RecalculateGroupRanking(c.Request.Context(), groupID); err != nil {
			failMainStation(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	group.POST("/groups/:id/accounts", func(c *gin.Context) {
		groupID, ok := mainStationUintParam(c, "id")
		if !ok {
			return
		}
		poolID, err := d.MainStation.GroupPoolID(groupID)
		if err != nil {
			failMainStation(c, err)
			return
		}
		var in mainstation.MemberInput
		if err := c.ShouldBindJSON(&in); err != nil {
			failMainStationRequest(c)
			return
		}
		item, err := d.MainStation.CreateMember(c.Request.Context(), poolID, in)
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusCreated, item)
	})
	group.PUT("/groups/:id/accounts/:member_id", func(c *gin.Context) {
		groupID, ok := mainStationUintParam(c, "id")
		if !ok {
			return
		}
		memberID, ok := mainStationUintParam(c, "member_id")
		if !ok {
			return
		}
		poolID, err := d.MainStation.GroupPoolID(groupID)
		if err != nil {
			failMainStation(c, err)
			return
		}
		var in mainstation.MemberInput
		if err := c.ShouldBindJSON(&in); err != nil {
			failMainStationRequest(c)
			return
		}
		item, err := d.MainStation.UpdateMember(c.Request.Context(), poolID, memberID, in)
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, item)
	})
	group.POST("/groups/:id/accounts/:member_id/sync", func(c *gin.Context) {
		groupID, ok := mainStationUintParam(c, "id")
		if !ok {
			return
		}
		memberID, ok := mainStationUintParam(c, "member_id")
		if !ok {
			return
		}
		poolID, err := d.MainStation.GroupPoolID(groupID)
		if err != nil {
			failMainStation(c, err)
			return
		}
		item, err := d.MainStation.SyncMember(c.Request.Context(), poolID, memberID)
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, item)
	})
	group.POST("/groups/:id/accounts/:member_id/check", func(c *gin.Context) {
		groupID, ok := mainStationUintParam(c, "id")
		if !ok {
			return
		}
		memberID, ok := mainStationUintParam(c, "member_id")
		if !ok {
			return
		}
		poolID, err := d.MainStation.GroupPoolID(groupID)
		if err != nil {
			failMainStation(c, err)
			return
		}
		var in mainstation.HealthCheckInput
		if err := c.ShouldBindJSON(&in); err != nil {
			failMainStationRequest(c)
			return
		}
		result, err := d.MainStation.CheckMember(c.Request.Context(), poolID, memberID, in)
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.DELETE("/groups/:id/accounts/:member_id", func(c *gin.Context) {
		groupID, ok := mainStationUintParam(c, "id")
		if !ok {
			return
		}
		memberID, ok := mainStationUintParam(c, "member_id")
		if !ok {
			return
		}
		poolID, err := d.MainStation.GroupPoolID(groupID)
		if err != nil {
			failMainStation(c, err)
			return
		}
		var in mainstation.DeleteMemberInput
		if err := c.ShouldBindJSON(&in); err != nil {
			failMainStationRequest(c)
			return
		}
		if err := d.MainStation.DeleteMember(c.Request.Context(), poolID, memberID, in); err != nil {
			failMainStation(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	group.POST("/groups/:id/check", func(c *gin.Context) {
		groupID, ok := mainStationUintParam(c, "id")
		if !ok {
			return
		}
		poolID, err := d.MainStation.GroupPoolID(groupID)
		if err != nil {
			failMainStation(c, err)
			return
		}
		var in mainstation.HealthCheckInput
		if err := c.ShouldBindJSON(&in); err != nil {
			failMainStationRequest(c)
			return
		}
		result, err := d.MainStation.BulkCheckPool(c.Request.Context(), poolID, in.Level, in.Force)
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.POST("/groups/:id/evaluate", func(c *gin.Context) {
		groupID, ok := mainStationUintParam(c, "id")
		if !ok {
			return
		}
		poolID, err := d.MainStation.GroupPoolID(groupID)
		if err != nil {
			failMainStation(c, err)
			return
		}
		result, err := d.MainStation.EvaluatePool(c.Request.Context(), poolID, "manual")
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.GET("/groups/:id/capacity", func(c *gin.Context) {
		groupID, ok := mainStationUintParam(c, "id")
		if !ok {
			return
		}
		poolID, err := d.MainStation.GroupPoolID(groupID)
		if err != nil {
			failMainStation(c, err)
			return
		}
		result, err := d.MainStation.EvaluatePoolCapacity(c.Request.Context(), poolID)
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.GET("/groups/:id/health-checks", func(c *gin.Context) {
		groupID, ok := mainStationUintParam(c, "id")
		if !ok {
			return
		}
		poolID, err := d.MainStation.GroupPoolID(groupID)
		if err != nil {
			failMainStation(c, err)
			return
		}
		result, err := d.MainStation.ListHealthChecks(poolID, uint(queryIntDefault(c, "member_id", 0)), c.Query("level"), queryIntDefault(c, "page", 1), queryIntDefault(c, "page_size", 20))
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.GET("/groups/:id/health-summary", func(c *gin.Context) {
		groupID, ok := mainStationUintParam(c, "id")
		if !ok {
			return
		}
		poolID, err := d.MainStation.GroupPoolID(groupID)
		if err != nil {
			failMainStation(c, err)
			return
		}
		result, err := d.MainStation.PoolHealthSummary(poolID)
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": result})
	})
	group.GET("/groups/:id/profit-checks", func(c *gin.Context) {
		groupID, ok := mainStationUintParam(c, "id")
		if !ok {
			return
		}
		poolID, err := d.MainStation.GroupPoolID(groupID)
		if err != nil {
			failMainStation(c, err)
			return
		}
		result, err := d.MainStation.ListProfitChecks(poolID, uint(queryIntDefault(c, "member_id", 0)), groupID, queryIntDefault(c, "page", 1), queryIntDefault(c, "page_size", 20))
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.GET("/accounts/:account_id/locks", func(c *gin.Context) {
		accountID, ok := mainStationInt64Param(c, "account_id")
		if !ok {
			return
		}
		items, err := d.MainStation.ListGuardLocks(accountID)
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	})
	group.POST("/accounts/:account_id/locks/:type", func(c *gin.Context) {
		accountID, ok := mainStationInt64Param(c, "account_id")
		if !ok {
			return
		}
		var in mainstation.GuardLockInput
		if err := c.ShouldBindJSON(&in); err != nil {
			failMainStationRequest(c)
			return
		}
		item, err := d.MainStation.ActivateGuardLock(c.Request.Context(), accountID, c.Param("type"), in.Reason, in.Evidence, "admin")
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, item)
	})
	group.POST("/accounts/:account_id/locks/:type/clear", func(c *gin.Context) {
		accountID, ok := mainStationInt64Param(c, "account_id")
		if !ok {
			return
		}
		result, err := d.MainStation.ClearGuardLock(c.Request.Context(), accountID, c.Param("type"), "admin")
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.POST("/accounts/:account_id/locks/automatic/clear", func(c *gin.Context) {
		accountID, ok := mainStationInt64Param(c, "account_id")
		if !ok {
			return
		}
		result, err := d.MainStation.ClearAutomaticLocks(c.Request.Context(), accountID, "admin")
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.POST("/accounts/:account_id/reconcile", func(c *gin.Context) {
		accountID, ok := mainStationInt64Param(c, "account_id")
		if !ok {
			return
		}
		result, err := d.MainStation.ReconcileAccount(c.Request.Context(), accountID, "admin")
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
}

func mainStationUintParam(c *gin.Context, name string) (uint, bool) {
	value, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || value == 0 {
		fail(c, http.StatusBadRequest, errors.New("请求路径参数不正确"))
		return 0, false
	}
	return uint(value), true
}

func mainStationInt64Param(c *gin.Context, name string) (int64, bool) {
	value, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || value <= 0 {
		fail(c, http.StatusBadRequest, errors.New("请求路径参数不正确"))
		return 0, false
	}
	return value, true
}

func failMainStationRequest(c *gin.Context) {
	fail(c, http.StatusBadRequest, errors.New("请求参数格式不正确"))
}

func queryIntDefault(c *gin.Context, name string, fallback int) int {
	value, err := strconv.Atoi(c.Query(name))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func queryBool(c *gin.Context, name string) bool {
	value, _ := strconv.ParseBool(c.Query(name))
	return value
}

func failMainStation(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, mainstation.ErrNotConfigured):
		status = http.StatusNotFound
	case errors.Is(err, mainstation.ErrAlreadyConfigured), errors.Is(err, mainstation.ErrBindingConflict), errors.Is(err, mainstation.ErrManagedAccountNameConflict):
		status = http.StatusConflict
	case isMainStationValidationError(err):
		status = http.StatusBadRequest
	}
	body := gin.H{"error": mainStationErrorMessage(err)}
	if errors.Is(err, mainstation.ErrManagedAccountNameConflict) {
		body["code"] = "managed_account_name_conflict"
	}
	c.JSON(status, body)
}

var mainStationHTTPStatusPattern = regexp.MustCompile(`(?i)(?:status|http)\s+(\d{3})`)

func mainStationErrorMessage(err error) string {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return "未找到对应的主站数据"
	case errors.Is(err, mainstation.ErrNotConfigured):
		return mainstation.ErrNotConfigured.Error()
	case errors.Is(err, mainstation.ErrAlreadyConfigured):
		return mainstation.ErrAlreadyConfigured.Error()
	case errors.Is(err, mainstation.ErrBindingConflict):
		return mainstation.ErrBindingConflict.Error()
	case errors.Is(err, mainstation.ErrManagedAccountNameConflict):
		return "主站已存在同名账号，确认后可以继续新建，原账号不会被覆盖"
	}
	text := strings.TrimSpace(err.Error())
	if match := mainStationHTTPStatusPattern.FindStringSubmatch(text); len(match) == 2 {
		status, _ := strconv.Atoi(match[1])
		switch status {
		case http.StatusBadRequest:
			return "主站接口拒绝了请求，请检查账号配置"
		case http.StatusUnauthorized:
			return "主站接口鉴权失败，请检查管理员 API Key"
		case http.StatusForbidden:
			return "主站接口没有执行该操作的权限"
		case http.StatusNotFound:
			return "主站中的目标账号或资源不存在"
		case http.StatusConflict:
			return "主站数据发生冲突，请刷新后重试"
		case http.StatusTooManyRequests:
			return "主站接口请求过于频繁，请稍后重试"
		default:
			if status >= 500 {
				return fmt.Sprintf("主站服务暂时不可用（HTTP %d）", status)
			}
			return fmt.Sprintf("主站接口请求失败（HTTP %d）", status)
		}
	}
	translations := []struct {
		keyword string
		message string
	}{
		{"the same member health check is already running", "该账号正在探测中，请稍后再试"},
		{"source account concurrency is unavailable", "无法获取上游账号最高并发，请手动填写"},
		{"sync managed account models from upstream returned no models", "上游没有返回任何可用模型"},
		{"account pool has no main station groups", "当前账号池没有关联主站分组"},
		{"selected source group no longer exists", "选择的上游套餐已不存在，请重新选择"},
		{"remote account is missing", "主站账号已不存在，请刷新列表"},
		{"member binding is invalid", "账号绑定关系无效，请重新接管"},
		{"检查主站同名账号失败", "检查主站同名账号失败，请稍后重试"},
	}
	for _, item := range translations {
		if strings.Contains(strings.ToLower(text), item.keyword) {
			return item.message
		}
	}
	if strings.IndexFunc(text, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
		return text
	}
	return "主站操作失败，请稍后重试"
}

func isMainStationValidationError(err error) bool {
	text := strings.ToLower(err.Error())
	for _, token := range []string{
		"required", "must ", "invalid", "confirmation", "does not belong", "still has", "cannot ",
		"missing", "no main station groups", "no longer exists", "differs from", "not managed", "read-only",
	} {
		if strings.Contains(text, token) {
			return true
		}
	}
	return false
}
