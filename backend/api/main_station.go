package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

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
			fail(c, http.StatusBadRequest, err)
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
			fail(c, http.StatusBadRequest, err)
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
				fail(c, http.StatusBadRequest, err)
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
			fail(c, http.StatusBadRequest, err)
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
	group.PUT("/groups/:id/settings", func(c *gin.Context) {
		groupID, ok := mainStationUintParam(c, "id")
		if !ok {
			return
		}
		var in mainstation.GroupSettingsInput
		if err := c.ShouldBindJSON(&in); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		item, err := d.MainStation.UpdateGroupSettings(groupID, in)
		if err != nil {
			failMainStation(c, err)
			return
		}
		c.JSON(http.StatusOK, item)
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
			fail(c, http.StatusBadRequest, err)
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
			fail(c, http.StatusBadRequest, err)
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
			fail(c, http.StatusBadRequest, err)
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
			fail(c, http.StatusBadRequest, err)
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
			fail(c, http.StatusBadRequest, err)
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
			fail(c, http.StatusBadRequest, err)
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
		fail(c, http.StatusBadRequest, errors.New("invalid "+name))
		return 0, false
	}
	return uint(value), true
}

func mainStationInt64Param(c *gin.Context, name string) (int64, bool) {
	value, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || value <= 0 {
		fail(c, http.StatusBadRequest, errors.New("invalid "+name))
		return 0, false
	}
	return value, true
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
	case errors.Is(err, mainstation.ErrAlreadyConfigured), errors.Is(err, mainstation.ErrBindingConflict):
		status = http.StatusConflict
	case isMainStationValidationError(err):
		status = http.StatusBadRequest
	}
	fail(c, status, err)
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
