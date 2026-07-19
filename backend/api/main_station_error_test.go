package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fausto2022/relaydeck/backend/mainstation"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestMainStationErrorMessageUsesChinese(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "not configured", err: mainstation.ErrNotConfigured, want: "尚未配置主站"},
		{name: "not found", err: gorm.ErrRecordNotFound, want: "未找到对应的主站数据"},
		{name: "unauthorized", err: errors.New("request failed with HTTP 401"), want: "主站接口鉴权失败，请检查管理员 API Key"},
		{name: "server error", err: errors.New("request failed with status 503"), want: "主站服务暂时不可用（HTTP 503）"},
		{name: "unknown english", err: errors.New("opaque upstream failure"), want: "主站操作失败，请稍后重试"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mainStationErrorMessage(tt.err); got != tt.want {
				t.Fatalf("mainStationErrorMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFailMainStationReturnsDuplicateNameConfirmationCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)

	failMainStation(context, fmt.Errorf("%w：OpenAI-01", mainstation.ErrManagedAccountNameConflict))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	var body struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != "managed_account_name_conflict" || body.Error == "" || !strings.Contains(body.Error, "确认后可以继续新建") {
		t.Fatalf("response = %#v", body)
	}
}
