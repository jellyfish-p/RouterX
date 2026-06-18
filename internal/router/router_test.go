package router

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/handler"
	"routerx/internal/model"
	"routerx/internal/relay"
	"routerx/internal/service"
)

func TestP0BackendFlow(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	status := performJSON(r, http.MethodGet, "/v0/setup/status", "", nil)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"initialized":false`) {
		t.Fatalf("expected uninitialized setup status, got %d %s", status.Code, status.Body.String())
	}
	uninitializedV1 := performJSON(r, http.MethodGet, "/v1/models", "Bearer sk-anything", nil)
	if uninitializedV1.Code != http.StatusServiceUnavailable || strings.Contains(uninitializedV1.Body.String(), `"success"`) {
		t.Fatalf("expected /v1 uninitialized OpenAI error, got %d %s", uninitializedV1.Code, uninitializedV1.Body.String())
	}

	initBody := map[string]interface{}{
		"username":     "admin",
		"password":     "password123",
		"display_name": "Admin",
	}
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", initBody)
	if initResp.Code != http.StatusOK || !strings.Contains(initResp.Body.String(), `"success":true`) {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	reinitResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", initBody)
	if reinitResp.Code != http.StatusConflict {
		t.Fatalf("expected re-init conflict, got %d %s", reinitResp.Code, reinitResp.Body.String())
	}

	unauthAdmin := performJSON(r, http.MethodGet, "/v0/admin/user", "", nil)
	if unauthAdmin.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauth admin 401, got %d %s", unauthAdmin.Code, unauthAdmin.Body.String())
	}

	loginResp := performJSON(r, http.MethodPost, "/v0/user/login", "", map[string]interface{}{
		"account":  "admin",
		"password": "password123",
	})
	var loginPayload struct {
		Success bool `json:"success"`
		Data    struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(loginResp.Body.Bytes(), &loginPayload); err != nil {
		t.Fatal(err)
	}
	if loginResp.Code != http.StatusOK || !loginPayload.Success || loginPayload.Data.Token == "" {
		t.Fatalf("login failed: %d %s", loginResp.Code, loginResp.Body.String())
	}
	userJWT := "Bearer " + loginPayload.Data.Token
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "admin").Update("quota", int64(100000)).Error; err != nil {
		t.Fatal(err)
	}

	adminList := performJSON(r, http.MethodGet, "/v0/admin/user", userJWT, nil)
	if adminList.Code != http.StatusOK || !strings.Contains(adminList.Body.String(), `"success":true`) {
		t.Fatalf("expected admin list success, got %d %s", adminList.Code, adminList.Body.String())
	}
	adminLoginRoute := performJSON(r, http.MethodPost, "/v0/admin/login", "", map[string]interface{}{
		"account":  "admin",
		"password": "password123",
	})
	if adminLoginRoute.Code != http.StatusNotFound {
		t.Fatalf("admin login route should not exist, got %d %s", adminLoginRoute.Code, adminLoginRoute.Body.String())
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", userJWT, map[string]interface{}{
		"name":      "sdk",
		"unlimited": true,
	})
	var tokenPayload struct {
		Success bool `json:"success"`
		Data    struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || !strings.HasPrefix(tokenPayload.Data.Key, "sk-") {
		t.Fatalf("expected api key create response, got %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.Key == tokenPayload.Data.Key || storedToken.Key != common.SHA256Hex(tokenPayload.Data.Key) {
		t.Fatalf("api key should be stored as sha256 hash")
	}
	quotaEdit := performJSON(r, http.MethodPut, "/v0/user/token/"+uintString(storedToken.ID), userJWT, map[string]interface{}{
		"remain_quota": 1000000,
		"unlimited":    true,
	})
	if quotaEdit.Code != http.StatusForbidden {
		t.Fatalf("token quota should not be editable through user API, got %d %s", quotaEdit.Code, quotaEdit.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", userJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "compat",
		"models":   "gpt-test",
		"base_url": "http://127.0.0.1",
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK || strings.Contains(channelResp.Body.String(), "upstream-secret") {
		t.Fatalf("channel response failed or leaked key: %d %s", channelResp.Code, channelResp.Body.String())
	}
	var storedChannel model.Channel
	if err := internal.DB.First(&storedChannel).Error; err != nil {
		t.Fatal(err)
	}
	if storedChannel.APIKey == "upstream-secret" || !strings.HasPrefix(storedChannel.APIKey, "enc:v1:") {
		t.Fatalf("channel api key should be encrypted when ENCRYPTION_KEY is set")
	}

	invalidModels := performJSON(r, http.MethodGet, "/v1/models", "Bearer sk-invalid", nil)
	if invalidModels.Code != http.StatusUnauthorized {
		t.Fatalf("expected invalid api key 401, got %d %s", invalidModels.Code, invalidModels.Body.String())
	}
	validModels := performJSON(r, http.MethodGet, "/v1/models", "Bearer "+tokenPayload.Data.Key, nil)
	if validModels.Code != http.StatusOK || !strings.Contains(validModels.Body.String(), "gpt-test") {
		t.Fatalf("expected valid api key model list, got %d %s", validModels.Code, validModels.Body.String())
	}
	if strings.Contains(validModels.Body.String(), "upstream-secret") {
		t.Fatalf("model list leaked upstream secret: %s", validModels.Body.String())
	}
	emptyTokenResp := performJSON(r, http.MethodPost, "/v0/user/token", userJWT, map[string]interface{}{
		"name": "empty",
	})
	var emptyTokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(emptyTokenResp.Body.Bytes(), &emptyTokenPayload); err != nil {
		t.Fatal(err)
	}
	if emptyTokenResp.Code != http.StatusOK || emptyTokenPayload.Data.Key == "" {
		t.Fatalf("expected empty api key response, got %d %s", emptyTokenResp.Code, emptyTokenResp.Body.String())
	}
	exhaustedModels := performJSON(r, http.MethodGet, "/v1/models", "Bearer "+emptyTokenPayload.Data.Key, nil)
	if exhaustedModels.Code != http.StatusTooManyRequests || strings.Contains(exhaustedModels.Body.String(), `"success"`) {
		t.Fatalf("expected exhausted api key 429 with OpenAI error, got %d %s", exhaustedModels.Code, exhaustedModels.Body.String())
	}
	var exhaustedLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND error_msg LIKE ?", common.LogStatusFailed, emptyTokenPayload.Data.ID, "%insufficient quota%").First(&exhaustedLog).Error; err != nil {
		t.Fatal(err)
	}
	var exhaustedPolicySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(exhaustedLog.PolicySnapshot), &exhaustedPolicySnapshot); err != nil {
		t.Fatalf("exhausted api key should store policy snapshot JSON, got %q: %v", exhaustedLog.PolicySnapshot, err)
	}
	exhaustedScopeResult, ok := exhaustedPolicySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		exhaustedPolicySnapshot["kind"] != "policy" ||
		exhaustedPolicySnapshot["access_decision"] != "deny" ||
		exhaustedPolicySnapshot["reject_code"] != "insufficient_quota" ||
		exhaustedPolicySnapshot["quota_precheck"] != "unavailable" ||
		exhaustedScopeResult["quota"] != "deny" {
		t.Fatalf("unexpected exhausted api key policy snapshot: %+v", exhaustedPolicySnapshot)
	}
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "admin").Update("status", common.UserStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	disabledUserModels := performJSON(r, http.MethodGet, "/v1/models", "Bearer "+tokenPayload.Data.Key, nil)
	if disabledUserModels.Code != http.StatusForbidden || strings.Contains(disabledUserModels.Body.String(), `"success"`) {
		t.Fatalf("expected disabled user api key 403 with OpenAI error, got %d %s", disabledUserModels.Code, disabledUserModels.Body.String())
	}
}

func TestModelListSupportsRouterXProtocolSelector(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "models-protocol",
		"remain_quota": 10,
	})
	var tokenPayload struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "models-protocol-channel",
		"models":   "gpt-protocol",
		"base_url": "http://127.0.0.1",
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	geminiResp := performJSON(r, http.MethodGet, "/v1/models?routerx_protocol=gemini", "Bearer "+tokenPayload.Data.Key, nil)
	if geminiResp.Code != http.StatusOK || !strings.Contains(geminiResp.Body.String(), `"models"`) || !strings.Contains(geminiResp.Body.String(), `"name":"models/gpt-protocol"`) {
		t.Fatalf("routerx_protocol=gemini should return Gemini model shape, got %d %s", geminiResp.Code, geminiResp.Body.String())
	}
	anthropicResp := performRawWithHeaders(r, http.MethodGet, "/v1/models", "Bearer "+tokenPayload.Data.Key, "", map[string]string{
		"X-RouterX-Protocol": "anthropic",
	})
	if anthropicResp.Code != http.StatusOK || !strings.Contains(anthropicResp.Body.String(), `"has_more":false`) || !strings.Contains(anthropicResp.Body.String(), `"type":"model"`) {
		t.Fatalf("X-RouterX-Protocol=anthropic should return Anthropic model shape, got %d %s", anthropicResp.Code, anthropicResp.Body.String())
	}
	precedenceResp := performRawWithHeaders(r, http.MethodGet, "/v1/models?format=gemini&routerx_protocol=anthropic", "Bearer "+tokenPayload.Data.Key, "", map[string]string{
		"X-RouterX-Protocol": "openai",
	})
	if precedenceResp.Code != http.StatusOK || !strings.Contains(precedenceResp.Body.String(), `"name":"models/gpt-protocol"`) {
		t.Fatalf("format should keep precedence over routerx protocol selectors, got %d %s", precedenceResp.Code, precedenceResp.Body.String())
	}
	openAIResp := performJSON(r, http.MethodGet, "/v1/models?routerx_protocol=openai", "Bearer "+tokenPayload.Data.Key, nil)
	if openAIResp.Code != http.StatusOK || !strings.Contains(openAIResp.Body.String(), `"object":"list"`) || !strings.Contains(openAIResp.Body.String(), `"id":"gpt-protocol"`) {
		t.Fatalf("routerx_protocol=openai should return OpenAI model shape, got %d %s", openAIResp.Code, openAIResp.Body.String())
	}
	invalidGeminiResp := performJSON(r, http.MethodGet, "/v1/models?routerx_protocol=gemini", "Bearer sk-invalid-models-protocol", nil)
	if invalidGeminiResp.Code != http.StatusUnauthorized || !strings.Contains(invalidGeminiResp.Body.String(), `"status":"UNAUTHENTICATED"`) {
		t.Fatalf("routerx_protocol=gemini should return Gemini auth error shape, got %d %s", invalidGeminiResp.Code, invalidGeminiResp.Body.String())
	}
	invalidAnthropicResp := performRawWithHeaders(r, http.MethodGet, "/v1/models", "Bearer sk-invalid-models-protocol", "", map[string]string{
		"X-RouterX-Protocol": "anthropic",
	})
	if invalidAnthropicResp.Code != http.StatusUnauthorized || !strings.Contains(invalidAnthropicResp.Body.String(), `"type":"error"`) || !strings.Contains(invalidAnthropicResp.Body.String(), `"type":"authentication_error"`) {
		t.Fatalf("X-RouterX-Protocol=anthropic should return Anthropic auth error shape, got %d %s", invalidAnthropicResp.Code, invalidAnthropicResp.Body.String())
	}
}

func TestUserAPIKeyManagementAuditLogs(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	createResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "audit-key",
		"remain_quota": int64(100),
	})
	var payload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createResp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if createResp.Code != http.StatusOK || payload.Data.ID == 0 || !strings.HasPrefix(payload.Data.Key, "sk-") {
		t.Fatalf("create api key failed: %d %s", createResp.Code, createResp.Body.String())
	}

	expiredAt := time.Now().Add(24 * time.Hour).Unix()
	updateResp := performJSON(r, http.MethodPut, "/v0/user/token/"+uintString(payload.Data.ID), rootJWT, map[string]interface{}{
		"name":       "audit-key-updated",
		"expired_at": expiredAt,
	})
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update api key failed: %d %s", updateResp.Code, updateResp.Body.String())
	}
	quotaResp := performJSON(r, http.MethodPut, "/v0/user/token/"+uintString(payload.Data.ID), rootJWT, map[string]interface{}{
		"remain_quota": int64(999),
		"unlimited":    true,
	})
	if quotaResp.Code != http.StatusForbidden {
		t.Fatalf("api key quota edit should be forbidden, got %d %s", quotaResp.Code, quotaResp.Body.String())
	}
	disableResp := performJSON(r, http.MethodPut, "/v0/user/token/"+uintString(payload.Data.ID), rootJWT, map[string]interface{}{
		"status": common.TokenStatusDisabled,
	})
	if disableResp.Code != http.StatusOK {
		t.Fatalf("disable api key failed: %d %s", disableResp.Code, disableResp.Body.String())
	}
	deleteResp := performJSON(r, http.MethodDelete, "/v0/user/token/"+uintString(payload.Data.ID), rootJWT, nil)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete api key failed: %d %s", deleteResp.Code, deleteResp.Body.String())
	}

	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=api_key&resource_id="+uintString(payload.Data.ID), rootJWT, nil)
	body := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(body, `"action":"api_key.created"`) ||
		!strings.Contains(body, `"action":"api_key.updated"`) ||
		!strings.Contains(body, `"action":"api_key.quota_limit_denied"`) ||
		!strings.Contains(body, `"action":"api_key.disabled"`) ||
		!strings.Contains(body, `"action":"api_key.deleted"`) {
		t.Fatalf("api key management should write audit logs, got %d %s", auditResp.Code, body)
	}
	if strings.Contains(body, payload.Data.Key) || strings.Contains(body, "sk-") {
		t.Fatalf("api key audit should not expose plaintext keys: %s", body)
	}
	deniedAuditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=api_key&resource_id="+uintString(payload.Data.ID)+"&result=denied&error_code=api_key_quota_edit_forbidden", rootJWT, nil)
	deniedBody := deniedAuditResp.Body.String()
	if deniedAuditResp.Code != http.StatusOK ||
		!strings.Contains(deniedBody, `"action":"api_key.quota_limit_denied"`) ||
		strings.Contains(deniedBody, `"action":"api_key.created"`) {
		t.Fatalf("api key denied audit filters should only return denied quota edits, got %d %s", deniedAuditResp.Code, deniedBody)
	}
	futureStart := time.Now().Add(24 * time.Hour).Unix()
	futureEnd := time.Now().Add(48 * time.Hour).Unix()
	futureAuditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=api_key&resource_id="+uintString(payload.Data.ID)+"&start_time="+strconv.FormatInt(futureStart, 10)+"&end_time="+strconv.FormatInt(futureEnd, 10), rootJWT, nil)
	futureBody := futureAuditResp.Body.String()
	if futureAuditResp.Code != http.StatusOK || strings.Contains(futureBody, `"action":"api_key.created"`) {
		t.Fatalf("api key audit time filters should exclude records outside the range, got %d %s", futureAuditResp.Code, futureBody)
	}
}

func TestUserAPIKeyAdvancedManagement(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	createResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "advanced-key",
		"remain_quota": int64(100),
	})
	var createPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createResp.Body.Bytes(), &createPayload); err != nil {
		t.Fatal(err)
	}
	if createResp.Code != http.StatusOK || createPayload.Data.ID == 0 || !strings.HasPrefix(createPayload.Data.Key, "sk-") {
		t.Fatalf("create api key failed: %d %s", createResp.Code, createResp.Body.String())
	}

	tokenID := createPayload.Data.ID
	now := time.Now()
	logs := []model.Log{
		{
			UserID:           root.ID,
			TokenID:          &tokenID,
			Model:            "gpt-success",
			PromptTokens:     3,
			CompletionTokens: 4,
			TotalTokens:      7,
			QuotaUsed:        7,
			Status:           common.LogStatusSuccess,
			CreatedAt:        now.Add(-time.Minute),
		},
		{
			UserID:      root.ID,
			TokenID:     &tokenID,
			Model:       "gpt-failed",
			TotalTokens: 2,
			Status:      common.LogStatusFailed,
			ErrorMsg:    "upstream timeout",
			CreatedAt:   now,
		},
	}
	if err := internal.DB.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}

	usageResp := performJSON(r, http.MethodGet, "/v0/user/token/"+uintString(tokenID)+"/usage", rootJWT, nil)
	usageBody := usageResp.Body.String()
	if usageResp.Code != http.StatusOK ||
		!strings.Contains(usageBody, `"call_count":2`) ||
		!strings.Contains(usageBody, `"success_count":1`) ||
		!strings.Contains(usageBody, `"error_count":1`) ||
		!strings.Contains(usageBody, `"total_quota":7`) ||
		!strings.Contains(usageBody, `"total_tokens":9`) ||
		!strings.Contains(usageBody, `"last_model":"gpt-failed"`) {
		t.Fatalf("api key usage summary mismatch: %d %s", usageResp.Code, usageBody)
	}

	rotateResp := performJSON(r, http.MethodPost, "/v0/user/token/"+uintString(tokenID)+"/rotate", rootJWT, nil)
	var rotatePayload struct {
		Data struct {
			ID            uint   `json:"id"`
			Key           string `json:"key"`
			RotatedFromID *uint  `json:"rotated_from_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rotateResp.Body.Bytes(), &rotatePayload); err != nil {
		t.Fatal(err)
	}
	if rotateResp.Code != http.StatusOK ||
		rotatePayload.Data.ID == 0 ||
		rotatePayload.Data.ID == tokenID ||
		!strings.HasPrefix(rotatePayload.Data.Key, "sk-") ||
		rotatePayload.Data.RotatedFromID == nil ||
		*rotatePayload.Data.RotatedFromID != tokenID {
		t.Fatalf("api key rotate response mismatch: %d %s", rotateResp.Code, rotateResp.Body.String())
	}
	if strings.Contains(rotateResp.Body.String(), createPayload.Data.Key) {
		t.Fatalf("rotate response leaked old plaintext key: %s", rotateResp.Body.String())
	}
	var oldRow struct {
		Status        int
		RevokedReason string
	}
	if err := internal.DB.Table("tokens").Select("status, revoked_reason").Where("id = ?", tokenID).Scan(&oldRow).Error; err != nil {
		t.Fatal(err)
	}
	if oldRow.Status != common.TokenStatusDisabled || oldRow.RevokedReason != "rotated" {
		t.Fatalf("old rotated key should be disabled with reason, got %+v", oldRow)
	}
	var newRow struct {
		Key           string
		RotatedFromID *uint
	}
	if err := internal.DB.Table("tokens").Select("key, rotated_from_id").Where("id = ?", rotatePayload.Data.ID).Scan(&newRow).Error; err != nil {
		t.Fatal(err)
	}
	if newRow.Key == rotatePayload.Data.Key || newRow.Key != common.SHA256Hex(rotatePayload.Data.Key) || newRow.RotatedFromID == nil || *newRow.RotatedFromID != tokenID {
		t.Fatalf("new rotated key should be hashed and linked, got %+v", newRow)
	}

	leakResp := performJSON(r, http.MethodPost, "/v0/user/token/"+uintString(rotatePayload.Data.ID)+"/report-leak", rootJWT, map[string]interface{}{
		"reason": "public_repo",
	})
	leakBody := leakResp.Body.String()
	if leakResp.Code != http.StatusOK || !strings.Contains(leakBody, `"replacement_recommended":true`) || strings.Contains(leakBody, rotatePayload.Data.Key) {
		t.Fatalf("report leak response mismatch or leaked key: %d %s", leakResp.Code, leakBody)
	}
	var leakedRow struct {
		Status        int
		RevokedReason string
	}
	if err := internal.DB.Table("tokens").Select("status, revoked_reason").Where("id = ?", rotatePayload.Data.ID).Scan(&leakedRow).Error; err != nil {
		t.Fatal(err)
	}
	if leakedRow.Status != common.TokenStatusDisabled || leakedRow.RevokedReason != "public_repo" {
		t.Fatalf("leaked key should be disabled with reason, got %+v", leakedRow)
	}

	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=api_key", rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(auditBody, `"action":"api_key.rotated"`) ||
		!strings.Contains(auditBody, `"action":"api_key.leak_reported"`) {
		t.Fatalf("advanced api key actions should write audit logs, got %d %s", auditResp.Code, auditBody)
	}
	if strings.Contains(auditBody, createPayload.Data.Key) || strings.Contains(auditBody, rotatePayload.Data.Key) || strings.Contains(auditBody, "sk-") {
		t.Fatalf("advanced api key audit should not expose plaintext keys: %s", auditBody)
	}
}

func TestAdminAPIKeyQueryAndBatchDisable(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	createAlice := performJSON(r, http.MethodPost, "/v0/admin/user", rootJWT, map[string]interface{}{
		"username":     "alice",
		"password":     "password123",
		"display_name": "Alice",
		"quota":        int64(100),
	})
	if createAlice.Code != http.StatusOK {
		t.Fatalf("create user failed: %d %s", createAlice.Code, createAlice.Body.String())
	}
	var alice model.User
	if err := internal.DB.Where("username = ?", "alice").First(&alice).Error; err != nil {
		t.Fatal(err)
	}

	rootTokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "root-admin-list-key",
		"remain_quota": int64(100),
	})
	var rootTokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rootTokenResp.Body.Bytes(), &rootTokenPayload); err != nil {
		t.Fatal(err)
	}
	aliceToken, err := service.NewTokenService().Create(alice.ID, "alice-batch-key", 100, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	alicePlainKey := aliceToken.Key
	aliceToken.Key = ""

	adminListResp := performJSON(r, http.MethodGet, "/v0/admin/token?user_id="+uintString(alice.ID), rootJWT, nil)
	adminListBody := adminListResp.Body.String()
	if adminListResp.Code != http.StatusOK ||
		!strings.Contains(adminListBody, `"name":"alice-batch-key"`) ||
		strings.Contains(adminListBody, `"name":"root-admin-list-key"`) ||
		strings.Contains(adminListBody, alicePlainKey) ||
		strings.Contains(adminListBody, rootTokenPayload.Data.Key) {
		t.Fatalf("admin token list should filter and avoid plaintext keys, got %d %s", adminListResp.Code, adminListBody)
	}

	noFilterResp := performJSON(r, http.MethodPost, "/v0/admin/token/batch-disable", rootJWT, map[string]interface{}{})
	if noFilterResp.Code != http.StatusBadRequest {
		t.Fatalf("batch disable without filters should be rejected, got %d %s", noFilterResp.Code, noFilterResp.Body.String())
	}
	batchResp := performJSON(r, http.MethodPost, "/v0/admin/token/batch-disable", rootJWT, map[string]interface{}{
		"user_id": alice.ID,
		"reason":  "risk_review",
	})
	batchBody := batchResp.Body.String()
	if batchResp.Code != http.StatusOK || !strings.Contains(batchBody, `"disabled_count":1`) || !strings.Contains(batchBody, `"matched_count":1`) {
		t.Fatalf("batch disable response mismatch: %d %s", batchResp.Code, batchBody)
	}
	var aliceRow struct {
		Status        int
		RevokedReason string
	}
	if err := internal.DB.Table("tokens").Select("status, revoked_reason").Where("id = ?", aliceToken.ID).Scan(&aliceRow).Error; err != nil {
		t.Fatal(err)
	}
	if aliceRow.Status != common.TokenStatusDisabled || aliceRow.RevokedReason != "risk_review" {
		t.Fatalf("alice key should be batch disabled with reason, got %+v", aliceRow)
	}
	var rootRow struct {
		Status int
	}
	if err := internal.DB.Table("tokens").Select("status").Where("id = ?", rootTokenPayload.Data.ID).Scan(&rootRow).Error; err != nil {
		t.Fatal(err)
	}
	if rootRow.Status != common.TokenStatusEnabled {
		t.Fatalf("batch disable should not affect other users, got %+v", rootRow)
	}

	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=api_key&resource_id=batch", rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK || !strings.Contains(auditBody, `"action":"api_key.batch_disabled"`) || strings.Contains(auditBody, alicePlainKey) || strings.Contains(auditBody, "sk-") {
		t.Fatalf("batch disable audit mismatch or leaked key: %d %s", auditResp.Code, auditBody)
	}
}

func TestAdminAPIKeyRiskViewSummarizesRiskyKeys(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	tokenSvc := service.NewTokenService()
	riskyToken, err := tokenSvc.Create(root.ID, "danger-view", 5, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	riskyPlainKey := riskyToken.Key
	riskyTokenID := riskyToken.ID
	safeToken, err := tokenSvc.Create(root.ID, "normal-view", 1000, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	safeTokenID := safeToken.ID
	now := time.Now()
	logs := []model.Log{
		{UserID: root.ID, TokenID: &riskyTokenID, Model: "gpt-risk", Status: common.LogStatusFailed, ErrorCode: "upstream_timeout", ErrorMsg: "upstream timeout", CreatedAt: now.Add(-10 * time.Minute)},
		{UserID: root.ID, TokenID: &riskyTokenID, Model: "gpt-risk", Status: common.LogStatusFailed, ErrorCode: "upstream_500", ErrorMsg: "upstream returned status 500", CreatedAt: now.Add(-5 * time.Minute)},
		{UserID: root.ID, TokenID: &riskyTokenID, Model: "gpt-risk", Status: common.LogStatusSuccess, TotalTokens: 4, QuotaUsed: 4, CreatedAt: now.Add(-time.Minute)},
		{UserID: root.ID, TokenID: &safeTokenID, Model: "gpt-safe", Status: common.LogStatusSuccess, TotalTokens: 2, QuotaUsed: 2, CreatedAt: now.Add(-time.Minute)},
	}
	if err := internal.DB.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}

	riskResp := performJSON(r, http.MethodGet, "/v0/admin/token/risk?window_hours=24&min_error_count=2&low_quota_below=10", rootJWT, nil)
	var payload struct {
		Data struct {
			Total int64 `json:"total"`
			Data  []struct {
				Token struct {
					ID   uint   `json:"id"`
					Name string `json:"name"`
				} `json:"token"`
				CallCount         int64    `json:"call_count"`
				SuccessCount      int64    `json:"success_count"`
				ErrorCount        int64    `json:"error_count"`
				TotalQuota        int64    `json:"total_quota"`
				RiskLevel         string   `json:"risk_level"`
				RiskReasons       []string `json:"risk_reasons"`
				RecommendedAction string   `json:"recommended_action"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(riskResp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("risk view response should be json: %v", err)
	}
	if riskResp.Code != http.StatusOK || payload.Data.Total != 1 || len(payload.Data.Data) != 1 {
		t.Fatalf("risk view should return one risky key, got %d %s", riskResp.Code, riskResp.Body.String())
	}
	item := payload.Data.Data[0]
	if item.Token.ID != riskyTokenID || item.Token.Name != "danger-view" || item.CallCount != 3 || item.SuccessCount != 1 || item.ErrorCount != 2 || item.TotalQuota != 4 {
		t.Fatalf("risk view summary mismatch: %+v", item)
	}
	reasons := strings.Join(item.RiskReasons, ",")
	if item.RiskLevel != "high" || !strings.Contains(reasons, "error_spike") || !strings.Contains(reasons, "low_quota") || item.RecommendedAction != "review_errors" {
		t.Fatalf("risk view should include high-risk reasons and action, got %+v", item)
	}
	body := riskResp.Body.String()
	if strings.Contains(body, "normal-view") || strings.Contains(body, riskyPlainKey) || strings.Contains(body, "sk-") {
		t.Fatalf("risk view should not include safe keys or plaintext API keys: %s", body)
	}
}

func TestAdminAPIKeyBatchExpire(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	createAlice := performJSON(r, http.MethodPost, "/v0/admin/user", rootJWT, map[string]interface{}{
		"username":     "alice",
		"password":     "password123",
		"display_name": "Alice",
		"quota":        int64(100),
	})
	if createAlice.Code != http.StatusOK {
		t.Fatalf("create user failed: %d %s", createAlice.Code, createAlice.Body.String())
	}
	var alice model.User
	if err := internal.DB.Where("username = ?", "alice").First(&alice).Error; err != nil {
		t.Fatal(err)
	}

	rootTokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "root-expire-safe-key",
		"remain_quota": int64(100),
	})
	var rootTokenPayload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rootTokenResp.Body.Bytes(), &rootTokenPayload); err != nil {
		t.Fatal(err)
	}
	aliceToken, err := service.NewTokenService().Create(alice.ID, "alice-expire-key", 100, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	alicePlainKey := aliceToken.Key
	aliceToken.Key = ""

	noFilterResp := performJSON(r, http.MethodPost, "/v0/admin/token/batch-expire", rootJWT, map[string]interface{}{})
	if noFilterResp.Code != http.StatusBadRequest {
		t.Fatalf("batch expire without filters should be rejected, got %d %s", noFilterResp.Code, noFilterResp.Body.String())
	}
	batchResp := performJSON(r, http.MethodPost, "/v0/admin/token/batch-expire", rootJWT, map[string]interface{}{
		"user_id": alice.ID,
		"reason":  "risk_review",
	})
	batchBody := batchResp.Body.String()
	if batchResp.Code != http.StatusOK || !strings.Contains(batchBody, `"expired_count":1`) || !strings.Contains(batchBody, `"matched_count":1`) {
		t.Fatalf("batch expire response mismatch: %d %s", batchResp.Code, batchBody)
	}
	var aliceRow struct {
		Status    int
		ExpiredAt *time.Time
	}
	if err := internal.DB.Table("tokens").Select("status, expired_at").Where("id = ?", aliceToken.ID).Scan(&aliceRow).Error; err != nil {
		t.Fatal(err)
	}
	if aliceRow.Status != common.TokenStatusEnabled || aliceRow.ExpiredAt == nil || aliceRow.ExpiredAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("alice key should be expired without being disabled, got %+v", aliceRow)
	}
	var rootRow struct {
		ExpiredAt *time.Time
	}
	if err := internal.DB.Table("tokens").Select("expired_at").Where("id = ?", rootTokenPayload.Data.ID).Scan(&rootRow).Error; err != nil {
		t.Fatal(err)
	}
	if rootRow.ExpiredAt != nil {
		t.Fatalf("batch expire should not affect other users, got %+v", rootRow)
	}
	expiredModels := performJSON(r, http.MethodGet, "/v1/models", "Bearer "+alicePlainKey, nil)
	if expiredModels.Code != http.StatusUnauthorized {
		t.Fatalf("expired key should be rejected by relay auth, got %d %s", expiredModels.Code, expiredModels.Body.String())
	}

	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=api_key&resource_id=batch", rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK || !strings.Contains(auditBody, `"action":"api_key.batch_expired"`) || strings.Contains(auditBody, alicePlainKey) || strings.Contains(auditBody, "sk-") {
		t.Fatalf("batch expire audit mismatch or leaked key: %d %s", auditResp.Code, auditBody)
	}
}

func TestUserRegisterRespectsRegistrationSettings(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}

	closedResp := performJSON(r, http.MethodPost, "/v0/user/register", "", map[string]interface{}{
		"username":     "closed-user",
		"password":     "password123",
		"display_name": "Closed User",
	})
	if closedResp.Code != http.StatusForbidden {
		t.Fatalf("self registration should be closed by default, got %d %s", closedResp.Code, closedResp.Body.String())
	}
	var closedCount int64
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "closed-user").Count(&closedCount).Error; err != nil {
		t.Fatal(err)
	}
	if closedCount != 0 {
		t.Fatalf("closed registration must not create user, got count=%d", closedCount)
	}

	settingSvc := service.NewSettingService()
	if err := settingSvc.Set("auth.register.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("auth.register.username.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	usernameDisabledResp := performJSON(r, http.MethodPost, "/v0/user/register", "", map[string]interface{}{
		"username": "disabled-method",
		"password": "password123",
	})
	if usernameDisabledResp.Code != http.StatusForbidden {
		t.Fatalf("username registration should respect method switch, got %d %s", usernameDisabledResp.Code, usernameDisabledResp.Body.String())
	}

	if err := settingSvc.Set("auth.register.username.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	captchaRequiredResp := performJSON(r, http.MethodPost, "/v0/user/register", "", map[string]interface{}{
		"username": "captcha-required",
		"password": "password123",
	})
	if captchaRequiredResp.Code != http.StatusForbidden {
		t.Fatalf("captcha-required registration should reject current no-captcha request, got %d %s", captchaRequiredResp.Code, captchaRequiredResp.Body.String())
	}

	trialGroup := model.Group{Name: "trial", Ratio: 1}
	if err := internal.DB.Create(&trialGroup).Error; err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("auth.register.captcha.required", "false"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("auth.register.default_quota", "1234"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("auth.register.default_group_id", "trial"); err != nil {
		t.Fatal(err)
	}

	openResp := performJSON(r, http.MethodPost, "/v0/user/register", "", map[string]interface{}{
		"username":     "trial-user",
		"password":     "password123",
		"display_name": "Trial User",
	})
	if openResp.Code != http.StatusOK {
		t.Fatalf("enabled username registration should succeed, got %d %s", openResp.Code, openResp.Body.String())
	}
	var registered model.User
	if err := internal.DB.Where("username = ?", "trial-user").First(&registered).Error; err != nil {
		t.Fatal(err)
	}
	if registered.Quota != 1234 {
		t.Fatalf("registered user should receive default quota, got %d", registered.Quota)
	}
	if registered.GroupID == nil || *registered.GroupID != trialGroup.ID {
		t.Fatalf("registered user should receive trial group id %d, got %v", trialGroup.ID, registered.GroupID)
	}
}

func TestUserSelfCancelDisablesAccountButPreservesIdentity(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}

	settingSvc := service.NewSettingService()
	if err := settingSvc.Set("auth.register.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("auth.register.username.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("auth.register.captcha.required", "false"); err != nil {
		t.Fatal(err)
	}

	registerBody := map[string]interface{}{
		"username":     "cancel-user",
		"password":     "password123",
		"display_name": "Cancel User",
	}
	registerResp := performJSON(r, http.MethodPost, "/v0/user/register", "", registerBody)
	if registerResp.Code != http.StatusOK {
		t.Fatalf("register cancel user failed: %d %s", registerResp.Code, registerResp.Body.String())
	}
	userJWT := loginBearer(t, r, "cancel-user", "password123")

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", userJWT, map[string]interface{}{
		"name":         "cancel-key",
		"remain_quota": 10,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.ID == 0 || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	cancelResp := performJSON(r, http.MethodDelete, "/v0/user/self", userJWT, nil)
	if cancelResp.Code != http.StatusOK {
		t.Fatalf("self cancel should succeed, got %d %s", cancelResp.Code, cancelResp.Body.String())
	}

	var user model.User
	if err := internal.DB.Where("username = ?", "cancel-user").First(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.Status != common.UserStatusDisabled {
		t.Fatalf("self-cancelled user should be disabled, got status=%d", user.Status)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.Status != common.TokenStatusDisabled {
		t.Fatalf("self-cancel should disable user API keys, got status=%d", storedToken.Status)
	}
	var identityCount int64
	if err := internal.DB.Model(&model.UserIdentity{}).
		Where("user_id = ? AND method = ? AND provider = ? AND identifier = ?", user.ID, model.UserIdentityMethodUsername, model.UserIdentityProviderLocal, "cancel-user").
		Count(&identityCount).Error; err != nil {
		t.Fatal(err)
	}
	if identityCount != 1 {
		t.Fatalf("self-cancel should preserve username identity, got count=%d", identityCount)
	}
	var auditCount int64
	if err := internal.DB.Model(&model.AdminAuditLog{}).
		Where("action = ? AND resource_type = ? AND resource_id = ?", "user.self_cancel", "user", fmt.Sprint(user.ID)).
		Count(&auditCount).Error; err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("self-cancel should write one audit record, got count=%d", auditCount)
	}

	loginAgainResp := performJSON(r, http.MethodPost, "/v0/user/login", "", map[string]interface{}{
		"account":  "cancel-user",
		"password": "password123",
	})
	if loginAgainResp.Code != http.StatusUnauthorized {
		t.Fatalf("self-cancelled user should not log in again, got %d %s", loginAgainResp.Code, loginAgainResp.Body.String())
	}
	recoverResp := performJSON(r, http.MethodPost, "/v0/user/register", "", map[string]interface{}{
		"username":     "cancel-user",
		"password":     "newpassword123",
		"display_name": "Recovered User",
	})
	if recoverResp.Code != http.StatusOK {
		t.Fatalf("preserved identity should recover cancelled account, got %d %s", recoverResp.Code, recoverResp.Body.String())
	}
	var recoveredPayload struct {
		Data struct {
			ID          uint   `json:"id"`
			DisplayName string `json:"display_name"`
			Status      int    `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recoverResp.Body.Bytes(), &recoveredPayload); err != nil {
		t.Fatal(err)
	}
	if recoveredPayload.Data.ID != user.ID || recoveredPayload.Data.DisplayName != "Recovered User" || recoveredPayload.Data.Status != common.UserStatusEnabled {
		t.Fatalf("recovery should return original enabled user, got %+v want id=%d", recoveredPayload.Data, user.ID)
	}
	if err := internal.DB.First(&user, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if user.Status != common.UserStatusEnabled || user.DisplayName != "Recovered User" {
		t.Fatalf("recovered user should be enabled with updated profile, got %+v", user)
	}
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.Status != common.TokenStatusDisabled {
		t.Fatalf("recovery must not re-enable old API keys, got status=%d", storedToken.Status)
	}
	loginOldPasswordResp := performJSON(r, http.MethodPost, "/v0/user/login", "", map[string]interface{}{
		"account":  "cancel-user",
		"password": "password123",
	})
	if loginOldPasswordResp.Code != http.StatusUnauthorized {
		t.Fatalf("old password should not work after recovery, got %d %s", loginOldPasswordResp.Code, loginOldPasswordResp.Body.String())
	}
	loginRecoveredJWT := loginBearer(t, r, "cancel-user", "newpassword123")
	if loginRecoveredJWT == "" {
		t.Fatal("recovered account should log in with new password")
	}
	var recoverAuditCount int64
	if err := internal.DB.Model(&model.AdminAuditLog{}).
		Where("action = ? AND resource_type = ? AND resource_id = ?", "user.recover", "user", fmt.Sprint(user.ID)).
		Count(&recoverAuditCount).Error; err != nil {
		t.Fatal(err)
	}
	if recoverAuditCount != 1 {
		t.Fatalf("recovery should write one audit record, got count=%d", recoverAuditCount)
	}
	var userCount int64
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "cancel-user").Count(&userCount).Error; err != nil {
		t.Fatal(err)
	}
	if userCount != 1 {
		t.Fatalf("recovery must not create a second account, got count=%d", userCount)
	}
}

func TestUserLoginRespectsLoginMethodSettings(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	createResp := performJSON(r, http.MethodPost, "/v0/admin/user", rootJWT, map[string]interface{}{
		"username":     "login-method-user",
		"password":     "password123",
		"display_name": "Login Method User",
		"email":        "method@example.com",
		"role":         common.RoleUser,
		"quota":        10,
	})
	if createResp.Code != http.StatusOK {
		t.Fatalf("create user failed: %d %s", createResp.Code, createResp.Body.String())
	}
	var user model.User
	if err := internal.DB.Where("username = ?", "login-method-user").First(&user).Error; err != nil {
		t.Fatal(err)
	}
	hash, err := common.HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := internal.DB.Create(&[]model.UserIdentity{
		{
			UserID:       user.ID,
			Method:       model.UserIdentityMethodEmail,
			Provider:     model.UserIdentityProviderLocal,
			Identifier:   "method@example.com",
			PasswordHash: hash,
			VerifiedAt:   &now,
		},
		{
			UserID:       user.ID,
			Method:       model.UserIdentityMethodPhone,
			Provider:     model.UserIdentityProviderLocal,
			Identifier:   "+15550001111",
			PasswordHash: hash,
			VerifiedAt:   &now,
		},
	}).Error; err != nil {
		t.Fatal(err)
	}

	usernameLogin := performJSON(r, http.MethodPost, "/v0/user/login", "", map[string]interface{}{
		"account":  "login-method-user",
		"password": "password123",
	})
	if usernameLogin.Code != http.StatusOK {
		t.Fatalf("username/password login should stay enabled by default, got %d %s", usernameLogin.Code, usernameLogin.Body.String())
	}
	emailDisabledLogin := performJSON(r, http.MethodPost, "/v0/user/login", "", map[string]interface{}{
		"account":  "method@example.com",
		"password": "password123",
	})
	if emailDisabledLogin.Code != http.StatusUnauthorized {
		t.Fatalf("email/password login should be disabled by default, got %d %s", emailDisabledLogin.Code, emailDisabledLogin.Body.String())
	}
	phoneDisabledLogin := performJSON(r, http.MethodPost, "/v0/user/login", "", map[string]interface{}{
		"account":  "+15550001111",
		"password": "password123",
	})
	if phoneDisabledLogin.Code != http.StatusUnauthorized {
		t.Fatalf("phone/password login should be disabled by default, got %d %s", phoneDisabledLogin.Code, phoneDisabledLogin.Body.String())
	}

	settingSvc := service.NewSettingService()
	if err := settingSvc.Set("auth.login.email_password.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("auth.login.phone_password.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	emailEnabledLogin := performJSON(r, http.MethodPost, "/v0/user/login", "", map[string]interface{}{
		"account":  "method@example.com",
		"password": "password123",
	})
	if emailEnabledLogin.Code != http.StatusOK {
		t.Fatalf("email/password login should work when enabled, got %d %s", emailEnabledLogin.Code, emailEnabledLogin.Body.String())
	}
	phoneEnabledLogin := performJSON(r, http.MethodPost, "/v0/user/login", "", map[string]interface{}{
		"account":  "+15550001111",
		"password": "password123",
	})
	if phoneEnabledLogin.Code != http.StatusOK {
		t.Fatalf("phone/password login should work when enabled, got %d %s", phoneEnabledLogin.Code, phoneEnabledLogin.Body.String())
	}
}

func TestAdminUserGroupManagement(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	createResp := performJSON(r, http.MethodPost, "/v0/admin/groups", rootJWT, map[string]interface{}{
		"name":  "vip",
		"ratio": 0.8,
	})
	if createResp.Code != http.StatusOK {
		t.Fatalf("create group failed: %d %s", createResp.Code, createResp.Body.String())
	}
	var createdGroupResp struct {
		Data struct {
			ID    uint    `json:"id"`
			Name  string  `json:"name"`
			Ratio float64 `json:"ratio"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createResp.Body.Bytes(), &createdGroupResp); err != nil {
		t.Fatal(err)
	}
	if createdGroupResp.Data.ID == 0 || createdGroupResp.Data.Name != "vip" || createdGroupResp.Data.Ratio != 0.8 {
		t.Fatalf("unexpected created group response: %s", createResp.Body.String())
	}

	listResp := performJSON(r, http.MethodGet, "/v0/admin/groups?keyword=vip", rootJWT, nil)
	if listResp.Code != http.StatusOK || !strings.Contains(listResp.Body.String(), `"name":"vip"`) {
		t.Fatalf("list groups should include created group, got %d %s", listResp.Code, listResp.Body.String())
	}

	blankNameResp := performJSON(r, http.MethodPut, fmt.Sprintf("/v0/admin/groups/%d", createdGroupResp.Data.ID), rootJWT, map[string]interface{}{
		"name": "   ",
	})
	if blankNameResp.Code != http.StatusBadRequest {
		t.Fatalf("blank group name update should be rejected, got %d %s", blankNameResp.Code, blankNameResp.Body.String())
	}

	updateResp := performJSON(r, http.MethodPut, fmt.Sprintf("/v0/admin/groups/%d", createdGroupResp.Data.ID), rootJWT, map[string]interface{}{
		"name":  "vip-renamed",
		"ratio": 0.9,
	})
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update group failed: %d %s", updateResp.Code, updateResp.Body.String())
	}
	var updatedGroup model.Group
	if err := internal.DB.First(&updatedGroup, createdGroupResp.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updatedGroup.Name != "vip-renamed" || updatedGroup.Ratio != 0.9 {
		t.Fatalf("group should be updated, got %+v", updatedGroup)
	}

	userResp := performJSON(r, http.MethodPost, "/v0/admin/user", rootJWT, map[string]interface{}{
		"username":     "grouped-user",
		"password":     "password123",
		"display_name": "Grouped User",
		"role":         common.RoleUser,
		"quota":        10,
		"group_id":     createdGroupResp.Data.ID,
	})
	if userResp.Code != http.StatusOK {
		t.Fatalf("create grouped user failed: %d %s", userResp.Code, userResp.Body.String())
	}
	inUseDelete := performJSON(r, http.MethodDelete, fmt.Sprintf("/v0/admin/groups/%d", createdGroupResp.Data.ID), rootJWT, nil)
	if inUseDelete.Code != http.StatusBadRequest {
		t.Fatalf("in-use group delete should be rejected, got %d %s", inUseDelete.Code, inUseDelete.Body.String())
	}

	unusedResp := performJSON(r, http.MethodPost, "/v0/admin/groups", rootJWT, map[string]interface{}{
		"name":  "unused",
		"ratio": 1.2,
	})
	if unusedResp.Code != http.StatusOK {
		t.Fatalf("create unused group failed: %d %s", unusedResp.Code, unusedResp.Body.String())
	}
	var unusedGroupResp struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(unusedResp.Body.Bytes(), &unusedGroupResp); err != nil {
		t.Fatal(err)
	}
	deleteUnused := performJSON(r, http.MethodDelete, fmt.Sprintf("/v0/admin/groups/%d", unusedGroupResp.Data.ID), rootJWT, nil)
	if deleteUnused.Code != http.StatusOK {
		t.Fatalf("delete unused group failed: %d %s", deleteUnused.Code, deleteUnused.Body.String())
	}
	afterDeleteList := performJSON(r, http.MethodGet, "/v0/admin/groups?keyword=unused", rootJWT, nil)
	if afterDeleteList.Code != http.StatusOK || strings.Contains(afterDeleteList.Body.String(), `"name":"unused"`) {
		t.Fatalf("deleted group should be absent from list, got %d %s", afterDeleteList.Code, afterDeleteList.Body.String())
	}

	var defaultGroup model.Group
	if err := internal.DB.Where("name = ?", "default").First(&defaultGroup).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		defaultGroup = model.Group{Name: "default", Ratio: 1}
		if err := internal.DB.Create(&defaultGroup).Error; err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	deleteDefault := performJSON(r, http.MethodDelete, fmt.Sprintf("/v0/admin/groups/%d", defaultGroup.ID), rootJWT, nil)
	if deleteDefault.Code != http.StatusBadRequest {
		t.Fatalf("default group delete should be rejected, got %d %s", deleteDefault.Code, deleteDefault.Body.String())
	}

	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=user_group", rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(auditBody, `"action":"user_group.create"`) ||
		!strings.Contains(auditBody, `"action":"user_group.update"`) ||
		!strings.Contains(auditBody, `"action":"user_group.delete"`) {
		t.Fatalf("group audits missing, got %d %s", auditResp.Code, auditBody)
	}
}

func TestAdminPrivilegeBoundaries(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	createAdmin := performJSON(r, http.MethodPost, "/v0/admin/admin", rootJWT, map[string]interface{}{
		"username": "ops",
		"password": "password123",
		"role":     common.RoleAdmin,
	})
	if createAdmin.Code != http.StatusOK {
		t.Fatalf("create admin failed: %d %s", createAdmin.Code, createAdmin.Body.String())
	}
	createPeerSuper := performJSON(r, http.MethodPost, "/v0/admin/admin", rootJWT, map[string]interface{}{
		"username": "peer-root",
		"password": "password123",
		"role":     common.RoleSuper,
	})
	if createPeerSuper.Code == http.StatusOK {
		t.Fatalf("super admin created same-level super admin: %s", createPeerSuper.Body.String())
	}
	opsJWT := loginBearer(t, r, "ops", "password123")
	var opsUser model.User
	if err := internal.DB.Where("username = ?", "ops").First(&opsUser).Error; err != nil {
		t.Fatal(err)
	}
	promoteOps := performJSON(r, http.MethodPut, "/v0/admin/admin/"+uintString(opsUser.ID), rootJWT, map[string]interface{}{
		"role": common.RoleSuper,
	})
	if promoteOps.Code == http.StatusOK {
		t.Fatalf("super admin promoted lower admin to same-level super: %s", promoteOps.Body.String())
	}

	createUser := performJSON(r, http.MethodPost, "/v0/admin/user", opsJWT, map[string]interface{}{
		"username": "alice",
		"password": "password123",
		"role":     common.RoleUser,
	})
	if createUser.Code != http.StatusOK {
		t.Fatalf("normal admin should create normal user, got %d %s", createUser.Code, createUser.Body.String())
	}

	createAdminThroughUser := performJSON(r, http.MethodPost, "/v0/admin/user", opsJWT, map[string]interface{}{
		"username": "mallory",
		"password": "password123",
		"role":     common.RoleAdmin,
	})
	if createAdminThroughUser.Code != http.StatusForbidden {
		t.Fatalf("user management must reject admin creation, got %d %s", createAdminThroughUser.Code, createAdminThroughUser.Body.String())
	}
	createAdminByAdmin := performJSON(r, http.MethodPost, "/v0/admin/admin", opsJWT, map[string]interface{}{
		"username": "mallory-admin",
		"password": "password123",
		"role":     common.RoleAdmin,
	})
	if createAdminByAdmin.Code != http.StatusForbidden {
		t.Fatalf("normal admin must not create admin through admin management, got %d %s", createAdminByAdmin.Code, createAdminByAdmin.Body.String())
	}

	adminMgmtByAdmin := performJSON(r, http.MethodGet, "/v0/admin/admin", opsJWT, nil)
	if adminMgmtByAdmin.Code != http.StatusOK || !strings.Contains(adminMgmtByAdmin.Body.String(), `"username":"root"`) || !strings.Contains(adminMgmtByAdmin.Body.String(), `"username":"ops"`) {
		t.Fatalf("normal admin should view admin list, got %d %s", adminMgmtByAdmin.Code, adminMgmtByAdmin.Body.String())
	}
	settingByAdmin := performJSON(r, http.MethodGet, "/v0/admin/setting", opsJWT, nil)
	if settingByAdmin.Code != http.StatusForbidden {
		t.Fatalf("normal admin must not access settings, got %d %s", settingByAdmin.Code, settingByAdmin.Body.String())
	}
	settingBySuper := performJSON(r, http.MethodGet, "/v0/admin/setting", rootJWT, nil)
	if settingBySuper.Code != http.StatusOK {
		t.Fatalf("super admin should access settings, got %d %s", settingBySuper.Code, settingBySuper.Body.String())
	}
	if strings.Contains(settingBySuper.Body.String(), "test-jwt-secret") {
		t.Fatalf("settings response leaked jwt secret: %s", settingBySuper.Body.String())
	}

	adminRoleList := performJSON(r, http.MethodGet, "/v0/admin/user?role=2", opsJWT, nil)
	if adminRoleList.Code != http.StatusOK || !strings.Contains(adminRoleList.Body.String(), `"username":"root"`) || strings.Contains(adminRoleList.Body.String(), `"username":"ops"`) || strings.Contains(adminRoleList.Body.String(), `"username":"alice"`) {
		t.Fatalf("normal admin should view super admins through user list, got %d %s", adminRoleList.Code, adminRoleList.Body.String())
	}
	allAccountList := performJSON(r, http.MethodGet, "/v0/admin/user", opsJWT, nil)
	if allAccountList.Code != http.StatusOK || !strings.Contains(allAccountList.Body.String(), `"username":"root"`) || !strings.Contains(allAccountList.Body.String(), `"username":"alice"`) {
		t.Fatalf("normal admin should view all users and admins, got %d %s", allAccountList.Code, allAccountList.Body.String())
	}

	var rootUser model.User
	if err := internal.DB.Where("username = ?", "root").First(&rootUser).Error; err != nil {
		t.Fatal(err)
	}
	disableRoot := performJSON(r, http.MethodPut, "/v0/admin/user/"+uintString(rootUser.ID), opsJWT, map[string]interface{}{
		"status": common.UserStatusDisabled,
	})
	if disableRoot.Code == http.StatusOK {
		t.Fatalf("normal admin disabled super admin through user management: %s", disableRoot.Body.String())
	}
	deleteRoot := performJSON(r, http.MethodDelete, "/v0/admin/user/"+uintString(rootUser.ID), opsJWT, nil)
	if deleteRoot.Code == http.StatusOK {
		t.Fatalf("normal admin deleted super admin through user management: %s", deleteRoot.Body.String())
	}

	selfDemote := performJSON(r, http.MethodPut, "/v0/admin/admin/"+uintString(rootUser.ID), rootJWT, map[string]interface{}{
		"role": common.RoleAdmin,
	})
	if selfDemote.Code == http.StatusOK {
		t.Fatalf("super admin demoted self: %s", selfDemote.Body.String())
	}
	selfDisable := performJSON(r, http.MethodPut, "/v0/admin/admin/"+uintString(rootUser.ID), rootJWT, map[string]interface{}{
		"status": common.UserStatusDisabled,
	})
	if selfDisable.Code == http.StatusOK {
		t.Fatalf("super admin disabled self: %s", selfDisable.Body.String())
	}
}

func TestAdminAccountManagementAuditLogs(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	deniedAdminResp := performJSON(r, http.MethodPost, "/v0/admin/admin", rootJWT, map[string]interface{}{
		"username": "ops-denied",
		"password": "password123",
		"role":     common.RoleAdmin,
	})
	if deniedAdminResp.Code != http.StatusOK {
		t.Fatalf("create denied actor admin failed: %d %s", deniedAdminResp.Code, deniedAdminResp.Body.String())
	}
	deniedJWT := loginBearer(t, r, "ops-denied", "password123")

	createResp := performJSON(r, http.MethodPost, "/v0/admin/admin", rootJWT, map[string]interface{}{
		"username":     "audit-admin",
		"password":     "password123",
		"display_name": "Audit Admin",
		"email":        "audit-admin@example.com",
		"role":         common.RoleAdmin,
	})
	if createResp.Code != http.StatusOK {
		t.Fatalf("create audited admin failed: %d %s", createResp.Code, createResp.Body.String())
	}
	var payload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createResp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}

	updateResp := performJSON(r, http.MethodPut, "/v0/admin/admin/"+uintString(payload.Data.ID), rootJWT, map[string]interface{}{
		"display_name": "Audit Admin Updated",
	})
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update audited admin failed: %d %s", updateResp.Code, updateResp.Body.String())
	}
	disableResp := performJSON(r, http.MethodPut, "/v0/admin/admin/"+uintString(payload.Data.ID), rootJWT, map[string]interface{}{
		"status": common.UserStatusDisabled,
	})
	if disableResp.Code != http.StatusOK {
		t.Fatalf("disable audited admin failed: %d %s", disableResp.Code, disableResp.Body.String())
	}
	deleteResp := performJSON(r, http.MethodDelete, "/v0/admin/admin/"+uintString(payload.Data.ID), rootJWT, nil)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete audited admin failed: %d %s", deleteResp.Code, deleteResp.Body.String())
	}
	deniedResp := performJSON(r, http.MethodPost, "/v0/admin/admin", deniedJWT, map[string]interface{}{
		"username": "should-not-create",
		"password": "password123",
		"role":     common.RoleAdmin,
	})
	if deniedResp.Code != http.StatusForbidden {
		t.Fatalf("normal admin should be denied creating admin, got %d %s", deniedResp.Code, deniedResp.Body.String())
	}

	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=admin", rootJWT, nil)
	body := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(body, `"action":"admin.create"`) ||
		!strings.Contains(body, `"action":"admin.update"`) ||
		!strings.Contains(body, `"action":"admin.disable"`) ||
		!strings.Contains(body, `"action":"admin.delete"`) ||
		!strings.Contains(body, `"action":"admin.denied"`) {
		t.Fatalf("admin account management should write audit logs, got %d %s", auditResp.Code, body)
	}
	if strings.Contains(body, "password123") {
		t.Fatalf("admin account audit should not expose passwords: %s", body)
	}
}

func TestAdminUserManagementAuditLogs(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	group := model.Group{Name: "audit-users", Ratio: 1}
	if err := internal.DB.Create(&group).Error; err != nil {
		t.Fatal(err)
	}

	createResp := performJSON(r, http.MethodPost, "/v0/admin/user", rootJWT, map[string]interface{}{
		"username":     "audit-user",
		"password":     "password123",
		"display_name": "Audit User",
		"email":        "audit-user@example.com",
		"role":         common.RoleUser,
		"quota":        20,
	})
	if createResp.Code != http.StatusOK {
		t.Fatalf("create audited user failed: %d %s", createResp.Code, createResp.Body.String())
	}
	var payload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createResp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}

	updateResp := performJSON(r, http.MethodPut, "/v0/admin/user/"+uintString(payload.Data.ID), rootJWT, map[string]interface{}{
		"display_name": "Audit User Updated",
		"group_id":     group.ID,
	})
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update audited user failed: %d %s", updateResp.Code, updateResp.Body.String())
	}
	deniedResp := performJSON(r, http.MethodPut, "/v0/admin/user/"+uintString(payload.Data.ID), rootJWT, map[string]interface{}{
		"role": common.RoleAdmin,
	})
	if deniedResp.Code != http.StatusForbidden {
		t.Fatalf("role change through user management should be denied, got %d %s", deniedResp.Code, deniedResp.Body.String())
	}
	disableResp := performJSON(r, http.MethodPut, "/v0/admin/user/"+uintString(payload.Data.ID), rootJWT, map[string]interface{}{
		"status": common.UserStatusDisabled,
	})
	if disableResp.Code != http.StatusOK {
		t.Fatalf("disable audited user failed: %d %s", disableResp.Code, disableResp.Body.String())
	}
	deleteResp := performJSON(r, http.MethodDelete, "/v0/admin/user/"+uintString(payload.Data.ID), rootJWT, nil)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete audited user failed: %d %s", deleteResp.Code, deleteResp.Body.String())
	}

	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=user&resource_id="+uintString(payload.Data.ID), rootJWT, nil)
	body := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(body, `"action":"user.create"`) ||
		!strings.Contains(body, `"action":"user.update"`) ||
		!strings.Contains(body, `"action":"user.denied"`) ||
		!strings.Contains(body, `"action":"user.disable"`) ||
		!strings.Contains(body, `"action":"user.delete"`) {
		t.Fatalf("admin user management should write audit logs, got %d %s", auditResp.Code, body)
	}
	if strings.Contains(body, "password123") {
		t.Fatalf("admin user audit should not expose passwords: %s", body)
	}
}

func TestAdminLogClearWritesAuditLog(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	oldLog := model.Log{
		UserID:      root.ID,
		Model:       "audit-log-model",
		Status:      common.LogStatusSuccess,
		QuotaUsed:   1,
		TotalTokens: 1,
		CreatedAt:   time.Now().AddDate(0, 0, -120),
	}
	if err := internal.DB.Create(&oldLog).Error; err != nil {
		t.Fatal(err)
	}

	before := time.Now().AddDate(0, 0, -90).UTC().Format(time.RFC3339)
	clearResp := performJSON(r, http.MethodDelete, "/v0/admin/log?before="+url.QueryEscape(before), rootJWT, nil)
	if clearResp.Code != http.StatusOK {
		t.Fatalf("clear admin logs failed: %d %s", clearResp.Code, clearResp.Body.String())
	}
	var remaining int64
	if err := internal.DB.Model(&model.Log{}).Where("id = ?", oldLog.ID).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("old log should be deleted, remaining=%d", remaining)
	}

	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=log", rootJWT, nil)
	body := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(body, `"action":"log.clear"`) ||
		!strings.Contains(body, before) {
		t.Fatalf("admin log clear should write audit log, got %d %s", auditResp.Code, body)
	}
}

func TestAdminLogExportWritesAuditLogAndRedactsSensitiveFields(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	exportLog := model.Log{
		UserID:           root.ID,
		Model:            "export-model",
		PromptTokens:     2,
		CompletionTokens: 3,
		TotalTokens:      5,
		UsageSource:      common.LogUsageSourceUpstream,
		QuotaUsed:        7,
		Status:           common.LogStatusSuccess,
		Content:          `{"prompt":"raw prompt","api_key":"sk-export-secret"}`,
		Response:         `{"output":"provider response","upstream_key":"upstream-secret"}`,
		ErrorMsg:         "provider message with sk-export-secret",
		RequestSnapshot:  `{"api_key":"sk-export-secret"}`,
		PolicySnapshot:   `{"payment_key":"PAYMENT_SECRET"}`,
		RouteSnapshot:    `{"upstream_key":"upstream-secret"}`,
		BillingSnapshot:  `{"secret":"PAYMENT_SECRET"}`,
		IP:               "203.0.113.5",
		RequestID:        "req-export-1",
		CreatedAt:        time.Date(2026, 6, 17, 8, 30, 0, 0, time.UTC),
	}
	if err := internal.DB.Create(&exportLog).Error; err != nil {
		t.Fatal(err)
	}

	exportResp := performJSON(r, http.MethodGet, "/v0/admin/log/export?model=export-model&limit=10", rootJWT, nil)
	if exportResp.Code != http.StatusOK {
		t.Fatalf("export admin logs failed: %d %s", exportResp.Code, exportResp.Body.String())
	}
	if contentType := exportResp.Header().Get("Content-Type"); !strings.Contains(contentType, "text/csv") {
		t.Fatalf("export should return csv content type, got %q", contentType)
	}
	if disposition := exportResp.Header().Get("Content-Disposition"); !strings.Contains(disposition, "attachment") || !strings.Contains(disposition, "routerx-logs") {
		t.Fatalf("export should return attachment disposition, got %q", disposition)
	}
	body := exportResp.Body.String()
	for _, forbidden := range []string{"sk-export-secret", "PAYMENT_SECRET", "upstream-secret", "raw prompt", "provider response", "203.0.113.5"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("export csv should not expose sensitive value %q: %s", forbidden, body)
		}
	}
	records, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("export should be valid csv: %v\n%s", err, body)
	}
	if len(records) != 2 {
		t.Fatalf("export should include header and one filtered log, got %d records: %#v", len(records), records)
	}
	expectedHeader := []string{"id", "user_id", "token_id", "channel_id", "model", "prompt_tokens", "completion_tokens", "total_tokens", "usage_source", "quota_used", "status", "error_code", "error_source", "upstream_status", "request_id", "created_at"}
	if fmt.Sprint(records[0]) != fmt.Sprint(expectedHeader) {
		t.Fatalf("unexpected csv header: %#v", records[0])
	}
	row := records[1]
	if row[4] != "export-model" || row[8] != common.LogUsageSourceUpstream || row[9] != "7" || row[14] != "req-export-1" {
		t.Fatalf("unexpected csv row: %#v", row)
	}

	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=log", rootJWT, nil)
	auditBody := auditResp.Body.String()
	if strings.Contains(auditBody, "sk-export-secret") || strings.Contains(auditBody, "PAYMENT_SECRET") {
		t.Fatalf("admin log export audit should not expose sensitive values: %s", auditBody)
	}
	var auditPayload struct {
		Data struct {
			Data []struct {
				Action       string `json:"action"`
				AfterSummary string `json:"after_summary"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(auditResp.Body.Bytes(), &auditPayload); err != nil {
		t.Fatalf("admin log export audit response should be json: %v", err)
	}
	if auditResp.Code != http.StatusOK || len(auditPayload.Data.Data) != 1 || auditPayload.Data.Data[0].Action != "log.export" {
		t.Fatalf("admin log export should write audit log, got %d %s", auditResp.Code, auditBody)
	}
	var afterSummary map[string]interface{}
	if err := json.Unmarshal([]byte(auditPayload.Data.Data[0].AfterSummary), &afterSummary); err != nil {
		t.Fatalf("admin log export audit summary should be json: %v", err)
	}
	filters, _ := afterSummary["filters"].(map[string]interface{})
	if afterSummary["exported_count"] != float64(1) || afterSummary["limit"] != float64(10) || filters["model"] != "export-model" {
		t.Fatalf("admin log export audit should record filters, limit and count, got %#v", afterSummary)
	}
}

func TestUserRedeemsRedemCodeOnce(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(10)).Error; err != nil {
		t.Fatal(err)
	}
	code := model.RedemCode{Code: "OFFLINE-CREDIT-1", Quota: 25, Status: common.RedemCodeStatusUnused}
	if err := internal.DB.Create(&code).Error; err != nil {
		t.Fatal(err)
	}

	first := performJSON(r, http.MethodPost, "/v0/user/redem", rootJWT, map[string]interface{}{
		"code": " OFFLINE-CREDIT-1 ",
	})
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"quota":35`) || !strings.Contains(first.Body.String(), `"redeemed_quota":25`) {
		t.Fatalf("redeem should increase user quota and return balance, got %d %s", first.Code, first.Body.String())
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 35 {
		t.Fatalf("redeem should add quota once, got %d", root.Quota)
	}
	var storedCode model.RedemCode
	if err := internal.DB.First(&storedCode, code.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedCode.Status != common.RedemCodeStatusUsed || storedCode.UsedBy == nil || *storedCode.UsedBy != root.ID || storedCode.UsedAt == nil {
		t.Fatalf("redeem should mark code used by current user: %+v", storedCode)
	}
	var quotaTx model.QuotaTransaction
	if err := internal.DB.Where("source_type = ? AND source_id = ?", common.QuotaSourceTypeRedemCode, fmt.Sprint(code.ID)).First(&quotaTx).Error; err != nil {
		t.Fatalf("redeem should write quota transaction: %v", err)
	}
	if quotaTx.UserID != root.ID || quotaTx.Type != common.QuotaTransactionTypeRedemRedeem || quotaTx.Amount != 25 || quotaTx.BalanceBefore != 10 || quotaTx.BalanceAfter != 35 {
		t.Fatalf("unexpected redeem quota transaction: %+v", quotaTx)
	}
	if quotaTx.IdempotencyKey != fmt.Sprintf("redem_code:%d", code.ID) {
		t.Fatalf("redeem quota transaction should use stable idempotency key, got %q", quotaTx.IdempotencyKey)
	}
	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=redem_code&resource_id="+uintString(code.ID), rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK || !strings.Contains(auditBody, `"action":"redem_code.redeem"`) {
		t.Fatalf("redeem should write redem_code.redeem audit log, got %d %s", auditResp.Code, auditBody)
	}
	if strings.Contains(auditBody, "OFFLINE-CREDIT-1") {
		t.Fatalf("redem redeem audit should not expose full code: %s", auditBody)
	}

	second := performJSON(r, http.MethodPost, "/v0/user/redem", rootJWT, map[string]interface{}{
		"code": "OFFLINE-CREDIT-1",
	})
	if second.Code != http.StatusBadRequest || strings.Contains(second.Body.String(), `"success":true`) {
		t.Fatalf("used redem code should be rejected, got %d %s", second.Code, second.Body.String())
	}
	if err := internal.DB.First(&root, root.ID).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 35 {
		t.Fatalf("used redem code must not add quota again, got %d", root.Quota)
	}
	var txCount int64
	if err := internal.DB.Model(&model.QuotaTransaction{}).Where("source_type = ? AND source_id = ?", common.QuotaSourceTypeRedemCode, fmt.Sprint(code.ID)).Count(&txCount).Error; err != nil {
		t.Fatal(err)
	}
	if txCount != 1 {
		t.Fatalf("used redem code must not write duplicate quota transactions, got %d", txCount)
	}
}

func TestAdminQuotaAdjustmentWritesTransaction(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	createResp := performJSON(r, http.MethodPost, "/v0/admin/user", rootJWT, map[string]interface{}{
		"username": "alice",
		"password": "password123",
		"quota":    10,
	})
	if createResp.Code != http.StatusOK {
		t.Fatalf("create user failed: %d %s", createResp.Code, createResp.Body.String())
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	var alice model.User
	if err := internal.DB.Where("username = ?", "alice").First(&alice).Error; err != nil {
		t.Fatal(err)
	}

	adjustResp := performJSON(r, http.MethodPatch, "/v0/admin/user/"+uintString(alice.ID)+"/quota", rootJWT, map[string]interface{}{
		"quota":  25,
		"reason": "support credit",
	})
	if adjustResp.Code != http.StatusOK {
		t.Fatalf("quota adjust failed: %d %s", adjustResp.Code, adjustResp.Body.String())
	}
	if err := internal.DB.First(&alice, alice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if alice.Quota != 35 {
		t.Fatalf("admin quota adjustment should update balance, got %d", alice.Quota)
	}
	var quotaTx model.QuotaTransaction
	if err := internal.DB.Where("user_id = ? AND type = ?", alice.ID, common.QuotaTransactionTypeAdminAdjust).First(&quotaTx).Error; err != nil {
		t.Fatalf("admin quota adjustment should write quota transaction: %v", err)
	}
	if quotaTx.Amount != 25 || quotaTx.BalanceBefore != 10 || quotaTx.BalanceAfter != 35 || quotaTx.SourceType != common.QuotaSourceTypeAdminAction {
		t.Fatalf("unexpected admin quota transaction: %+v", quotaTx)
	}
	if quotaTx.ActorUserID == nil || *quotaTx.ActorUserID != root.ID || quotaTx.Reason != "support credit" || quotaTx.IdempotencyKey == "" {
		t.Fatalf("admin quota transaction should include actor, reason and idempotency key: %+v", quotaTx)
	}
	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=user&resource_id="+uintString(alice.ID), rootJWT, nil)
	body := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(body, `"action":"user.quota_update"`) ||
		!strings.Contains(body, `"resource_type":"user"`) ||
		!strings.Contains(body, `"resource_id":"`+uintString(alice.ID)+`"`) ||
		!strings.Contains(body, "support credit") {
		t.Fatalf("admin quota adjustment should write audit log, got %d %s", auditResp.Code, body)
	}
}

func TestQuotaTransactionListAPIs(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	for _, username := range []string{"ledger-user", "other-ledger-user"} {
		createResp := performJSON(r, http.MethodPost, "/v0/admin/user", rootJWT, map[string]interface{}{
			"username": username,
			"password": "password123",
			"role":     common.RoleUser,
			"quota":    0,
		})
		if createResp.Code != http.StatusOK {
			t.Fatalf("create %s failed: %d %s", username, createResp.Code, createResp.Body.String())
		}
	}

	var ledgerUser model.User
	if err := internal.DB.Where("username = ?", "ledger-user").First(&ledgerUser).Error; err != nil {
		t.Fatal(err)
	}
	var otherUser model.User
	if err := internal.DB.Where("username = ?", "other-ledger-user").First(&otherUser).Error; err != nil {
		t.Fatal(err)
	}

	adjustResp := performJSON(r, http.MethodPatch, "/v0/admin/user/"+uintString(ledgerUser.ID)+"/quota", rootJWT, map[string]interface{}{
		"quota":  25,
		"reason": "seed credit",
	})
	if adjustResp.Code != http.StatusOK {
		t.Fatalf("quota adjust failed: %d %s", adjustResp.Code, adjustResp.Body.String())
	}
	otherAdjustResp := performJSON(r, http.MethodPatch, "/v0/admin/user/"+uintString(otherUser.ID)+"/quota", rootJWT, map[string]interface{}{
		"quota":  9,
		"reason": "other seed credit",
	})
	if otherAdjustResp.Code != http.StatusOK {
		t.Fatalf("other quota adjust failed: %d %s", otherAdjustResp.Code, otherAdjustResp.Body.String())
	}

	userJWT := loginBearer(t, r, "ledger-user", "password123")
	userResp := performJSON(r, http.MethodGet, "/v0/user/quota-transactions?type="+common.QuotaTransactionTypeAdminAdjust, userJWT, nil)
	if userResp.Code != http.StatusOK {
		t.Fatalf("user quota transactions failed: %d %s", userResp.Code, userResp.Body.String())
	}
	var userPayload struct {
		Data struct {
			Total int64 `json:"total"`
			Data  []struct {
				UserID        uint   `json:"user_id"`
				Type          string `json:"type"`
				Amount        int64  `json:"amount"`
				BalanceBefore int64  `json:"balance_before"`
				BalanceAfter  int64  `json:"balance_after"`
				SourceType    string `json:"source_type"`
				Reason        string `json:"reason"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(userResp.Body.Bytes(), &userPayload); err != nil {
		t.Fatal(err)
	}
	if userPayload.Data.Total != 1 || len(userPayload.Data.Data) != 1 {
		t.Fatalf("user should only see own quota transaction, got %s", userResp.Body.String())
	}
	userTx := userPayload.Data.Data[0]
	if userTx.UserID != ledgerUser.ID ||
		userTx.Type != common.QuotaTransactionTypeAdminAdjust ||
		userTx.Amount != 25 ||
		userTx.BalanceBefore != 0 ||
		userTx.BalanceAfter != 25 ||
		userTx.SourceType != common.QuotaSourceTypeAdminAction ||
		userTx.Reason != "seed credit" {
		t.Fatalf("unexpected user quota transaction: %+v", userTx)
	}

	adminResp := performJSON(r, http.MethodGet, "/v0/admin/quota-transactions?user_id="+uintString(ledgerUser.ID)+"&type="+common.QuotaTransactionTypeAdminAdjust, rootJWT, nil)
	if adminResp.Code != http.StatusOK {
		t.Fatalf("admin quota transactions failed: %d %s", adminResp.Code, adminResp.Body.String())
	}
	var adminPayload struct {
		Data struct {
			Total int64 `json:"total"`
			Data  []struct {
				UserID        uint   `json:"user_id"`
				Type          string `json:"type"`
				Amount        int64  `json:"amount"`
				BalanceBefore int64  `json:"balance_before"`
				BalanceAfter  int64  `json:"balance_after"`
				SourceType    string `json:"source_type"`
				Reason        string `json:"reason"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(adminResp.Body.Bytes(), &adminPayload); err != nil {
		t.Fatal(err)
	}
	if adminPayload.Data.Total != 1 || len(adminPayload.Data.Data) != 1 {
		t.Fatalf("admin should filter quota transactions by user, got %s", adminResp.Body.String())
	}
	adminTx := adminPayload.Data.Data[0]
	if adminTx.UserID != ledgerUser.ID ||
		adminTx.Type != common.QuotaTransactionTypeAdminAdjust ||
		adminTx.Amount != 25 ||
		adminTx.BalanceBefore != 0 ||
		adminTx.BalanceAfter != 25 ||
		adminTx.SourceType != common.QuotaSourceTypeAdminAction ||
		adminTx.Reason != "seed credit" {
		t.Fatalf("unexpected admin quota transaction: %+v", adminTx)
	}

	sourceResp := performJSON(r, http.MethodGet, "/v0/admin/quota-transactions?source_type="+common.QuotaSourceTypeAdminAction, rootJWT, nil)
	if sourceResp.Code != http.StatusOK {
		t.Fatalf("admin quota transactions source filter failed: %d %s", sourceResp.Code, sourceResp.Body.String())
	}
	var sourcePayload struct {
		Data struct {
			Total int64 `json:"total"`
			Data  []struct {
				UserID     uint   `json:"user_id"`
				SourceType string `json:"source_type"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(sourceResp.Body.Bytes(), &sourcePayload); err != nil {
		t.Fatal(err)
	}
	if sourcePayload.Data.Total != 2 || len(sourcePayload.Data.Data) != 2 {
		t.Fatalf("admin should filter quota transactions by source type, got %s", sourceResp.Body.String())
	}
	seenLedger := false
	seenOther := false
	for _, tx := range sourcePayload.Data.Data {
		if tx.SourceType != common.QuotaSourceTypeAdminAction {
			t.Fatalf("unexpected source type in filtered transaction: %+v", tx)
		}
		if tx.UserID == ledgerUser.ID {
			seenLedger = true
		}
		if tx.UserID == otherUser.ID {
			seenOther = true
		}
	}
	if !seenLedger || !seenOther {
		t.Fatalf("source type filter should include both admin adjustment transactions, got %s", sourceResp.Body.String())
	}
}

func TestAdminPaymentManualAdjustmentRequiresReason(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	createResp := performJSON(r, http.MethodPost, "/v0/admin/user", rootJWT, map[string]interface{}{
		"username": "alice",
		"password": "password123",
		"quota":    50,
	})
	if createResp.Code != http.StatusOK {
		t.Fatalf("create user failed: %d %s", createResp.Code, createResp.Body.String())
	}
	var alice model.User
	if err := internal.DB.Where("username = ?", "alice").First(&alice).Error; err != nil {
		t.Fatal(err)
	}

	resp := performJSON(r, http.MethodPost, "/v0/admin/payment/adjustments", rootJWT, map[string]interface{}{
		"user_id":         alice.ID,
		"amount":          10,
		"idempotency_key": "manual-missing-reason",
	})
	if resp.Code != http.StatusBadRequest || !strings.Contains(strings.ToLower(resp.Body.String()), "reason") {
		t.Fatalf("manual payment adjustment without reason should fail, got %d %s", resp.Code, resp.Body.String())
	}
	if err := internal.DB.First(&alice, alice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if alice.Quota != 50 {
		t.Fatalf("manual payment adjustment without reason must not change quota, got %d", alice.Quota)
	}
}

func TestAdminPaymentManualAdjustmentWritesManualTransactionAndAudit(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	createResp := performJSON(r, http.MethodPost, "/v0/admin/user", rootJWT, map[string]interface{}{
		"username": "alice",
		"password": "password123",
		"quota":    50,
	})
	if createResp.Code != http.StatusOK {
		t.Fatalf("create user failed: %d %s", createResp.Code, createResp.Body.String())
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	var alice model.User
	if err := internal.DB.Where("username = ?", "alice").First(&alice).Error; err != nil {
		t.Fatal(err)
	}
	order := model.PaymentOrder{
		OrderNo:   "PAYMANUAL1000",
		UserID:    alice.ID,
		ProductID: "quota_manual",
		Provider:  common.PaymentProviderStripe,
		Amount:    "9.99",
		Currency:  "usd",
		Quota:     100,
		Status:    common.PaymentOrderStatusPaid,
	}
	if err := internal.DB.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	adjustResp := performJSON(r, http.MethodPost, "/v0/admin/payment/adjustments", rootJWT, map[string]interface{}{
		"user_id":         alice.ID,
		"order_no":        order.OrderNo,
		"amount":          -20,
		"reason":          "chargeback correction",
		"idempotency_key": "manual-payment-adjust-1",
	})
	adjustBody := adjustResp.Body.String()
	if adjustResp.Code != http.StatusOK || !strings.Contains(adjustBody, `"type":"manual_debit"`) || !strings.Contains(adjustBody, `"balance_after":30`) {
		t.Fatalf("manual payment adjustment should succeed with manual_debit result, got %d %s", adjustResp.Code, adjustBody)
	}
	if err := internal.DB.First(&alice, alice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if alice.Quota != 30 {
		t.Fatalf("manual payment adjustment should deduct quota, got %d", alice.Quota)
	}
	var quotaTx model.QuotaTransaction
	if err := internal.DB.Where("idempotency_key = ?", "manual-payment-adjust-1").First(&quotaTx).Error; err != nil {
		t.Fatalf("manual payment adjustment should write quota transaction: %v", err)
	}
	if quotaTx.UserID != alice.ID ||
		quotaTx.Type != common.QuotaTransactionTypeManualDebit ||
		quotaTx.Amount != -20 ||
		quotaTx.BalanceBefore != 50 ||
		quotaTx.BalanceAfter != 30 ||
		quotaTx.SourceType != common.QuotaSourceTypePaymentOrder ||
		quotaTx.SourceID != order.OrderNo {
		t.Fatalf("unexpected manual payment quota transaction: %+v", quotaTx)
	}
	if quotaTx.ActorUserID == nil || *quotaTx.ActorUserID != root.ID || quotaTx.Reason != "chargeback correction" {
		t.Fatalf("manual payment quota transaction should include actor and reason: %+v", quotaTx)
	}
	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=payment_order&resource_id="+order.OrderNo, rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(auditBody, `"action":"payment_manual_adjust.debit"`) ||
		!strings.Contains(auditBody, order.OrderNo) ||
		!strings.Contains(auditBody, "chargeback correction") ||
		!strings.Contains(auditBody, "manual-payment-adjust-1") {
		t.Fatalf("manual payment adjustment should write payment order audit log, got %d %s", auditResp.Code, auditBody)
	}
}

func TestAdminPaymentManualRefundMarksOrderAndDeductsQuota(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	createResp := performJSON(r, http.MethodPost, "/v0/admin/user", rootJWT, map[string]interface{}{
		"username": "alice",
		"password": "password123",
		"quota":    100,
	})
	if createResp.Code != http.StatusOK {
		t.Fatalf("create user failed: %d %s", createResp.Code, createResp.Body.String())
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	var alice model.User
	if err := internal.DB.Where("username = ?", "alice").First(&alice).Error; err != nil {
		t.Fatal(err)
	}
	order := model.PaymentOrder{
		OrderNo:   "PAYREFUND1000",
		UserID:    alice.ID,
		ProductID: "quota_manual_refund",
		Provider:  common.PaymentProviderStripe,
		Amount:    "9.99",
		Currency:  "usd",
		Quota:     100,
		Status:    common.PaymentOrderStatusPaid,
	}
	if err := internal.DB.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	refundResp := performJSON(r, http.MethodPost, "/v0/admin/payment/refunds", rootJWT, map[string]interface{}{
		"order_no":        order.OrderNo,
		"refund_quota":    40,
		"reason":          "customer refund",
		"idempotency_key": "manual-refund-1",
	})
	refundBody := refundResp.Body.String()
	if refundResp.Code != http.StatusOK ||
		!strings.Contains(refundBody, `"order_status":"partially_refunded"`) ||
		!strings.Contains(refundBody, `"balance_after":60`) {
		t.Fatalf("manual refund should deduct quota and mark partial refund, got %d %s", refundResp.Code, refundBody)
	}
	if err := internal.DB.First(&alice, alice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if alice.Quota != 60 {
		t.Fatalf("manual refund should deduct user quota, got %d", alice.Quota)
	}
	if err := internal.DB.First(&order, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != common.PaymentOrderStatusPartiallyRefunded {
		t.Fatalf("manual partial refund should mark order partially_refunded, got %+v", order)
	}
	var quotaTx model.QuotaTransaction
	if err := internal.DB.Where("idempotency_key = ?", "manual-refund-1").First(&quotaTx).Error; err != nil {
		t.Fatalf("manual refund should write quota transaction: %v", err)
	}
	if quotaTx.UserID != alice.ID ||
		quotaTx.Type != common.QuotaTransactionTypeRefundDeduct ||
		quotaTx.Amount != -40 ||
		quotaTx.BalanceBefore != 100 ||
		quotaTx.BalanceAfter != 60 ||
		quotaTx.SourceType != common.QuotaSourceTypeRefund ||
		quotaTx.SourceID != order.OrderNo {
		t.Fatalf("unexpected manual refund quota transaction: %+v", quotaTx)
	}
	if quotaTx.ActorUserID == nil || *quotaTx.ActorUserID != root.ID || quotaTx.Reason != "customer refund" {
		t.Fatalf("manual refund quota transaction should include actor and reason: %+v", quotaTx)
	}
	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=payment_order&resource_id="+order.OrderNo, rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(auditBody, `"action":"payment_refund.manual"`) ||
		!strings.Contains(auditBody, order.OrderNo) ||
		!strings.Contains(auditBody, "customer refund") ||
		!strings.Contains(auditBody, "manual-refund-1") {
		t.Fatalf("manual refund should write payment refund audit log, got %d %s", auditResp.Code, auditBody)
	}
	duplicateResp := performJSON(r, http.MethodPost, "/v0/admin/payment/refunds", rootJWT, map[string]interface{}{
		"order_no":        order.OrderNo,
		"refund_quota":    40,
		"reason":          "customer refund",
		"idempotency_key": "manual-refund-1",
	})
	if duplicateResp.Code != http.StatusBadRequest {
		t.Fatalf("duplicate manual refund idempotency key should fail, got %d %s", duplicateResp.Code, duplicateResp.Body.String())
	}
	var txCount int64
	if err := internal.DB.Model(&model.QuotaTransaction{}).Where("idempotency_key = ?", "manual-refund-1").Count(&txCount).Error; err != nil {
		t.Fatal(err)
	}
	if txCount != 1 {
		t.Fatalf("duplicate manual refund must not write duplicate transactions, got %d", txCount)
	}
}

func TestAdminStripeRefundRequestCreatesProviderRefundAndPendingOrder(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	t.Setenv("PAYMENT_STRIPE_WEBHOOK_SECRET", "whsec_test_secret")

	var refundAPICalls int32
	stripeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		atomic.AddInt32(&refundAPICalls, 1)
		if req.Method != http.MethodPost || req.URL.Path != "/v1/refunds" {
			t.Fatalf("unexpected stripe refund request: %s %s", req.Method, req.URL.Path)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer sk_test_refund" {
			t.Fatalf("stripe refund should use secret key authorization, got %q", got)
		}
		if got := req.Header.Get("Idempotency-Key"); got != "refund-request-1" {
			t.Fatalf("stripe refund should send idempotency key, got %q", got)
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		expected := map[string]string{
			"payment_intent":            "pi_refund_request_1",
			"amount":                    "500",
			"metadata[order_no]":        "PAYREFREQ1000",
			"metadata[idempotency_key]": "refund-request-1",
			"metadata[reason]":          "customer requested partial refund",
		}
		for key, want := range expected {
			if got := req.Form.Get(key); got != want {
				t.Fatalf("stripe refund form %s = %q, want %q", key, got, want)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"re_refund_request_1","status":"pending"}`))
	}))
	defer stripeAPI.Close()
	t.Setenv("PAYMENT_STRIPE_SECRET_KEY", "sk_test_refund")
	t.Setenv("PAYMENT_STRIPE_API_BASE", stripeAPI.URL)

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	createUserResp := performJSON(r, http.MethodPost, "/v0/admin/user", rootJWT, map[string]interface{}{
		"username": "alice",
		"password": "password123",
		"quota":    100,
	})
	if createUserResp.Code != http.StatusOK {
		t.Fatalf("create user failed: %d %s", createUserResp.Code, createUserResp.Body.String())
	}
	var alice model.User
	if err := internal.DB.Where("username = ?", "alice").First(&alice).Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.Create(&model.PaymentProduct{
		ProductID: "quota_refund_request",
		Name:      "Refund request credits",
		Amount:    "9.99",
		Currency:  "usd",
		Quota:     100,
		Enabled:   true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	settingSvc := service.NewSettingService()
	if err := settingSvc.Set("payment.stripe.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	paymentIntent := "pi_refund_request_1"
	paidAt := time.Now()
	order := model.PaymentOrder{
		OrderNo:           "PAYREFREQ1000",
		UserID:            alice.ID,
		ProductID:         "quota_refund_request",
		Provider:          common.PaymentProviderStripe,
		Amount:            "9.99",
		Currency:          "usd",
		Quota:             100,
		Status:            common.PaymentOrderStatusPaid,
		ProviderPaymentID: &paymentIntent,
		PaidAt:            &paidAt,
	}
	if err := internal.DB.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	refundResp := performJSON(r, http.MethodPost, "/v0/admin/payment/refund-requests", rootJWT, map[string]interface{}{
		"order_no":        order.OrderNo,
		"refund_amount":   "5.00",
		"reason":          "customer requested partial refund",
		"idempotency_key": "refund-request-1",
	})
	refundBody := refundResp.Body.String()
	if refundResp.Code != http.StatusOK ||
		!strings.Contains(refundBody, `"provider_refund_id":"re_refund_request_1"`) ||
		!strings.Contains(refundBody, `"order_status":"refund_pending"`) ||
		!strings.Contains(refundBody, `"refund_quota":50`) {
		t.Fatalf("stripe refund request should create provider refund and pending order, got %d %s", refundResp.Code, refundBody)
	}
	if atomic.LoadInt32(&refundAPICalls) != 1 {
		t.Fatalf("stripe refund API should be called once, got %d", refundAPICalls)
	}
	if err := internal.DB.First(&order, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "refund_pending" {
		t.Fatalf("stripe refund request should mark order refund_pending, got %+v", order)
	}
	var refundRequest struct {
		OrderNo          string
		Provider         string
		ProviderRefundID string
		Amount           string
		AmountMinor      int64
		Currency         string
		RefundQuota      int64
		Status           string
		IdempotencyKey   string
		Reason           string
		ActorUserID      uint
	}
	if err := internal.DB.Table("payment_refund_requests").Where("idempotency_key = ?", "refund-request-1").First(&refundRequest).Error; err != nil {
		t.Fatalf("stripe refund request should be recorded: %v", err)
	}
	if refundRequest.OrderNo != order.OrderNo ||
		refundRequest.Provider != common.PaymentProviderStripe ||
		refundRequest.ProviderRefundID != "re_refund_request_1" ||
		refundRequest.Amount != "5.00" ||
		refundRequest.AmountMinor != 500 ||
		refundRequest.Currency != "usd" ||
		refundRequest.RefundQuota != 50 ||
		refundRequest.Status != "pending" ||
		refundRequest.Reason != "customer requested partial refund" ||
		refundRequest.ActorUserID == 0 {
		t.Fatalf("unexpected refund request record: %+v", refundRequest)
	}
	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=payment_order&resource_id="+order.OrderNo, rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(auditBody, `"action":"payment_refund.requested"`) ||
		!strings.Contains(auditBody, "re_refund_request_1") ||
		!strings.Contains(auditBody, "refund-request-1") {
		t.Fatalf("stripe refund request should write audit log, got %d %s", auditResp.Code, auditBody)
	}

	webhookBody := stripeChargeRefundedPayload("evt_refund_requested_1", order, "pi_refund_request_1", 500)
	webhookResp := performStripeWebhook(r, webhookBody, "whsec_test_secret")
	if webhookResp.Code != http.StatusOK || strings.TrimSpace(webhookResp.Body.String()) != "success" {
		t.Fatalf("stripe refund webhook should finalize pending refund, got %d %s", webhookResp.Code, webhookResp.Body.String())
	}
	if err := internal.DB.First(&order, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != common.PaymentOrderStatusPartiallyRefunded {
		t.Fatalf("stripe refund webhook should finalize pending refund, got %+v", order)
	}
}

func TestAdminEpayRefundRequestCreatesProviderRefundAndPendingOrder(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	t.Setenv("PAYMENT_EPAY_KEY", "epay-refund-secret")

	var refundAPICalls int32
	epayAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		atomic.AddInt32(&refundAPICalls, 1)
		if req.Method != http.MethodPost || req.URL.Path != "/refund" {
			t.Fatalf("unexpected epay refund request: %s %s", req.Method, req.URL.Path)
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		expected := map[string]string{
			"act":             "refund",
			"pid":             "merchant-epay-1",
			"out_trade_no":    "PAYEPAYREF1000",
			"money":           "5.00",
			"reason":          "customer requested epay partial refund",
			"idempotency_key": "epay-refund-request-1",
		}
		for key, want := range expected {
			if got := req.Form.Get(key); got != want {
				t.Fatalf("epay refund form %s = %q, want %q", key, got, want)
			}
		}
		if req.Form.Get("sign_type") != "MD5" || req.Form.Get("sign") != epaySign(req.Form, "epay-refund-secret") {
			t.Fatalf("epay refund sign mismatch: %s", req.Form.Encode())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1,"msg":"success","refund_no":"epay_refund_1","status":"pending"}`))
	}))
	defer epayAPI.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	createUserResp := performJSON(r, http.MethodPost, "/v0/admin/user", rootJWT, map[string]interface{}{
		"username": "alice",
		"password": "password123",
		"quota":    100,
	})
	if createUserResp.Code != http.StatusOK {
		t.Fatalf("create user failed: %d %s", createUserResp.Code, createUserResp.Body.String())
	}
	var alice model.User
	if err := internal.DB.Where("username = ?", "alice").First(&alice).Error; err != nil {
		t.Fatal(err)
	}
	settingSvc := service.NewSettingService()
	if err := settingSvc.Set("payment.epay.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("payment.epay.pid", "merchant-epay-1"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("payment.epay.refund_url", epayAPI.URL+"/refund"); err != nil {
		t.Fatal(err)
	}
	paidAt := time.Now()
	providerPaymentID := "TRADE_EP_REF_1"
	order := model.PaymentOrder{
		OrderNo:           "PAYEPAYREF1000",
		UserID:            alice.ID,
		ProductID:         "quota_epay_refund_request",
		Provider:          common.PaymentProviderEpay,
		Amount:            "9.99",
		Currency:          "cny",
		Quota:             100,
		Status:            common.PaymentOrderStatusPaid,
		ProviderPaymentID: &providerPaymentID,
		PaidAt:            &paidAt,
	}
	if err := internal.DB.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	refundResp := performJSON(r, http.MethodPost, "/v0/admin/payment/refund-requests", rootJWT, map[string]interface{}{
		"order_no":        order.OrderNo,
		"refund_amount":   "5.00",
		"reason":          "customer requested epay partial refund",
		"idempotency_key": "epay-refund-request-1",
	})
	refundBody := refundResp.Body.String()
	if refundResp.Code != http.StatusOK ||
		!strings.Contains(refundBody, `"provider":"epay"`) ||
		!strings.Contains(refundBody, `"provider_refund_id":"epay_refund_1"`) ||
		!strings.Contains(refundBody, `"order_status":"refund_pending"`) ||
		!strings.Contains(refundBody, `"refund_quota":50`) {
		t.Fatalf("epay refund request should create provider refund and pending order, got %d %s", refundResp.Code, refundBody)
	}
	if atomic.LoadInt32(&refundAPICalls) != 1 {
		t.Fatalf("epay refund API should be called once, got %d", refundAPICalls)
	}
	if err := internal.DB.First(&order, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != common.PaymentOrderStatusRefundPending {
		t.Fatalf("epay refund request should mark order refund_pending, got %+v", order)
	}
	var refundRequest struct {
		OrderNo          string
		Provider         string
		ProviderRefundID string
		Amount           string
		AmountMinor      int64
		Currency         string
		RefundQuota      int64
		Status           string
		IdempotencyKey   string
		Reason           string
		ActorUserID      uint
	}
	if err := internal.DB.Table("payment_refund_requests").Where("idempotency_key = ?", "epay-refund-request-1").First(&refundRequest).Error; err != nil {
		t.Fatalf("epay refund request should be recorded: %v", err)
	}
	if refundRequest.OrderNo != order.OrderNo ||
		refundRequest.Provider != common.PaymentProviderEpay ||
		refundRequest.ProviderRefundID != "epay_refund_1" ||
		refundRequest.Amount != "5.00" ||
		refundRequest.AmountMinor != 500 ||
		refundRequest.Currency != "cny" ||
		refundRequest.RefundQuota != 50 ||
		refundRequest.Status != "pending" ||
		refundRequest.Reason != "customer requested epay partial refund" ||
		refundRequest.ActorUserID == 0 {
		t.Fatalf("unexpected epay refund request record: %+v", refundRequest)
	}
	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=payment_order&resource_id="+order.OrderNo, rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(auditBody, `"action":"payment_refund.requested"`) ||
		!strings.Contains(auditBody, "epay_refund_1") ||
		!strings.Contains(auditBody, "epay-refund-request-1") {
		t.Fatalf("epay refund request should write audit log, got %d %s", auditResp.Code, auditBody)
	}

	duplicateResp := performJSON(r, http.MethodPost, "/v0/admin/payment/refund-requests", rootJWT, map[string]interface{}{
		"order_no":        order.OrderNo,
		"refund_amount":   "5.00",
		"reason":          "customer requested epay partial refund",
		"idempotency_key": "epay-refund-request-1",
	})
	if duplicateResp.Code != http.StatusBadRequest {
		t.Fatalf("duplicate epay refund request idempotency key should fail, got %d %s", duplicateResp.Code, duplicateResp.Body.String())
	}
	if atomic.LoadInt32(&refundAPICalls) != 1 {
		t.Fatalf("duplicate epay refund request must not call provider again, got %d", refundAPICalls)
	}
}

func TestRedemCodeBatchNoteAndExpirationPolicy(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(0)).Error; err != nil {
		t.Fatal(err)
	}

	future := time.Now().Add(24 * time.Hour).Unix()
	createResp := performJSON(r, http.MethodPost, "/v0/admin/redem", rootJWT, map[string]interface{}{
		"quota":      33,
		"codes":      []string{"BATCH-CODE-1"},
		"batch_no":   "launch-2026",
		"note":       "private beta",
		"expired_at": future,
	})
	createBody := createResp.Body.String()
	if createResp.Code != http.StatusOK ||
		!strings.Contains(createBody, `"batch_no":"launch-2026"`) ||
		!strings.Contains(createBody, `"note":"private beta"`) ||
		!strings.Contains(createBody, `"expired_at"`) {
		t.Fatalf("redem code create should return batch, note and expiry, got %d %s", createResp.Code, createBody)
	}
	var stored struct {
		BatchNo   string
		Note      string
		ExpiredAt *time.Time
	}
	if err := internal.DB.Table("redem_codes").Select("batch_no, note, expired_at").Where("code = ?", "BATCH-CODE-1").Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.BatchNo != "launch-2026" || stored.Note != "private beta" || stored.ExpiredAt == nil {
		t.Fatalf("redem code metadata should be stored, got %+v", stored)
	}

	listResp := performJSON(r, http.MethodGet, "/v0/admin/redem?batch_no=launch-2026", rootJWT, nil)
	if listResp.Code != http.StatusOK || !strings.Contains(listResp.Body.String(), "BATCH-CODE-1") {
		t.Fatalf("admin should filter redem codes by batch_no, got %d %s", listResp.Code, listResp.Body.String())
	}
	pastCreate := performJSON(r, http.MethodPost, "/v0/admin/redem", rootJWT, map[string]interface{}{
		"quota":      10,
		"codes":      []string{"PAST-CODE-1"},
		"expired_at": time.Now().Add(-time.Hour).Unix(),
	})
	if pastCreate.Code != http.StatusBadRequest {
		t.Fatalf("redem code creation should reject past expired_at, got %d %s", pastCreate.Code, pastCreate.Body.String())
	}

	expiredAt := time.Now().Add(-time.Hour)
	if err := internal.DB.Exec(
		"INSERT INTO redem_codes (code, quota, status, batch_no, note, expired_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"EXPIRED-CODE-1", int64(44), common.RedemCodeStatusUnused, "legacy", "expired fixture", expiredAt, time.Now(),
	).Error; err != nil {
		t.Fatal(err)
	}
	expiredRedeem := performJSON(r, http.MethodPost, "/v0/user/redem", rootJWT, map[string]interface{}{
		"code": "EXPIRED-CODE-1",
	})
	if expiredRedeem.Code != http.StatusBadRequest {
		t.Fatalf("expired redem code should not be redeemable, got %d %s", expiredRedeem.Code, expiredRedeem.Body.String())
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 0 {
		t.Fatalf("expired redem code must not grant quota, got %d", root.Quota)
	}
}

func TestAdminManagesRedemCodes(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	generateResp := performJSON(r, http.MethodPost, "/v0/admin/redem", rootJWT, map[string]interface{}{
		"quota": 40,
		"count": 2,
	})
	if generateResp.Code != http.StatusOK || !strings.Contains(generateResp.Body.String(), `"quota":40`) {
		t.Fatalf("admin should generate redem codes, got %d %s", generateResp.Code, generateResp.Body.String())
	}
	importResp := performJSON(r, http.MethodPost, "/v0/admin/redem", rootJWT, map[string]interface{}{
		"quota": 55,
		"codes": []string{"IMPORT-CREDIT-1"},
	})
	if importResp.Code != http.StatusOK || !strings.Contains(importResp.Body.String(), `"code":"IMPORT-CREDIT-1"`) {
		t.Fatalf("admin should import explicit redem code, got %d %s", importResp.Code, importResp.Body.String())
	}

	var codes []model.RedemCode
	if err := internal.DB.Order("id ASC").Find(&codes).Error; err != nil {
		t.Fatal(err)
	}
	if len(codes) != 3 {
		t.Fatalf("expected three redem codes, got %d", len(codes))
	}
	listResp := performJSON(r, http.MethodGet, "/v0/admin/redem?status=0", rootJWT, nil)
	if listResp.Code != http.StatusOK || !strings.Contains(listResp.Body.String(), `"total":3`) || !strings.Contains(listResp.Body.String(), `"code":"IMPORT-CREDIT-1"`) {
		t.Fatalf("admin should list unused redem codes, got %d %s", listResp.Code, listResp.Body.String())
	}

	disableResp := performJSON(r, http.MethodPatch, "/v0/admin/redem/"+uintString(codes[0].ID)+"/disable", rootJWT, nil)
	if disableResp.Code != http.StatusOK {
		t.Fatalf("admin should disable unused redem code, got %d %s", disableResp.Code, disableResp.Body.String())
	}
	var disabled model.RedemCode
	if err := internal.DB.First(&disabled, codes[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if disabled.Status != common.RedemCodeStatusDisabled {
		t.Fatalf("disabled redem code should be marked disabled, got %+v", disabled)
	}
	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=redem_code", rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(auditBody, `"action":"redem_code.create"`) ||
		!strings.Contains(auditBody, `"action":"redem_code.disable"`) ||
		!strings.Contains(auditBody, `"resource_id":"`+uintString(disabled.ID)+`"`) {
		t.Fatalf("admin redem management should write audit logs, got %d %s", auditResp.Code, auditBody)
	}
	redeemDisabled := performJSON(r, http.MethodPost, "/v0/user/redem", rootJWT, map[string]interface{}{
		"code": codes[0].Code,
	})
	if redeemDisabled.Code != http.StatusBadRequest {
		t.Fatalf("disabled redem code should not be redeemable, got %d %s", redeemDisabled.Code, redeemDisabled.Body.String())
	}
}

func TestUserListsAvailableModels(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Create(&model.Channel{Idx: 1, Type: common.ChannelTypeOpenAICompat, Name: "enabled", Models: "gpt-b,gpt-a,gpt-a", Status: common.ChannelStatusEnabled}).Error; err != nil {
		t.Fatal(err)
	}
	disabled := model.Channel{Idx: 2, Type: common.ChannelTypeOpenAICompat, Name: "disabled", Models: "gpt-hidden", Status: common.ChannelStatusEnabled}
	if err := internal.DB.Create(&disabled).Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.Model(&model.Channel{}).Where("id = ?", disabled.ID).Update("status", common.ChannelStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}

	resp := performJSON(r, http.MethodGet, "/v0/user/models", rootJWT, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("user models failed: %d %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	if !strings.Contains(body, `"id":"gpt-a"`) || !strings.Contains(body, `"id":"gpt-b"`) || strings.Contains(body, "gpt-hidden") {
		t.Fatalf("user models should list enabled channel models only, got %s", body)
	}
	if !strings.Contains(body, `"pricing_ready":false`) || !strings.Contains(body, `"price_rule":"minimum_usage"`) {
		t.Fatalf("user models should expose current pricing readiness, got %s", body)
	}
}

func TestAdminModelPriceManagementUpdatesUserModelPricing(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Create(&model.Channel{Idx: 1, Type: common.ChannelTypeOpenAICompat, Name: "priced-channel", Models: "gpt-priced,gpt-unpriced", Status: common.ChannelStatusEnabled}).Error; err != nil {
		t.Fatal(err)
	}

	createResp := performJSON(r, http.MethodPost, "/v0/admin/model-prices", rootJWT, map[string]interface{}{
		"model":            "gpt-priced",
		"price_mode":       "token",
		"price_expression": "prompt_tokens + completion_tokens",
		"variables_json": map[string]interface{}{
			"prompt_price":     1,
			"completion_price": 1,
		},
		"unit_tokens": 1000,
		"enabled":     true,
	})
	var createPayload struct {
		Data struct {
			ID          uint   `json:"id"`
			Model       string `json:"model"`
			PriceMode   string `json:"price_mode"`
			RuleVersion int64  `json:"rule_version"`
			Enabled     bool   `json:"enabled"`
		} `json:"data"`
	}
	if createResp.Code != http.StatusOK {
		t.Fatalf("admin should create model price, got %d %s", createResp.Code, createResp.Body.String())
	}
	if err := json.Unmarshal(createResp.Body.Bytes(), &createPayload); err != nil {
		t.Fatal(err)
	}
	if createPayload.Data.ID == 0 || createPayload.Data.Model != "gpt-priced" || createPayload.Data.PriceMode != "token" || createPayload.Data.RuleVersion != 1 || !createPayload.Data.Enabled {
		t.Fatalf("admin should create model price with initial version, got %d %s", createResp.Code, createResp.Body.String())
	}

	assertUserModelPricing := func(modelID, priceRule string, ready bool) {
		t.Helper()
		resp := performJSON(r, http.MethodGet, "/v0/user/models", rootJWT, nil)
		if resp.Code != http.StatusOK {
			t.Fatalf("user models failed: %d %s", resp.Code, resp.Body.String())
		}
		var payload struct {
			Data struct {
				Models []struct {
					ID           string `json:"id"`
					PriceRule    string `json:"price_rule"`
					PricingReady bool   `json:"pricing_ready"`
				} `json:"models"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		for _, item := range payload.Data.Models {
			if item.ID == modelID {
				if item.PriceRule != priceRule || item.PricingReady != ready {
					t.Fatalf("model %s pricing mismatch, got rule=%s ready=%v in %s", modelID, item.PriceRule, item.PricingReady, resp.Body.String())
				}
				return
			}
		}
		t.Fatalf("model %s not found in %s", modelID, resp.Body.String())
	}
	assertUserModelPricing("gpt-priced", "model_price:token:v1", true)
	assertUserModelPricing("gpt-unpriced", "minimum_usage", false)

	updateResp := performJSON(r, http.MethodPut, "/v0/admin/model-prices/"+uintString(createPayload.Data.ID), rootJWT, map[string]interface{}{
		"model":            "gpt-priced",
		"price_mode":       "request",
		"price_expression": "request_price",
		"variables_json": map[string]interface{}{
			"request_price": 2,
		},
		"unit_tokens": 1,
		"enabled":     true,
	})
	if updateResp.Code != http.StatusOK || !strings.Contains(updateResp.Body.String(), `"price_mode":"request"`) || !strings.Contains(updateResp.Body.String(), `"rule_version":2`) {
		t.Fatalf("admin should update model price and bump version, got %d %s", updateResp.Code, updateResp.Body.String())
	}
	assertUserModelPricing("gpt-priced", "model_price:request:v2", true)

	adminList := performJSON(r, http.MethodGet, "/v0/admin/model-prices?keyword=gpt-priced", rootJWT, nil)
	if adminList.Code != http.StatusOK || !strings.Contains(adminList.Body.String(), `"model":"gpt-priced"`) || !strings.Contains(adminList.Body.String(), `"enabled":true`) {
		t.Fatalf("admin should list model prices, got %d %s", adminList.Code, adminList.Body.String())
	}

	disableResp := performJSON(r, http.MethodPatch, "/v0/admin/model-prices/"+uintString(createPayload.Data.ID)+"/disable", rootJWT, nil)
	if disableResp.Code != http.StatusOK {
		t.Fatalf("admin should disable model price, got %d %s", disableResp.Code, disableResp.Body.String())
	}
	assertUserModelPricing("gpt-priced", "minimum_usage", false)

	enableResp := performJSON(r, http.MethodPatch, "/v0/admin/model-prices/"+uintString(createPayload.Data.ID)+"/enable", rootJWT, nil)
	if enableResp.Code != http.StatusOK {
		t.Fatalf("admin should enable model price, got %d %s", enableResp.Code, enableResp.Body.String())
	}
	assertUserModelPricing("gpt-priced", "model_price:request:v4", true)

	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=model_price&resource_id=gpt-priced", rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(auditBody, `"action":"model_price.create"`) ||
		!strings.Contains(auditBody, `"action":"model_price.update"`) ||
		!strings.Contains(auditBody, `"action":"model_price.disable"`) ||
		!strings.Contains(auditBody, `"action":"model_price.enable"`) {
		t.Fatalf("model price management should write audit logs, got %d %s", auditResp.Code, auditBody)
	}
}

func TestAdminChannelModelPriceControlsUserModelPricingAndVisibility(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	channel := model.Channel{Idx: 1, Type: common.ChannelTypeOpenAICompat, Name: "priced-channel", Models: "gpt-overridden,gpt-blocked", Status: common.ChannelStatusEnabled}
	if err := internal.DB.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}

	systemPrice := performJSON(r, http.MethodPost, "/v0/admin/model-prices", rootJWT, map[string]interface{}{
		"model":            "gpt-overridden",
		"price_mode":       "token",
		"price_expression": "prompt_tokens + completion_tokens",
		"unit_tokens":      1000,
		"enabled":          true,
	})
	if systemPrice.Code != http.StatusOK {
		t.Fatalf("admin should create fallback system price, got %d %s", systemPrice.Code, systemPrice.Body.String())
	}

	createResp := performJSON(r, http.MethodPost, "/v0/admin/channel-model-prices", rootJWT, map[string]interface{}{
		"channel_id":       channel.ID,
		"model":            "gpt-overridden",
		"enabled":          true,
		"user_enabled":     true,
		"price_mode":       "request",
		"override_mode":    "override",
		"price_expression": "request_price",
		"variables_json": map[string]interface{}{
			"request_price": 2,
		},
		"unit_tokens": 1,
	})
	var createPayload struct {
		Data struct {
			ID          uint   `json:"id"`
			ChannelID   uint   `json:"channel_id"`
			Model       string `json:"model"`
			PriceMode   string `json:"price_mode"`
			RuleVersion int64  `json:"rule_version"`
			Enabled     bool   `json:"enabled"`
			UserEnabled bool   `json:"user_enabled"`
		} `json:"data"`
	}
	if createResp.Code != http.StatusOK {
		t.Fatalf("admin should create channel model price, got %d %s", createResp.Code, createResp.Body.String())
	}
	if err := json.Unmarshal(createResp.Body.Bytes(), &createPayload); err != nil {
		t.Fatal(err)
	}
	if createPayload.Data.ID == 0 ||
		createPayload.Data.ChannelID != channel.ID ||
		createPayload.Data.Model != "gpt-overridden" ||
		createPayload.Data.PriceMode != "request" ||
		createPayload.Data.RuleVersion != 1 ||
		!createPayload.Data.Enabled ||
		!createPayload.Data.UserEnabled {
		t.Fatalf("admin should create channel model price with initial version, got %d %s", createResp.Code, createResp.Body.String())
	}

	blockedResp := performJSON(r, http.MethodPost, "/v0/admin/channel-model-prices", rootJWT, map[string]interface{}{
		"channel_id":       channel.ID,
		"model":            "gpt-blocked",
		"enabled":          true,
		"user_enabled":     false,
		"price_mode":       "token",
		"override_mode":    "override",
		"price_expression": "prompt_tokens + completion_tokens",
		"unit_tokens":      1000,
	})
	if blockedResp.Code != http.StatusOK {
		t.Fatalf("admin should create hidden channel model price, got %d %s", blockedResp.Code, blockedResp.Body.String())
	}

	assertUserModelPricing := func(modelID, priceRule string, ready bool, visible bool) {
		t.Helper()
		resp := performJSON(r, http.MethodGet, "/v0/user/models", rootJWT, nil)
		if resp.Code != http.StatusOK {
			t.Fatalf("user models failed: %d %s", resp.Code, resp.Body.String())
		}
		var payload struct {
			Data struct {
				Models []struct {
					ID           string `json:"id"`
					PriceRule    string `json:"price_rule"`
					PricingReady bool   `json:"pricing_ready"`
				} `json:"models"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		for _, item := range payload.Data.Models {
			if item.ID == modelID {
				if !visible {
					t.Fatalf("model %s should be hidden, got %s", modelID, resp.Body.String())
				}
				if item.PriceRule != priceRule || item.PricingReady != ready {
					t.Fatalf("model %s pricing mismatch, got rule=%s ready=%v in %s", modelID, item.PriceRule, item.PricingReady, resp.Body.String())
				}
				return
			}
		}
		if visible {
			t.Fatalf("model %s not found in %s", modelID, resp.Body.String())
		}
	}
	assertUserModelPricing("gpt-overridden", "channel_model_price:request:v1", true, true)
	assertUserModelPricing("gpt-blocked", "", false, false)

	updateResp := performJSON(r, http.MethodPut, "/v0/admin/channel-model-prices/"+uintString(createPayload.Data.ID), rootJWT, map[string]interface{}{
		"channel_id":       channel.ID,
		"model":            "gpt-overridden",
		"enabled":          true,
		"user_enabled":     true,
		"price_mode":       "second",
		"override_mode":    "override",
		"price_expression": "seconds * second_price",
		"variables_json": map[string]interface{}{
			"second_price": 3,
		},
		"unit_tokens": 1,
	})
	if updateResp.Code != http.StatusOK || !strings.Contains(updateResp.Body.String(), `"price_mode":"second"`) || !strings.Contains(updateResp.Body.String(), `"rule_version":2`) {
		t.Fatalf("admin should update channel model price and bump version, got %d %s", updateResp.Code, updateResp.Body.String())
	}
	assertUserModelPricing("gpt-overridden", "channel_model_price:second:v2", true, true)

	adminList := performJSON(r, http.MethodGet, "/v0/admin/channel-model-prices?channel_id="+uintString(channel.ID)+"&keyword=gpt-overridden", rootJWT, nil)
	if adminList.Code != http.StatusOK || !strings.Contains(adminList.Body.String(), `"model":"gpt-overridden"`) || !strings.Contains(adminList.Body.String(), `"channel_id":`+uintString(channel.ID)) {
		t.Fatalf("admin should list channel model prices, got %d %s", adminList.Code, adminList.Body.String())
	}

	disableResp := performJSON(r, http.MethodPatch, "/v0/admin/channel-model-prices/"+uintString(createPayload.Data.ID)+"/disable", rootJWT, nil)
	if disableResp.Code != http.StatusOK {
		t.Fatalf("admin should disable channel model price, got %d %s", disableResp.Code, disableResp.Body.String())
	}
	assertUserModelPricing("gpt-overridden", "model_price:token:v1", true, true)

	enableResp := performJSON(r, http.MethodPatch, "/v0/admin/channel-model-prices/"+uintString(createPayload.Data.ID)+"/enable", rootJWT, nil)
	if enableResp.Code != http.StatusOK {
		t.Fatalf("admin should enable channel model price, got %d %s", enableResp.Code, enableResp.Body.String())
	}
	assertUserModelPricing("gpt-overridden", "channel_model_price:second:v4", true, true)

	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=channel_model_price&resource_id="+uintString(channel.ID)+":gpt-overridden", rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(auditBody, `"action":"channel_model_price.create"`) ||
		!strings.Contains(auditBody, `"action":"channel_model_price.update"`) ||
		!strings.Contains(auditBody, `"action":"channel_model_price.disable"`) ||
		!strings.Contains(auditBody, `"action":"channel_model_price.enable"`) {
		t.Fatalf("channel model price management should write audit logs, got %d %s", auditResp.Code, auditBody)
	}
}

func TestUserCreatesAndListsPaymentOrders(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(0)).Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.Create(&model.PaymentProduct{
		ProductID:  "quota_100",
		Name:       "100 credits",
		Amount:     "9.99",
		Currency:   "usd",
		Quota:      100,
		BonusQuota: 20,
		Enabled:    true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("payment.order_expire_minutes", "5"); err != nil {
		t.Fatal(err)
	}

	productsResp := performJSON(r, http.MethodGet, "/v0/user/payment/products", rootJWT, nil)
	if productsResp.Code != http.StatusOK || !strings.Contains(productsResp.Body.String(), `"product_id":"quota_100"`) || !strings.Contains(productsResp.Body.String(), `"quota":120`) {
		t.Fatalf("user should list enabled payment products, got %d %s", productsResp.Code, productsResp.Body.String())
	}
	disabledProviderResp := performJSON(r, http.MethodPost, "/v0/user/payment/orders", rootJWT, map[string]interface{}{
		"provider":   "stripe",
		"product_id": "quota_100",
	})
	if disabledProviderResp.Code != http.StatusBadRequest {
		t.Fatalf("disabled payment provider should reject new orders, got %d %s", disabledProviderResp.Code, disabledProviderResp.Body.String())
	}
	if err := service.NewSettingService().Set("payment.stripe.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	createResp := performJSON(r, http.MethodPost, "/v0/user/payment/orders", rootJWT, map[string]interface{}{
		"provider":   "stripe",
		"product_id": "quota_100",
		"pay_type":   "card",
		"return_url": "https://app.example.com/billing/result",
	})
	if createResp.Code != http.StatusOK || !strings.Contains(createResp.Body.String(), `"status":"pending"`) || !strings.Contains(createResp.Body.String(), `"quota":120`) || !strings.Contains(createResp.Body.String(), `"checkout_url"`) {
		t.Fatalf("user should create pending payment order, got %d %s", createResp.Code, createResp.Body.String())
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	var order model.PaymentOrder
	if err := internal.DB.Where("user_id = ? AND product_id = ?", root.ID, "quota_100").First(&order).Error; err != nil {
		t.Fatalf("payment order should be stored: %v", err)
	}
	if order.Status != common.PaymentOrderStatusPending || order.Quota != 120 || order.Amount != "9.99" || order.Currency != "usd" || order.ExpiredAt == nil {
		t.Fatalf("unexpected payment order snapshot: %+v", order)
	}
	expiresIn := order.ExpiredAt.Sub(order.CreatedAt)
	if expiresIn < 4*time.Minute || expiresIn > 6*time.Minute {
		t.Fatalf("payment order should use configured expiration, got %s", expiresIn)
	}
	if root.Quota != 0 {
		t.Fatalf("pending payment order must not grant quota, got %d", root.Quota)
	}
	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=payment_order&resource_id="+uintString(order.ID), rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK || !strings.Contains(auditBody, `"action":"payment_order.create"`) || !strings.Contains(auditBody, order.OrderNo) {
		t.Fatalf("payment order creation should write audit log, got %d %s", auditResp.Code, auditBody)
	}
	if strings.Contains(auditBody, "checkout_url") {
		t.Fatalf("payment order audit should not store checkout URL: %s", auditBody)
	}
	listResp := performJSON(r, http.MethodGet, "/v0/user/payment/orders", rootJWT, nil)
	if listResp.Code != http.StatusOK || !strings.Contains(listResp.Body.String(), order.OrderNo) {
		t.Fatalf("user should list own payment orders, got %d %s", listResp.Code, listResp.Body.String())
	}
	detailResp := performJSON(r, http.MethodGet, "/v0/user/payment/orders/"+order.OrderNo, rootJWT, nil)
	if detailResp.Code != http.StatusOK || !strings.Contains(detailResp.Body.String(), `"order_no":"`+order.OrderNo+`"`) {
		t.Fatalf("user should fetch own payment order detail, got %d %s", detailResp.Code, detailResp.Body.String())
	}
}

func TestUserCancelsPendingPaymentOrder(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(0)).Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.Create(&model.PaymentProduct{
		ProductID: "quota_cancel",
		Name:      "Cancel credits",
		Amount:    "4.99",
		Currency:  "usd",
		Quota:     50,
		Enabled:   true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("payment.stripe.enabled", "true"); err != nil {
		t.Fatal(err)
	}

	createOrder := func() string {
		resp := performJSON(r, http.MethodPost, "/v0/user/payment/orders", rootJWT, map[string]interface{}{
			"provider":   "stripe",
			"product_id": "quota_cancel",
			"return_url": "https://app.example.com/billing/result",
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("create payment order failed: %d %s", resp.Code, resp.Body.String())
		}
		var payload struct {
			Data struct {
				OrderNo string `json:"order_no"`
				Status  string `json:"status"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Data.OrderNo == "" || payload.Data.Status != common.PaymentOrderStatusPending {
			t.Fatalf("unexpected created order payload: %s", resp.Body.String())
		}
		return payload.Data.OrderNo
	}

	orderNo := createOrder()
	cancelResp := performJSON(r, http.MethodPost, "/v0/user/payment/orders/"+orderNo+"/cancel", rootJWT, nil)
	if cancelResp.Code != http.StatusOK || !strings.Contains(cancelResp.Body.String(), `"status":"closed"`) {
		t.Fatalf("user should cancel pending payment order, got %d %s", cancelResp.Code, cancelResp.Body.String())
	}
	var order model.PaymentOrder
	if err := internal.DB.Where("order_no = ?", orderNo).First(&order).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != common.PaymentOrderStatusClosed {
		t.Fatalf("payment order should be closed after cancellation, got %+v", order)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 0 {
		t.Fatalf("cancelled payment order must not grant quota, got %d", root.Quota)
	}
	var cancelAuditCount int64
	if err := internal.DB.Model(&model.AdminAuditLog{}).
		Where("action = ? AND resource_type = ? AND resource_id = ?", "payment_order.cancel", "payment_order", fmt.Sprint(order.ID)).
		Count(&cancelAuditCount).Error; err != nil {
		t.Fatal(err)
	}
	if cancelAuditCount != 1 {
		t.Fatalf("payment order cancellation should write one audit log, got %d", cancelAuditCount)
	}

	secondCancelResp := performJSON(r, http.MethodPost, "/v0/user/payment/orders/"+orderNo+"/cancel", rootJWT, nil)
	if secondCancelResp.Code != http.StatusOK || !strings.Contains(secondCancelResp.Body.String(), `"status":"closed"`) {
		t.Fatalf("cancelled payment order should be idempotent, got %d %s", secondCancelResp.Code, secondCancelResp.Body.String())
	}
	cancelAuditCount = 0
	if err := internal.DB.Model(&model.AdminAuditLog{}).
		Where("action = ? AND resource_type = ? AND resource_id = ?", "payment_order.cancel", "payment_order", fmt.Sprint(order.ID)).
		Count(&cancelAuditCount).Error; err != nil {
		t.Fatal(err)
	}
	if cancelAuditCount != 1 {
		t.Fatalf("idempotent cancellation must not duplicate audit logs, got %d", cancelAuditCount)
	}

	paidOrderNo := createOrder()
	now := time.Now()
	if err := internal.DB.Model(&model.PaymentOrder{}).Where("order_no = ?", paidOrderNo).Updates(map[string]interface{}{
		"status":  common.PaymentOrderStatusPaid,
		"paid_at": &now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	paidCancelResp := performJSON(r, http.MethodPost, "/v0/user/payment/orders/"+paidOrderNo+"/cancel", rootJWT, nil)
	if paidCancelResp.Code != http.StatusBadRequest {
		t.Fatalf("paid payment order should reject cancellation, got %d %s", paidCancelResp.Code, paidCancelResp.Body.String())
	}
	var paidOrder model.PaymentOrder
	if err := internal.DB.Where("order_no = ?", paidOrderNo).First(&paidOrder).Error; err != nil {
		t.Fatal(err)
	}
	if paidOrder.Status != common.PaymentOrderStatusPaid {
		t.Fatalf("paid payment order must remain paid after cancellation attempt, got %+v", paidOrder)
	}
}

func TestStripeOrderCreatesCheckoutSessionWhenConfigured(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	t.Setenv("PAYMENT_STRIPE_SECRET_KEY", "sk_test_routerx")
	var called atomic.Bool
	stripeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		called.Store(true)
		if req.Method != http.MethodPost || req.URL.Path != "/v1/checkout/sessions" {
			t.Errorf("unexpected stripe request: %s %s", req.Method, req.URL.Path)
			http.NotFound(w, req)
			return
		}
		if got := req.Header.Get("Authorization"); got != "Bearer sk_test_routerx" {
			t.Errorf("unexpected stripe authorization header: %q", got)
		}
		if err := req.ParseForm(); err != nil {
			t.Errorf("parse stripe form: %v", err)
		}
		orderNo := req.PostForm.Get("metadata[order_no]")
		if orderNo == "" || !strings.HasPrefix(orderNo, "pay_") {
			t.Errorf("stripe metadata should include generated order_no, got %q", orderNo)
		}
		expected := map[string]string{
			"mode":                                   "payment",
			"client_reference_id":                    orderNo,
			"success_url":                            "https://app.example.com/billing/success",
			"cancel_url":                             "https://app.example.com/billing/success",
			"line_items[0][price_data][currency]":    "usd",
			"line_items[0][price_data][unit_amount]": "999",
			"line_items[0][price_data][product_data][name]":       "100 credits",
			"line_items[0][quantity]":                             "1",
			"metadata[product_id]":                                "quota_stripe_session",
			"metadata[user_id]":                                   "1",
			"payment_intent_data[metadata][order_no]":             orderNo,
			"payment_intent_data[metadata][product_id]":           "quota_stripe_session",
			"payment_intent_data[metadata][user_id]":              "1",
			"payment_intent_data[metadata][routerx_order_source]": "payment_order",
		}
		for key, want := range expected {
			if got := req.PostForm.Get(key); got != want {
				t.Errorf("stripe form %s = %q, want %q", key, got, want)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_test_123","url":"https://checkout.stripe.com/c/session_123"}`))
	}))
	defer stripeAPI.Close()
	t.Setenv("PAYMENT_STRIPE_API_BASE", stripeAPI.URL)
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Create(&model.PaymentProduct{
		ProductID: "quota_stripe_session",
		Name:      "100 credits",
		Amount:    "9.99",
		Currency:  "usd",
		Quota:     100,
		Enabled:   true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("payment.stripe.enabled", "true"); err != nil {
		t.Fatal(err)
	}

	createResp := performJSON(r, http.MethodPost, "/v0/user/payment/orders", rootJWT, map[string]interface{}{
		"provider":   "stripe",
		"product_id": "quota_stripe_session",
		"return_url": "https://app.example.com/billing/success",
	})
	if createResp.Code != http.StatusOK || !strings.Contains(createResp.Body.String(), "https://checkout.stripe.com/c/session_123") {
		t.Fatalf("stripe order should return checkout session URL, got %d %s", createResp.Code, createResp.Body.String())
	}
	if !called.Load() {
		t.Fatal("stripe checkout session API was not called")
	}
	var order model.PaymentOrder
	if err := internal.DB.Where("provider = ? AND product_id = ?", common.PaymentProviderStripe, "quota_stripe_session").First(&order).Error; err != nil {
		t.Fatal(err)
	}
	if order.ProviderOrderID == nil || *order.ProviderOrderID != "cs_test_123" || order.CheckoutURL == nil || *order.CheckoutURL != "https://checkout.stripe.com/c/session_123" {
		t.Fatalf("stripe order should store checkout session identifiers, got %+v", order)
	}
}

func TestAdminManagesPaymentProducts(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	createResp := performJSON(r, http.MethodPost, "/v0/admin/payment/products", rootJWT, map[string]interface{}{
		"product_id":  "quota_50",
		"name":        "50 credits",
		"amount":      "5.00",
		"currency":    "usd",
		"quota":       50,
		"bonus_quota": 5,
		"enabled":     true,
		"provider_config_json": map[string]interface{}{
			"stripe_price_id": "price_50",
		},
	})
	var createPayload struct {
		Data struct {
			ID        uint   `json:"id"`
			ProductID string `json:"product_id"`
			Quota     int64  `json:"quota"`
			Enabled   bool   `json:"enabled"`
		} `json:"data"`
	}
	if createResp.Code != http.StatusOK {
		t.Fatalf("admin should create payment product, got %d %s", createResp.Code, createResp.Body.String())
	}
	if err := json.Unmarshal(createResp.Body.Bytes(), &createPayload); err != nil {
		t.Fatal(err)
	}
	if createPayload.Data.ID == 0 || createPayload.Data.ProductID != "quota_50" || createPayload.Data.Quota != 55 || !createPayload.Data.Enabled {
		t.Fatalf("admin should create payment product, got %d %s", createResp.Code, createResp.Body.String())
	}

	updateResp := performJSON(r, http.MethodPut, "/v0/admin/payment/products/"+uintString(createPayload.Data.ID), rootJWT, map[string]interface{}{
		"product_id":  "quota_50",
		"name":        "60 credits",
		"amount":      "6.00",
		"currency":    "usd",
		"quota":       60,
		"bonus_quota": 10,
		"enabled":     true,
	})
	if updateResp.Code != http.StatusOK || !strings.Contains(updateResp.Body.String(), `"quota":70`) || !strings.Contains(updateResp.Body.String(), `"amount":"6.00"`) {
		t.Fatalf("admin should update payment product, got %d %s", updateResp.Code, updateResp.Body.String())
	}
	adminList := performJSON(r, http.MethodGet, "/v0/admin/payment/products?keyword=quota_50", rootJWT, nil)
	if adminList.Code != http.StatusOK || !strings.Contains(adminList.Body.String(), `"product_id":"quota_50"`) || !strings.Contains(adminList.Body.String(), `"enabled":true`) {
		t.Fatalf("admin should list payment products, got %d %s", adminList.Code, adminList.Body.String())
	}
	userProducts := performJSON(r, http.MethodGet, "/v0/user/payment/products", rootJWT, nil)
	if userProducts.Code != http.StatusOK || !strings.Contains(userProducts.Body.String(), `"quota":70`) {
		t.Fatalf("enabled product should be visible to users, got %d %s", userProducts.Code, userProducts.Body.String())
	}
	if err := service.NewSettingService().Set("payment.epay.enabled", "true"); err != nil {
		t.Fatal(err)
	}

	disableResp := performJSON(r, http.MethodPatch, "/v0/admin/payment/products/"+uintString(createPayload.Data.ID)+"/disable", rootJWT, nil)
	if disableResp.Code != http.StatusOK {
		t.Fatalf("admin should disable payment product, got %d %s", disableResp.Code, disableResp.Body.String())
	}
	hiddenProducts := performJSON(r, http.MethodGet, "/v0/user/payment/products", rootJWT, nil)
	if hiddenProducts.Code != http.StatusOK || strings.Contains(hiddenProducts.Body.String(), `"product_id":"quota_50"`) {
		t.Fatalf("disabled product should be hidden from users, got %d %s", hiddenProducts.Code, hiddenProducts.Body.String())
	}
	blockedOrder := performJSON(r, http.MethodPost, "/v0/user/payment/orders", rootJWT, map[string]interface{}{
		"provider":   "epay",
		"product_id": "quota_50",
	})
	if blockedOrder.Code != http.StatusBadRequest {
		t.Fatalf("disabled product should not create orders, got %d %s", blockedOrder.Code, blockedOrder.Body.String())
	}

	enableResp := performJSON(r, http.MethodPatch, "/v0/admin/payment/products/"+uintString(createPayload.Data.ID)+"/enable", rootJWT, nil)
	if enableResp.Code != http.StatusOK {
		t.Fatalf("admin should enable payment product, got %d %s", enableResp.Code, enableResp.Body.String())
	}
	orderResp := performJSON(r, http.MethodPost, "/v0/user/payment/orders", rootJWT, map[string]interface{}{
		"provider":   "epay",
		"product_id": "quota_50",
	})
	if orderResp.Code != http.StatusOK || !strings.Contains(orderResp.Body.String(), `"quota":70`) {
		t.Fatalf("enabled product should create orders, got %d %s", orderResp.Code, orderResp.Body.String())
	}
}

func TestAdminPaymentProductAuditLogs(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	createResp := performJSON(r, http.MethodPost, "/v0/admin/payment/products", rootJWT, map[string]interface{}{
		"product_id":  "quota_audit",
		"name":        "Audit credits",
		"amount":      "7.00",
		"currency":    "usd",
		"quota":       70,
		"bonus_quota": 0,
	})
	var createPayload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if createResp.Code != http.StatusOK {
		t.Fatalf("create payment product failed: %d %s", createResp.Code, createResp.Body.String())
	}
	if err := json.Unmarshal(createResp.Body.Bytes(), &createPayload); err != nil {
		t.Fatal(err)
	}
	updateResp := performJSON(r, http.MethodPut, "/v0/admin/payment/products/"+uintString(createPayload.Data.ID), rootJWT, map[string]interface{}{
		"product_id":  "quota_audit",
		"name":        "Audit credits updated",
		"amount":      "8.00",
		"currency":    "usd",
		"quota":       80,
		"bonus_quota": 5,
	})
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update payment product failed: %d %s", updateResp.Code, updateResp.Body.String())
	}
	disableResp := performJSON(r, http.MethodPatch, "/v0/admin/payment/products/"+uintString(createPayload.Data.ID)+"/disable", rootJWT, nil)
	if disableResp.Code != http.StatusOK {
		t.Fatalf("disable payment product failed: %d %s", disableResp.Code, disableResp.Body.String())
	}

	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=payment_product", rootJWT, nil)
	body := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(body, `"action":"payment_product.create"`) ||
		!strings.Contains(body, `"action":"payment_product.update"`) ||
		!strings.Contains(body, `"action":"payment_product.disable"`) ||
		!strings.Contains(body, `"resource_id":"`+uintString(createPayload.Data.ID)+`"`) {
		t.Fatalf("admin audit should include payment product changes, got %d %s", auditResp.Code, body)
	}
}

func TestAdminAuditRequiresSuperAdmin(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	createAdmin := performJSON(r, http.MethodPost, "/v0/admin/admin", rootJWT, map[string]interface{}{
		"username": "ops",
		"password": "password123",
		"role":     common.RoleAdmin,
	})
	if createAdmin.Code != http.StatusOK {
		t.Fatalf("create admin failed: %d %s", createAdmin.Code, createAdmin.Body.String())
	}
	opsJWT := loginBearer(t, r, "ops", "password123")

	auditByAdmin := performJSON(r, http.MethodGet, "/v0/admin/audit", opsJWT, nil)
	if auditByAdmin.Code != http.StatusForbidden {
		t.Fatalf("normal admin must not query audit logs, got %d %s", auditByAdmin.Code, auditByAdmin.Body.String())
	}
}

func TestEpayOrderBuildsSignedCheckoutURL(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	t.Setenv("PAYMENT_EPAY_KEY", "epay-checkout-secret")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Create(&model.PaymentProduct{
		ProductID: "quota_epay",
		Name:      "Epay quota",
		Amount:    "9.99",
		Currency:  "usd",
		Quota:     100,
		Enabled:   true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	settingSvc := service.NewSettingService()
	for key, value := range map[string]string{
		"payment.epay.enabled":    "true",
		"payment.epay.gateway":    "https://pay.example.com/submit.php",
		"payment.epay.pid":        "merchant-1",
		"payment.epay.notify_url": "https://api.example.com/v0/payment/epay/notify",
		"payment.epay.return_url": "https://app.example.com/payment/return",
	} {
		if err := settingSvc.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}

	createResp := performJSON(r, http.MethodPost, "/v0/user/payment/orders", rootJWT, map[string]interface{}{
		"provider":   "epay",
		"product_id": "quota_epay",
		"pay_type":   "alipay",
	})
	var payload struct {
		Data struct {
			OrderNo     string  `json:"order_no"`
			CheckoutURL *string `json:"checkout_url"`
		} `json:"data"`
	}
	if createResp.Code != http.StatusOK {
		t.Fatalf("create epay order failed: %d %s", createResp.Code, createResp.Body.String())
	}
	if err := json.Unmarshal(createResp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.CheckoutURL == nil {
		t.Fatalf("epay order should return checkout_url: %s", createResp.Body.String())
	}
	parsed, err := url.Parse(*payload.Data.CheckoutURL)
	if err != nil {
		t.Fatal(err)
	}
	values := parsed.Query()
	if parsed.Scheme+"://"+parsed.Host+parsed.Path != "https://pay.example.com/submit.php" ||
		values.Get("pid") != "merchant-1" ||
		values.Get("type") != "alipay" ||
		values.Get("out_trade_no") != payload.Data.OrderNo ||
		values.Get("money") != "9.99" ||
		values.Get("name") != "Epay quota" ||
		values.Get("notify_url") != "https://api.example.com/v0/payment/epay/notify" ||
		values.Get("return_url") != "https://app.example.com/payment/return" ||
		values.Get("sign_type") != "MD5" {
		t.Fatalf("unexpected epay checkout params: %s", *payload.Data.CheckoutURL)
	}
	if values.Get("sign") == "" || values.Get("sign") != epaySign(values, "epay-checkout-secret") {
		t.Fatalf("epay checkout sign mismatch: %s", *payload.Data.CheckoutURL)
	}
}

func TestEpayNotifyPaysOrderIdempotently(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	t.Setenv("PAYMENT_EPAY_KEY", "epay-test-secret")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(0)).Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.Create(&model.PaymentProduct{
		ProductID: "quota_100",
		Name:      "100 credits",
		Amount:    "9.99",
		Currency:  "usd",
		Quota:     100,
		Enabled:   true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("payment.epay.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	createResp := performJSON(r, http.MethodPost, "/v0/user/payment/orders", rootJWT, map[string]interface{}{
		"provider":   "epay",
		"product_id": "quota_100",
	})
	if createResp.Code != http.StatusOK {
		t.Fatalf("create payment order failed: %d %s", createResp.Code, createResp.Body.String())
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	var order model.PaymentOrder
	if err := internal.DB.Where("user_id = ? AND provider = ?", root.ID, common.PaymentProviderEpay).First(&order).Error; err != nil {
		t.Fatal(err)
	}

	returnBeforeNotify := performJSON(r, http.MethodGet, "/v0/payment/epay/return?out_trade_no="+url.QueryEscape(order.OrderNo), "", nil)
	if returnBeforeNotify.Code != http.StatusOK || !strings.Contains(returnBeforeNotify.Body.String(), `"status":"pending"`) || !strings.Contains(returnBeforeNotify.Body.String(), `"quota":100`) {
		t.Fatalf("epay return should show local pending order only, got %d %s", returnBeforeNotify.Code, returnBeforeNotify.Body.String())
	}
	if err := internal.DB.First(&root, root.ID).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 0 {
		t.Fatalf("epay return must not grant quota, got %d", root.Quota)
	}

	amountMismatch := epayNotifyValues(order.OrderNo, "TRADE-BAD", "1.00", "epay-test-secret")
	mismatchResp := performForm(r, http.MethodPost, "/v0/payment/epay/notify", amountMismatch)
	if mismatchResp.Code == http.StatusOK && strings.TrimSpace(mismatchResp.Body.String()) == "success" {
		t.Fatalf("amount mismatch must not be accepted: %d %s", mismatchResp.Code, mismatchResp.Body.String())
	}
	if err := internal.DB.First(&root, root.ID).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 0 {
		t.Fatalf("amount mismatch must not grant quota, got %d", root.Quota)
	}

	successNotify := epayNotifyValues(order.OrderNo, "TRADE1000", "9.99", "epay-test-secret")
	firstNotify := performForm(r, http.MethodPost, "/v0/payment/epay/notify", successNotify)
	if firstNotify.Code != http.StatusOK || strings.TrimSpace(firstNotify.Body.String()) != "success" {
		t.Fatalf("valid epay notify should return success, got %d %s", firstNotify.Code, firstNotify.Body.String())
	}
	if err := internal.DB.First(&root, root.ID).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("valid epay notify should grant order quota once, got %d", root.Quota)
	}
	if err := internal.DB.First(&order, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != common.PaymentOrderStatusPaid || order.PaidAt == nil || order.ProviderPaymentID == nil || *order.ProviderPaymentID != "TRADE1000" {
		t.Fatalf("valid epay notify should mark order paid: %+v", order)
	}
	returnAfterNotify := performJSON(r, http.MethodGet, "/v0/payment/epay/return?out_trade_no="+url.QueryEscape(order.OrderNo), "", nil)
	if returnAfterNotify.Code != http.StatusOK || !strings.Contains(returnAfterNotify.Body.String(), `"status":"paid"`) {
		t.Fatalf("epay return should show paid status after notify, got %d %s", returnAfterNotify.Code, returnAfterNotify.Body.String())
	}
	var quotaTxCount int64
	if err := internal.DB.Model(&model.QuotaTransaction{}).Where("source_type = ? AND source_id = ?", common.QuotaSourceTypePaymentOrder, order.OrderNo).Count(&quotaTxCount).Error; err != nil {
		t.Fatal(err)
	}
	if quotaTxCount != 1 {
		t.Fatalf("valid epay notify should write one quota transaction, got %d", quotaTxCount)
	}
	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=payment_event&resource_id=TRADE1000", rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(auditBody, `"action":"payment_webhook.processed"`) ||
		!strings.Contains(auditBody, `"action":"payment_order.paid"`) ||
		!strings.Contains(auditBody, "event_id") ||
		!strings.Contains(auditBody, "TRADE1000") ||
		!strings.Contains(auditBody, order.OrderNo) {
		t.Fatalf("epay notify should write payment event audit logs, got %d %s", auditResp.Code, auditBody)
	}

	duplicateNotify := performForm(r, http.MethodPost, "/v0/payment/epay/notify", successNotify)
	if duplicateNotify.Code != http.StatusOK || strings.TrimSpace(duplicateNotify.Body.String()) != "success" {
		t.Fatalf("duplicate epay notify should return success, got %d %s", duplicateNotify.Code, duplicateNotify.Body.String())
	}
	if err := internal.DB.First(&root, root.ID).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("duplicate epay notify must not grant quota twice, got %d", root.Quota)
	}
	if err := internal.DB.Model(&model.QuotaTransaction{}).Where("source_type = ? AND source_id = ?", common.QuotaSourceTypePaymentOrder, order.OrderNo).Count(&quotaTxCount).Error; err != nil {
		t.Fatal(err)
	}
	if quotaTxCount != 1 {
		t.Fatalf("duplicate epay notify must not write duplicate quota transactions, got %d", quotaTxCount)
	}
}

func TestStripeWebhookPaysOrderIdempotently(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	t.Setenv("PAYMENT_STRIPE_WEBHOOK_SECRET", "whsec_test_secret")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(0)).Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.Create(&model.PaymentProduct{
		ProductID: "quota_100",
		Name:      "100 credits",
		Amount:    "9.99",
		Currency:  "usd",
		Quota:     100,
		Enabled:   true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("payment.stripe.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	createResp := performJSON(r, http.MethodPost, "/v0/user/payment/orders", rootJWT, map[string]interface{}{
		"provider":   "stripe",
		"product_id": "quota_100",
	})
	if createResp.Code != http.StatusOK {
		t.Fatalf("create payment order failed: %d %s", createResp.Code, createResp.Body.String())
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	var order model.PaymentOrder
	if err := internal.DB.Where("user_id = ? AND provider = ?", root.ID, common.PaymentProviderStripe).First(&order).Error; err != nil {
		t.Fatal(err)
	}

	mismatchBody := stripeCheckoutCompletedPayload("evt_stripe_bad", &order, root.ID, 100, "pi_bad")
	mismatchResp := performStripeWebhook(r, mismatchBody, "whsec_test_secret")
	if mismatchResp.Code == http.StatusOK && strings.TrimSpace(mismatchResp.Body.String()) == "success" {
		t.Fatalf("amount mismatch must not be accepted: %d %s", mismatchResp.Code, mismatchResp.Body.String())
	}
	if err := internal.DB.First(&root, root.ID).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 0 {
		t.Fatalf("amount mismatch must not grant quota, got %d", root.Quota)
	}

	successBody := stripeCheckoutCompletedPayload("evt_stripe_1000", &order, root.ID, 999, "pi_1000")
	firstNotify := performStripeWebhook(r, successBody, "whsec_test_secret")
	if firstNotify.Code != http.StatusOK || strings.TrimSpace(firstNotify.Body.String()) != "success" {
		t.Fatalf("valid stripe webhook should return success, got %d %s", firstNotify.Code, firstNotify.Body.String())
	}
	if err := internal.DB.First(&root, root.ID).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("valid stripe webhook should grant order quota once, got %d", root.Quota)
	}
	if err := internal.DB.First(&order, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != common.PaymentOrderStatusPaid || order.PaidAt == nil || order.ProviderPaymentID == nil || *order.ProviderPaymentID != "pi_1000" {
		t.Fatalf("valid stripe webhook should mark order paid: %+v", order)
	}
	var quotaTxCount int64
	if err := internal.DB.Model(&model.QuotaTransaction{}).Where("source_type = ? AND source_id = ?", common.QuotaSourceTypePaymentOrder, order.OrderNo).Count(&quotaTxCount).Error; err != nil {
		t.Fatal(err)
	}
	if quotaTxCount != 1 {
		t.Fatalf("valid stripe webhook should write one quota transaction, got %d", quotaTxCount)
	}
	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=payment_event&resource_id=evt_stripe_1000", rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(auditBody, `"action":"payment_webhook.processed"`) ||
		!strings.Contains(auditBody, `"action":"payment_order.paid"`) ||
		!strings.Contains(auditBody, "event_id") ||
		!strings.Contains(auditBody, "evt_stripe_1000") ||
		!strings.Contains(auditBody, order.OrderNo) {
		t.Fatalf("stripe webhook payment should write payment event audit logs, got %d %s", auditResp.Code, auditBody)
	}

	duplicateNotify := performStripeWebhook(r, successBody, "whsec_test_secret")
	if duplicateNotify.Code != http.StatusOK || strings.TrimSpace(duplicateNotify.Body.String()) != "success" {
		t.Fatalf("duplicate stripe webhook should return success, got %d %s", duplicateNotify.Code, duplicateNotify.Body.String())
	}
	if err := internal.DB.First(&root, root.ID).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("duplicate stripe webhook must not grant quota twice, got %d", root.Quota)
	}
	if err := internal.DB.Model(&model.QuotaTransaction{}).Where("source_type = ? AND source_id = ?", common.QuotaSourceTypePaymentOrder, order.OrderNo).Count(&quotaTxCount).Error; err != nil {
		t.Fatal(err)
	}
	if quotaTxCount != 1 {
		t.Fatalf("duplicate stripe webhook must not write duplicate quota transactions, got %d", quotaTxCount)
	}
}

func TestStripeRefundWebhookRecordsAndOptionallyDeductsQuota(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	t.Setenv("PAYMENT_STRIPE_WEBHOOK_SECRET", "whsec_test_secret")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(0)).Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.Create(&model.PaymentProduct{
		ProductID: "quota_refund",
		Name:      "Refundable credits",
		Amount:    "9.99",
		Currency:  "usd",
		Quota:     100,
		Enabled:   true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	settingSvc := service.NewSettingService()
	if err := settingSvc.Set("payment.stripe.enabled", "true"); err != nil {
		t.Fatal(err)
	}

	firstOrder := createStripePaidOrder(t, r, rootJWT, "quota_refund", "evt_paid_refund_1", "pi_refund_1")
	refundBody := stripeChargeRefundedPayload("evt_refund_1", firstOrder, "pi_refund_1", 999)
	refundResp := performStripeWebhook(r, refundBody, "whsec_test_secret")
	if refundResp.Code != http.StatusOK || strings.TrimSpace(refundResp.Body.String()) != "success" {
		t.Fatalf("stripe refund webhook should return success, got %d %s", refundResp.Code, refundResp.Body.String())
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("refund with auto_deduct=false should not deduct quota, got %d", root.Quota)
	}
	if err := internal.DB.First(&firstOrder, firstOrder.ID).Error; err != nil {
		t.Fatal(err)
	}
	if firstOrder.Status != common.PaymentOrderStatusRefunded {
		t.Fatalf("refund should mark order refunded, got %+v", firstOrder)
	}
	var refundTxCount int64
	if err := internal.DB.Model(&model.QuotaTransaction{}).Where("type = ? AND source_id = ?", common.QuotaTransactionTypeRefundDeduct, "evt_refund_1").Count(&refundTxCount).Error; err != nil {
		t.Fatal(err)
	}
	if refundTxCount != 0 {
		t.Fatalf("refund with auto_deduct=false should not write refund deduct transaction, got %d", refundTxCount)
	}

	if err := settingSvc.Set("payment.refund.auto_deduct", "true"); err != nil {
		t.Fatal(err)
	}
	secondOrder := createStripePaidOrder(t, r, rootJWT, "quota_refund", "evt_paid_refund_2", "pi_refund_2")
	refundDeductBody := stripeChargeRefundedPayload("evt_refund_2", secondOrder, "pi_refund_2", 999)
	deductResp := performStripeWebhook(r, refundDeductBody, "whsec_test_secret")
	if deductResp.Code != http.StatusOK || strings.TrimSpace(deductResp.Body.String()) != "success" {
		t.Fatalf("stripe refund auto deduct should return success, got %d %s", deductResp.Code, deductResp.Body.String())
	}
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("second refund should deduct the second grant once, got %d", root.Quota)
	}
	if err := internal.DB.Model(&model.QuotaTransaction{}).Where("type = ? AND source_id = ?", common.QuotaTransactionTypeRefundDeduct, "evt_refund_2").Count(&refundTxCount).Error; err != nil {
		t.Fatal(err)
	}
	if refundTxCount != 1 {
		t.Fatalf("refund auto deduct should write one refund deduct transaction, got %d", refundTxCount)
	}
	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=payment_event&resource_id=evt_refund_2", rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(auditBody, `"action":"payment_refund.processed"`) ||
		!strings.Contains(auditBody, `"action":"payment_refund.deducted"`) ||
		!strings.Contains(auditBody, "event_id") ||
		!strings.Contains(auditBody, "evt_refund_2") ||
		!strings.Contains(auditBody, secondOrder.OrderNo) {
		t.Fatalf("stripe refund should write payment event audit logs, got %d %s", auditResp.Code, auditBody)
	}
	duplicateRefund := performStripeWebhook(r, refundDeductBody, "whsec_test_secret")
	if duplicateRefund.Code != http.StatusOK || strings.TrimSpace(duplicateRefund.Body.String()) != "success" {
		t.Fatalf("duplicate stripe refund should return success, got %d %s", duplicateRefund.Code, duplicateRefund.Body.String())
	}
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("duplicate refund must not deduct quota twice, got %d", root.Quota)
	}
	if err := internal.DB.Model(&model.QuotaTransaction{}).Where("type = ? AND source_id = ?", common.QuotaTransactionTypeRefundDeduct, "evt_refund_2").Count(&refundTxCount).Error; err != nil {
		t.Fatal(err)
	}
	if refundTxCount != 1 {
		t.Fatalf("duplicate refund must not write duplicate refund transaction, got %d", refundTxCount)
	}
}

func TestStripePartialRefundWebhookRecordsAndDeductsProportionally(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	t.Setenv("PAYMENT_STRIPE_WEBHOOK_SECRET", "whsec_test_secret")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(0)).Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.Create(&model.PaymentProduct{
		ProductID: "quota_partial_refund",
		Name:      "Partially refundable credits",
		Amount:    "9.99",
		Currency:  "usd",
		Quota:     100,
		Enabled:   true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	settingSvc := service.NewSettingService()
	if err := settingSvc.Set("payment.stripe.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("payment.refund.auto_deduct", "true"); err != nil {
		t.Fatal(err)
	}

	order := createStripePaidOrder(t, r, rootJWT, "quota_partial_refund", "evt_paid_partial_refund", "pi_partial_refund")
	refundBody := stripeChargeRefundedPayload("evt_partial_refund_1", order, "pi_partial_refund", 500)
	refundResp := performStripeWebhook(r, refundBody, "whsec_test_secret")
	if refundResp.Code != http.StatusOK || strings.TrimSpace(refundResp.Body.String()) != "success" {
		t.Fatalf("stripe partial refund webhook should return success, got %d %s", refundResp.Code, refundResp.Body.String())
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 50 {
		t.Fatalf("partial refund should deduct proportional quota once, got %d", root.Quota)
	}
	if err := internal.DB.First(&order, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != common.PaymentOrderStatusPartiallyRefunded {
		t.Fatalf("partial refund should mark order partially_refunded, got %+v", order)
	}
	var refundTx model.QuotaTransaction
	if err := internal.DB.Where("type = ? AND source_id = ?", common.QuotaTransactionTypeRefundDeduct, "evt_partial_refund_1").First(&refundTx).Error; err != nil {
		t.Fatalf("partial refund should write refund deduct transaction: %v", err)
	}
	if refundTx.Amount != -50 || refundTx.BalanceBefore != 100 || refundTx.BalanceAfter != 50 || refundTx.Reason != "stripe partial refund deduct" {
		t.Fatalf("unexpected partial refund deduct transaction: %+v", refundTx)
	}
	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=payment_event&resource_id=evt_partial_refund_1", rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(auditBody, `"action":"payment_refund.processed"`) ||
		!strings.Contains(auditBody, `"action":"payment_refund.deducted"`) ||
		!strings.Contains(auditBody, "partial") ||
		!strings.Contains(auditBody, "evt_partial_refund_1") ||
		!strings.Contains(auditBody, order.OrderNo) {
		t.Fatalf("partial refund should write payment event audit logs, got %d %s", auditResp.Code, auditBody)
	}
}

func TestStripeDisputeWebhookRecordsEventAndDisablesTokensByPolicy(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	t.Setenv("PAYMENT_STRIPE_WEBHOOK_SECRET", "whsec_test_secret")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(0)).Error; err != nil {
		t.Fatal(err)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "risk key",
		"remain_quota": int64(100),
	})
	if tokenResp.Code != http.StatusOK {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	var tokenPayload struct {
		Success bool `json:"success"`
		Data    struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if !tokenPayload.Success || tokenPayload.Data.ID == 0 {
		t.Fatalf("create token returned invalid payload: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	if err := internal.DB.Create(&model.PaymentProduct{
		ProductID: "quota_dispute",
		Name:      "Disputable credits",
		Amount:    "9.99",
		Currency:  "usd",
		Quota:     100,
		Enabled:   true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	settingSvc := service.NewSettingService()
	if err := settingSvc.Set("payment.stripe.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("payment.dispute.auto_disable_tokens", "true"); err != nil {
		t.Fatal(err)
	}

	order := createStripePaidOrder(t, r, rootJWT, "quota_dispute", "evt_paid_dispute", "pi_dispute")
	disputeBody := stripeChargeDisputeCreatedPayload("evt_dispute_1", order, "pi_dispute", 999)
	disputeResp := performStripeWebhook(r, disputeBody, "whsec_test_secret")
	if disputeResp.Code != http.StatusOK || strings.TrimSpace(disputeResp.Body.String()) != "success" {
		t.Fatalf("stripe dispute webhook should return success, got %d %s", disputeResp.Code, disputeResp.Body.String())
	}
	var paymentEvent model.PaymentEvent
	if err := internal.DB.Where("provider = ? AND provider_event_id = ?", common.PaymentProviderStripe, "evt_dispute_1").First(&paymentEvent).Error; err != nil {
		t.Fatal(err)
	}
	if !paymentEvent.Processed || paymentEvent.OrderNo != order.OrderNo || paymentEvent.EventType != "charge.dispute.created" {
		t.Fatalf("stripe dispute should write processed payment event, got %+v", paymentEvent)
	}
	var token model.Token
	if err := internal.DB.First(&token, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if token.Status != common.TokenStatusDisabled || token.RevokedReason != "payment_dispute" {
		t.Fatalf("stripe dispute should disable enabled user tokens by policy, got %+v", token)
	}
	if err := internal.DB.First(&root, root.ID).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("dispute event should not directly mutate user quota, got %d", root.Quota)
	}
	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=payment_event&resource_id=evt_dispute_1", rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(auditBody, `"action":"payment_dispute.created"`) ||
		!strings.Contains(auditBody, "evt_dispute_1") ||
		!strings.Contains(auditBody, order.OrderNo) ||
		!strings.Contains(auditBody, "tokens_disabled") {
		t.Fatalf("stripe dispute should write dispute audit logs, got %d %s", auditResp.Code, auditBody)
	}
	duplicateDispute := performStripeWebhook(r, disputeBody, "whsec_test_secret")
	if duplicateDispute.Code != http.StatusOK || strings.TrimSpace(duplicateDispute.Body.String()) != "success" {
		t.Fatalf("duplicate stripe dispute should return success, got %d %s", duplicateDispute.Code, duplicateDispute.Body.String())
	}
	var disputeAuditCount int64
	if err := internal.DB.Model(&model.AdminAuditLog{}).
		Where("action = ? AND resource_type = ? AND resource_id = ?", "payment_dispute.created", common.QuotaSourceTypePaymentEvent, "evt_dispute_1").
		Count(&disputeAuditCount).Error; err != nil {
		t.Fatal(err)
	}
	if disputeAuditCount != 1 {
		t.Fatalf("duplicate stripe dispute must not write duplicate audit logs, got %d", disputeAuditCount)
	}
}

func TestStripeDisputeLifecycleUpdatesDisputeFact(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	t.Setenv("PAYMENT_STRIPE_WEBHOOK_SECRET", "whsec_test_secret")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Create(&model.PaymentProduct{
		ProductID: "quota_dispute_lifecycle",
		Name:      "Dispute lifecycle credits",
		Amount:    "9.99",
		Currency:  "usd",
		Quota:     100,
		Enabled:   true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	settingSvc := service.NewSettingService()
	if err := settingSvc.Set("payment.stripe.enabled", "true"); err != nil {
		t.Fatal(err)
	}

	order := createStripePaidOrder(t, r, rootJWT, "quota_dispute_lifecycle", "evt_paid_dispute_lifecycle", "pi_dispute_lifecycle")
	createdBody := stripeChargeDisputePayload("evt_dispute_lifecycle_created", "charge.dispute.created", order, "pi_dispute_lifecycle", "dp_lifecycle_1", "needs_response", 999)
	createdResp := performStripeWebhook(r, createdBody, "whsec_test_secret")
	if createdResp.Code != http.StatusOK || strings.TrimSpace(createdResp.Body.String()) != "success" {
		t.Fatalf("stripe dispute created webhook should return success, got %d %s", createdResp.Code, createdResp.Body.String())
	}
	var disputeFact struct {
		Provider          string
		ProviderDisputeID string
		OrderNo           string
		UserID            uint
		ProviderPaymentID string
		AmountMinor       int64
		Currency          string
		Status            string
		Reason            string
		LastEventType     string
		LastEventID       string
		FundsStatus       string
	}
	if err := internal.DB.Table("payment_disputes").Where("provider_dispute_id = ?", "dp_lifecycle_1").First(&disputeFact).Error; err != nil {
		t.Fatalf("stripe dispute created should write dispute fact: %v", err)
	}
	if disputeFact.Provider != common.PaymentProviderStripe ||
		disputeFact.OrderNo != order.OrderNo ||
		disputeFact.UserID != order.UserID ||
		disputeFact.ProviderPaymentID != "pi_dispute_lifecycle" ||
		disputeFact.AmountMinor != 999 ||
		disputeFact.Currency != "usd" ||
		disputeFact.Status != "needs_response" ||
		disputeFact.LastEventType != "charge.dispute.created" ||
		disputeFact.LastEventID != "evt_dispute_lifecycle_created" {
		t.Fatalf("unexpected dispute fact after created event: %+v", disputeFact)
	}

	closedBody := stripeChargeDisputePayload("evt_dispute_lifecycle_closed", "charge.dispute.closed", order, "pi_dispute_lifecycle", "dp_lifecycle_1", "won", 999)
	closedResp := performStripeWebhook(r, closedBody, "whsec_test_secret")
	if closedResp.Code != http.StatusOK || strings.TrimSpace(closedResp.Body.String()) != "success" {
		t.Fatalf("stripe dispute closed webhook should return success, got %d %s", closedResp.Code, closedResp.Body.String())
	}
	if err := internal.DB.Table("payment_disputes").Where("provider_dispute_id = ?", "dp_lifecycle_1").First(&disputeFact).Error; err != nil {
		t.Fatalf("stripe dispute closed should keep dispute fact: %v", err)
	}
	if disputeFact.Status != "won" ||
		disputeFact.LastEventType != "charge.dispute.closed" ||
		disputeFact.LastEventID != "evt_dispute_lifecycle_closed" ||
		disputeFact.FundsStatus != "" {
		t.Fatalf("unexpected dispute fact after closed event: %+v", disputeFact)
	}
	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=payment_dispute&resource_id=dp_lifecycle_1", rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(auditBody, `"action":"payment_dispute.created"`) ||
		!strings.Contains(auditBody, `"action":"payment_dispute.closed"`) ||
		!strings.Contains(auditBody, "dp_lifecycle_1") ||
		!strings.Contains(auditBody, "won") {
		t.Fatalf("stripe dispute lifecycle should write dispute audit logs, got %d %s", auditResp.Code, auditBody)
	}

	duplicateClosed := performStripeWebhook(r, closedBody, "whsec_test_secret")
	if duplicateClosed.Code != http.StatusOK || strings.TrimSpace(duplicateClosed.Body.String()) != "success" {
		t.Fatalf("duplicate stripe dispute closed should return success, got %d %s", duplicateClosed.Code, duplicateClosed.Body.String())
	}
	var closedAuditCount int64
	if err := internal.DB.Model(&model.AdminAuditLog{}).
		Where("action = ? AND resource_type = ? AND resource_id = ?", "payment_dispute.closed", "payment_dispute", "dp_lifecycle_1").
		Count(&closedAuditCount).Error; err != nil {
		t.Fatal(err)
	}
	if closedAuditCount != 1 {
		t.Fatalf("duplicate stripe dispute closed must not write duplicate audit logs, got %d", closedAuditCount)
	}
}

func TestChannelExtendedManagement(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	createResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"idx":                7,
		"type":               common.ChannelTypeOpenAICompat,
		"name":               "extended",
		"models":             "client-model",
		"base_urls":          []string{"http://127.0.0.1:9000"},
		"api_keys":           []string{"upstream-secret-a", "upstream-secret-b"},
		"key_selection_mode": "random",
		"model_rewrites":     map[string]string{"client-model": "upstream-model"},
		"group":              "paid",
		"priority":           10,
		"weight":             3,
	})
	if createResp.Code != http.StatusOK || strings.Contains(createResp.Body.String(), "upstream-secret") {
		t.Fatalf("extended channel response failed or leaked key: %d %s", createResp.Code, createResp.Body.String())
	}
	var payload struct {
		Data struct {
			ID          uint   `json:"id"`
			Idx         int    `json:"idx"`
			Group       string `json:"group"`
			APIKeyCount int    `json:"api_key_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createResp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.ID == 0 || payload.Data.Idx != 7 || payload.Data.Group != "paid" || payload.Data.APIKeyCount != 2 {
		t.Fatalf("unexpected extended channel payload: %s", createResp.Body.String())
	}
	var stored model.Channel
	if err := internal.DB.First(&stored, payload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored.APIKeys), "upstream-secret") || !strings.Contains(string(stored.APIKeys), "enc:v1:") {
		t.Fatalf("api_keys should be encrypted: %s", string(stored.APIKeys))
	}

	disableResp := performJSON(r, http.MethodPatch, "/v0/admin/channel/"+uintString(payload.Data.ID)+"/disable", rootJWT, nil)
	if disableResp.Code != http.StatusOK {
		t.Fatalf("disable channel failed: %d %s", disableResp.Code, disableResp.Body.String())
	}
	enableResp := performJSON(r, http.MethodPatch, "/v0/admin/channel/"+uintString(payload.Data.ID)+"/enable", rootJWT, nil)
	if enableResp.Code != http.StatusOK {
		t.Fatalf("enable channel failed: %d %s", enableResp.Code, enableResp.Body.String())
	}
	deleteResp := performJSON(r, http.MethodDelete, "/v0/admin/channel/"+uintString(payload.Data.ID), rootJWT, nil)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete channel failed: %d %s", deleteResp.Code, deleteResp.Body.String())
	}
	var deleted model.Channel
	err := internal.DB.Unscoped().First(&deleted, payload.Data.ID).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("channel should be hard deleted, got err=%v row=%+v", err, deleted)
	}
}

func TestAdminChannelManagementAuditLogs(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"audit-model","object":"model"}]}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	createResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"idx":                8,
		"type":               common.ChannelTypeOpenAICompat,
		"name":               "audit-channel",
		"models":             "audit-model",
		"base_urls":          []string{upstream.URL},
		"api_keys":           []string{"audit-upstream-secret"},
		"key_selection_mode": "round_robin",
		"group":              "paid",
	})
	if createResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", createResp.Code, createResp.Body.String())
	}
	var payload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createResp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}

	testResp := performJSON(r, http.MethodPost, "/v0/admin/channel/"+uintString(payload.Data.ID)+"/test", rootJWT, nil)
	if testResp.Code != http.StatusOK || !strings.Contains(testResp.Body.String(), `"success":true`) {
		t.Fatalf("test channel failed: %d %s", testResp.Code, testResp.Body.String())
	}
	modelsResp := performJSON(r, http.MethodGet, "/v0/admin/channel/"+uintString(payload.Data.ID)+"/models", rootJWT, nil)
	if modelsResp.Code != http.StatusOK || !strings.Contains(modelsResp.Body.String(), `"audit-model"`) {
		t.Fatalf("fetch channel models failed: %d %s", modelsResp.Code, modelsResp.Body.String())
	}

	updateResp := performJSON(r, http.MethodPut, "/v0/admin/channel/"+uintString(payload.Data.ID), rootJWT, map[string]interface{}{
		"name":   "audit-channel-updated",
		"weight": 4,
	})
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update channel failed: %d %s", updateResp.Code, updateResp.Body.String())
	}
	disableResp := performJSON(r, http.MethodPatch, "/v0/admin/channel/"+uintString(payload.Data.ID)+"/disable", rootJWT, nil)
	if disableResp.Code != http.StatusOK {
		t.Fatalf("disable channel failed: %d %s", disableResp.Code, disableResp.Body.String())
	}
	enableResp := performJSON(r, http.MethodPatch, "/v0/admin/channel/"+uintString(payload.Data.ID)+"/enable", rootJWT, nil)
	if enableResp.Code != http.StatusOK {
		t.Fatalf("enable channel failed: %d %s", enableResp.Code, enableResp.Body.String())
	}
	deleteResp := performJSON(r, http.MethodDelete, "/v0/admin/channel/"+uintString(payload.Data.ID), rootJWT, nil)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete channel failed: %d %s", deleteResp.Code, deleteResp.Body.String())
	}

	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=channel&resource_id="+uintString(payload.Data.ID), rootJWT, nil)
	body := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(body, `"action":"channel.create"`) ||
		!strings.Contains(body, `"action":"channel.test"`) ||
		!strings.Contains(body, `"action":"channel.fetch_models"`) ||
		!strings.Contains(body, `"action":"channel.update"`) ||
		!strings.Contains(body, `"action":"channel.disable"`) ||
		!strings.Contains(body, `"action":"channel.enable"`) ||
		!strings.Contains(body, `"action":"channel.delete"`) {
		t.Fatalf("channel management should write audit logs, got %d %s", auditResp.Code, body)
	}
	if strings.Contains(body, "audit-upstream-secret") {
		t.Fatalf("channel audit should not expose upstream secrets: %s", body)
	}
}

func TestSetupBootstrapAdminQuotaAndSettingsDefaults(t *testing.T) {
	jwtSecret := "test-jwt-secret-with-at-least-32-bytes"
	t.Setenv("JWT_SECRET", jwtSecret)
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	var quotaSetting model.Setting
	if err := internal.DB.Where("key = ?", "billing.bootstrap_admin_quota").First(&quotaSetting).Error; err != nil {
		t.Fatalf("bootstrap quota setting missing: %v", err)
	}
	bootstrapQuota, err := strconv.ParseInt(quotaSetting.Value, 10, 64)
	if err != nil || bootstrapQuota <= 0 {
		t.Fatalf("bootstrap quota should be a positive integer, got value=%q err=%v", quotaSetting.Value, err)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != bootstrapQuota {
		t.Fatalf("root quota should equal bootstrap quota, got user=%d setting=%d", root.Quota, bootstrapQuota)
	}

	for _, key := range []string{
		"auth.login.username_password.enabled",
		"auth.login.email_password.enabled",
		"auth.login.phone_password.enabled",
		"auth.login.email_code.enabled",
		"auth.login.phone_code.enabled",
		"auth.login.oauth.enabled",
		"auth.login.oidc.enabled",
		"auth.register.enabled",
		"auth.register.username.enabled",
		"auth.register.email.enabled",
		"auth.register.phone.enabled",
		"auth.register.captcha.required",
		"auth.register.default_quota",
		"auth.register.default_group_id",
		"jwt.secret",
		"rate_limit.enabled",
		"rate_limit.global_per_min",
		"rate_limit.per_token_per_min",
		"rate_limit.per_ip_per_min",
		"rate_limit.per_user_per_min",
		"rate_limit.per_model_per_min",
		"rate_limit.per_channel_per_min",
		"relay.timeout",
		"relay.retry_count",
		"relay.retry_on_status",
		"relay.max_request_body_bytes",
		"relay.max_response_body_bytes",
		"relay.routerx_max_hops",
		"relay.log_body_max_bytes",
		"log.body_max_bytes",
		"log.request_body_enabled",
		"log.response_body_enabled",
		"observability.metrics_enabled",
		"observability.audit_enabled",
		"observability.request_id_header",
		"ready.production_strict",
		"routing.channel_cache.enabled",
		"routing.channel_cache.preload",
		"routing.channel_cache.ttl_seconds",
		"routing.channel_cache.version",
		"billing.default_ratio",
		"billing.bootstrap_admin_quota",
		"billing.default_user_channel_group_access",
		"billing.user_group_ratios",
		"billing.channel_group_ratios",
		"billing.model_group_ratios",
		"billing.user_group_channel_ratios",
		"billing.user_group_channel_group_access",
		"billing.usage_missing_strategy",
		"payment.stripe.enabled",
		"payment.epay.enabled",
		"payment.epay.gateway",
		"payment.epay.pid",
		"payment.epay.notify_url",
		"payment.epay.return_url",
		"payment.epay.refund_url",
		"payment.currency",
		"payment.order_expire_minutes",
		"payment.refund.auto_deduct",
		"payment.refund.allow_negative_balance",
		"payment.dispute.auto_disable_tokens",
	} {
		var count int64
		if err := internal.DB.Model(&model.Setting{}).Where("key = ?", key).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("expected setting %s to be initialized once, got %d", key, count)
		}
	}

	rootJWT := loginBearer(t, r, "root", "password123")
	settingsResp := performJSON(r, http.MethodGet, "/v0/admin/setting", rootJWT, nil)
	if settingsResp.Code != http.StatusOK {
		t.Fatalf("settings list failed: %d %s", settingsResp.Code, settingsResp.Body.String())
	}
	if strings.Contains(settingsResp.Body.String(), jwtSecret) {
		t.Fatalf("settings response leaked jwt secret: %s", settingsResp.Body.String())
	}
}

func TestRequestIDHeaderUsesConfiguredSetting(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	if err := service.NewSettingService().Set("observability.request_id_header", "X-Correlation-Id"); err != nil {
		t.Fatal(err)
	}

	providedResp := performRawWithHeaders(r, http.MethodGet, "/health", "", "", map[string]string{
		"X-Correlation-Id": "trace-custom-1",
	})
	if got := providedResp.Header().Get("X-Correlation-Id"); got != "trace-custom-1" {
		t.Fatalf("configured request id header should echo provided id, got %q", got)
	}
	if legacy := providedResp.Header().Get("X-Request-Id"); legacy != "" {
		t.Fatalf("configured request id header should not also emit legacy header, got %q", legacy)
	}

	generatedResp := performRawWithHeaders(r, http.MethodGet, "/health", "", "", nil)
	if got := generatedResp.Header().Get("X-Correlation-Id"); strings.TrimSpace(got) == "" {
		t.Fatalf("configured request id header should receive generated id, headers=%v", generatedResp.Header())
	}
}

func TestMetricsEndpointRequiresSettingAndExposesPrometheusText(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	disabledResp := performJSON(r, http.MethodGet, "/metrics", "", nil)
	if disabledResp.Code != http.StatusNotFound {
		t.Fatalf("metrics should be disabled by default, got %d %s", disabledResp.Code, disabledResp.Body.String())
	}
	if err := service.NewSettingService().Set("observability.metrics_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	enabledResp := performJSON(r, http.MethodGet, "/metrics", "", nil)
	body := enabledResp.Body.String()
	if enabledResp.Code != http.StatusOK ||
		!strings.Contains(enabledResp.Header().Get("Content-Type"), "text/plain") ||
		!strings.Contains(body, "routerx_users_total 1") ||
		!strings.Contains(body, "routerx_tokens_total 0") ||
		!strings.Contains(body, "routerx_ready 1") {
		t.Fatalf("metrics should expose prometheus text when enabled, got %d %s", enabledResp.Code, body)
	}
	if strings.Contains(body, "\nrouterx_users_total\n") {
		t.Fatalf("metrics should not emit bare metric family names without values: %s", body)
	}
}

func TestMetricsEndpointIncludesHTTPMetrics(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	if err := service.NewSettingService().Set("observability.metrics_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	healthResp := performJSON(r, http.MethodGet, "/health", "", nil)
	if healthResp.Code != http.StatusOK {
		t.Fatalf("health check failed: %d %s", healthResp.Code, healthResp.Body.String())
	}

	resp := performJSON(r, http.MethodGet, "/metrics", "", nil)
	body := resp.Body.String()
	if resp.Code != http.StatusOK ||
		!strings.Contains(body, `routerx_http_requests_total{method="GET",path_group="/health",status="200"} 1`) ||
		!strings.Contains(body, `routerx_http_request_duration_seconds_count{method="GET",path_group="/health"} 1`) {
		t.Fatalf("metrics should include HTTP request counters and duration histograms, got %d %s", resp.Code, body)
	}
}

func TestMetricsEndpointIncludesRelayDurationMetrics(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-metrics",
			"object": "chat.completion",
			"model": "gpt-test",
			"choices": [
				{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}
			],
			"usage": {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}
		}`))
	}))
	defer upstream.Close()
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	if err := service.NewSettingService().Set("observability.metrics_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":      "metrics-sdk",
		"unlimited": true,
	})
	var tokenPayload struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "metrics-compat",
		"models":   "gpt-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model":    "gpt-test",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	if chatResp.Code != http.StatusOK {
		t.Fatalf("chat completion failed: %d %s", chatResp.Code, chatResp.Body.String())
	}

	resp := performJSON(r, http.MethodGet, "/metrics", "", nil)
	body := resp.Body.String()
	if resp.Code != http.StatusOK ||
		!strings.Contains(body, `routerx_relay_duration_seconds_count{protocol="openai",api_type="chat",provider="openai-compatible"} 1`) ||
		!strings.Contains(body, `routerx_upstream_duration_seconds_count{provider="openai-compatible",channel_id="1",status="success"} 1`) {
		t.Fatalf("metrics should include relay and upstream duration histograms, got %d %s", resp.Code, body)
	}
}

func TestMetricsEndpointIncludesRelayPaymentAndInfrastructureSignals(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	if err := service.NewSettingService().Set("observability.metrics_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{
		Type:       common.ChannelTypeOpenAICompat,
		Name:       "metrics-channel",
		Models:     "gpt-test",
		APIKey:     "enc:v1:test",
		Status:     common.ChannelStatusEnabled,
		ErrorCount: 3,
	}
	if err := internal.DB.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	disabledChannel := model.Channel{
		Type:   common.ChannelTypeClaude,
		Name:   "metrics-disabled-channel",
		Models: "claude-test",
		Status: common.ChannelStatusManualOff,
	}
	if err := internal.DB.Create(&disabledChannel).Error; err != nil {
		t.Fatal(err)
	}
	token, err := service.NewTokenService().Create(root.ID, "metrics-key", 100, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	tokenID := token.ID
	now := time.Now()
	logs := []model.Log{
		{
			UserID:          root.ID,
			TokenID:         &tokenID,
			ChannelID:       &channel.ID,
			Model:           "gpt-test",
			Status:          common.LogStatusSuccess,
			QuotaUsed:       7,
			TotalTokens:     7,
			UsageSource:     common.LogUsageSourceUpstream,
			RequestSnapshot: `{"entry_protocol":"openai","api_type":"chat"}`,
			RouteSnapshot:   `{"selected_provider":"openai-compatible"}`,
			CreatedAt:       now.Add(-time.Minute),
		},
		{
			UserID:          root.ID,
			TokenID:         &tokenID,
			ChannelID:       &channel.ID,
			Model:           "gpt-test",
			Status:          common.LogStatusFailed,
			ErrorMsg:        "upstream 500",
			ErrorCode:       "upstream_500",
			ErrorSource:     common.LogErrorSourceUpstream,
			RequestSnapshot: `{"entry_protocol":"openai","api_type":"chat"}`,
			CreatedAt:       now,
		},
		{
			UserID:          root.ID,
			TokenID:         &tokenID,
			ChannelID:       &channel.ID,
			Model:           "gpt-test",
			Status:          common.LogStatusFailed,
			ErrorMsg:        "token rate limit exceeded",
			ErrorCode:       "rate_limit_exceeded",
			PolicySnapshot:  `{"scope_result":{"rate_limit":"deny","rate_limit_dimension":"token"}}`,
			RequestSnapshot: `{"entry_protocol":"openai"}`,
			CreatedAt:       now.Add(time.Minute),
		},
		{
			UserID:          root.ID,
			TokenID:         &tokenID,
			ChannelID:       &channel.ID,
			Model:           "gpt-test",
			Status:          common.LogStatusFailed,
			ErrorMsg:        "billing failed",
			ErrorCode:       "billing_failed",
			ErrorSource:     common.LogErrorSourceBilling,
			RequestSnapshot: `{"entry_protocol":"openai","api_type":"chat"}`,
			BillingSnapshot: `{"reason":"post_deduct_failed"}`,
			CreatedAt:       now.Add(2 * time.Minute),
		},
	}
	if err := internal.DB.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	orders := []model.PaymentOrder{
		{OrderNo: "pay_metrics_1", UserID: root.ID, ProductID: "quota_10", Provider: common.PaymentProviderStripe, Amount: "9.99", Currency: "usd", Quota: 10, Status: common.PaymentOrderStatusPaid},
		{OrderNo: "pay_metrics_2", UserID: root.ID, ProductID: "quota_20", Provider: common.PaymentProviderEpay, Amount: "19.99", Currency: "usd", Quota: 20, Status: common.PaymentOrderStatusPending},
	}
	if err := internal.DB.Create(&orders).Error; err != nil {
		t.Fatal(err)
	}
	processedAt := now
	events := []model.PaymentEvent{
		{Provider: common.PaymentProviderStripe, ProviderEventID: "evt_metrics_1", OrderNo: "pay_metrics_1", EventType: "checkout.session.completed", SignatureValid: true, Processed: true, ProcessedAt: &processedAt},
		{Provider: common.PaymentProviderEpay, ProviderEventID: "evt_metrics_2", OrderNo: "pay_metrics_2", EventType: "notify", SignatureValid: true, Processed: false},
	}
	if err := internal.DB.Create(&events).Error; err != nil {
		t.Fatal(err)
	}
	auditLogs := []model.AdminAuditLog{
		{ActorUserID: root.ID, ActorRole: common.RoleSuper, Action: "setting.update", ResourceType: "setting", ResourceID: "observability.metrics_enabled", Result: "success", CreatedAt: now},
		{ActorUserID: root.ID, ActorRole: common.RoleSuper, Action: "channel.test", ResourceType: "channel", ResourceID: uintString(channel.ID), Result: "failed", ErrorCode: "channel_test_failed", CreatedAt: now.Add(time.Second)},
	}
	if err := internal.DB.Create(&auditLogs).Error; err != nil {
		t.Fatal(err)
	}

	resp := performJSON(r, http.MethodGet, "/metrics", "", nil)
	body := resp.Body.String()
	enabledChannelMetric := fmt.Sprintf(`routerx_channel_available{channel_id="%d",provider="openai-compatible"} 1`, channel.ID)
	disabledChannelMetric := fmt.Sprintf(`routerx_channel_available{channel_id="%d",provider="anthropic"} 0`, disabledChannel.ID)
	enabledChannelErrorsMetric := fmt.Sprintf(`routerx_channel_error_count{channel_id="%d",provider="openai-compatible"} 3`, channel.ID)
	disabledChannelErrorsMetric := fmt.Sprintf(`routerx_channel_error_count{channel_id="%d",provider="anthropic"} 0`, disabledChannel.ID)
	if resp.Code != http.StatusOK ||
		!strings.Contains(body, "routerx_db_up 1") ||
		!strings.Contains(body, "routerx_redis_up 0") ||
		!strings.Contains(body, `routerx_logs_total{status="success"} 1`) ||
		!strings.Contains(body, `routerx_logs_total{status="failed"} 3`) ||
		!strings.Contains(body, `routerx_relay_requests_total{protocol="openai",api_type="chat",model="gpt-test",status="success"} 1`) ||
		!strings.Contains(body, `routerx_relay_requests_total{protocol="openai",api_type="chat",model="gpt-test",status="failed"} 2`) ||
		!strings.Contains(body, `routerx_tokens_used_total{model="gpt-test",provider="openai-compatible",usage_source="upstream"} 7`) ||
		!strings.Contains(body, `routerx_quota_used_total{model="gpt-test",provider="openai-compatible",user_group="default"} 7`) ||
		!strings.Contains(body, enabledChannelMetric) ||
		!strings.Contains(body, disabledChannelMetric) ||
		!strings.Contains(body, enabledChannelErrorsMetric) ||
		!strings.Contains(body, disabledChannelErrorsMetric) ||
		!strings.Contains(body, `routerx_relay_errors_total{protocol="openai",api_type="chat",error_code="upstream_500",source="upstream"} 1`) ||
		!strings.Contains(body, `routerx_rate_limit_rejections_total{dimension="token"} 1`) ||
		!strings.Contains(body, `routerx_billing_failures_total{reason="post_deduct_failed"} 1`) ||
		!strings.Contains(body, `routerx_payment_orders_total{provider="stripe",status="paid"} 1`) ||
		!strings.Contains(body, `routerx_payment_orders_total{provider="epay",status="pending"} 1`) ||
		!strings.Contains(body, `routerx_payment_events_total{provider="stripe",event_type="checkout.session.completed",processed="true"} 1`) ||
		!strings.Contains(body, `routerx_payment_events_total{provider="epay",event_type="notify",processed="false"} 1`) ||
		!strings.Contains(body, `routerx_audit_events_total{action="setting.update",resource_type="setting",result="success"} 1`) ||
		!strings.Contains(body, `routerx_audit_events_total{action="channel.test",resource_type="channel",result="failed"} 1`) {
		t.Fatalf("metrics should include relay/payment/infrastructure signals, got %d %s", resp.Code, body)
	}
}

func TestMetricsEndpointReportsIndependentLogDBHealth(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	if err := service.NewSettingService().Set("observability.metrics_enabled", "true"); err != nil {
		t.Fatal(err)
	}

	oldLogDB := internal.LogDB
	logDB := newRouterLogDB(t, "metrics_health")
	internal.LogDB = logDB
	t.Cleanup(func() {
		if sqlDB, err := logDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		internal.LogDB = oldLogDB
	})

	healthyResp := performJSON(r, http.MethodGet, "/metrics", "", nil)
	healthyBody := healthyResp.Body.String()
	if healthyResp.Code != http.StatusOK ||
		!strings.Contains(healthyBody, "routerx_log_db_configured 1") ||
		!strings.Contains(healthyBody, "routerx_log_db_up 1") {
		t.Fatalf("metrics should report configured healthy independent log DB, got %d %s", healthyResp.Code, healthyBody)
	}

	sqlDB, err := logDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	downResp := performJSON(r, http.MethodGet, "/metrics", "", nil)
	downBody := downResp.Body.String()
	if downResp.Code != http.StatusOK ||
		!strings.Contains(downBody, "routerx_log_db_configured 1") ||
		!strings.Contains(downBody, "routerx_log_db_up 0") {
		t.Fatalf("metrics should keep serving main DB facts while reporting log DB down, got %d %s", downResp.Code, downBody)
	}
}

func TestSettingsValidationAndReadiness(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	badUpdate := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"relay.timeout": "0",
	})
	if badUpdate.Code != http.StatusBadRequest {
		t.Fatalf("invalid relay.timeout should be rejected, got %d %s", badUpdate.Code, badUpdate.Body.String())
	}
	var relayTimeout model.Setting
	if err := internal.DB.Where("key = ?", "relay.timeout").First(&relayTimeout).Error; err != nil {
		t.Fatal(err)
	}
	if relayTimeout.Value != "120" {
		t.Fatalf("invalid setting update should not be persisted, got %q", relayTimeout.Value)
	}

	disabledTokenRateLimit := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"rate_limit.per_token_per_min": "0",
	})
	if disabledTokenRateLimit.Code != http.StatusOK {
		t.Fatalf("rate_limit.per_token_per_min=0 should be accepted to disable the dimension, got %d %s", disabledTokenRateLimit.Code, disabledTokenRateLimit.Body.String())
	}
	var tokenLimit model.Setting
	if err := internal.DB.Where("key = ?", "rate_limit.per_token_per_min").First(&tokenLimit).Error; err != nil {
		t.Fatal(err)
	}
	if tokenLimit.Value != "0" {
		t.Fatalf("rate_limit.per_token_per_min=0 should be persisted, got %q", tokenLimit.Value)
	}
	badChannelRateLimit := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"rate_limit.per_channel_per_min": "-1",
	})
	if badChannelRateLimit.Code != http.StatusBadRequest {
		t.Fatalf("rate_limit.per_channel_per_min should reject negative values, got %d %s", badChannelRateLimit.Code, badChannelRateLimit.Body.String())
	}

	badPort := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"server.port": "70000",
	})
	if badPort.Code != http.StatusBadRequest {
		t.Fatalf("server.port outside 1..65535 should be rejected, got %d %s", badPort.Code, badPort.Body.String())
	}
	badMode := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"server.mode": "benchmark",
	})
	if badMode.Code != http.StatusBadRequest {
		t.Fatalf("invalid server.mode should be rejected, got %d %s", badMode.Code, badMode.Body.String())
	}
	badPaymentSwitch := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"payment.epay.enabled": "sometimes",
	})
	if badPaymentSwitch.Code != http.StatusBadRequest {
		t.Fatalf("invalid payment.epay.enabled should be rejected, got %d %s", badPaymentSwitch.Code, badPaymentSwitch.Body.String())
	}
	badRequestIDHeader := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"observability.request_id_header": "X Bad:Request",
	})
	if badRequestIDHeader.Code != http.StatusBadRequest {
		t.Fatalf("invalid observability.request_id_header should be rejected, got %d %s", badRequestIDHeader.Code, badRequestIDHeader.Body.String())
	}
	badEpayGateway := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"payment.epay.gateway": "pay.example.com/submit.php",
	})
	if badEpayGateway.Code != http.StatusBadRequest {
		t.Fatalf("invalid payment.epay.gateway should be rejected, got %d %s", badEpayGateway.Code, badEpayGateway.Body.String())
	}
	badEpayRefundURL := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"payment.epay.refund_url": "pay.example.com/refund",
	})
	if badEpayRefundURL.Code != http.StatusBadRequest {
		t.Fatalf("invalid payment.epay.refund_url should be rejected, got %d %s", badEpayRefundURL.Code, badEpayRefundURL.Body.String())
	}
	badDefaultAccess := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"billing.default_user_channel_group_access": `"default"`,
	})
	if badDefaultAccess.Code != http.StatusBadRequest {
		t.Fatalf("default channel group access must be a JSON array, got %d %s", badDefaultAccess.Code, badDefaultAccess.Body.String())
	}
	badGroupAccess := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"billing.user_group_channel_group_access": `{"vip":{"allow":[""]}}`,
	})
	if badGroupAccess.Code != http.StatusBadRequest {
		t.Fatalf("user group channel group access should reject empty channel groups, got %d %s", badGroupAccess.Code, badGroupAccess.Body.String())
	}
	badUserGroupRatio := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"billing.user_group_ratios": `{"vip":0}`,
	})
	if badUserGroupRatio.Code != http.StatusBadRequest {
		t.Fatalf("user group ratios should reject zero values, got %d %s", badUserGroupRatio.Code, badUserGroupRatio.Body.String())
	}
	badChannelGroupRatio := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"billing.channel_group_ratios": `{"paid":-1}`,
	})
	if badChannelGroupRatio.Code != http.StatusBadRequest {
		t.Fatalf("channel group ratios should reject negative values, got %d %s", badChannelGroupRatio.Code, badChannelGroupRatio.Body.String())
	}
	badNestedRatio := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"billing.user_group_channel_ratios": `{"vip":{"paid":0}}`,
	})
	if badNestedRatio.Code != http.StatusBadRequest {
		t.Fatalf("user group channel ratios should reject zero nested values, got %d %s", badNestedRatio.Code, badNestedRatio.Body.String())
	}
	badUsageMissingStrategy := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"billing.usage_missing_strategy": "free",
	})
	if badUsageMissingStrategy.Code != http.StatusBadRequest {
		t.Fatalf("usage missing strategy should reject unknown values, got %d %s", badUsageMissingStrategy.Code, badUsageMissingStrategy.Body.String())
	}
	badChannelCacheEnabled := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"routing.channel_cache.enabled": "maybe",
	})
	if badChannelCacheEnabled.Code != http.StatusBadRequest {
		t.Fatalf("channel cache enabled should be boolean, got %d %s", badChannelCacheEnabled.Code, badChannelCacheEnabled.Body.String())
	}
	badChannelCacheTTL := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"routing.channel_cache.ttl_seconds": "-1",
	})
	if badChannelCacheTTL.Code != http.StatusBadRequest {
		t.Fatalf("channel cache ttl should reject negative values, got %d %s", badChannelCacheTTL.Code, badChannelCacheTTL.Body.String())
	}
	badChannelCacheVersion := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"routing.channel_cache.version": "0",
	})
	if badChannelCacheVersion.Code != http.StatusBadRequest {
		t.Fatalf("channel cache version should reject zero values, got %d %s", badChannelCacheVersion.Code, badChannelCacheVersion.Body.String())
	}
	badRelayRequestBodyLimit := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"relay.max_request_body_bytes": "-1",
	})
	if badRelayRequestBodyLimit.Code != http.StatusBadRequest {
		t.Fatalf("relay max request body bytes should reject negative values, got %d %s", badRelayRequestBodyLimit.Code, badRelayRequestBodyLimit.Body.String())
	}
	badRelayResponseBodyLimit := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"relay.max_response_body_bytes": "-1",
	})
	if badRelayResponseBodyLimit.Code != http.StatusBadRequest {
		t.Fatalf("relay max response body bytes should reject negative values, got %d %s", badRelayResponseBodyLimit.Code, badRelayResponseBodyLimit.Body.String())
	}
	badRouterXHopLimit := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"relay.routerx_max_hops": "0",
	})
	if badRouterXHopLimit.Code != http.StatusBadRequest {
		t.Fatalf("routerx max hops should reject zero values, got %d %s", badRouterXHopLimit.Code, badRouterXHopLimit.Body.String())
	}
	badRelayRetryStatus := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"relay.retry_on_status": "[200]",
	})
	if badRelayRetryStatus.Code != http.StatusBadRequest {
		t.Fatalf("relay retry statuses should reject non-error status codes, got %d %s", badRelayRetryStatus.Code, badRelayRetryStatus.Body.String())
	}
	badRegisterEnabled := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"auth.register.enabled": "maybe",
	})
	if badRegisterEnabled.Code != http.StatusBadRequest {
		t.Fatalf("register enabled should be boolean, got %d %s", badRegisterEnabled.Code, badRegisterEnabled.Body.String())
	}
	badRegisterQuota := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"auth.register.default_quota": "-1",
	})
	if badRegisterQuota.Code != http.StatusBadRequest {
		t.Fatalf("register default quota should reject negative values, got %d %s", badRegisterQuota.Code, badRegisterQuota.Body.String())
	}
	badRegisterGroup := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"auth.register.default_group_id": "",
	})
	if badRegisterGroup.Code != http.StatusBadRequest {
		t.Fatalf("register default group should reject empty values, got %d %s", badRegisterGroup.Code, badRegisterGroup.Body.String())
	}
	badEmailPasswordLogin := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"auth.login.email_password.enabled": "maybe",
	})
	if badEmailPasswordLogin.Code != http.StatusBadRequest {
		t.Fatalf("email password login setting should be boolean, got %d %s", badEmailPasswordLogin.Code, badEmailPasswordLogin.Body.String())
	}
	disableUsernamePasswordLogin := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"auth.login.username_password.enabled": "false",
	})
	if disableUsernamePasswordLogin.Code != http.StatusBadRequest {
		t.Fatalf("username password login must not be disabled, got %d %s", disableUsernamePasswordLogin.Code, disableUsernamePasswordLogin.Body.String())
	}

	if err := service.NewSettingService().Set("payment.epay.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	epayMissingKey := performJSON(r, http.MethodGet, "/ready", "", nil)
	if epayMissingKey.Code != http.StatusServiceUnavailable || !strings.Contains(epayMissingKey.Body.String(), "PAYMENT_EPAY_KEY") {
		t.Fatalf("enabled epay without key should make ready fail, got %d %s", epayMissingKey.Code, epayMissingKey.Body.String())
	}
	t.Setenv("PAYMENT_EPAY_KEY", "epay-test-secret")
	epayReady := performJSON(r, http.MethodGet, "/ready", "", nil)
	if epayReady.Code != http.StatusOK {
		t.Fatalf("epay key should restore ready, got %d %s", epayReady.Code, epayReady.Body.String())
	}

	if err := service.NewSettingService().Set("payment.stripe.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	stripeMissingKeys := performJSON(r, http.MethodGet, "/ready", "", nil)
	if stripeMissingKeys.Code != http.StatusServiceUnavailable || !strings.Contains(stripeMissingKeys.Body.String(), "PAYMENT_STRIPE_SECRET_KEY") {
		t.Fatalf("enabled stripe without secret key should make ready fail, got %d %s", stripeMissingKeys.Code, stripeMissingKeys.Body.String())
	}
	t.Setenv("PAYMENT_STRIPE_SECRET_KEY", "sk_test_routerx")
	stripeMissingWebhook := performJSON(r, http.MethodGet, "/ready", "", nil)
	if stripeMissingWebhook.Code != http.StatusServiceUnavailable || !strings.Contains(stripeMissingWebhook.Body.String(), "PAYMENT_STRIPE_WEBHOOK_SECRET") {
		t.Fatalf("enabled stripe without webhook secret should make ready fail, got %d %s", stripeMissingWebhook.Code, stripeMissingWebhook.Body.String())
	}
	t.Setenv("PAYMENT_STRIPE_WEBHOOK_SECRET", "whsec_routerx")
	stripeReady := performJSON(r, http.MethodGet, "/ready", "", nil)
	if stripeReady.Code != http.StatusOK {
		t.Fatalf("stripe keys should restore ready, got %d %s", stripeReady.Code, stripeReady.Body.String())
	}

	if err := internal.DB.Model(&model.Setting{}).Where("key = ?", "relay.timeout").Update("value", "0").Error; err != nil {
		t.Fatal(err)
	}
	notReady := performJSON(r, http.MethodGet, "/ready", "", nil)
	if notReady.Code != http.StatusServiceUnavailable || !strings.Contains(notReady.Body.String(), "relay.timeout") {
		t.Fatalf("invalid relay.timeout should make ready fail, got %d %s", notReady.Code, notReady.Body.String())
	}
}

func TestReadinessRequiresRedisForExternalDatabaseMode(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	t.Setenv("SQL_DSN", "postgres://routerx:secret@db.example/routerx?sslmode=disable")
	t.Setenv("REDIS_CONN", "")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}

	readyResp := performJSON(r, http.MethodGet, "/ready", "", nil)
	if readyResp.Code != http.StatusServiceUnavailable || !strings.Contains(readyResp.Body.String(), "redis") {
		t.Fatalf("external database mode without Redis should be not ready, got %d %s", readyResp.Code, readyResp.Body.String())
	}
}

func TestReadinessRequiresEncryptionKeyForEncryptedChannelSecrets(t *testing.T) {
	cases := []struct {
		name    string
		channel model.Channel
	}{
		{
			name: "single api key",
			channel: model.Channel{
				APIKey: "enc:v1:single-encrypted",
			},
		},
		{
			name: "multi api keys",
			channel: model.Channel{
				APIKeys: model.NewJSONValue([]string{"enc:v1:multi-encrypted"}),
			},
		},
		{
			name: "upstream api key",
			channel: model.Channel{
				Upstreams: model.NewJSONValue([]map[string]string{{
					"base_url": "https://upstream.example",
					"api_key":  "enc:v1:upstream-encrypted",
				}}),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
			t.Setenv("ENCRYPTION_KEY", "")
			r := newTestRouter(t)

			initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
				"username": "root",
				"password": "password123",
			})
			if initResp.Code != http.StatusOK {
				t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
			}

			channel := tc.channel
			channel.Type = common.ChannelTypeOpenAICompat
			channel.Name = "encrypted-ready"
			channel.Models = "gpt-ready"
			channel.BaseURL = "https://upstream.example"
			channel.Status = common.ChannelStatusEnabled
			if err := internal.DB.Create(&channel).Error; err != nil {
				t.Fatal(err)
			}

			readyResp := performJSON(r, http.MethodGet, "/ready", "", nil)
			if readyResp.Code != http.StatusServiceUnavailable || !strings.Contains(readyResp.Body.String(), "ENCRYPTION_KEY") {
				t.Fatalf("encrypted channel secrets without ENCRYPTION_KEY should make ready fail, got %d %s", readyResp.Code, readyResp.Body.String())
			}
		})
	}
}

func TestAdminSettingUpdateWritesAuditLog(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	gateway := "https://pay.example.com/submit"
	updateResp := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"payment.epay.gateway": gateway,
	})
	if updateResp.Code != http.StatusOK {
		t.Fatalf("setting update failed: %d %s", updateResp.Code, updateResp.Body.String())
	}

	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=setting&resource_id=payment.epay.gateway", rootJWT, nil)
	body := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(body, `"action":"setting.update"`) ||
		!strings.Contains(body, `"resource_type":"setting"`) ||
		!strings.Contains(body, `"resource_id":"payment.epay.gateway"`) {
		t.Fatalf("setting update should write audit log, got %d %s", auditResp.Code, body)
	}
	if strings.Contains(body, gateway) {
		t.Fatalf("setting audit should redact sensitive payment value: %s", body)
	}
}

func TestAdminSettingValidationFailureWritesDeniedAuditLog(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")

	settingSvc := service.NewSettingService()
	beforeGateway, err := settingSvc.Get("payment.epay.gateway")
	if err != nil {
		t.Fatal(err)
	}
	rawInvalidGateway := "not-a-url-secret"
	updateResp := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"payment.epay.gateway": rawInvalidGateway,
	})
	if updateResp.Code != http.StatusBadRequest {
		t.Fatalf("invalid setting update should be rejected, got %d %s", updateResp.Code, updateResp.Body.String())
	}
	afterGateway, err := settingSvc.Get("payment.epay.gateway")
	if err != nil {
		t.Fatal(err)
	}
	if afterGateway != beforeGateway {
		t.Fatalf("invalid setting update should not persist value, before=%q after=%q", beforeGateway, afterGateway)
	}

	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=setting&resource_id=payment.epay.gateway&result=denied&error_code=setting_validation_failed", rootJWT, nil)
	body := auditResp.Body.String()
	if auditResp.Code != http.StatusOK ||
		!strings.Contains(body, `"action":"setting.denied"`) ||
		!strings.Contains(body, `"resource_id":"payment.epay.gateway"`) ||
		!strings.Contains(body, `"result":"denied"`) ||
		!strings.Contains(body, `"error_code":"setting_validation_failed"`) {
		t.Fatalf("invalid setting update should write denied audit log, got %d %s", auditResp.Code, body)
	}
	if strings.Contains(body, rawInvalidGateway) {
		t.Fatalf("denied setting audit should redact sensitive attempted value: %s", body)
	}
}

func TestSettingDefaultsBackfillPreservesExistingValues(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	if err := internal.DB.Model(&model.Setting{}).Where("key = ?", "relay.timeout").Update("value", "30").Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.Where("key = ?", "ready.production_strict").Delete(&model.Setting{}).Error; err != nil {
		t.Fatal(err)
	}

	if err := service.NewSettingService().EnsureDefaults(); err != nil {
		t.Fatal(err)
	}

	var relayTimeout model.Setting
	if err := internal.DB.Where("key = ?", "relay.timeout").First(&relayTimeout).Error; err != nil {
		t.Fatal(err)
	}
	if relayTimeout.Value != "30" {
		t.Fatalf("backfill must preserve existing setting values, got %q", relayTimeout.Value)
	}
	var restored model.Setting
	if err := internal.DB.Where("key = ?", "ready.production_strict").First(&restored).Error; err != nil {
		t.Fatalf("missing default setting should be restored: %v", err)
	}
	if restored.Value != "true" {
		t.Fatalf("restored setting should use registry default, got %q", restored.Value)
	}
}

func TestAPIKeyAuthErrorsUseEntryProtocolShape(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}

	anthropicResp := performJSON(r, http.MethodPost, "/v1/messages", "Bearer sk-invalid", map[string]interface{}{
		"model": "claude-test",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if anthropicResp.Code != http.StatusUnauthorized || !strings.Contains(anthropicResp.Body.String(), `"type":"error"`) || !strings.Contains(anthropicResp.Body.String(), `"type":"authentication_error"`) {
		t.Fatalf("anthropic auth error should use Anthropic shape, got %d %s", anthropicResp.Code, anthropicResp.Body.String())
	}

	geminiResp := performJSON(r, http.MethodPost, "/v1/models/gemini-test:generateContent", "Bearer sk-invalid", map[string]interface{}{
		"contents": []map[string]interface{}{
			{"role": "user", "parts": []map[string]string{{"text": "hello"}}},
		},
	})
	if geminiResp.Code != http.StatusUnauthorized || !strings.Contains(geminiResp.Body.String(), `"code":401`) || !strings.Contains(geminiResp.Body.String(), `"status":"UNAUTHENTICATED"`) {
		t.Fatalf("gemini auth error should use Gemini shape, got %d %s", geminiResp.Code, geminiResp.Body.String())
	}
}

func TestAnthropicAndGeminiEntrypointsConvertSuccessAndDegradeFields(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamBodies := make([]string, 0, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(req.Body)
		upstreamBodies = append(upstreamBodies, raw.String())
		if strings.Contains(raw.String(), "upstream-secret") {
			t.Errorf("upstream request body leaked channel secret: %s", raw.String())
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(raw.String(), `"model":"claude-test"`):
			_, _ = w.Write([]byte(`{"id":"chatcmpl-anthropic","object":"chat.completion","model":"claude-test","choices":[{"index":0,"message":{"role":"assistant","content":"anthropic ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`))
		case strings.Contains(raw.String(), `"model":"gemini-test"`):
			_, _ = w.Write([]byte(`{"id":"chatcmpl-gemini","object":"chat.completion","model":"gemini-test","choices":[{"index":0,"message":{"role":"assistant","content":"gemini ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11}}`))
		default:
			t.Errorf("unexpected upstream body: %s", raw.String())
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "protocols",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "protocols",
		"models":   "claude-test,gemini-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	anthropicResp := performJSON(r, http.MethodPost, "/v1/messages", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model":      "claude-test",
		"max_tokens": 64,
		"system":     "be precise",
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "hello"},
					{"type": "tool_use", "id": "toolu_1", "name": "lookup", "input": map[string]string{"q": "routerx"}},
				},
			},
		},
		"temperature":    0.2,
		"stop_sequences": []string{"END"},
	})
	if anthropicResp.Code != http.StatusOK || !strings.Contains(anthropicResp.Body.String(), `"type":"message"`) || !strings.Contains(anthropicResp.Body.String(), `"text":"anthropic ok"`) || !strings.Contains(anthropicResp.Body.String(), `"input_tokens":3`) || !strings.Contains(anthropicResp.Body.String(), `"output_tokens":4`) {
		t.Fatalf("anthropic success should use Anthropic shape and usage, got %d %s", anthropicResp.Code, anthropicResp.Body.String())
	}
	if len(upstreamBodies) != 1 || !strings.Contains(upstreamBodies[0], "be precise") || !strings.Contains(upstreamBodies[0], "tool_use") || !strings.Contains(upstreamBodies[0], `"stop":["END"]`) {
		t.Fatalf("anthropic conversion should preserve system, stop and degraded content blocks, got %#v", upstreamBodies)
	}

	geminiResp := performJSON(r, http.MethodPost, "/v1/models/gemini-test:generateContent", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"role": "user",
				"parts": []map[string]interface{}{
					{"text": "hello"},
					{"functionCall": map[string]interface{}{"name": "lookup", "args": map[string]string{"q": "routerx"}}},
				},
			},
		},
		"systemInstruction": map[string]interface{}{
			"parts": []map[string]string{{"text": "follow policy"}},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": 9,
			"temperature":     0.1,
			"topP":            0.8,
			"stopSequences":   []string{"STOP"},
		},
	})
	if geminiResp.Code != http.StatusOK || !strings.Contains(geminiResp.Body.String(), `"candidates"`) || !strings.Contains(geminiResp.Body.String(), `"text":"gemini ok"`) || !strings.Contains(geminiResp.Body.String(), `"finishReason":"STOP"`) || !strings.Contains(geminiResp.Body.String(), `"totalTokenCount":11`) {
		t.Fatalf("gemini success should use Gemini shape and usage, got %d %s", geminiResp.Code, geminiResp.Body.String())
	}
	if len(upstreamBodies) != 2 || !strings.Contains(upstreamBodies[1], "follow policy") || !strings.Contains(upstreamBodies[1], "functionCall") || !strings.Contains(upstreamBodies[1], `"max_tokens":9`) {
		t.Fatalf("gemini conversion should preserve system, config and degraded non-text parts, got %#v", upstreamBodies)
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 32 {
		t.Fatalf("protocol calls should deduct combined usage from token budget, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 82 {
		t.Fatalf("protocol calls should deduct combined usage from user quota, got %d", root.Quota)
	}
}

func TestGeminiEmbedContentConvertsOpenAIEmbeddingsAndDeductsUsage(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamPath := ""
	upstreamBody := map[string]interface{}{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPath = req.URL.Path
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(req.Body)
		if err := json.Unmarshal(raw.Bytes(), &upstreamBody); err != nil {
			t.Errorf("upstream body should be json: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.25,0.5]}],"model":"text-embedding-test","usage":{"prompt_tokens":6,"total_tokens":6}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "gemini-embed",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "gemini-embed",
		"models":   "text-embedding-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	resp := performJSON(r, http.MethodPost, "/v1/models/text-embedding-test:embedContent", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"content": map[string]interface{}{
			"parts": []map[string]interface{}{
				{"text": "hello"},
				{"text": "world"},
			},
		},
	})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"embedding":{"values":[0.25,0.5]}`) {
		t.Fatalf("gemini embedContent should return Gemini embedding response, got %d %s", resp.Code, resp.Body.String())
	}
	if upstreamCalls != 1 || upstreamPath != "/v1/embeddings" {
		t.Fatalf("gemini embedContent should call OpenAI embeddings upstream once, calls=%d path=%q", upstreamCalls, upstreamPath)
	}
	if upstreamBody["model"] != "text-embedding-test" || upstreamBody["input"] != "hello\nworld" {
		t.Fatalf("gemini embedContent should map path model and text parts to embeddings request, got %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 44 {
		t.Fatalf("gemini embedContent usage should deduct token budget by 6, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 94 {
		t.Fatalf("gemini embedContent usage should deduct user quota by 6, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 6 || callLog.TotalTokens != 6 || callLog.PromptTokens != 6 || callLog.CompletionTokens != 0 {
		t.Fatalf("unexpected gemini embedContent success log: %+v", callLog)
	}
}

func TestGeminiBatchEmbedContentsConvertsOpenAIEmbeddingsAndDeductsUsage(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamPath := ""
	upstreamBody := map[string]interface{}{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPath = req.URL.Path
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(req.Body)
		if err := json.Unmarshal(raw.Bytes(), &upstreamBody); err != nil {
			t.Errorf("upstream body should be json: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]},{"object":"embedding","index":1,"embedding":[0.3,0.4]}],"model":"text-embedding-test","usage":{"prompt_tokens":8,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "gemini-batch-embed",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "gemini-batch-embed",
		"models":   "text-embedding-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	resp := performJSON(r, http.MethodPost, "/v1/models/text-embedding-test:batchEmbedContents", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"requests": []map[string]interface{}{
			{
				"content": map[string]interface{}{
					"parts": []map[string]interface{}{{"text": "hello"}},
				},
			},
			{
				"content": map[string]interface{}{
					"parts": []map[string]interface{}{{"text": "world"}},
				},
			},
		},
	})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"embeddings":[{"values":[0.1,0.2]},{"values":[0.3,0.4]}]`) {
		t.Fatalf("gemini batchEmbedContents should return Gemini embeddings response, got %d %s", resp.Code, resp.Body.String())
	}
	if upstreamCalls != 1 || upstreamPath != "/v1/embeddings" {
		t.Fatalf("gemini batchEmbedContents should call OpenAI embeddings upstream once, calls=%d path=%q", upstreamCalls, upstreamPath)
	}
	input, ok := upstreamBody["input"].([]interface{})
	if !ok || len(input) != 2 || input[0] != "hello" || input[1] != "world" || upstreamBody["model"] != "text-embedding-test" {
		t.Fatalf("gemini batchEmbedContents should map requests to embeddings input array, got %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 42 {
		t.Fatalf("gemini batchEmbedContents usage should deduct token budget by 8, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 92 {
		t.Fatalf("gemini batchEmbedContents usage should deduct user quota by 8, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 8 || callLog.TotalTokens != 8 || callLog.PromptTokens != 8 || callLog.CompletionTokens != 0 {
		t.Fatalf("unexpected gemini batchEmbedContents success log: %+v", callLog)
	}
}

func TestGeminiStreamGenerateContentConvertsOpenAISSEAndDeductsUsage(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamPath := ""
	var upstreamBody map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPath = req.URL.Path
		if err := json.NewDecoder(req.Body).Decode(&upstreamBody); err != nil {
			t.Errorf("upstream received invalid JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-gemini-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"he\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-gemini-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"llo\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "gemini-stream",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "gemini-stream",
		"models":   "gemini-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	streamResp := performJSON(r, http.MethodPost, "/v1/models/gemini-test:streamGenerateContent", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"role":  "user",
				"parts": []map[string]string{{"text": "hello"}},
			},
		},
		"generationConfig": map[string]interface{}{"temperature": 0.2},
	})
	body := streamResp.Body.String()
	if streamResp.Code != http.StatusOK || !strings.Contains(streamResp.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("gemini stream should return SSE, got %d %s %s", streamResp.Code, streamResp.Header().Get("Content-Type"), body)
	}
	if !strings.Contains(body, `"candidates"`) || !strings.Contains(body, `"text":"he"`) || !strings.Contains(body, `"text":"llo"`) || !strings.Contains(body, `"usageMetadata"`) || !strings.Contains(body, `"totalTokenCount":5`) {
		t.Fatalf("gemini stream should convert OpenAI chunks to Gemini events, got %s", body)
	}
	if strings.Contains(body, `"choices"`) || strings.Contains(body, `[DONE]`) {
		t.Fatalf("gemini stream should not leak OpenAI stream shape, got %s", body)
	}
	if upstreamCalls != 1 || upstreamPath != "/v1/chat/completions" {
		t.Fatalf("gemini stream should call OpenAI-compatible chat stream once, calls=%d path=%q", upstreamCalls, upstreamPath)
	}
	if upstreamBody["model"] != "gemini-test" || upstreamBody["stream"] != true {
		t.Fatalf("upstream stream request should use canonical OpenAI body, got %#v", upstreamBody)
	}
	messages, ok := upstreamBody["messages"].([]interface{})
	if !ok || len(messages) != 1 || !strings.Contains(fmt.Sprint(messages[0]), "hello") {
		t.Fatalf("upstream stream request should preserve Gemini text content, got %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 45 {
		t.Fatalf("gemini stream usage should deduct token budget by 5, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 95 {
		t.Fatalf("gemini stream usage should deduct user quota by 5, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 5 || callLog.TotalTokens != 5 || callLog.PromptTokens != 2 || callLog.CompletionTokens != 3 {
		t.Fatalf("unexpected gemini stream success log: %+v", callLog)
	}
}

func TestAnthropicMessagesStreamConvertsOpenAISSEAndDeductsUsage(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamPath := ""
	var upstreamBody map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPath = req.URL.Path
		if err := json.NewDecoder(req.Body).Decode(&upstreamBody); err != nil {
			t.Errorf("upstream received invalid JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-anthropic-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"he\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-anthropic-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"llo\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "anthropic-stream",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "anthropic-stream",
		"models":   "claude-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	streamResp := performJSON(r, http.MethodPost, "/v1/messages", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model":      "claude-test",
		"max_tokens": 32,
		"stream":     true,
		"messages": []map[string]interface{}{
			{
				"role":    "user",
				"content": "hello",
			},
		},
	})
	body := streamResp.Body.String()
	if streamResp.Code != http.StatusOK || !strings.Contains(streamResp.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("anthropic stream should return SSE, got %d %s %s", streamResp.Code, streamResp.Header().Get("Content-Type"), body)
	}
	if !strings.Contains(body, "event: message_start") || !strings.Contains(body, "event: content_block_delta") || !strings.Contains(body, `"text":"he"`) || !strings.Contains(body, `"text":"llo"`) || !strings.Contains(body, `"output_tokens":3`) || !strings.Contains(body, "event: message_stop") {
		t.Fatalf("anthropic stream should convert OpenAI chunks to Anthropic events, got %s", body)
	}
	if strings.Contains(body, `"choices"`) || strings.Contains(body, `[DONE]`) {
		t.Fatalf("anthropic stream should not leak OpenAI stream shape, got %s", body)
	}
	if upstreamCalls != 1 || upstreamPath != "/v1/chat/completions" {
		t.Fatalf("anthropic stream should call OpenAI-compatible chat stream once, calls=%d path=%q", upstreamCalls, upstreamPath)
	}
	if upstreamBody["model"] != "claude-test" || upstreamBody["stream"] != true {
		t.Fatalf("upstream stream request should use canonical OpenAI body, got %#v", upstreamBody)
	}
	messages, ok := upstreamBody["messages"].([]interface{})
	if !ok || len(messages) != 1 || !strings.Contains(fmt.Sprint(messages[0]), "hello") {
		t.Fatalf("upstream stream request should preserve Anthropic text content, got %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 45 {
		t.Fatalf("anthropic stream usage should deduct token budget by 5, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 95 {
		t.Fatalf("anthropic stream usage should deduct user quota by 5, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 5 || callLog.TotalTokens != 5 || callLog.PromptTokens != 2 || callLog.CompletionTokens != 3 {
		t.Fatalf("unexpected anthropic stream success log: %+v", callLog)
	}
}

func TestAnthropicAndGeminiEntrypointsMapUpstreamErrorsToEntryProtocol(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"temporary","secret":"upstream-secret"}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "protocol-errors",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "protocol-errors",
		"models":   "claude-test,gemini-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	anthropicResp := performJSON(r, http.MethodPost, "/v1/messages", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model":      "claude-test",
		"max_tokens": 16,
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	anthropicBody := anthropicResp.Body.String()
	if anthropicResp.Code != http.StatusBadGateway || !strings.Contains(anthropicBody, `"type":"error"`) || !strings.Contains(anthropicBody, `"type":"upstream_error"`) || strings.Contains(anthropicBody, `"code":"upstream_500"`) || strings.Contains(anthropicBody, "upstream-secret") {
		t.Fatalf("anthropic upstream error should use Anthropic shape and stay sanitized, got %d %s", anthropicResp.Code, anthropicBody)
	}

	geminiResp := performJSON(r, http.MethodPost, "/v1/models/gemini-test:generateContent", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"contents": []map[string]interface{}{
			{"role": "user", "parts": []map[string]string{{"text": "hello"}}},
		},
	})
	geminiBody := geminiResp.Body.String()
	if geminiResp.Code != http.StatusBadGateway || !strings.Contains(geminiBody, `"code":502`) || !strings.Contains(geminiBody, `"status":"UNAVAILABLE"`) || strings.Contains(geminiBody, `"code":"upstream_500"`) || strings.Contains(geminiBody, "upstream-secret") {
		t.Fatalf("gemini upstream error should use Gemini shape and stay sanitized, got %d %s", geminiResp.Code, geminiBody)
	}
	if upstreamCalls != 2 {
		t.Fatalf("expected one upstream call per protocol request, got %d", upstreamCalls)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 50 {
		t.Fatalf("protocol upstream errors should not deduct token budget, got %d", storedToken.RemainQuota)
	}
}

func TestRateLimitUsesSettingsAndEntryProtocolErrorShape(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-rate-limit","object":"chat.completion","model":"claude-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	redisServer := newRouterFakeRedisServer(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr(), Protocol: 2, DisableIdentity: true})
	internal.RDB = rdb
	t.Cleanup(func() {
		_ = rdb.Close()
		internal.RDB = nil
	})
	if err := service.NewSettingService().Set("rate_limit.per_token_per_min", "1"); err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "limited",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "limited",
		"models":   "claude-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	body := map[string]interface{}{
		"model":      "claude-test",
		"max_tokens": 16,
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	first := performJSON(r, http.MethodPost, "/v1/messages", "Bearer "+tokenPayload.Data.Key, body)
	if first.Code != http.StatusOK {
		t.Fatalf("first request should pass before token limit is exceeded, got %d %s", first.Code, first.Body.String())
	}
	second := performJSON(r, http.MethodPost, "/v1/messages", "Bearer "+tokenPayload.Data.Key, body)
	secondBody := second.Body.String()
	if second.Code != http.StatusTooManyRequests || !strings.Contains(secondBody, `"type":"error"`) || !strings.Contains(secondBody, `"type":"rate_limit_error"`) || strings.Contains(secondBody, `"code":"rate_limit_exceeded"`) {
		t.Fatalf("rate limit should use Anthropic error shape, got %d %s", second.Code, secondBody)
	}
	if upstreamCalls != 1 {
		t.Fatalf("limited request should be rejected before upstream, got %d upstream calls", upstreamCalls)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 48 {
		t.Fatalf("only the successful request should deduct quota, got %d", storedToken.RemainQuota)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND error_msg LIKE ?", common.LogStatusFailed, tokenPayload.Data.ID, "%rate limit%").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("rate limit rejection should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["kind"] != "policy" ||
		policySnapshot["access_decision"] != "deny" ||
		policySnapshot["reject_code"] != "rate_limit_exceeded" ||
		policySnapshot["quota_precheck"] != "rate_limit_exceeded" ||
		scopeResult["rate_limit"] != "deny" ||
		scopeResult["rate_limit_dimension"] != "token" {
		t.Fatalf("unexpected rate limit policy snapshot: %+v", policySnapshot)
	}
}

func TestRateLimitPerUserAppliesAcrossAPIKeys(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-user-limit","object":"chat.completion","model":"gpt-user-limit","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	redisServer := newRouterFakeRedisServer(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr(), Protocol: 2, DisableIdentity: true})
	internal.RDB = rdb
	t.Cleanup(func() {
		_ = rdb.Close()
		internal.RDB = nil
	})
	settingSvc := service.NewSettingService()
	if err := settingSvc.Set("rate_limit.global_per_min", "0"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("rate_limit.per_ip_per_min", "0"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("rate_limit.per_token_per_min", "0"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("rate_limit.per_user_per_min", "1"); err != nil {
		t.Fatal(err)
	}

	createToken := func(name string) (uint, string) {
		resp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
			"name":         name,
			"remain_quota": 50,
		})
		var payload struct {
			Data struct {
				ID  uint   `json:"id"`
				Key string `json:"key"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if resp.Code != http.StatusOK || payload.Data.ID == 0 || payload.Data.Key == "" {
			t.Fatalf("create token %s failed: %d %s", name, resp.Code, resp.Body.String())
		}
		return payload.Data.ID, payload.Data.Key
	}
	firstTokenID, firstKey := createToken("user-limit-a")
	secondTokenID, secondKey := createToken("user-limit-b")

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "user-limit",
		"models":   "gpt-user-limit",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}
	body := map[string]interface{}{
		"model": "gpt-user-limit",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	first := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+firstKey, body)
	if first.Code != http.StatusOK {
		t.Fatalf("first request should pass user limit, got %d %s", first.Code, first.Body.String())
	}
	second := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+secondKey, body)
	if second.Code != http.StatusTooManyRequests || !strings.Contains(second.Body.String(), `"code":"rate_limit_exceeded"`) {
		t.Fatalf("second token for same user should be blocked by user limit, got %d %s", second.Code, second.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("user-level limited request should not call upstream, got %d", upstreamCalls)
	}
	var firstToken, secondToken model.Token
	if err := internal.DB.First(&firstToken, firstTokenID).Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.First(&secondToken, secondTokenID).Error; err != nil {
		t.Fatal(err)
	}
	if firstToken.RemainQuota != 48 || secondToken.RemainQuota != 50 {
		t.Fatalf("only first token should be charged, first=%d second=%d", firstToken.RemainQuota, secondToken.RemainQuota)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND error_msg LIKE ?", common.LogStatusFailed, secondTokenID, "%user rate limit%").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("user rate limit rejection should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["reject_code"] != "rate_limit_exceeded" ||
		policySnapshot["quota_precheck"] != "rate_limit_exceeded" ||
		scopeResult["rate_limit"] != "deny" ||
		scopeResult["rate_limit_dimension"] != "user" {
		t.Fatalf("unexpected user rate limit policy snapshot: %+v", policySnapshot)
	}
}

func TestRateLimitPerModelRejectsBeforeUpstream(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-model-limit","object":"chat.completion","model":"gpt-model-limit","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	redisServer := newRouterFakeRedisServer(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr(), Protocol: 2, DisableIdentity: true})
	internal.RDB = rdb
	t.Cleanup(func() {
		_ = rdb.Close()
		internal.RDB = nil
	})
	settingSvc := service.NewSettingService()
	for key, value := range map[string]string{
		"rate_limit.global_per_min":    "0",
		"rate_limit.per_ip_per_min":    "0",
		"rate_limit.per_token_per_min": "0",
		"rate_limit.per_user_per_min":  "0",
		"rate_limit.per_model_per_min": "1",
	} {
		if err := settingSvc.Set(key, value); err != nil {
			t.Fatalf("set %s failed: %v", key, err)
		}
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "model-limited",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.ID == 0 || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "model-limit",
		"models":   "gpt-model-limit",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}
	body := map[string]interface{}{
		"model": "gpt-model-limit",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	first := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, body)
	if first.Code != http.StatusOK {
		t.Fatalf("first request should pass model limit, got %d %s", first.Code, first.Body.String())
	}
	second := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, body)
	if second.Code != http.StatusTooManyRequests || !strings.Contains(second.Body.String(), `"code":"rate_limit_exceeded"`) {
		t.Fatalf("second request for same model should be blocked by model limit, got %d %s", second.Code, second.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("model-level limited request should not call upstream, got %d", upstreamCalls)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 48 {
		t.Fatalf("only first request should be charged, got %d", storedToken.RemainQuota)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND model = ? AND error_msg LIKE ?", common.LogStatusFailed, tokenPayload.Data.ID, "gpt-model-limit", "%model rate limit%").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("model rate limit rejection should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["reject_code"] != "rate_limit_exceeded" ||
		policySnapshot["quota_precheck"] != "rate_limit_exceeded" ||
		scopeResult["rate_limit"] != "deny" ||
		scopeResult["rate_limit_dimension"] != "model" {
		t.Fatalf("unexpected model rate limit policy snapshot: %+v", policySnapshot)
	}
}

func TestRateLimitPerChannelRejectsBeforeUpstream(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-channel-limit","object":"chat.completion","model":"gpt-channel-limit","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	redisServer := newRouterFakeRedisServer(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr(), Protocol: 2, DisableIdentity: true})
	internal.RDB = rdb
	t.Cleanup(func() {
		_ = rdb.Close()
		internal.RDB = nil
	})
	settingSvc := service.NewSettingService()
	for key, value := range map[string]string{
		"rate_limit.global_per_min":      "0",
		"rate_limit.per_ip_per_min":      "0",
		"rate_limit.per_token_per_min":   "0",
		"rate_limit.per_user_per_min":    "0",
		"rate_limit.per_model_per_min":   "0",
		"rate_limit.per_channel_per_min": "1",
	} {
		if err := settingSvc.Set(key, value); err != nil {
			t.Fatalf("set %s failed: %v", key, err)
		}
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "channel-limited",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.ID == 0 || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "channel-limit",
		"models":   "gpt-channel-limit",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	var channelPayload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(channelResp.Body.Bytes(), &channelPayload); err != nil {
		t.Fatal(err)
	}
	if channelResp.Code != http.StatusOK || channelPayload.Data.ID == 0 {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}
	body := map[string]interface{}{
		"model": "gpt-channel-limit",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	first := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, body)
	if first.Code != http.StatusOK {
		t.Fatalf("first request should pass channel limit, got %d %s", first.Code, first.Body.String())
	}
	second := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, body)
	if second.Code != http.StatusTooManyRequests || !strings.Contains(second.Body.String(), `"code":"rate_limit_exceeded"`) {
		t.Fatalf("second request for same channel should be blocked by channel limit, got %d %s", second.Code, second.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("channel-level limited request should not call upstream, got %d", upstreamCalls)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 48 {
		t.Fatalf("only first request should be charged, got %d", storedToken.RemainQuota)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND channel_id = ? AND model = ? AND error_msg LIKE ?", common.LogStatusFailed, tokenPayload.Data.ID, channelPayload.Data.ID, "gpt-channel-limit", "%channel rate limit%").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("channel rate limit rejection should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["reject_code"] != "rate_limit_exceeded" ||
		policySnapshot["quota_precheck"] != "rate_limit_exceeded" ||
		scopeResult["channel_group"] != "allow" ||
		scopeResult["rate_limit"] != "deny" ||
		scopeResult["rate_limit_dimension"] != "channel" {
		t.Fatalf("unexpected channel rate limit policy snapshot: %+v", policySnapshot)
	}
}

func TestRelayPrecheckRejectsBeforeUpstream(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusTeapot)
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "precheck",
		"models":   "gpt-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	var channelPayload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(channelResp.Body.Bytes(), &channelPayload); err != nil {
		t.Fatal(err)
	}
	if channelResp.Code != http.StatusOK || channelPayload.Data.ID == 0 {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	createToken := func(name string, remainQuota int64) (uint, string) {
		t.Helper()
		tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
			"name":         name,
			"remain_quota": remainQuota,
		})
		var tokenPayload struct {
			Data struct {
				ID  uint   `json:"id"`
				Key string `json:"key"`
			} `json:"data"`
		}
		if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
			t.Fatal(err)
		}
		if tokenResp.Code != http.StatusOK || tokenPayload.Data.ID == 0 || tokenPayload.Data.Key == "" {
			t.Fatalf("create token %q failed: %d %s", name, tokenResp.Code, tokenResp.Body.String())
		}
		return tokenPayload.Data.ID, tokenPayload.Data.Key
	}
	chat := func(key string) *httptest.ResponseRecorder {
		t.Helper()
		return performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+key, map[string]interface{}{
			"model": "gpt-test",
			"messages": []map[string]string{
				{"role": "user", "content": "hello"},
			},
		})
	}

	invalidKeyResp := chat("sk-invalid")
	if invalidKeyResp.Code != http.StatusUnauthorized {
		t.Fatalf("invalid key should be rejected before upstream, got %d %s", invalidKeyResp.Code, invalidKeyResp.Body.String())
	}

	exhaustedTokenID, exhaustedKey := createToken("exhausted", 0)
	exhaustedResp := chat(exhaustedKey)
	if exhaustedResp.Code != http.StatusTooManyRequests {
		t.Fatalf("exhausted key should be rejected before upstream, got %d %s", exhaustedResp.Code, exhaustedResp.Body.String())
	}

	disabledTokenID, disabledKey := createToken("disabled", 10)
	disableTokenResp := performJSON(r, http.MethodPut, "/v0/user/token/"+uintString(disabledTokenID), rootJWT, map[string]interface{}{
		"status": common.TokenStatusDisabled,
	})
	if disableTokenResp.Code != http.StatusOK {
		t.Fatalf("disable token failed: %d %s", disableTokenResp.Code, disableTokenResp.Body.String())
	}
	disabledTokenResp := chat(disabledKey)
	if disabledTokenResp.Code != http.StatusUnauthorized {
		t.Fatalf("disabled key should be rejected before upstream, got %d %s", disabledTokenResp.Code, disabledTokenResp.Body.String())
	}

	zeroUserTokenID, zeroUserKey := createToken("zero-user", 10)
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(0)).Error; err != nil {
		t.Fatal(err)
	}
	zeroUserResp := chat(zeroUserKey)
	if zeroUserResp.Code != http.StatusTooManyRequests {
		t.Fatalf("zero user quota should be rejected before upstream, got %d %s", zeroUserResp.Code, zeroUserResp.Body.String())
	}
	var zeroUserToken model.Token
	if err := internal.DB.First(&zeroUserToken, zeroUserTokenID).Error; err != nil {
		t.Fatal(err)
	}
	if zeroUserToken.RemainQuota != 10 {
		t.Fatalf("zero user quota precheck should not deduct token budget, got %d", zeroUserToken.RemainQuota)
	}

	noChannelTokenID, noChannelKey := createToken("no-channel", 10)
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	disableChannelResp := performJSON(r, http.MethodPatch, "/v0/admin/channel/"+uintString(channelPayload.Data.ID)+"/disable", rootJWT, nil)
	if disableChannelResp.Code != http.StatusOK {
		t.Fatalf("disable channel failed: %d %s", disableChannelResp.Code, disableChannelResp.Body.String())
	}
	noChannelResp := chat(noChannelKey)
	if noChannelResp.Code != http.StatusBadGateway || !strings.Contains(noChannelResp.Body.String(), `"code":"no_available_channel"`) {
		t.Fatalf("disabled channel should fail before upstream, got %d %s", noChannelResp.Code, noChannelResp.Body.String())
	}

	if upstreamCalls != 0 {
		t.Fatalf("precheck failures must not call upstream, got %d calls", upstreamCalls)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("precheck failures should not deduct user quota, got %d", root.Quota)
	}
	var noChannelToken model.Token
	if err := internal.DB.First(&noChannelToken, noChannelTokenID).Error; err != nil {
		t.Fatal(err)
	}
	if noChannelToken.RemainQuota != 10 {
		t.Fatalf("channel precheck should not deduct token budget, got %d", noChannelToken.RemainQuota)
	}
	var noChannelFailedLogs int64
	if err := internal.DB.Model(&model.Log{}).Where("status = ? AND token_id = ?", common.LogStatusFailed, noChannelTokenID).Count(&noChannelFailedLogs).Error; err != nil {
		t.Fatal(err)
	}
	if noChannelFailedLogs != 1 {
		t.Fatalf("relay precheck should write one failed log for the routed no-channel rejection, got %d", noChannelFailedLogs)
	}
	var quotaFailedLogs int64
	if err := internal.DB.Model(&model.Log{}).Where("status = ? AND token_id IN ?", common.LogStatusFailed, []uint{exhaustedTokenID, zeroUserTokenID}).Count(&quotaFailedLogs).Error; err != nil {
		t.Fatal(err)
	}
	if quotaFailedLogs != 2 {
		t.Fatalf("quota precheck rejections should write failed logs, got %d", quotaFailedLogs)
	}
}

func TestChatCompletionInvalidRequestDoesNotCallUpstream(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "invalid-request",
		"remain_quota": 10,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "invalid-request",
		"models":   "gpt-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	var channelPayload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(channelResp.Body.Bytes(), &channelPayload); err != nil {
		t.Fatal(err)
	}
	if channelResp.Code != http.StatusOK || channelPayload.Data.ID == 0 {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	cases := []struct {
		name string
		body string
		code string
	}{
		{name: "invalid json", body: `{"model":`, code: "invalid_json"},
		{name: "missing model", body: `{"messages":[{"role":"user","content":"hello"}]}`, code: "model_required"},
	}
	for _, tt := range cases {
		resp := performRaw(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, tt.body)
		if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), `"code":"`+tt.code+`"`) {
			t.Fatalf("%s should return %s before upstream, got %d %s", tt.name, tt.code, resp.Code, resp.Body.String())
		}
	}

	if upstreamCalls != 0 {
		t.Fatalf("invalid local requests must not call upstream, got %d calls", upstreamCalls)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 10 {
		t.Fatalf("invalid local requests should not deduct token budget, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("invalid local requests should not deduct user quota, got %d", root.Quota)
	}
	var channel model.Channel
	if err := internal.DB.First(&channel, channelPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.ErrorCount != 0 {
		t.Fatalf("invalid local requests should not mark channel failure, got %d", channel.ErrorCount)
	}
	var logCount int64
	if err := internal.DB.Model(&model.Log{}).Count(&logCount).Error; err != nil {
		t.Fatal(err)
	}
	if logCount != 0 {
		t.Fatalf("invalid local requests should not write relay logs, got %d", logCount)
	}
}

func TestChannelRoutingConfigResolution(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamAuth := ""
	var upstreamBody map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamAuth = req.Header.Get("Authorization")
		if err := json.NewDecoder(req.Body).Decode(&upstreamBody); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-routing",
			"object": "chat.completion",
			"model": "upstream-model",
			"choices": [
				{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}
			],
			"usage": {"prompt_tokens": 2, "completion_tokens": 2, "total_tokens": 4}
		}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":               common.ChannelTypeOpenAICompat,
		"name":               "routing",
		"models":             "client-model",
		"base_url":           "http://127.0.0.1:1",
		"base_urls":          []string{"http://127.0.0.1:2/"},
		"api_key":            "outer-secret-a",
		"api_keys":           []string{"outer-secret-b"},
		"key_selection_mode": "mystery",
		"upstreams": []map[string]string{
			{"base_url": upstream.URL + "/", "api_key": "upstream-secret"},
		},
		"model_rewrites": map[string]string{"client-model": "upstream-model"},
	})
	var channelPayload struct {
		Data struct {
			ID               uint   `json:"id"`
			KeySelectionMode string `json:"key_selection_mode"`
			APIKeyCount      int    `json:"api_key_count"`
			Upstreams        []struct {
				BaseURL   string `json:"base_url"`
				HasAPIKey bool   `json:"has_api_key"`
			} `json:"upstreams"`
		} `json:"data"`
	}
	if err := json.Unmarshal(channelResp.Body.Bytes(), &channelPayload); err != nil {
		t.Fatal(err)
	}
	if channelResp.Code != http.StatusOK || channelPayload.Data.ID == 0 {
		t.Fatalf("create routing channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}
	if channelPayload.Data.KeySelectionMode != "round_robin" || channelPayload.Data.APIKeyCount != 3 {
		t.Fatalf("unexpected normalized channel payload: %s", channelResp.Body.String())
	}
	if len(channelPayload.Data.Upstreams) != 1 || channelPayload.Data.Upstreams[0].BaseURL != upstream.URL || !channelPayload.Data.Upstreams[0].HasAPIKey {
		t.Fatalf("upstream public payload should be normalized and secret-free: %s", channelResp.Body.String())
	}
	if strings.Contains(channelResp.Body.String(), "outer-secret") || strings.Contains(channelResp.Body.String(), "upstream-secret") {
		t.Fatalf("channel response leaked secret: %s", channelResp.Body.String())
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "routing",
		"remain_quota": 10,
	})
	var tokenPayload struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "client-model",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if chatResp.Code != http.StatusOK {
		t.Fatalf("chat through routing channel failed: %d %s", chatResp.Code, chatResp.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("expected one upstream call, got %d", upstreamCalls)
	}
	if upstreamAuth != "Bearer upstream-secret" {
		t.Fatalf("upstreams.api_key should take priority over outer keys, got %q", upstreamAuth)
	}
	if upstreamBody["model"] != "upstream-model" {
		t.Fatalf("model rewrite should be applied before upstream call, got %#v", upstreamBody["model"])
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	var routeSnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(callLog.RouteSnapshot), &routeSnapshot); err != nil {
		t.Fatalf("routing log should store route snapshot JSON, got %q: %v", callLog.RouteSnapshot, err)
	}
	modelRewrite, ok := routeSnapshot["model_rewrite"].(map[string]interface{})
	if !ok || modelRewrite["from"] != "client-model" || modelRewrite["to"] != "upstream-model" {
		t.Fatalf("route snapshot should record model rewrite: %+v", routeSnapshot)
	}
	if strings.Contains(chatResp.Body.String(), "outer-secret") || strings.Contains(chatResp.Body.String(), "upstream-secret") {
		t.Fatalf("chat response leaked channel secret: %s", chatResp.Body.String())
	}
}

func TestRouterXRoutePreferenceFiltersChannels(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	freeCalls := 0
	paidCalls := 0
	upstreamHandler := func(label string, calls *int) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			*calls++
			var body map[string]interface{}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Errorf("%s upstream received invalid JSON: %v", label, err)
			}
			if _, ok := body["routerx"]; ok {
				t.Errorf("%s upstream received private routerx field: %#v", label, body["routerx"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-` + label + `","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"` + label + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":1}}`))
		}
	}
	freeUpstream := httptest.NewServer(upstreamHandler("free", &freeCalls))
	defer freeUpstream.Close()
	paidUpstream := httptest.NewServer(upstreamHandler("paid", &paidCalls))
	defer paidUpstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("billing.default_user_channel_group_access", `["free","paid"]`); err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "route",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	createChannel := func(name, group, baseURL string, priority int) {
		t.Helper()
		resp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
			"type":     common.ChannelTypeOpenAICompat,
			"name":     name,
			"models":   "gpt-test",
			"base_url": baseURL,
			"api_key":  "upstream-secret-" + name,
			"group":    group,
			"priority": priority,
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("create %s channel failed: %d %s", name, resp.Code, resp.Body.String())
		}
	}
	createChannel("paid", "paid", paidUpstream.URL, 1)
	createChannel("free", "free", freeUpstream.URL, 50)

	chat := func(routerx interface{}) *httptest.ResponseRecorder {
		body := map[string]interface{}{
			"model": "gpt-test",
			"messages": []map[string]string{
				{"role": "user", "content": "hello"},
			},
		}
		if routerx != nil {
			body["routerx"] = routerx
		}
		return performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, body)
	}

	paidResp := chat(map[string]interface{}{"route": map[string]interface{}{"channel_group": "paid"}})
	if paidResp.Code != http.StatusOK || !strings.Contains(paidResp.Body.String(), "paid") {
		t.Fatalf("paid route should select paid channel, got %d %s", paidResp.Code, paidResp.Body.String())
	}
	if paidCalls != 1 || freeCalls != 0 {
		t.Fatalf("paid route should not fall back to higher-priority free channel, paid=%d free=%d", paidCalls, freeCalls)
	}
	var paidLog model.Log
	if err := internal.DB.Order("id ASC").First(&paidLog).Error; err != nil {
		t.Fatal(err)
	}
	var paidRouteSnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(paidLog.RouteSnapshot), &paidRouteSnapshot); err != nil {
		t.Fatalf("paid route should store route snapshot JSON, got %q: %v", paidLog.RouteSnapshot, err)
	}
	filteredReasons, ok := paidRouteSnapshot["filtered_reasons"].(map[string]interface{})
	if !ok || filteredReasons["route_preference"] != float64(1) {
		t.Fatalf("paid route snapshot should record route preference filtering: %+v", paidRouteSnapshot)
	}

	ignoredResp := chat(map[string]interface{}{"route": map[string]interface{}{"unknown": "keep-compatible"}})
	if ignoredResp.Code != http.StatusOK || !strings.Contains(ignoredResp.Body.String(), "free") {
		t.Fatalf("unknown route keys should be ignored, got %d %s", ignoredResp.Code, ignoredResp.Body.String())
	}
	if paidCalls != 1 || freeCalls != 1 {
		t.Fatalf("ignored route should use normal priority selection, paid=%d free=%d", paidCalls, freeCalls)
	}

	invalidOptions := chat("not-an-object")
	if invalidOptions.Code != http.StatusBadRequest || !strings.Contains(invalidOptions.Body.String(), `"code":"invalid_routerx_options"`) {
		t.Fatalf("invalid routerx options should return 400, got %d %s", invalidOptions.Code, invalidOptions.Body.String())
	}
	invalidRoute := chat(map[string]interface{}{"route": map[string]interface{}{"channel_group": 123}})
	if invalidRoute.Code != http.StatusBadRequest || !strings.Contains(invalidRoute.Body.String(), `"code":"invalid_routerx_route"`) {
		t.Fatalf("invalid routerx route should return 400, got %d %s", invalidRoute.Code, invalidRoute.Body.String())
	}
	noCandidate := chat(map[string]interface{}{"route": map[string]interface{}{"channel_group": "internal"}})
	if noCandidate.Code != http.StatusBadGateway || !strings.Contains(noCandidate.Body.String(), `"code":"no_available_channel"`) {
		t.Fatalf("route with no candidates should return no_available_channel, got %d %s", noCandidate.Code, noCandidate.Body.String())
	}
	var noCandidateLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND error_msg = ?", common.LogStatusFailed, tokenPayload.Data.ID, "no available channel").First(&noCandidateLog).Error; err != nil {
		t.Fatal(err)
	}
	var noCandidatePolicySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(noCandidateLog.PolicySnapshot), &noCandidatePolicySnapshot); err != nil {
		t.Fatalf("no-candidate route should store policy snapshot JSON, got %q: %v", noCandidateLog.PolicySnapshot, err)
	}
	noCandidateScopeResult, ok := noCandidatePolicySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		noCandidatePolicySnapshot["kind"] != "policy" ||
		noCandidatePolicySnapshot["access_decision"] != "deny" ||
		noCandidatePolicySnapshot["reject_code"] != "no_available_channel" ||
		noCandidatePolicySnapshot["quota_precheck"] != "available" ||
		noCandidateScopeResult["api_type"] != "allow" ||
		noCandidateScopeResult["model"] != "allow" ||
		noCandidateScopeResult["route_candidate"] != "deny" {
		t.Fatalf("unexpected no-candidate policy snapshot: %+v", noCandidatePolicySnapshot)
	}
	if paidCalls != 1 || freeCalls != 1 {
		t.Fatalf("invalid or empty route results must not call upstream, paid=%d free=%d", paidCalls, freeCalls)
	}
}

func TestRouterXUpstreamOptionsSupplementRequest(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamAuth := ""
	upstreamAPIKey := ""
	upstreamFeature := ""
	upstreamQuery := ""
	var upstreamBody map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamAuth = req.Header.Get("Authorization")
		upstreamAPIKey = req.Header.Get("X-Api-Key")
		upstreamFeature = req.Header.Get("X-Upstream-Feature")
		upstreamQuery = req.URL.Query().Get("trace")
		if err := json.NewDecoder(req.Body).Decode(&upstreamBody); err != nil {
			t.Errorf("upstream received invalid json: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-upstream-options","object":"chat.completion","model":"gpt-options","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "upstream-options",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "upstream-options",
		"models":   "gpt-options",
		"base_url": upstream.URL,
		"api_key":  "channel-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model":       "gpt-options",
		"temperature": 0.2,
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"routerx": map[string]interface{}{
			"upstream": map[string]interface{}{
				"headers": map[string]string{
					"X-Upstream-Feature": "beta",
					"Authorization":      "Bearer user-supplied",
					"X-Api-Key":          "user-key",
				},
				"query": map[string]string{
					"trace": "enabled",
				},
				"body": map[string]interface{}{
					"reasoning_effort": "high",
					"temperature":      0.9,
					"model":            "evil-model",
				},
			},
		},
	})
	if chatResp.Code != http.StatusOK {
		t.Fatalf("chat completion with upstream options failed: %d %s", chatResp.Code, chatResp.Body.String())
	}
	if upstreamAuth != "Bearer channel-secret" || upstreamAPIKey != "" {
		t.Fatalf("sensitive upstream headers must not be user-controlled, auth=%q x-api-key=%q", upstreamAuth, upstreamAPIKey)
	}
	if upstreamFeature != "beta" || upstreamQuery != "enabled" {
		t.Fatalf("safe upstream options should be forwarded, header=%q query=%q", upstreamFeature, upstreamQuery)
	}
	if upstreamBody["reasoning_effort"] != "high" || upstreamBody["temperature"] != float64(0.2) || upstreamBody["model"] != "gpt-options" {
		t.Fatalf("upstream body options should supplement without overriding existing/internal fields: %#v", upstreamBody)
	}
	if _, ok := upstreamBody["routerx"]; ok {
		t.Fatalf("routerx private field leaked to upstream: %#v", upstreamBody)
	}
}

func TestRouterXProviderOptionsApplyOnlyToSelectedProvider(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	xaiCalls := 0
	openAICalls := 0
	var xaiBody map[string]interface{}
	var openAIBody map[string]interface{}
	xaiUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		xaiCalls++
		if err := json.NewDecoder(req.Body).Decode(&xaiBody); err != nil {
			t.Errorf("xAI upstream received invalid json: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-provider-xai","object":"chat.completion","model":"grok-test","choices":[{"index":0,"message":{"role":"assistant","content":"xai"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer xaiUpstream.Close()
	openAIUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		openAICalls++
		if err := json.NewDecoder(req.Body).Decode(&openAIBody); err != nil {
			t.Errorf("OpenAI-compatible upstream received invalid json: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-provider-openai","object":"chat.completion","model":"grok-test","choices":[{"index":0,"message":{"role":"assistant","content":"openai"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer openAIUpstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "provider-options",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	for _, channel := range []struct {
		channelType int
		name        string
		baseURL     string
	}{
		{channelType: common.ChannelTypeXAI, name: "xai-provider", baseURL: xaiUpstream.URL},
		{channelType: common.ChannelTypeOpenAICompat, name: "openai-provider", baseURL: openAIUpstream.URL},
	} {
		resp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
			"type":     channel.channelType,
			"name":     channel.name,
			"models":   "grok-test",
			"base_url": channel.baseURL,
			"api_key":  channel.name + "-secret",
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("create %s channel failed: %d %s", channel.name, resp.Code, resp.Body.String())
		}
	}

	xaiResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model":       "grok-test",
		"temperature": 0.2,
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"routerx": map[string]interface{}{
			"route": map[string]string{"provider": "xai"},
			"upstream": map[string]interface{}{
				"body": map[string]interface{}{
					"generic_param":     true,
					"search_parameters": map[string]interface{}{"mode": "generic"},
				},
			},
			"provider": map[string]interface{}{
				"openai": map[string]interface{}{"reasoning_effort": "medium"},
				"xai": map[string]interface{}{
					"search_parameters": map[string]interface{}{"mode": "auto"},
					"temperature":       0.9,
					"model":             "evil-model",
				},
			},
		},
	})
	if xaiResp.Code != http.StatusOK {
		t.Fatalf("xAI provider options request failed: %d %s", xaiResp.Code, xaiResp.Body.String())
	}
	searchParameters, ok := xaiBody["search_parameters"].(map[string]interface{})
	if !ok || searchParameters["mode"] != "auto" {
		t.Fatalf("xAI provider options should override generic supplements before merge: %#v", xaiBody)
	}
	if xaiBody["generic_param"] != true || xaiBody["temperature"] != float64(0.2) || xaiBody["model"] != "grok-test" {
		t.Fatalf("provider options should supplement without overriding existing/internal fields: %#v", xaiBody)
	}
	if _, ok := xaiBody["reasoning_effort"]; ok {
		t.Fatalf("non-selected provider options leaked to xAI upstream: %#v", xaiBody)
	}

	openAIResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "grok-test",
		"messages": []map[string]string{
			{"role": "user", "content": "hello again"},
		},
		"routerx": map[string]interface{}{
			"route": map[string]string{"provider": "openai-compatible"},
			"provider": map[string]interface{}{
				"xai": map[string]interface{}{"search_parameters": map[string]interface{}{"mode": "auto"}},
			},
		},
	})
	if openAIResp.Code != http.StatusOK {
		t.Fatalf("OpenAI-compatible provider options request failed: %d %s", openAIResp.Code, openAIResp.Body.String())
	}
	if xaiCalls != 1 || openAICalls != 1 {
		t.Fatalf("requests should route once to each selected provider, xai=%d openai=%d", xaiCalls, openAICalls)
	}
	if _, ok := openAIBody["search_parameters"]; ok {
		t.Fatalf("xAI provider options leaked to OpenAI-compatible upstream: %#v", openAIBody)
	}
}

func TestRouterXCompatibleUpstreamPreservesRouterXAndIncrementsHop(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamHop := ""
	upstreamChain := ""
	var upstreamBody map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamHop = req.Header.Get("X-RouterX-Hop")
		upstreamChain = req.Header.Get("X-RouterX-Chain")
		if err := json.NewDecoder(req.Body).Decode(&upstreamBody); err != nil {
			t.Errorf("routerx upstream received invalid json: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-routerx-hop","object":"chat.completion","model":"gpt-routerx","choices":[{"index":0,"message":{"role":"assistant","content":"routerx"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "routerx-compatible",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeRouterX,
		"name":     "routerx-compatible-hop",
		"models":   "gpt-routerx",
		"base_url": upstream.URL,
		"api_key":  "routerx-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create routerx channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	body := map[string]interface{}{
		"model": "gpt-routerx",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"routerx": map[string]interface{}{
			"route": map[string]string{"provider": "routerx"},
			"provider": map[string]interface{}{
				"xai": map[string]interface{}{"search_parameters": map[string]interface{}{"mode": "auto"}},
			},
		},
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(rawBody))
	req.Header.Set("Authorization", "Bearer "+tokenPayload.Data.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-RouterX-Hop", "1")
	req.Header.Set("X-RouterX-Chain", "edge")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("routerx-compatible request failed: %d %s", resp.Code, resp.Body.String())
	}
	if upstreamHop != "2" {
		t.Fatalf("routerx-compatible upstream should receive incremented hop, got %q", upstreamHop)
	}
	if upstreamChain != "edge,routerx" {
		t.Fatalf("routerx-compatible upstream should receive appended chain, got %q", upstreamChain)
	}
	routerXBody, ok := upstreamBody["routerx"].(map[string]interface{})
	if !ok {
		t.Fatalf("routerx-compatible upstream should receive routerx private field: %#v", upstreamBody)
	}
	route, ok := routerXBody["route"].(map[string]interface{})
	if !ok || route["provider"] != "routerx" {
		t.Fatalf("routerx route should be preserved for next RouterX hop: %#v", routerXBody)
	}
}

func TestRouterXCompatibleUpstreamRejectsHopLimit(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-routerx-hop-limit","object":"chat.completion","model":"gpt-routerx","choices":[{"index":0,"message":{"role":"assistant","content":"routerx"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "routerx-hop-limit",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeRouterX,
		"name":     "routerx-hop-limit",
		"models":   "gpt-routerx",
		"base_url": upstream.URL,
		"api_key":  "routerx-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create routerx channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	rawBody, err := json.Marshal(map[string]interface{}{
		"model": "gpt-routerx",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"routerx": map[string]interface{}{
			"route": map[string]string{"provider": "routerx"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(rawBody))
	req.Header.Set("Authorization", "Bearer "+tokenPayload.Data.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-RouterX-Hop", "3")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "routerx_hop_exceeded") {
		t.Fatalf("routerx hop limit should be rejected locally, got %d %s", resp.Code, resp.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("routerx hop limit rejection must not call upstream, calls=%d", upstreamCalls)
	}
}

func TestRouterXCompatibleUpstreamUsesConfiguredHopLimit(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-routerx-configured-hop-limit","object":"chat.completion","model":"gpt-routerx","choices":[{"index":0,"message":{"role":"assistant","content":"routerx"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("relay.routerx_max_hops", "1"); err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "routerx-configured-hop-limit",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeRouterX,
		"name":     "routerx-configured-hop-limit",
		"models":   "gpt-routerx",
		"base_url": upstream.URL,
		"api_key":  "routerx-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create routerx channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	rawBody, err := json.Marshal(map[string]interface{}{
		"model": "gpt-routerx",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"routerx": map[string]interface{}{
			"route": map[string]string{"provider": "routerx"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(rawBody))
	req.Header.Set("Authorization", "Bearer "+tokenPayload.Data.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-RouterX-Hop", "1")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "routerx_hop_exceeded") {
		t.Fatalf("configured routerx hop limit should be rejected locally, got %d %s", resp.Code, resp.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("configured routerx hop limit rejection must not call upstream, calls=%d", upstreamCalls)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 50 {
		t.Fatalf("configured routerx hop limit rejection should not deduct token budget, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("configured routerx hop limit rejection should not deduct user quota, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusFailed ||
		callLog.QuotaUsed != 0 ||
		callLog.ErrorCode != "routerx_hop_exceeded" ||
		callLog.ErrorSource != common.LogErrorSourceRequest {
		t.Fatalf("unexpected configured routerx hop limit failure log: %+v", callLog)
	}
}

func TestUserGroupChannelGroupAccessFiltersRelayCandidates(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	defaultCalls := 0
	paidCalls := 0
	upstreamHandler := func(label string, calls *int) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			*calls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-access-` + label + `","object":"chat.completion","model":"gpt-access","choices":[{"index":0,"message":{"role":"assistant","content":"` + label + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		}
	}
	defaultUpstream := httptest.NewServer(upstreamHandler("default", &defaultCalls))
	defer defaultUpstream.Close()
	paidUpstream := httptest.NewServer(upstreamHandler("paid", &paidCalls))
	defer paidUpstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "group-access",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	createChannel := func(name, group, baseURL string, priority int) {
		t.Helper()
		resp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
			"type":     common.ChannelTypeOpenAICompat,
			"name":     name,
			"models":   "gpt-access",
			"base_url": baseURL,
			"api_key":  "upstream-secret-" + name,
			"group":    group,
			"priority": priority,
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("create %s channel failed: %d %s", name, resp.Code, resp.Body.String())
		}
	}
	createChannel("paid", "paid", paidUpstream.URL, 50)
	createChannel("default", "default", defaultUpstream.URL, 1)

	chat := func(routerx interface{}) *httptest.ResponseRecorder {
		body := map[string]interface{}{
			"model": "gpt-access",
			"messages": []map[string]string{
				{"role": "user", "content": "hello"},
			},
		}
		if routerx != nil {
			body["routerx"] = routerx
		}
		return performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, body)
	}

	defaultResp := chat(nil)
	if defaultResp.Code != http.StatusOK || !strings.Contains(defaultResp.Body.String(), "default") {
		t.Fatalf("default user group should only use default channel group, got %d %s", defaultResp.Code, defaultResp.Body.String())
	}
	if defaultCalls != 1 || paidCalls != 0 {
		t.Fatalf("user group access should filter higher-priority paid channel, default=%d paid=%d", defaultCalls, paidCalls)
	}

	paidResp := chat(map[string]interface{}{"route": map[string]interface{}{"channel_group": "paid"}})
	if paidResp.Code != http.StatusForbidden || !strings.Contains(paidResp.Body.String(), `"code":"route_forbidden"`) {
		t.Fatalf("route to forbidden channel group should fail before upstream, got %d %s", paidResp.Code, paidResp.Body.String())
	}
	if defaultCalls != 1 || paidCalls != 0 {
		t.Fatalf("forbidden user group route must not call upstream, default=%d paid=%d", defaultCalls, paidCalls)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND model = ?", common.LogStatusFailed, tokenPayload.Data.ID, "gpt-access").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	if failedLog.QuotaUsed != 0 || !strings.Contains(failedLog.ErrorMsg, "user group access") {
		t.Fatalf("user group access denial should write a zero-quota failed log, got %+v", failedLog)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("user group access denial should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["kind"] != "policy" ||
		policySnapshot["access_decision"] != "deny" ||
		policySnapshot["reject_code"] != "route_forbidden" ||
		policySnapshot["quota_precheck"] != "available" ||
		scopeResult["api_type"] != "allow" ||
		scopeResult["model"] != "allow" ||
		scopeResult["user_group_channel_group"] != "deny" {
		t.Fatalf("unexpected user group access policy snapshot: %+v", policySnapshot)
	}
}

func TestChannelModelUserEnabledFiltersRelayCandidates(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	hiddenCalls := 0
	visibleCalls := 0
	upstreamHandler := func(label string, calls *int) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			*calls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-` + label + `","object":"chat.completion","model":"gpt-user-enabled","choices":[{"index":0,"message":{"role":"assistant","content":"` + label + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		}
	}
	hiddenUpstream := httptest.NewServer(upstreamHandler("hidden", &hiddenCalls))
	defer hiddenUpstream.Close()
	visibleUpstream := httptest.NewServer(upstreamHandler("visible", &visibleCalls))
	defer visibleUpstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	createUserResp := performJSON(r, http.MethodPost, "/v0/admin/user", rootJWT, map[string]interface{}{
		"username":     "alice",
		"password":     "password123",
		"display_name": "Alice",
		"role":         common.RoleUser,
		"quota":        100,
	})
	if createUserResp.Code != http.StatusOK {
		t.Fatalf("create ordinary user failed: %d %s", createUserResp.Code, createUserResp.Body.String())
	}
	userJWT := loginBearer(t, r, "alice", "password123")
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", userJWT, map[string]interface{}{
		"name":         "ordinary",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create ordinary token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	createChannel := func(name, baseURL string, priority int) uint {
		t.Helper()
		resp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
			"type":     common.ChannelTypeOpenAICompat,
			"name":     name,
			"models":   "gpt-user-enabled",
			"base_url": baseURL,
			"api_key":  "upstream-secret-" + name,
			"group":    "default",
			"priority": priority,
		})
		var payload struct {
			Data struct {
				ID uint `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if resp.Code != http.StatusOK || payload.Data.ID == 0 {
			t.Fatalf("create %s channel failed: %d %s", name, resp.Code, resp.Body.String())
		}
		return payload.Data.ID
	}
	hiddenChannelID := createChannel("hidden", hiddenUpstream.URL, 50)
	visibleChannelID := createChannel("visible", visibleUpstream.URL, 1)
	if err := internal.DB.Create(&model.ChannelModelPrice{
		ChannelID:       hiddenChannelID,
		Model:           "gpt-user-enabled",
		Enabled:         true,
		UserEnabled:     false,
		PriceMode:       "token",
		OverrideMode:    "override",
		PriceExpression: "total_tokens",
		UnitTokens:      1,
		RuleVersion:     1,
	}).Error; err != nil {
		t.Fatal(err)
	}

	resp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-user-enabled",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "visible") {
		t.Fatalf("ordinary user should use visible channel, got %d %s", resp.Code, resp.Body.String())
	}
	if hiddenCalls != 0 || visibleCalls != 1 {
		t.Fatalf("user_enabled=false channel must not be called by ordinary user, hidden=%d visible=%d", hiddenCalls, visibleCalls)
	}
	var callLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND model = ?", common.LogStatusSuccess, tokenPayload.Data.ID, "gpt-user-enabled").First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.ChannelID == nil || *callLog.ChannelID != visibleChannelID || callLog.QuotaUsed != 2 {
		t.Fatalf("success log should use visible channel and upstream usage, got %+v", callLog)
	}
	var routeSnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(callLog.RouteSnapshot), &routeSnapshot); err != nil {
		t.Fatalf("success log should store route snapshot JSON, got %q: %v", callLog.RouteSnapshot, err)
	}
	filteredReasons, ok := routeSnapshot["filtered_reasons"].(map[string]interface{})
	if !ok || filteredReasons["access_denied"] != float64(1) {
		t.Fatalf("route snapshot should record hidden channel access filter: %+v", routeSnapshot)
	}

	deniedResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-user-enabled",
		"messages": []map[string]string{
			{"role": "user", "content": "hidden please"},
		},
		"routerx": map[string]interface{}{
			"route": map[string]interface{}{"channel_id": hiddenChannelID},
		},
	})
	if deniedResp.Code != http.StatusForbidden || !strings.Contains(deniedResp.Body.String(), `"code":"route_forbidden"`) {
		t.Fatalf("explicit hidden channel route should be forbidden, got %d %s", deniedResp.Code, deniedResp.Body.String())
	}
	if hiddenCalls != 0 || visibleCalls != 1 {
		t.Fatalf("hidden route denial must not call upstream, hidden=%d visible=%d", hiddenCalls, visibleCalls)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND model = ?", common.LogStatusFailed, tokenPayload.Data.ID, "gpt-user-enabled").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	if failedLog.QuotaUsed != 0 || !strings.Contains(failedLog.ErrorMsg, "ordinary users") {
		t.Fatalf("hidden channel denial should write zero-quota failed log, got %+v", failedLog)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("hidden channel denial should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["access_decision"] != "deny" ||
		policySnapshot["reject_code"] != "route_forbidden" ||
		scopeResult["channel_model"] != "deny" {
		t.Fatalf("unexpected hidden channel denial policy snapshot: %+v", policySnapshot)
	}
}

func TestUserBillingMatchesLogs(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		if upstreamCalls == 2 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"bad upstream request","type":"invalid_request_error","code":"bad_request"}}`))
			return
		}
		totalTokens := 5
		if upstreamCalls == 3 {
			totalTokens = 7
		}
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-billing",
			"object": "chat.completion",
			"model": "gpt-test",
			"choices": [
				{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}
			],
			"usage": {"prompt_tokens": 2, "completion_tokens": 3, "total_tokens": ` + strconv.Itoa(totalTokens) + `}
		}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "billing",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "billing",
		"models":   "gpt-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	chatBody := map[string]interface{}{
		"model": "gpt-test",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	first := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	failed := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	second := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	if first.Code != http.StatusOK || failed.Code != http.StatusBadRequest || second.Code != http.StatusOK {
		t.Fatalf("unexpected mixed chat statuses: first=%d failed=%d second=%d failed_body=%s", first.Code, failed.Code, second.Code, failed.Body.String())
	}
	if upstreamCalls != 3 {
		t.Fatalf("expected three upstream calls, got %d", upstreamCalls)
	}

	otherName := "other"
	other := model.User{Username: &otherName, DisplayName: "Other", Role: common.RoleUser, Status: common.UserStatusEnabled, Quota: 100}
	if err := internal.DB.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.Create(&model.Log{UserID: other.ID, Model: "other-model", Status: common.LogStatusSuccess, QuotaUsed: 99, TotalTokens: 99}).Error; err != nil {
		t.Fatal(err)
	}

	var successLogs, failedLogs int64
	if err := internal.DB.Model(&model.Log{}).Where("user_id <> ? AND status = ?", other.ID, common.LogStatusSuccess).Count(&successLogs).Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.Model(&model.Log{}).Where("user_id <> ? AND status = ?", other.ID, common.LogStatusFailed).Count(&failedLogs).Error; err != nil {
		t.Fatal(err)
	}
	if successLogs != 2 || failedLogs != 1 {
		t.Fatalf("unexpected current user log counts: success=%d failed=%d", successLogs, failedLogs)
	}
	var quotaSum, tokenSum int64
	if err := internal.DB.Model(&model.Log{}).
		Where("user_id <> ? AND status = ?", other.ID, common.LogStatusSuccess).
		Select("COALESCE(SUM(quota_used), 0), COALESCE(SUM(total_tokens), 0)").
		Row().Scan(&quotaSum, &tokenSum); err != nil {
		t.Fatal(err)
	}
	if quotaSum != 12 || tokenSum != 12 {
		t.Fatalf("successful logs should sum to 12 quota/tokens, got quota=%d tokens=%d", quotaSum, tokenSum)
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 38 {
		t.Fatalf("token budget should only deduct successful usage, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 88 {
		t.Fatalf("user quota should only deduct successful usage, got %d", root.Quota)
	}

	billingResp := performJSON(r, http.MethodGet, "/v0/user/billing", rootJWT, nil)
	if billingResp.Code != http.StatusOK || !strings.Contains(billingResp.Body.String(), `"call_count":2`) || !strings.Contains(billingResp.Body.String(), `"total_quota":12`) || !strings.Contains(billingResp.Body.String(), `"total_tokens":12`) {
		t.Fatalf("billing should aggregate only current user's successful logs, got %d %s", billingResp.Code, billingResp.Body.String())
	}
	logResp := performJSON(r, http.MethodGet, "/v0/user/log", rootJWT, nil)
	if logResp.Code != http.StatusOK || !strings.Contains(logResp.Body.String(), `"total":3`) || strings.Contains(logResp.Body.String(), "other-model") {
		t.Fatalf("user logs should include only current user's three calls, got %d %s", logResp.Code, logResp.Body.String())
	}
}

func TestRelayMaxRequestBodyBytesRejectsBeforeUpstream(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-limit","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("relay.max_request_body_bytes", "64"); err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "body-limit",
		"remain_quota": 10,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "body-limit",
		"models":   "gpt-limit",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	resp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-limit",
		"messages": []map[string]string{
			{"role": "user", "content": strings.Repeat("x", 200)},
		},
	})
	if resp.Code != http.StatusRequestEntityTooLarge || !strings.Contains(resp.Body.String(), `"code":"request_body_too_large"`) {
		t.Fatalf("oversized relay request should return request_body_too_large, got %d %s", resp.Code, resp.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("oversized local requests must not call upstream, got %d calls", upstreamCalls)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 10 {
		t.Fatalf("oversized local requests should not deduct token budget, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("oversized local requests should not deduct user quota, got %d", root.Quota)
	}
}

func TestChatCompletionSuccessLogsAndDeductsQuota(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamAuth := ""
	upstreamPath := ""
	upstreamRequestID := ""
	var upstreamBody map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamAuth = req.Header.Get("Authorization")
		upstreamPath = req.URL.Path
		upstreamRequestID = req.Header.Get("X-Request-Id")
		if err := json.NewDecoder(req.Body).Decode(&upstreamBody); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-test",
			"object": "chat.completion",
			"model": "gpt-test",
			"choices": [
				{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}
			],
			"usage": {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}
		}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("billing.default_user_channel_group_access", `["paid"]`); err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "limited-sdk",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create limited token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("limited api key creation must not deduct user quota, got %d", root.Quota)
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "compat",
		"models":   "gpt-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
		"group":    "paid",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	requestID := "req-upstream-propagation"
	chatResp := performRawWithHeaders(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, `{
		"model": "gpt-test",
		"messages": [{"role": "user", "content": "hello"}],
		"stream": false,
		"routerx": {"route": {"channel_group": "paid"}}
	}`, map[string]string{"X-Request-Id": requestID})
	if chatResp.Code != http.StatusOK {
		t.Fatalf("chat completion failed: %d %s", chatResp.Code, chatResp.Body.String())
	}
	if strings.Contains(chatResp.Body.String(), "upstream-secret") || strings.Contains(chatResp.Body.String(), tokenPayload.Data.Key) {
		t.Fatalf("chat response leaked secret: %s", chatResp.Body.String())
	}
	if upstreamCalls != 1 || upstreamPath != "/v1/chat/completions" {
		t.Fatalf("expected one upstream chat request, got calls=%d path=%q", upstreamCalls, upstreamPath)
	}
	if upstreamAuth != "Bearer upstream-secret" {
		t.Fatalf("upstream authorization should use channel secret, got %q", upstreamAuth)
	}
	if upstreamRequestID != requestID {
		t.Fatalf("upstream should receive request id header, got %q", upstreamRequestID)
	}
	if upstreamBody["model"] != "gpt-test" {
		t.Fatalf("unexpected upstream model: %#v", upstreamBody["model"])
	}
	if _, ok := upstreamBody["routerx"]; ok {
		t.Fatalf("routerx private field leaked to upstream: %#v", upstreamBody)
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 45 {
		t.Fatalf("token budget should be deducted by usage, got %d", storedToken.RemainQuota)
	}
	if err := internal.DB.First(&root, root.ID).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 95 {
		t.Fatalf("user quota should be deducted by usage, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 5 || callLog.TotalTokens != 5 || callLog.PromptTokens != 3 || callLog.CompletionTokens != 2 {
		t.Fatalf("unexpected success log: %+v", callLog)
	}
	if callLog.UsageSource != common.LogUsageSourceUpstream {
		t.Fatalf("success log should record upstream usage source, got %+v", callLog)
	}
	if callLog.TokenID == nil || *callLog.TokenID != tokenPayload.Data.ID || callLog.ChannelID == nil {
		t.Fatalf("success log should reference token and channel: %+v", callLog)
	}
	var requestSnapshotRaw string
	if err := internal.DB.Model(&model.Log{}).Select("request_snapshot").Where("id = ?", callLog.ID).Scan(&requestSnapshotRaw).Error; err != nil {
		t.Fatal(err)
	}
	var requestSnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(requestSnapshotRaw), &requestSnapshot); err != nil {
		t.Fatalf("success log should store request snapshot JSON, got %q: %v", requestSnapshotRaw, err)
	}
	if requestSnapshot["kind"] != "request" ||
		requestSnapshot["ingress_protocol"] != "openai" ||
		requestSnapshot["api_type"] != "openai.chat" ||
		requestSnapshot["requested_model"] != "gpt-test" ||
		requestSnapshot["stream"] != false ||
		requestSnapshot["request_id"] != callLog.RequestID {
		t.Fatalf("unexpected request snapshot: %+v log=%+v", requestSnapshot, callLog)
	}
	var policySnapshotRaw string
	if err := internal.DB.Model(&model.Log{}).Select("policy_snapshot").Where("id = ?", callLog.ID).Scan(&policySnapshotRaw).Error; err != nil {
		t.Fatal(err)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(policySnapshotRaw), &policySnapshot); err != nil {
		t.Fatalf("success log should store policy snapshot JSON, got %q: %v", policySnapshotRaw, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["kind"] != "policy" ||
		policySnapshot["request_id"] != callLog.RequestID ||
		policySnapshot["access_decision"] != "allow" ||
		policySnapshot["quota_precheck"] != "available" ||
		scopeResult["api_type"] != "allow" ||
		scopeResult["model"] != "allow" ||
		scopeResult["channel_group"] != "allow" {
		t.Fatalf("unexpected policy snapshot: %+v", policySnapshot)
	}
	var routeSnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(callLog.RouteSnapshot), &routeSnapshot); err != nil {
		t.Fatalf("success log should store route snapshot JSON, got %q: %v", callLog.RouteSnapshot, err)
	}
	if routeSnapshot["requested_model"] != "gpt-test" || routeSnapshot["selected_channel_group"] != "paid" || routeSnapshot["candidate_count"] != float64(1) {
		t.Fatalf("unexpected route snapshot: %+v", routeSnapshot)
	}
	if selectedChannelID, ok := routeSnapshot["selected_channel_id"].(float64); !ok || uint(selectedChannelID) != *callLog.ChannelID {
		t.Fatalf("route snapshot should reference selected channel, snapshot=%+v log=%+v", routeSnapshot, callLog)
	}
	var billingSnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(callLog.BillingSnapshot), &billingSnapshot); err != nil {
		t.Fatalf("success log should store billing snapshot JSON, got %q: %v", callLog.BillingSnapshot, err)
	}
	if billingSnapshot["billing_status"] != "settled" || billingSnapshot["usage_source"] != common.LogUsageSourceUpstream || billingSnapshot["final_quota_used"] != float64(5) {
		t.Fatalf("unexpected billing snapshot: %+v", billingSnapshot)
	}
	if billingSnapshot["key_budget_before"] != float64(50) || billingSnapshot["key_budget_after"] != float64(45) || billingSnapshot["user_balance_before"] != float64(100) || billingSnapshot["user_balance_after"] != float64(95) {
		t.Fatalf("billing snapshot should record budget before/after values: %+v", billingSnapshot)
	}
	expressionSnapshot, ok := billingSnapshot["billing_expression_snapshot"].(map[string]interface{})
	if !ok || expressionSnapshot["source"] != "p0_usage" || expressionSnapshot["expression"] != "total_tokens" || expressionSnapshot["base_quota"] != float64(5) {
		t.Fatalf("billing snapshot should record P0 usage expression: %+v", billingSnapshot)
	}
	expressionVariables, ok := expressionSnapshot["variables"].(map[string]interface{})
	if !ok || expressionVariables["total_tokens"] != float64(5) || expressionVariables["prompt_tokens"] != float64(3) || expressionVariables["completion_tokens"] != float64(2) {
		t.Fatalf("billing expression snapshot should record token variables: %+v", expressionSnapshot)
	}
	multiplierSnapshot, ok := billingSnapshot["multiplier_snapshot"].(map[string]interface{})
	if !ok || multiplierSnapshot["effective_ratio"] != float64(1) {
		t.Fatalf("billing snapshot should record default multiplier summary: %+v", billingSnapshot)
	}

	billingResp := performJSON(r, http.MethodGet, "/v0/user/billing", rootJWT, nil)
	if billingResp.Code != http.StatusOK || !strings.Contains(billingResp.Body.String(), `"call_count":1`) || !strings.Contains(billingResp.Body.String(), `"total_quota":5`) || !strings.Contains(billingResp.Body.String(), `"total_tokens":5`) {
		t.Fatalf("billing should aggregate successful logs, got %d %s", billingResp.Code, billingResp.Body.String())
	}
	logResp := performJSON(r, http.MethodGet, "/v0/user/log", rootJWT, nil)
	if logResp.Code != http.StatusOK ||
		!strings.Contains(logResp.Body.String(), `"usage_source":"upstream"`) ||
		!strings.Contains(logResp.Body.String(), `"route_snapshot":`) ||
		!strings.Contains(logResp.Body.String(), `"billing_snapshot":`) {
		t.Fatalf("user log should expose upstream usage source, route snapshot and billing snapshot, got %d %s", logResp.Code, logResp.Body.String())
	}
}

func TestChatCompletionDeductionFailureWritesBillingSnapshot(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-deduct-failed",
			"object": "chat.completion",
			"model": "gpt-deduct-failed",
			"choices": [
				{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}
			],
			"usage": {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}
		}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(3)).Error; err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "deduct-failure",
		"remain_quota": 10,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "deduct-failure-channel",
		"models":   "gpt-deduct-failed",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-deduct-failed",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if chatResp.Code != http.StatusTooManyRequests || !strings.Contains(chatResp.Body.String(), `"code":"insufficient_quota"`) {
		t.Fatalf("deduction failure should return insufficient_quota, got %d %s", chatResp.Code, chatResp.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("deduction failure should happen after one upstream call, got %d", upstreamCalls)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 10 {
		t.Fatalf("failed deduction should not consume token budget, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 3 {
		t.Fatalf("failed deduction should not consume user quota, got %d", root.Quota)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND model = ?", common.LogStatusFailed, tokenPayload.Data.ID, "gpt-deduct-failed").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	if failedLog.QuotaUsed != 0 || failedLog.TotalTokens != 5 || failedLog.ErrorCode != "insufficient_quota" || failedLog.ErrorSource != common.LogErrorSourceQuota {
		t.Fatalf("deduction failure should write zero-quota failed log with usage and stable code, got %+v", failedLog)
	}
	var billingSnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.BillingSnapshot), &billingSnapshot); err != nil {
		t.Fatalf("deduction failure should store billing snapshot JSON, got %q: %v", failedLog.BillingSnapshot, err)
	}
	if billingSnapshot["billing_status"] != "failed" ||
		billingSnapshot["deduction_result"] != "failed" ||
		billingSnapshot["deduction_error_code"] != "insufficient_user_quota" ||
		billingSnapshot["attempted_quota_used"] != float64(5) ||
		billingSnapshot["final_quota_used"] != float64(0) ||
		billingSnapshot["key_budget_before"] != float64(10) ||
		billingSnapshot["key_budget_after"] != float64(10) ||
		billingSnapshot["user_balance_before"] != float64(3) ||
		billingSnapshot["user_balance_after"] != float64(3) {
		t.Fatalf("deduction failure snapshot should explain failed charge: %+v", billingSnapshot)
	}
	expressionSnapshot, ok := billingSnapshot["billing_expression_snapshot"].(map[string]interface{})
	if !ok || expressionSnapshot["base_quota"] != float64(5) {
		t.Fatalf("deduction failure snapshot should preserve base billing expression: %+v", billingSnapshot)
	}
}

func TestChatCompletionUsesModelPriceExpressionForBilling(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-priced",
			"object": "chat.completion",
			"model": "gpt-priced",
			"choices": [
				{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}
			],
			"usage": {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}
		}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("billing.default_user_channel_group_access", `["paid"]`); err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "priced-key",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "priced-compat",
		"models":   "gpt-priced",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
		"group":    "paid",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}
	var channelPayload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(channelResp.Body.Bytes(), &channelPayload); err != nil {
		t.Fatal(err)
	}
	if channelPayload.Data.ID == 0 {
		t.Fatalf("create channel should return id: %s", channelResp.Body.String())
	}
	if err := internal.DB.Create(&model.ModelPrice{
		Model:           "gpt-priced",
		PriceMode:       "token",
		PriceExpression: "prompt_tokens * prompt_multiplier + completion_tokens * completion_multiplier",
		VariablesJSON: model.NewJSONValue(map[string]interface{}{
			"prompt_multiplier":     2,
			"completion_multiplier": 3,
		}),
		UnitTokens:  1,
		RuleVersion: 7,
		Enabled:     true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-priced",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"stream": false,
		"routerx": map[string]interface{}{
			"route": map[string]string{"channel_group": "paid"},
		},
	})
	if chatResp.Code != http.StatusOK {
		t.Fatalf("chat completion failed: %d %s", chatResp.Code, chatResp.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("expected one upstream request, got %d", upstreamCalls)
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 38 {
		t.Fatalf("token budget should be deducted by model price expression, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 88 {
		t.Fatalf("user quota should be deducted by model price expression, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.QuotaUsed != 12 || callLog.TotalTokens != 5 || callLog.PromptTokens != 3 || callLog.CompletionTokens != 2 {
		t.Fatalf("success log should record expression quota and upstream usage, got %+v", callLog)
	}
	var billingSnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(callLog.BillingSnapshot), &billingSnapshot); err != nil {
		t.Fatalf("success log should store billing snapshot JSON, got %q: %v", callLog.BillingSnapshot, err)
	}
	if billingSnapshot["billing_expression_source"] != "model_prices" || billingSnapshot["price_source"] != "model_prices" || billingSnapshot["final_quota_used"] != float64(12) {
		t.Fatalf("billing snapshot should record model price source and expression quota: %+v", billingSnapshot)
	}
	expressionSnapshot, ok := billingSnapshot["billing_expression_snapshot"].(map[string]interface{})
	if !ok || expressionSnapshot["source"] != "model_prices" || expressionSnapshot["expression"] != "prompt_tokens * prompt_multiplier + completion_tokens * completion_multiplier" || expressionSnapshot["base_quota"] != float64(12) || expressionSnapshot["rule_version"] != float64(7) {
		t.Fatalf("billing expression snapshot should record model price expression: %+v", billingSnapshot)
	}
	expressionVariables, ok := expressionSnapshot["variables"].(map[string]interface{})
	if !ok || expressionVariables["prompt_tokens"] != float64(3) || expressionVariables["completion_tokens"] != float64(2) || expressionVariables["total_tokens"] != float64(5) || expressionVariables["prompt_multiplier"] != float64(2) || expressionVariables["completion_multiplier"] != float64(3) {
		t.Fatalf("billing expression snapshot should record token and price variables: %+v", expressionSnapshot)
	}

	if err := internal.DB.Create(&model.ChannelModelPrice{
		ChannelID:       channelPayload.Data.ID,
		Model:           "gpt-priced",
		Enabled:         true,
		UserEnabled:     true,
		PriceMode:       "token",
		OverrideMode:    "override",
		PriceExpression: "total_tokens * channel_price_per_token",
		VariablesJSON: model.NewJSONValue(map[string]interface{}{
			"channel_price_per_token": 4,
		}),
		UnitTokens:  1,
		RuleVersion: 9,
	}).Error; err != nil {
		t.Fatal(err)
	}

	secondResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-priced",
		"messages": []map[string]string{
			{"role": "user", "content": "hello again"},
		},
		"stream": false,
		"routerx": map[string]interface{}{
			"route": map[string]string{"channel_group": "paid"},
		},
	})
	if secondResp.Code != http.StatusOK {
		t.Fatalf("second chat completion failed: %d %s", secondResp.Code, secondResp.Body.String())
	}
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 18 {
		t.Fatalf("token budget should prefer channel model price expression, got %d", storedToken.RemainQuota)
	}
	if err := internal.DB.First(&root, root.ID).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 68 {
		t.Fatalf("user quota should prefer channel model price expression, got %d", root.Quota)
	}
	var secondLog model.Log
	if err := internal.DB.Order("id DESC").First(&secondLog).Error; err != nil {
		t.Fatal(err)
	}
	if secondLog.QuotaUsed != 20 {
		t.Fatalf("second success log should record channel expression quota, got %+v", secondLog)
	}
	if err := json.Unmarshal([]byte(secondLog.BillingSnapshot), &billingSnapshot); err != nil {
		t.Fatalf("second success log should store billing snapshot JSON, got %q: %v", secondLog.BillingSnapshot, err)
	}
	if billingSnapshot["billing_expression_source"] != "channel_model_prices" || billingSnapshot["price_source"] != "channel_model_prices" || billingSnapshot["final_quota_used"] != float64(20) {
		t.Fatalf("billing snapshot should record channel price source and expression quota: %+v", billingSnapshot)
	}
	expressionSnapshot, ok = billingSnapshot["billing_expression_snapshot"].(map[string]interface{})
	if !ok || expressionSnapshot["source"] != "channel_model_prices" || expressionSnapshot["expression"] != "total_tokens * channel_price_per_token" || expressionSnapshot["base_quota"] != float64(20) || expressionSnapshot["rule_version"] != float64(9) {
		t.Fatalf("billing expression snapshot should record channel price expression: %+v", billingSnapshot)
	}
}

func TestChatCompletionAppliesBillingMultipliers(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-ratio",
			"object": "chat.completion",
			"model": "gpt-ratio",
			"choices": [
				{"index": 0, "message": {"role": "assistant", "content": "ratio ok"}, "finish_reason": "stop"}
			],
			"usage": {"prompt_tokens": 4, "completion_tokens": 6, "total_tokens": 10}
		}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := service.NewSettingService().Set("billing.default_user_channel_group_access", `["paid"]`); err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("billing.user_group_ratios", `{"vip":0.5}`); err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("billing.channel_group_ratios", `{"paid":4}`); err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("billing.user_group_channel_ratios", `{"vip":{"paid":1.5}}`); err != nil {
		t.Fatal(err)
	}

	vipGroup := model.Group{Name: "vip", Ratio: 1}
	if err := internal.DB.Create(&vipGroup).Error; err != nil {
		t.Fatal(err)
	}
	createUserResp := performJSON(r, http.MethodPost, "/v0/admin/user", rootJWT, map[string]interface{}{
		"username":     "ratio-user",
		"password":     "password123",
		"display_name": "Ratio User",
		"role":         common.RoleUser,
		"quota":        100,
		"group_id":     vipGroup.ID,
	})
	if createUserResp.Code != http.StatusOK {
		t.Fatalf("create ratio user failed: %d %s", createUserResp.Code, createUserResp.Body.String())
	}
	userJWT := loginBearer(t, r, "ratio-user", "password123")
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", userJWT, map[string]interface{}{
		"name":         "ratio-key",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create ratio token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "ratio-channel",
		"models":   "gpt-ratio",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
		"group":    "paid",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create ratio channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}
	if err := internal.DB.Create(&model.ModelPrice{
		Model:           "gpt-ratio",
		PriceMode:       "token",
		PriceExpression: "total_tokens",
		UnitTokens:      1,
		RuleVersion:     1,
		Enabled:         true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-ratio",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"routerx": map[string]interface{}{
			"route": map[string]string{"channel_group": "paid"},
		},
	})
	if chatResp.Code != http.StatusOK {
		t.Fatalf("chat completion failed: %d %s", chatResp.Code, chatResp.Body.String())
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 35 {
		t.Fatalf("token budget should be deducted by base quota and effective multiplier, got %d", storedToken.RemainQuota)
	}
	var user model.User
	if err := internal.DB.Where("username = ?", "ratio-user").First(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.Quota != 85 {
		t.Fatalf("user quota should be deducted by base quota and effective multiplier, got %d", user.Quota)
	}
	var callLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND model = ?", common.LogStatusSuccess, tokenPayload.Data.ID, "gpt-ratio").First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.QuotaUsed != 15 || callLog.TotalTokens != 10 {
		t.Fatalf("success log should record multiplier-adjusted quota and upstream usage, got %+v", callLog)
	}
	var billingSnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(callLog.BillingSnapshot), &billingSnapshot); err != nil {
		t.Fatalf("success log should store billing snapshot JSON, got %q: %v", callLog.BillingSnapshot, err)
	}
	if billingSnapshot["final_quota_used"] != float64(15) {
		t.Fatalf("billing snapshot should record multiplier-adjusted final quota: %+v", billingSnapshot)
	}
	expressionSnapshot, ok := billingSnapshot["billing_expression_snapshot"].(map[string]interface{})
	if !ok || expressionSnapshot["base_quota"] != float64(10) {
		t.Fatalf("billing expression snapshot should keep pre-multiplier base quota: %+v", billingSnapshot)
	}
	multiplierSnapshot, ok := billingSnapshot["multiplier_snapshot"].(map[string]interface{})
	if !ok ||
		multiplierSnapshot["user_group"] != "vip" ||
		multiplierSnapshot["channel_group"] != "paid" ||
		multiplierSnapshot["user_group_ratio"] != float64(0.5) ||
		multiplierSnapshot["channel_group_ratio"] != float64(4) ||
		multiplierSnapshot["user_group_channel_ratio"] != float64(1.5) ||
		multiplierSnapshot["ratio_mode"] != "user_group_channel_override" ||
		multiplierSnapshot["effective_ratio"] != float64(1.5) {
		t.Fatalf("billing snapshot should record combination override multiplier inputs: %+v", billingSnapshot)
	}

	if err := service.NewSettingService().Set("billing.user_group_channel_ratios", `{}`); err != nil {
		t.Fatal(err)
	}
	secondChatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-ratio",
		"messages": []map[string]string{
			{"role": "user", "content": "hello again"},
		},
		"routerx": map[string]interface{}{
			"route": map[string]string{"channel_group": "paid"},
		},
	})
	if secondChatResp.Code != http.StatusOK {
		t.Fatalf("second chat completion failed: %d %s", secondChatResp.Code, secondChatResp.Body.String())
	}
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 15 {
		t.Fatalf("token budget should use separate user/channel factors when no combination override exists, got %d", storedToken.RemainQuota)
	}
	if err := internal.DB.Where("username = ?", "ratio-user").First(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.Quota != 65 {
		t.Fatalf("user quota should use separate user/channel factors when no combination override exists, got %d", user.Quota)
	}
	callLog = model.Log{}
	if err := internal.DB.Where("status = ? AND token_id = ? AND model = ?", common.LogStatusSuccess, tokenPayload.Data.ID, "gpt-ratio").Order("id DESC").First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.QuotaUsed != 20 || callLog.TotalTokens != 10 {
		t.Fatalf("second success log should record separately multiplied quota and upstream usage, got %+v", callLog)
	}
	if err := json.Unmarshal([]byte(callLog.BillingSnapshot), &billingSnapshot); err != nil {
		t.Fatalf("second success log should store billing snapshot JSON, got %q: %v", callLog.BillingSnapshot, err)
	}
	multiplierSnapshot, ok = billingSnapshot["multiplier_snapshot"].(map[string]interface{})
	if !ok ||
		multiplierSnapshot["user_group_ratio"] != float64(0.5) ||
		multiplierSnapshot["channel_group_ratio"] != float64(4) ||
		multiplierSnapshot["user_group_channel_ratio"] != float64(1) ||
		multiplierSnapshot["ratio_mode"] != "separate_factors" ||
		multiplierSnapshot["effective_ratio"] != float64(2) {
		t.Fatalf("billing snapshot should record separate multiplier inputs: %+v", billingSnapshot)
	}
}

func TestAPIKeyModelScopeRestrictsRelayBeforeUpstream(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-scope","object":"chat.completion","model":"gpt-allowed","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "scoped",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.ID == 0 || tokenPayload.Data.Key == "" {
		t.Fatalf("create scoped token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	scopeResp := performJSON(r, http.MethodPut, "/v0/user/token/"+uintString(tokenPayload.Data.ID)+"/scope", rootJWT, map[string]interface{}{
		"allow_models": []string{"gpt-allowed"},
	})
	if scopeResp.Code != http.StatusOK || !strings.Contains(scopeResp.Body.String(), `"allow_models":["gpt-allowed"]`) {
		t.Fatalf("update token scope failed: %d %s", scopeResp.Code, scopeResp.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "scoped-channel",
		"models":   "gpt-allowed,gpt-denied",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create scoped channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	allowedResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-allowed",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if allowedResp.Code != http.StatusOK {
		t.Fatalf("allowed model should reach upstream, got %d %s", allowedResp.Code, allowedResp.Body.String())
	}

	deniedResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-denied",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if deniedResp.Code != http.StatusForbidden || !strings.Contains(deniedResp.Body.String(), `"code":"model_not_allowed"`) {
		t.Fatalf("denied model should be blocked before upstream, got %d %s", deniedResp.Code, deniedResp.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("scope rejection must not call upstream, got %d calls", upstreamCalls)
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 45 {
		t.Fatalf("denied model should not deduct token budget after one success, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 95 {
		t.Fatalf("denied model should not deduct user quota after one success, got %d", root.Quota)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND model = ?", common.LogStatusFailed, "gpt-denied").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	if failedLog.QuotaUsed != 0 || !strings.Contains(failedLog.ErrorMsg, "scope") {
		t.Fatalf("scope denial should write a zero-quota failed log, got %+v", failedLog)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("scope denial should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["kind"] != "policy" ||
		policySnapshot["access_decision"] != "deny" ||
		policySnapshot["reject_code"] != "model_not_allowed" ||
		policySnapshot["quota_precheck"] != "not_evaluated" ||
		scopeResult["model"] != "deny" ||
		scopeResult["api_type"] != "allow" {
		t.Fatalf("unexpected scope denial policy snapshot: %+v", policySnapshot)
	}
	auditResp := performJSON(r, http.MethodGet, "/v0/admin/audit?resource_type=api_key&resource_id="+uintString(tokenPayload.Data.ID), rootJWT, nil)
	auditBody := auditResp.Body.String()
	if auditResp.Code != http.StatusOK || !strings.Contains(auditBody, `"action":"api_key.scope_updated"`) || strings.Contains(auditBody, tokenPayload.Data.Key) || strings.Contains(auditBody, "sk-") {
		t.Fatalf("scope update audit should be present and secret-free, got %d %s", auditResp.Code, auditBody)
	}
}

func TestAPIKeyAPIScopeRestrictsRelayBeforeUpstream(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamPaths := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPaths = append(upstreamPaths, req.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"id":"chatcmpl-api-scope","object":"chat.completion","model":"gpt-api-scope","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
		case "/v1/embeddings":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"gpt-api-scope","usage":{"prompt_tokens":7,"total_tokens":7}}`))
		default:
			http.NotFound(w, req)
		}
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "api-scoped",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.ID == 0 || tokenPayload.Data.Key == "" {
		t.Fatalf("create scoped token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	scopeResp := performJSON(r, http.MethodPut, "/v0/user/token/"+uintString(tokenPayload.Data.ID)+"/scope", rootJWT, map[string]interface{}{
		"api_types": []string{"openai.chat"},
	})
	if scopeResp.Code != http.StatusOK || !strings.Contains(scopeResp.Body.String(), `"api_types":["openai.chat"]`) {
		t.Fatalf("update token api scope failed: %d %s", scopeResp.Code, scopeResp.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "api-scoped-channel",
		"models":   "gpt-api-scope",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create scoped channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-api-scope",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if chatResp.Code != http.StatusOK {
		t.Fatalf("allowed api type should reach upstream, got %d %s", chatResp.Code, chatResp.Body.String())
	}

	embeddingResp := performJSON(r, http.MethodPost, "/v1/embeddings", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-api-scope",
		"input": "hello",
	})
	if embeddingResp.Code != http.StatusForbidden || !strings.Contains(embeddingResp.Body.String(), `"code":"token_forbidden"`) {
		t.Fatalf("disallowed api type should be blocked before upstream, got %d %s", embeddingResp.Code, embeddingResp.Body.String())
	}
	modelsResp := performJSON(r, http.MethodGet, "/v1/models", "Bearer "+tokenPayload.Data.Key, nil)
	if modelsResp.Code != http.StatusForbidden || !strings.Contains(modelsResp.Body.String(), `"code":"token_forbidden"`) {
		t.Fatalf("disallowed models api type should be blocked, got %d %s", modelsResp.Code, modelsResp.Body.String())
	}
	if upstreamCalls != 1 || len(upstreamPaths) != 1 || upstreamPaths[0] != "/v1/chat/completions" {
		t.Fatalf("api scope rejection must not call upstream, calls=%d paths=%v", upstreamCalls, upstreamPaths)
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 45 {
		t.Fatalf("disallowed api type should not deduct token budget after one success, got %d", storedToken.RemainQuota)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND model = ?", common.LogStatusFailed, "gpt-api-scope").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	if failedLog.QuotaUsed != 0 || !strings.Contains(failedLog.ErrorMsg, "api type") {
		t.Fatalf("api scope denial should write a zero-quota failed log, got %+v", failedLog)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("api scope denial should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["kind"] != "policy" ||
		policySnapshot["access_decision"] != "deny" ||
		policySnapshot["reject_code"] != "token_forbidden" ||
		policySnapshot["quota_precheck"] != "not_evaluated" ||
		scopeResult["api_type"] != "deny" ||
		scopeResult["model"] != "not_evaluated" {
		t.Fatalf("unexpected api scope denial policy snapshot: %+v", policySnapshot)
	}
}

func TestAPIKeyChannelGroupScopeFiltersRelayCandidates(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	cheapCalls := 0
	cheapUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		cheapCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-cheap","object":"chat.completion","model":"gpt-group-scope","choices":[{"index":0,"message":{"role":"assistant","content":"cheap ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2,"total_tokens":4}}`))
	}))
	defer cheapUpstream.Close()

	premiumCalls := 0
	premiumUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		premiumCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-premium","object":"chat.completion","model":"gpt-group-scope","choices":[{"index":0,"message":{"role":"assistant","content":"premium ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":10,"total_tokens":20}}`))
	}))
	defer premiumUpstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("billing.default_user_channel_group_access", `["cheap","premium"]`); err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "group-scoped",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.ID == 0 || tokenPayload.Data.Key == "" {
		t.Fatalf("create scoped token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	scopeResp := performJSON(r, http.MethodPut, "/v0/user/token/"+uintString(tokenPayload.Data.ID)+"/scope", rootJWT, map[string]interface{}{
		"channel_groups": []string{"cheap"},
	})
	if scopeResp.Code != http.StatusOK || !strings.Contains(scopeResp.Body.String(), `"channel_groups":["cheap"]`) {
		t.Fatalf("update token channel group scope failed: %d %s", scopeResp.Code, scopeResp.Body.String())
	}

	premiumChannel := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "premium-channel",
		"models":   "gpt-group-scope",
		"base_url": premiumUpstream.URL,
		"api_key":  "premium-secret",
		"group":    "premium",
		"priority": 10,
	})
	if premiumChannel.Code != http.StatusOK {
		t.Fatalf("create premium channel failed: %d %s", premiumChannel.Code, premiumChannel.Body.String())
	}
	cheapChannel := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "cheap-channel",
		"models":   "gpt-group-scope",
		"base_url": cheapUpstream.URL,
		"api_key":  "cheap-secret",
		"group":    "cheap",
		"priority": 1,
	})
	if cheapChannel.Code != http.StatusOK {
		t.Fatalf("create cheap channel failed: %d %s", cheapChannel.Code, cheapChannel.Body.String())
	}

	chatBody := map[string]interface{}{
		"model": "gpt-group-scope",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	allowedResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	if allowedResp.Code != http.StatusOK || !strings.Contains(allowedResp.Body.String(), "cheap ok") {
		t.Fatalf("allowed channel group should use cheap upstream, got %d %s", allowedResp.Code, allowedResp.Body.String())
	}
	if cheapCalls != 1 || premiumCalls != 0 {
		t.Fatalf("channel group scope should filter higher-priority premium channel, cheap=%d premium=%d", cheapCalls, premiumCalls)
	}

	deniedResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-group-scope",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"routerx": map[string]interface{}{
			"route": map[string]string{"channel_group": "premium"},
		},
	})
	if deniedResp.Code != http.StatusForbidden || !strings.Contains(deniedResp.Body.String(), `"code":"route_forbidden"`) {
		t.Fatalf("disallowed channel group route should be forbidden, got %d %s", deniedResp.Code, deniedResp.Body.String())
	}
	if cheapCalls != 1 || premiumCalls != 0 {
		t.Fatalf("denied channel group route must not call upstream, cheap=%d premium=%d", cheapCalls, premiumCalls)
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 46 {
		t.Fatalf("disallowed channel group should not deduct token budget after one success, got %d", storedToken.RemainQuota)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND model = ?", common.LogStatusFailed, "gpt-group-scope").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	if failedLog.QuotaUsed != 0 || !strings.Contains(failedLog.ErrorMsg, "channel group") {
		t.Fatalf("channel group scope denial should write a zero-quota failed log, got %+v", failedLog)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("channel group scope denial should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["kind"] != "policy" ||
		policySnapshot["access_decision"] != "deny" ||
		policySnapshot["reject_code"] != "route_forbidden" ||
		policySnapshot["quota_precheck"] != "not_evaluated" ||
		scopeResult["api_type"] != "allow" ||
		scopeResult["model"] != "allow" ||
		scopeResult["channel_group"] != "deny" {
		t.Fatalf("unexpected channel group scope denial policy snapshot: %+v", policySnapshot)
	}
}

func TestAPIKeyIPScopeRejectsBeforeRelay(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-ip-scope","object":"chat.completion","model":"gpt-ip-scope","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "ip-scoped",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.ID == 0 || tokenPayload.Data.Key == "" {
		t.Fatalf("create scoped token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	scopeResp := performJSON(r, http.MethodPut, "/v0/user/token/"+uintString(tokenPayload.Data.ID)+"/scope", rootJWT, map[string]interface{}{
		"ip_cidrs": []string{"192.0.2.0/24"},
	})
	if scopeResp.Code != http.StatusOK || !strings.Contains(scopeResp.Body.String(), `"ip_cidrs":["192.0.2.0/24"]`) {
		t.Fatalf("update token ip scope failed: %d %s", scopeResp.Code, scopeResp.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "ip-scoped-channel",
		"models":   "gpt-ip-scope",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create scoped channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	chatBody := map[string]interface{}{
		"model": "gpt-ip-scope",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	allowedResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	if allowedResp.Code != http.StatusOK {
		t.Fatalf("allowed ip should reach upstream, got %d %s", allowedResp.Code, allowedResp.Body.String())
	}

	denyScopeResp := performJSON(r, http.MethodPut, "/v0/user/token/"+uintString(tokenPayload.Data.ID)+"/scope", rootJWT, map[string]interface{}{
		"ip_cidrs": []string{"198.51.100.0/24"},
	})
	if denyScopeResp.Code != http.StatusOK || !strings.Contains(denyScopeResp.Body.String(), `"ip_cidrs":["198.51.100.0/24"]`) {
		t.Fatalf("update denied ip scope failed: %d %s", denyScopeResp.Code, denyScopeResp.Body.String())
	}
	deniedResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	if deniedResp.Code != http.StatusForbidden || !strings.Contains(deniedResp.Body.String(), `"code":"token_forbidden"`) {
		t.Fatalf("disallowed ip should be blocked before relay, got %d %s", deniedResp.Code, deniedResp.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("ip scope rejection must not call upstream, got %d calls", upstreamCalls)
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 45 {
		t.Fatalf("disallowed ip should not deduct token budget after one success, got %d", storedToken.RemainQuota)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND error_msg LIKE ?", common.LogStatusFailed, tokenPayload.Data.ID, "%ip%scope%").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	if failedLog.QuotaUsed != 0 {
		t.Fatalf("ip scope denial should write a zero-quota failed log, got %+v", failedLog)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("ip scope denial should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["kind"] != "policy" ||
		policySnapshot["access_decision"] != "deny" ||
		policySnapshot["reject_code"] != "token_forbidden" ||
		policySnapshot["quota_precheck"] != "not_evaluated" ||
		scopeResult["ip"] != "deny" ||
		scopeResult["api_type"] != "not_evaluated" {
		t.Fatalf("unexpected ip scope denial policy snapshot: %+v", policySnapshot)
	}
}

func TestAPIKeyMethodScopeRejectsBeforeRelay(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"id":"chatcmpl-method-scope","object":"chat.completion","model":"gpt-method-scope","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
		case "/v1/embeddings":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"gpt-method-scope","usage":{"prompt_tokens":7,"total_tokens":7}}`))
		default:
			http.NotFound(w, req)
		}
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "method-scoped",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.ID == 0 || tokenPayload.Data.Key == "" {
		t.Fatalf("create scoped token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	scopeResp := performJSON(r, http.MethodPut, "/v0/user/token/"+uintString(tokenPayload.Data.ID)+"/scope", rootJWT, map[string]interface{}{
		"methods": []string{"POST /v1/chat/completions"},
	})
	if scopeResp.Code != http.StatusOK || !strings.Contains(scopeResp.Body.String(), `"methods":["POST /v1/chat/completions"]`) {
		t.Fatalf("update token method scope failed: %d %s", scopeResp.Code, scopeResp.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "method-scoped-channel",
		"models":   "gpt-method-scope",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create scoped channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-method-scope",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if chatResp.Code != http.StatusOK {
		t.Fatalf("allowed method should reach upstream, got %d %s", chatResp.Code, chatResp.Body.String())
	}

	embeddingResp := performJSON(r, http.MethodPost, "/v1/embeddings", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-method-scope",
		"input": "hello",
	})
	if embeddingResp.Code != http.StatusForbidden || !strings.Contains(embeddingResp.Body.String(), `"code":"token_forbidden"`) {
		t.Fatalf("disallowed method should be blocked before relay, got %d %s", embeddingResp.Code, embeddingResp.Body.String())
	}
	modelsResp := performJSON(r, http.MethodGet, "/v1/models", "Bearer "+tokenPayload.Data.Key, nil)
	if modelsResp.Code != http.StatusForbidden || !strings.Contains(modelsResp.Body.String(), `"code":"token_forbidden"`) {
		t.Fatalf("disallowed models method should be blocked, got %d %s", modelsResp.Code, modelsResp.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("method scope rejection must not call upstream, got %d calls", upstreamCalls)
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 45 {
		t.Fatalf("disallowed method should not deduct token budget after one success, got %d", storedToken.RemainQuota)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND error_msg LIKE ?", common.LogStatusFailed, tokenPayload.Data.ID, "%method%scope%").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	if failedLog.QuotaUsed != 0 {
		t.Fatalf("method scope denial should write a zero-quota failed log, got %+v", failedLog)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("method scope denial should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["kind"] != "policy" ||
		policySnapshot["access_decision"] != "deny" ||
		policySnapshot["reject_code"] != "token_forbidden" ||
		policySnapshot["quota_precheck"] != "not_evaluated" ||
		scopeResult["method"] != "deny" ||
		scopeResult["api_type"] != "not_evaluated" {
		t.Fatalf("unexpected method scope denial policy snapshot: %+v", policySnapshot)
	}
}

func TestAPIKeyDailyQuotaScopeRejectsAfterDailyBudgetUsed(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-daily-scope","object":"chat.completion","model":"gpt-daily-scope","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "daily-scoped",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.ID == 0 || tokenPayload.Data.Key == "" {
		t.Fatalf("create scoped token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	scopeResp := performJSON(r, http.MethodPut, "/v0/user/token/"+uintString(tokenPayload.Data.ID)+"/scope", rootJWT, map[string]interface{}{
		"daily_quota": 5,
	})
	if scopeResp.Code != http.StatusOK || !strings.Contains(scopeResp.Body.String(), `"daily_quota":5`) {
		t.Fatalf("update token daily quota scope failed: %d %s", scopeResp.Code, scopeResp.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "daily-scoped-channel",
		"models":   "gpt-daily-scope",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create scoped channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	chatBody := map[string]interface{}{
		"model": "gpt-daily-scope",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	firstResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	if firstResp.Code != http.StatusOK {
		t.Fatalf("first request within daily budget should succeed, got %d %s", firstResp.Code, firstResp.Body.String())
	}
	secondResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	if secondResp.Code != http.StatusTooManyRequests || !strings.Contains(secondResp.Body.String(), `"code":"insufficient_quota"`) {
		t.Fatalf("second request should be blocked by daily budget, got %d %s", secondResp.Code, secondResp.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("daily quota scope rejection must not call upstream, got %d calls", upstreamCalls)
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 45 {
		t.Fatalf("daily quota rejection should not deduct token budget after one success, got %d", storedToken.RemainQuota)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND error_msg LIKE ?", common.LogStatusFailed, tokenPayload.Data.ID, "%daily%quota%").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	if failedLog.QuotaUsed != 0 {
		t.Fatalf("daily quota scope denial should write a zero-quota failed log, got %+v", failedLog)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("daily quota scope denial should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["kind"] != "policy" ||
		policySnapshot["access_decision"] != "deny" ||
		policySnapshot["reject_code"] != "insufficient_quota" ||
		policySnapshot["quota_precheck"] != "scope_limit_exceeded" ||
		scopeResult["daily_quota"] != "deny" ||
		scopeResult["api_type"] != "not_evaluated" {
		t.Fatalf("unexpected daily quota scope denial policy snapshot: %+v", policySnapshot)
	}
}

func TestAPIKeyMonthlyQuotaScopeRejectsAfterMonthlyBudgetUsed(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-monthly-scope","object":"chat.completion","model":"gpt-monthly-scope","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "monthly-scoped",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.ID == 0 || tokenPayload.Data.Key == "" {
		t.Fatalf("create scoped token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	scopeResp := performJSON(r, http.MethodPut, "/v0/user/token/"+uintString(tokenPayload.Data.ID)+"/scope", rootJWT, map[string]interface{}{
		"monthly_quota": 5,
	})
	if scopeResp.Code != http.StatusOK || !strings.Contains(scopeResp.Body.String(), `"monthly_quota":5`) {
		t.Fatalf("update token monthly quota scope failed: %d %s", scopeResp.Code, scopeResp.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "monthly-scoped-channel",
		"models":   "gpt-monthly-scope",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create scoped channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	chatBody := map[string]interface{}{
		"model": "gpt-monthly-scope",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	firstResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	if firstResp.Code != http.StatusOK {
		t.Fatalf("first request within monthly budget should succeed, got %d %s", firstResp.Code, firstResp.Body.String())
	}
	secondResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	if secondResp.Code != http.StatusTooManyRequests || !strings.Contains(secondResp.Body.String(), `"code":"insufficient_quota"`) {
		t.Fatalf("second request should be blocked by monthly budget, got %d %s", secondResp.Code, secondResp.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("monthly quota scope rejection must not call upstream, got %d calls", upstreamCalls)
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 45 {
		t.Fatalf("monthly quota rejection should not deduct token budget after one success, got %d", storedToken.RemainQuota)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND error_msg LIKE ?", common.LogStatusFailed, tokenPayload.Data.ID, "%monthly%quota%").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	if failedLog.QuotaUsed != 0 {
		t.Fatalf("monthly quota scope denial should write a zero-quota failed log, got %+v", failedLog)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("monthly quota scope denial should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["kind"] != "policy" ||
		policySnapshot["access_decision"] != "deny" ||
		policySnapshot["reject_code"] != "insufficient_quota" ||
		policySnapshot["quota_precheck"] != "scope_limit_exceeded" ||
		scopeResult["monthly_quota"] != "deny" ||
		scopeResult["api_type"] != "not_evaluated" {
		t.Fatalf("unexpected monthly quota scope denial policy snapshot: %+v", policySnapshot)
	}
}

func TestAPIKeyMaxConcurrencyScopeRejectsOnlyWhileInFlight(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseFirstOnce sync.Once
	releaseBlockedFirst := func() {
		releaseFirstOnce.Do(func() {
			close(releaseFirst)
		})
	}
	defer releaseBlockedFirst()
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		call := upstreamCalls.Add(1)
		if call == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-concurrency-scope","object":"chat.completion","model":"gpt-concurrency-scope","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "concurrency-scoped",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.ID == 0 || tokenPayload.Data.Key == "" {
		t.Fatalf("create scoped token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	scopeResp := performJSON(r, http.MethodPut, "/v0/user/token/"+uintString(tokenPayload.Data.ID)+"/scope", rootJWT, map[string]interface{}{
		"max_concurrency": 1,
	})
	if scopeResp.Code != http.StatusOK || !strings.Contains(scopeResp.Body.String(), `"max_concurrency":1`) {
		t.Fatalf("update token concurrency scope failed: %d %s", scopeResp.Code, scopeResp.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "concurrency-scoped-channel",
		"models":   "gpt-concurrency-scope",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create scoped channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	chatBody := map[string]interface{}{
		"model": "gpt-concurrency-scope",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	}()
	select {
	case <-firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("first request did not reach upstream")
	}

	secondResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	if secondResp.Code != http.StatusTooManyRequests || !strings.Contains(secondResp.Body.String(), `"code":"rate_limit_exceeded"`) {
		t.Fatalf("second in-flight request should be blocked by concurrency scope, got %d %s", secondResp.Code, secondResp.Body.String())
	}
	if got := upstreamCalls.Load(); got != 1 {
		t.Fatalf("concurrency rejection must not call upstream, got %d calls", got)
	}

	releaseBlockedFirst()
	firstResp := <-firstDone
	if firstResp.Code != http.StatusOK {
		t.Fatalf("first request should complete after release, got %d %s", firstResp.Code, firstResp.Body.String())
	}
	thirdResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	if thirdResp.Code != http.StatusOK {
		t.Fatalf("concurrency slot should be released after first request, got %d %s", thirdResp.Code, thirdResp.Body.String())
	}
	if got := upstreamCalls.Load(); got != 2 {
		t.Fatalf("expected two successful upstream calls after slot release, got %d", got)
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 40 {
		t.Fatalf("concurrency rejection should not deduct token budget after two successes, got %d", storedToken.RemainQuota)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND error_msg LIKE ?", common.LogStatusFailed, tokenPayload.Data.ID, "%concurrency%scope%").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	if failedLog.QuotaUsed != 0 {
		t.Fatalf("concurrency scope denial should write a zero-quota failed log, got %+v", failedLog)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("concurrency scope denial should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["kind"] != "policy" ||
		policySnapshot["access_decision"] != "deny" ||
		policySnapshot["reject_code"] != "rate_limit_exceeded" ||
		policySnapshot["quota_precheck"] != "rate_limit_exceeded" ||
		scopeResult["max_concurrency"] != "deny" ||
		scopeResult["api_type"] != "not_evaluated" {
		t.Fatalf("unexpected concurrency scope denial policy snapshot: %+v", policySnapshot)
	}
}

func TestAPIKeyRPMScopeRejectsWithinMinuteBeforeRelay(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-rpm-scope","object":"chat.completion","model":"gpt-rpm-scope","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "rpm-scoped",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.ID == 0 || tokenPayload.Data.Key == "" {
		t.Fatalf("create scoped token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	scopeResp := performJSON(r, http.MethodPut, "/v0/user/token/"+uintString(tokenPayload.Data.ID)+"/scope", rootJWT, map[string]interface{}{
		"rpm": 1,
	})
	if scopeResp.Code != http.StatusOK || !strings.Contains(scopeResp.Body.String(), `"rpm":1`) {
		t.Fatalf("update token rpm scope failed: %d %s", scopeResp.Code, scopeResp.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "rpm-scoped-channel",
		"models":   "gpt-rpm-scope",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create scoped channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	chatBody := map[string]interface{}{
		"model": "gpt-rpm-scope",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	firstResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	if firstResp.Code != http.StatusOK {
		t.Fatalf("first request within rpm scope should succeed, got %d %s", firstResp.Code, firstResp.Body.String())
	}
	secondResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	if secondResp.Code != http.StatusTooManyRequests || !strings.Contains(secondResp.Body.String(), `"code":"rate_limit_exceeded"`) {
		t.Fatalf("second request should be blocked by rpm scope, got %d %s", secondResp.Code, secondResp.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("rpm scope rejection must not call upstream, got %d calls", upstreamCalls)
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 45 {
		t.Fatalf("rpm rejection should not deduct token budget after one success, got %d", storedToken.RemainQuota)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND error_msg LIKE ?", common.LogStatusFailed, tokenPayload.Data.ID, "%rpm%scope%").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	if failedLog.QuotaUsed != 0 {
		t.Fatalf("rpm scope denial should write a zero-quota failed log, got %+v", failedLog)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("rpm scope denial should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["kind"] != "policy" ||
		policySnapshot["access_decision"] != "deny" ||
		policySnapshot["reject_code"] != "rate_limit_exceeded" ||
		policySnapshot["quota_precheck"] != "rate_limit_exceeded" ||
		scopeResult["rpm"] != "deny" ||
		scopeResult["api_type"] != "not_evaluated" {
		t.Fatalf("unexpected rpm scope denial policy snapshot: %+v", policySnapshot)
	}
}

func TestAPIKeyTPMScopeRejectsAfterMinuteTokenBudgetUsed(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-tpm-scope","object":"chat.completion","model":"gpt-tpm-scope","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "tpm-scoped",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.ID == 0 || tokenPayload.Data.Key == "" {
		t.Fatalf("create scoped token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	scopeResp := performJSON(r, http.MethodPut, "/v0/user/token/"+uintString(tokenPayload.Data.ID)+"/scope", rootJWT, map[string]interface{}{
		"tpm": 5,
	})
	if scopeResp.Code != http.StatusOK || !strings.Contains(scopeResp.Body.String(), `"tpm":5`) {
		t.Fatalf("update token tpm scope failed: %d %s", scopeResp.Code, scopeResp.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "tpm-scoped-channel",
		"models":   "gpt-tpm-scope",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create scoped channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	chatBody := map[string]interface{}{
		"model": "gpt-tpm-scope",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	firstResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	if firstResp.Code != http.StatusOK {
		t.Fatalf("first request within tpm scope should succeed, got %d %s", firstResp.Code, firstResp.Body.String())
	}
	secondResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	if secondResp.Code != http.StatusTooManyRequests || !strings.Contains(secondResp.Body.String(), `"code":"rate_limit_exceeded"`) {
		t.Fatalf("second request should be blocked by tpm scope, got %d %s", secondResp.Code, secondResp.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("tpm scope rejection must not call upstream, got %d calls", upstreamCalls)
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 45 {
		t.Fatalf("tpm rejection should not deduct token budget after one success, got %d", storedToken.RemainQuota)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND error_msg LIKE ?", common.LogStatusFailed, tokenPayload.Data.ID, "%tpm%scope%").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	if failedLog.QuotaUsed != 0 {
		t.Fatalf("tpm scope denial should write a zero-quota failed log, got %+v", failedLog)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("tpm scope denial should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["kind"] != "policy" ||
		policySnapshot["access_decision"] != "deny" ||
		policySnapshot["reject_code"] != "rate_limit_exceeded" ||
		policySnapshot["quota_precheck"] != "rate_limit_exceeded" ||
		scopeResult["tpm"] != "deny" ||
		scopeResult["api_type"] != "allow" ||
		scopeResult["model"] != "allow" {
		t.Fatalf("unexpected tpm scope denial policy snapshot: %+v", policySnapshot)
	}
}

func TestAPIKeyPersistsLastUsageSourceSummary(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-source-summary","object":"chat.completion","model":"gpt-source-summary","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "source-summary",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.ID == 0 || tokenPayload.Data.Key == "" {
		t.Fatalf("create source summary token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "source-summary-channel",
		"models":   "gpt-source-summary",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create source summary channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	userAgent := "routerx-sdk/1.2.3 source-summary"
	chatResp := performRawWithHeaders(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, `{"model":"gpt-source-summary","messages":[{"role":"user","content":"hello"}]}`, map[string]string{
		"User-Agent": userAgent,
	})
	if chatResp.Code != http.StatusOK {
		t.Fatalf("chat request should succeed, got %d %s", chatResp.Code, chatResp.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("expected one upstream call, got %d", upstreamCalls)
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.LastUsedAt == nil {
		t.Fatalf("token should persist last_used_at after relay")
	}
	if storedToken.LastModel != "gpt-source-summary" {
		t.Fatalf("token should persist last model, got %q", storedToken.LastModel)
	}
	if storedToken.LastErrorCode != "" {
		t.Fatalf("successful relay should clear last error code, got %q", storedToken.LastErrorCode)
	}
	if len(storedToken.LastUsedIPHash) != 64 || storedToken.LastUsedIPHash == "192.0.2.1" {
		t.Fatalf("token should persist hashed client ip, got %q", storedToken.LastUsedIPHash)
	}
	if len(storedToken.LastUserAgentHash) != 64 || storedToken.LastUserAgentHash == userAgent {
		t.Fatalf("token should persist hashed user agent, got %q", storedToken.LastUserAgentHash)
	}

	listResp := performJSON(r, http.MethodGet, "/v0/user/token", rootJWT, nil)
	listBody := listResp.Body.String()
	if listResp.Code != http.StatusOK ||
		!strings.Contains(listBody, storedToken.LastUsedIPHash) ||
		!strings.Contains(listBody, storedToken.LastUserAgentHash) ||
		!strings.Contains(listBody, `"last_model":"gpt-source-summary"`) {
		t.Fatalf("token list should expose persisted source summary, got %d %s", listResp.Code, listBody)
	}
	if strings.Contains(listBody, userAgent) || strings.Contains(listBody, "192.0.2.1") {
		t.Fatalf("token list should not expose raw source values: %s", listBody)
	}
}

func TestAPIKeyEntryProtocolScopeRejectsBeforeRelay(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-entry-scope","object":"chat.completion","model":"gpt-entry-scope","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "entry-scoped",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.ID == 0 || tokenPayload.Data.Key == "" {
		t.Fatalf("create scoped token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	scopeResp := performJSON(r, http.MethodPut, "/v0/user/token/"+uintString(tokenPayload.Data.ID)+"/scope", rootJWT, map[string]interface{}{
		"entry_protocols": []string{"openai"},
	})
	if scopeResp.Code != http.StatusOK || !strings.Contains(scopeResp.Body.String(), `"entry_protocols":["openai"]`) {
		t.Fatalf("update token entry protocol scope failed: %d %s", scopeResp.Code, scopeResp.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "entry-scoped-channel",
		"models":   "gpt-entry-scope",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create scoped channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	chatBody := map[string]interface{}{
		"model": "gpt-entry-scope",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	allowedResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, chatBody)
	if allowedResp.Code != http.StatusOK {
		t.Fatalf("allowed entry protocol should reach upstream, got %d %s", allowedResp.Code, allowedResp.Body.String())
	}

	anthropicResp := performJSON(r, http.MethodPost, "/v1/messages", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model":      "gpt-entry-scope",
		"max_tokens": 16,
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if anthropicResp.Code != http.StatusForbidden ||
		!strings.Contains(anthropicResp.Body.String(), `"type":"error"`) ||
		!strings.Contains(anthropicResp.Body.String(), `"type":"permission_error"`) {
		t.Fatalf("disallowed entry protocol should be blocked with Anthropic error shape, got %d %s", anthropicResp.Code, anthropicResp.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("entry protocol scope rejection must not call upstream, got %d calls", upstreamCalls)
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 45 {
		t.Fatalf("disallowed entry protocol should not deduct token budget after one success, got %d", storedToken.RemainQuota)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND error_msg LIKE ?", common.LogStatusFailed, tokenPayload.Data.ID, "%entry protocol%scope%").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	if failedLog.QuotaUsed != 0 {
		t.Fatalf("entry protocol scope denial should write a zero-quota failed log, got %+v", failedLog)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("entry protocol scope denial should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["kind"] != "policy" ||
		policySnapshot["access_decision"] != "deny" ||
		policySnapshot["reject_code"] != "token_forbidden" ||
		policySnapshot["quota_precheck"] != "not_evaluated" ||
		scopeResult["entry_protocol"] != "deny" ||
		scopeResult["api_type"] != "not_evaluated" {
		t.Fatalf("unexpected entry protocol scope denial policy snapshot: %+v", policySnapshot)
	}
}

func TestAzureChatCompletionUsesDeploymentPathAndAPIKey(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamPath := ""
	upstreamAPIVersion := ""
	upstreamAPIKey := ""
	upstreamAuth := ""
	upstreamBody := map[string]interface{}{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPath = req.URL.Path
		upstreamAPIVersion = req.URL.Query().Get("api-version")
		upstreamAPIKey = req.Header.Get("api-key")
		upstreamAuth = req.Header.Get("Authorization")
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(req.Body)
		if err := json.Unmarshal(raw.Bytes(), &upstreamBody); err != nil {
			t.Errorf("azure upstream body should be json: %v", err)
		}
		if _, ok := upstreamBody["model"]; ok {
			t.Errorf("azure upstream body should not include model field: %#v", upstreamBody)
		}
		if _, ok := upstreamBody["routerx"]; ok {
			t.Errorf("azure upstream body should not include routerx field: %#v", upstreamBody)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-azure","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"azure ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":5,"total_tokens":9}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "azure-chat",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeAzure,
		"name":     "azure",
		"models":   "gpt-test",
		"base_url": upstream.URL,
		"api_key":  "azure-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create azure channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	resp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-test",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"routerx": map[string]interface{}{
			"route": map[string]string{"provider": "azure"},
		},
	})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"content":"azure ok"`) {
		t.Fatalf("azure chat should return upstream OpenAI-compatible response, got %d %s", resp.Code, resp.Body.String())
	}
	if upstreamCalls != 1 || upstreamPath != "/openai/deployments/gpt-test/chat/completions" || upstreamAPIVersion == "" {
		t.Fatalf("azure chat should use deployment path and api-version, calls=%d path=%q api-version=%q", upstreamCalls, upstreamPath, upstreamAPIVersion)
	}
	if upstreamAPIKey != "azure-secret" || upstreamAuth != "" {
		t.Fatalf("azure chat should use api-key header only, api-key=%q authorization=%q", upstreamAPIKey, upstreamAuth)
	}
	if messages, ok := upstreamBody["messages"].([]interface{}); !ok || len(messages) != 1 {
		t.Fatalf("azure chat should preserve messages, got %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 41 {
		t.Fatalf("azure chat usage should deduct token budget by 9, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 91 {
		t.Fatalf("azure chat usage should deduct user quota by 9, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 9 || callLog.TotalTokens != 9 || callLog.PromptTokens != 4 || callLog.CompletionTokens != 5 {
		t.Fatalf("unexpected azure chat success log: %+v", callLog)
	}
}

func TestAzureCompletionsUsesDeploymentPathAndAPIKey(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamPath := ""
	upstreamAPIVersion := ""
	upstreamAPIKey := ""
	upstreamAuth := ""
	upstreamBody := map[string]interface{}{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPath = req.URL.Path
		upstreamAPIVersion = req.URL.Query().Get("api-version")
		upstreamAPIKey = req.Header.Get("api-key")
		upstreamAuth = req.Header.Get("Authorization")
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(req.Body)
		if err := json.Unmarshal(raw.Bytes(), &upstreamBody); err != nil {
			t.Errorf("azure completions upstream body should be json: %v", err)
		}
		if _, ok := upstreamBody["model"]; ok {
			t.Errorf("azure completions upstream body should not include model field: %#v", upstreamBody)
		}
		if _, ok := upstreamBody["routerx"]; ok {
			t.Errorf("azure completions upstream body should not include routerx field: %#v", upstreamBody)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cmpl-azure","object":"text_completion","choices":[{"index":0,"text":"azure completion","finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "azure-completions",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeAzure,
		"name":     "azure-completions",
		"models":   "text-prod",
		"base_url": upstream.URL,
		"api_key":  "azure-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create azure completions channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	resp := performJSON(r, http.MethodPost, "/v1/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model":      "text-prod",
		"prompt":     "hello",
		"max_tokens": 4,
		"routerx": map[string]interface{}{
			"route": map[string]string{"provider": "azure"},
		},
	})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"text":"azure completion"`) {
		t.Fatalf("azure completions should return upstream OpenAI-compatible response, got %d %s", resp.Code, resp.Body.String())
	}
	if upstreamCalls != 1 || upstreamPath != "/openai/deployments/text-prod/completions" || upstreamAPIVersion == "" {
		t.Fatalf("azure completions should use deployment path and api-version, calls=%d path=%q api-version=%q", upstreamCalls, upstreamPath, upstreamAPIVersion)
	}
	if upstreamAPIKey != "azure-secret" || upstreamAuth != "" {
		t.Fatalf("azure completions should use api-key header only, api-key=%q authorization=%q", upstreamAPIKey, upstreamAuth)
	}
	if upstreamBody["prompt"] != "hello" {
		t.Fatalf("azure completions should preserve prompt, got %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 43 {
		t.Fatalf("azure completions usage should deduct token budget by 7, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 93 {
		t.Fatalf("azure completions usage should deduct user quota by 7, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 7 || callLog.TotalTokens != 7 || callLog.PromptTokens != 3 || callLog.CompletionTokens != 4 {
		t.Fatalf("unexpected azure completions success log: %+v", callLog)
	}
}

func TestAzureChannelFetchModelsUsesDeploymentsEndpoint(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamPath := ""
	upstreamAPIVersion := ""
	upstreamAPIKey := ""
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPath = req.URL.Path
		upstreamAPIVersion = req.URL.Query().Get("api-version")
		upstreamAPIKey = req.Header.Get("api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-prod","model":"gpt-4o"},{"id":"embed-prod","model":"text-embedding-3-large"}]}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeAzure,
		"name":     "azure-model-list",
		"models":   "gpt-prod",
		"base_url": upstream.URL,
		"api_key":  "azure-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create azure channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}
	var channelPayload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(channelResp.Body.Bytes(), &channelPayload); err != nil {
		t.Fatal(err)
	}

	modelsResp := performJSON(r, http.MethodGet, "/v0/admin/channel/"+uintString(channelPayload.Data.ID)+"/models", rootJWT, nil)
	if modelsResp.Code != http.StatusOK ||
		!strings.Contains(modelsResp.Body.String(), `"gpt-prod"`) ||
		!strings.Contains(modelsResp.Body.String(), `"embed-prod"`) {
		t.Fatalf("azure fetch models should return deployment ids, got %d %s", modelsResp.Code, modelsResp.Body.String())
	}
	if upstreamCalls != 1 || upstreamPath != "/openai/deployments" || upstreamAPIVersion == "" {
		t.Fatalf("azure fetch models should call deployments endpoint once, calls=%d path=%q api-version=%q", upstreamCalls, upstreamPath, upstreamAPIVersion)
	}
	if upstreamAPIKey != "azure-secret" {
		t.Fatalf("azure fetch models should use api-key header, got %q", upstreamAPIKey)
	}
}

func TestAzureResponsesUsesV1EndpointAndUsage(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamPath := ""
	upstreamAPIVersion := ""
	upstreamAPIKey := ""
	upstreamAuth := ""
	upstreamBody := map[string]interface{}{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPath = req.URL.Path
		upstreamAPIVersion = req.URL.Query().Get("api-version")
		upstreamAPIKey = req.Header.Get("api-key")
		upstreamAuth = req.Header.Get("Authorization")
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(req.Body)
		if err := json.Unmarshal(raw.Bytes(), &upstreamBody); err != nil {
			t.Errorf("azure responses upstream body should be json: %v", err)
		}
		if _, ok := upstreamBody["routerx"]; ok {
			t.Errorf("azure responses upstream body should not include routerx field: %#v", upstreamBody)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_azure","object":"response","model":"gpt-responses-prod","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"azure response"}]}],"usage":{"input_tokens":4,"output_tokens":5,"total_tokens":9}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "azure-responses",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeAzure,
		"name":     "azure-responses",
		"models":   "gpt-responses-prod",
		"base_url": upstream.URL,
		"api_key":  "azure-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create azure responses channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	resp := performJSON(r, http.MethodPost, "/v1/responses", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-responses-prod",
		"input": "hello responses",
		"routerx": map[string]interface{}{
			"route": map[string]string{"provider": "azure"},
		},
	})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"text":"azure response"`) {
		t.Fatalf("azure responses should return upstream response, got %d %s", resp.Code, resp.Body.String())
	}
	if upstreamCalls != 1 || upstreamPath != "/openai/v1/responses" || upstreamAPIVersion != "preview" {
		t.Fatalf("azure responses should use Azure v1 path and preview api-version, calls=%d path=%q api-version=%q", upstreamCalls, upstreamPath, upstreamAPIVersion)
	}
	if upstreamAPIKey != "azure-secret" || upstreamAuth != "" {
		t.Fatalf("azure responses should use api-key header only, api-key=%q authorization=%q", upstreamAPIKey, upstreamAuth)
	}
	if upstreamBody["model"] != "gpt-responses-prod" || upstreamBody["input"] != "hello responses" {
		t.Fatalf("azure responses should preserve model and input, got %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 41 {
		t.Fatalf("azure responses usage should deduct token budget by 9, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 91 {
		t.Fatalf("azure responses usage should deduct user quota by 9, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 9 || callLog.TotalTokens != 9 || callLog.PromptTokens != 4 || callLog.CompletionTokens != 5 {
		t.Fatalf("unexpected azure responses success log: %+v", callLog)
	}
}

func TestAzureEmbeddingsUsesDeploymentPathAndAPIKey(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamPath := ""
	upstreamAPIVersion := ""
	upstreamAPIKey := ""
	upstreamAuth := ""
	upstreamBody := map[string]interface{}{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPath = req.URL.Path
		upstreamAPIVersion = req.URL.Query().Get("api-version")
		upstreamAPIKey = req.Header.Get("api-key")
		upstreamAuth = req.Header.Get("Authorization")
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(req.Body)
		if err := json.Unmarshal(raw.Bytes(), &upstreamBody); err != nil {
			t.Errorf("azure embeddings upstream body should be json: %v", err)
		}
		if _, ok := upstreamBody["model"]; ok {
			t.Errorf("azure embeddings upstream body should not include model field: %#v", upstreamBody)
		}
		if _, ok := upstreamBody["routerx"]; ok {
			t.Errorf("azure embeddings upstream body should not include routerx field: %#v", upstreamBody)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"embed-prod","usage":{"prompt_tokens":6,"total_tokens":6}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "azure-embeddings",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeAzure,
		"name":     "azure-embeddings",
		"models":   "embed-prod",
		"base_url": upstream.URL,
		"api_key":  "azure-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create azure embeddings channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	resp := performJSON(r, http.MethodPost, "/v1/embeddings", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "embed-prod",
		"input": "hello",
		"routerx": map[string]interface{}{
			"route": map[string]string{"provider": "azure"},
		},
	})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"object":"embedding"`) {
		t.Fatalf("azure embeddings should return upstream OpenAI-compatible response, got %d %s", resp.Code, resp.Body.String())
	}
	if upstreamCalls != 1 || upstreamPath != "/openai/deployments/embed-prod/embeddings" || upstreamAPIVersion == "" {
		t.Fatalf("azure embeddings should use deployment path and api-version, calls=%d path=%q api-version=%q", upstreamCalls, upstreamPath, upstreamAPIVersion)
	}
	if upstreamAPIKey != "azure-secret" || upstreamAuth != "" {
		t.Fatalf("azure embeddings should use api-key header only, api-key=%q authorization=%q", upstreamAPIKey, upstreamAuth)
	}
	if upstreamBody["input"] != "hello" {
		t.Fatalf("azure embeddings should preserve input, got %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 44 {
		t.Fatalf("azure embeddings usage should deduct token budget by 6, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 94 {
		t.Fatalf("azure embeddings usage should deduct user quota by 6, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 6 || callLog.TotalTokens != 6 || callLog.PromptTokens != 6 {
		t.Fatalf("unexpected azure embeddings success log: %+v", callLog)
	}
}

func TestAzureImageGenerationsUsesV1EndpointAndMinimumCharge(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamPath := ""
	upstreamAPIVersion := ""
	upstreamAPIKey := ""
	upstreamAuth := ""
	upstreamBody := map[string]interface{}{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPath = req.URL.Path
		upstreamAPIVersion = req.URL.Query().Get("api-version")
		upstreamAPIKey = req.Header.Get("api-key")
		upstreamAuth = req.Header.Get("Authorization")
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(req.Body)
		if err := json.Unmarshal(raw.Bytes(), &upstreamBody); err != nil {
			t.Errorf("azure image generations upstream body should be json: %v", err)
		}
		if _, ok := upstreamBody["routerx"]; ok {
			t.Errorf("azure image generations upstream body should not include routerx field: %#v", upstreamBody)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1710000000,"data":[{"b64_json":"aW1hZ2U="}]}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "azure-images",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeAzure,
		"name":     "azure-images",
		"models":   "dalle-prod",
		"base_url": upstream.URL,
		"api_key":  "azure-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create azure images channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	resp := performJSON(r, http.MethodPost, "/v1/images/generations", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model":  "dalle-prod",
		"prompt": "routerx image",
		"size":   "1024x1024",
		"routerx": map[string]interface{}{
			"route": map[string]string{"provider": "azure"},
		},
	})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"b64_json":"aW1hZ2U="`) {
		t.Fatalf("azure image generations should return upstream response, got %d %s", resp.Code, resp.Body.String())
	}
	if upstreamCalls != 1 || upstreamPath != "/openai/v1/images/generations" || upstreamAPIVersion != "preview" {
		t.Fatalf("azure image generations should use Azure v1 path and preview api-version, calls=%d path=%q api-version=%q", upstreamCalls, upstreamPath, upstreamAPIVersion)
	}
	if upstreamAPIKey != "azure-secret" || upstreamAuth != "" {
		t.Fatalf("azure image generations should use api-key header only, api-key=%q authorization=%q", upstreamAPIKey, upstreamAuth)
	}
	if upstreamBody["model"] != "dalle-prod" || upstreamBody["prompt"] != "routerx image" || upstreamBody["size"] != "1024x1024" {
		t.Fatalf("azure image generations should preserve model, prompt and size, got %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 49 {
		t.Fatalf("azure image generations without usage should deduct minimum token budget charge, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 99 {
		t.Fatalf("azure image generations without usage should deduct minimum user quota charge, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 1 || callLog.TotalTokens != 0 || callLog.UsageSource != common.LogUsageSourceMinimum {
		t.Fatalf("unexpected azure image generations success log: %+v", callLog)
	}
}

func TestAzureImageEditsMultipartUsesV1EndpointAndMinimumCharge(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	imageBytes := []byte("PNG-routerx-azure-image")
	maskBytes := []byte("PNG-routerx-azure-mask")
	upstreamCalls := 0
	upstreamPath := ""
	upstreamAPIVersion := ""
	upstreamAPIKey := ""
	upstreamAuth := ""
	upstreamModel := ""
	upstreamPrompt := ""
	var upstreamImage []byte
	var upstreamMask []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPath = req.URL.Path
		upstreamAPIVersion = req.URL.Query().Get("api-version")
		upstreamAPIKey = req.Header.Get("api-key")
		upstreamAuth = req.Header.Get("Authorization")
		if !strings.Contains(req.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("azure image edits upstream should receive multipart content type, got %q", req.Header.Get("Content-Type"))
		}
		if err := req.ParseMultipartForm(20 << 20); err != nil {
			t.Errorf("azure image edits upstream received invalid multipart body: %v", err)
		}
		upstreamModel = req.FormValue("model")
		upstreamPrompt = req.FormValue("prompt")
		if leaked := req.FormValue("routerx"); leaked != "" {
			t.Errorf("routerx private form field leaked to azure image edits upstream: %q", leaked)
		}
		file, _, err := req.FormFile("image")
		if err != nil {
			t.Errorf("azure image edits upstream missing image file: %v", err)
		} else {
			defer file.Close()
			raw := new(bytes.Buffer)
			_, _ = raw.ReadFrom(file)
			upstreamImage = raw.Bytes()
		}
		mask, _, err := req.FormFile("mask")
		if err != nil {
			t.Errorf("azure image edits upstream missing mask file: %v", err)
		} else {
			defer mask.Close()
			raw := new(bytes.Buffer)
			_, _ = raw.ReadFrom(mask)
			upstreamMask = raw.Bytes()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1710000000,"data":[{"b64_json":"ZWRpdGVk"}]}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "azure-image-edits",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeAzure,
		"name":     "azure-image-edits",
		"models":   "dalle-edit-prod",
		"base_url": upstream.URL,
		"api_key":  "azure-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create azure image edits channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	var reqBody bytes.Buffer
	writer := multipart.NewWriter(&reqBody)
	if err := writer.WriteField("model", "dalle-edit-prod"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("prompt", "add routerx label"); err != nil {
		t.Fatal(err)
	}
	routerxOptions, err := json.Marshal(map[string]interface{}{
		"route": map[string]string{"provider": "azure"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("routerx", string(routerxOptions)); err != nil {
		t.Fatal(err)
	}
	imageWriter, err := writer.CreateFormFile("image", "input.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := imageWriter.Write(imageBytes); err != nil {
		t.Fatal(err)
	}
	maskWriter, err := writer.CreateFormFile("mask", "mask.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := maskWriter.Write(maskBytes); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &reqBody)
	req.Header.Set("Authorization", "Bearer "+tokenPayload.Data.Key)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"b64_json":"ZWRpdGVk"`) {
		t.Fatalf("azure image edits should return upstream response, got %d %s", resp.Code, resp.Body.String())
	}
	if upstreamCalls != 1 || upstreamPath != "/openai/v1/images/edits" || upstreamAPIVersion != "preview" {
		t.Fatalf("azure image edits should use Azure v1 path and preview api-version, calls=%d path=%q api-version=%q", upstreamCalls, upstreamPath, upstreamAPIVersion)
	}
	if upstreamAPIKey != "azure-secret" || upstreamAuth != "" {
		t.Fatalf("azure image edits should use api-key header only, api-key=%q authorization=%q", upstreamAPIKey, upstreamAuth)
	}
	if upstreamModel != "dalle-edit-prod" || upstreamPrompt != "add routerx label" {
		t.Fatalf("azure image edits should preserve model and prompt, model=%q prompt=%q", upstreamModel, upstreamPrompt)
	}
	if !bytes.Equal(upstreamImage, imageBytes) || !bytes.Equal(upstreamMask, maskBytes) {
		t.Fatalf("azure image edits should preserve image and mask files, image=%q mask=%q", string(upstreamImage), string(upstreamMask))
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 49 {
		t.Fatalf("azure image edits without usage should deduct minimum token budget charge, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 99 {
		t.Fatalf("azure image edits without usage should deduct minimum user quota charge, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 1 || callLog.TotalTokens != 0 || callLog.UsageSource != common.LogUsageSourceMinimum {
		t.Fatalf("unexpected azure image edits success log: %+v", callLog)
	}
}

func TestAzureAudioSpeechUsesV1EndpointAndMinimumCharge(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	audioBytes := []byte{0x49, 0x44, 0x33, 0x04, 0x00, 0x00, 0x00, 0x00}
	upstreamCalls := 0
	upstreamPath := ""
	upstreamAPIVersion := ""
	upstreamAPIKey := ""
	upstreamAuth := ""
	upstreamBody := map[string]interface{}{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPath = req.URL.Path
		upstreamAPIVersion = req.URL.Query().Get("api-version")
		upstreamAPIKey = req.Header.Get("api-key")
		upstreamAuth = req.Header.Get("Authorization")
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(req.Body)
		if err := json.Unmarshal(raw.Bytes(), &upstreamBody); err != nil {
			t.Errorf("azure audio speech upstream body should be json: %v", err)
		}
		if _, ok := upstreamBody["routerx"]; ok {
			t.Errorf("azure audio speech upstream body should not include routerx field: %#v", upstreamBody)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(audioBytes)
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "azure-audio-speech",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeAzure,
		"name":     "azure-audio-speech",
		"models":   "tts-prod",
		"base_url": upstream.URL,
		"api_key":  "azure-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create azure audio speech channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	resp := performJSON(r, http.MethodPost, "/v1/audio/speech", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "tts-prod",
		"input": "hello from azure",
		"voice": "alloy",
		"routerx": map[string]interface{}{
			"route": map[string]string{"provider": "azure"},
		},
	})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Header().Get("Content-Type"), "audio/mpeg") || !bytes.Equal(resp.Body.Bytes(), audioBytes) {
		t.Fatalf("azure audio speech should return upstream audio response, got %d %s %#v", resp.Code, resp.Header().Get("Content-Type"), resp.Body.Bytes())
	}
	if upstreamCalls != 1 || upstreamPath != "/openai/v1/audio/speech" || upstreamAPIVersion != "preview" {
		t.Fatalf("azure audio speech should use Azure v1 path and preview api-version, calls=%d path=%q api-version=%q", upstreamCalls, upstreamPath, upstreamAPIVersion)
	}
	if upstreamAPIKey != "azure-secret" || upstreamAuth != "" {
		t.Fatalf("azure audio speech should use api-key header only, api-key=%q authorization=%q", upstreamAPIKey, upstreamAuth)
	}
	if upstreamBody["model"] != "tts-prod" || upstreamBody["input"] != "hello from azure" || upstreamBody["voice"] != "alloy" {
		t.Fatalf("azure audio speech should preserve model, input and voice, got %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 49 {
		t.Fatalf("azure audio speech without usage should deduct minimum token budget charge, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 99 {
		t.Fatalf("azure audio speech without usage should deduct minimum user quota charge, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 1 || callLog.TotalTokens != 0 || callLog.UsageSource != common.LogUsageSourceMinimum {
		t.Fatalf("unexpected azure audio speech success log: %+v", callLog)
	}
}

func TestAzureAudioMultipartUsesV1EndpointAndMinimumCharge(t *testing.T) {
	for _, tt := range []struct {
		name             string
		downstreamPath   string
		wantUpstreamPath string
		wantText         string
	}{
		{
			name:             "transcriptions",
			downstreamPath:   "/v1/audio/transcriptions",
			wantUpstreamPath: "/openai/v1/audio/transcriptions",
			wantText:         "azure transcript",
		},
		{
			name:             "translations",
			downstreamPath:   "/v1/audio/translations",
			wantUpstreamPath: "/openai/v1/audio/translations",
			wantText:         "azure translation",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("JWT_SECRET", "test-jwt-secret")
			t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

			audioBytes := []byte("RIFF-azure-audio")
			upstreamCalls := 0
			upstreamPath := ""
			upstreamAPIVersion := ""
			upstreamAPIKey := ""
			upstreamAuth := ""
			upstreamModel := ""
			upstreamPrompt := ""
			var upstreamFile []byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				upstreamCalls++
				upstreamPath = req.URL.Path
				upstreamAPIVersion = req.URL.Query().Get("api-version")
				upstreamAPIKey = req.Header.Get("api-key")
				upstreamAuth = req.Header.Get("Authorization")
				if !strings.Contains(req.Header.Get("Content-Type"), "multipart/form-data") {
					t.Errorf("azure audio %s upstream should receive multipart content type, got %q", tt.name, req.Header.Get("Content-Type"))
				}
				if err := req.ParseMultipartForm(20 << 20); err != nil {
					t.Errorf("azure audio %s upstream received invalid multipart body: %v", tt.name, err)
				}
				upstreamModel = req.FormValue("model")
				upstreamPrompt = req.FormValue("prompt")
				if leaked := req.FormValue("routerx"); leaked != "" {
					t.Errorf("azure audio %s leaked routerx private form field: %q", tt.name, leaked)
				}
				file, _, err := req.FormFile("file")
				if err != nil {
					t.Errorf("azure audio %s upstream missing audio file: %v", tt.name, err)
				} else {
					defer file.Close()
					raw := new(bytes.Buffer)
					_, _ = raw.ReadFrom(file)
					upstreamFile = raw.Bytes()
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"text":"` + tt.wantText + `"}`))
			}))
			defer upstream.Close()

			r := newTestRouter(t)
			initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
				"username": "root",
				"password": "password123",
			})
			if initResp.Code != http.StatusOK {
				t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
			}
			rootJWT := loginBearer(t, r, "root", "password123")
			if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
				t.Fatal(err)
			}
			tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
				"name":         "azure-audio-" + tt.name,
				"remain_quota": 50,
			})
			var tokenPayload struct {
				Data struct {
					ID  uint   `json:"id"`
					Key string `json:"key"`
				} `json:"data"`
			}
			if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
				t.Fatal(err)
			}
			if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
				t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
			}
			channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
				"type":     common.ChannelTypeAzure,
				"name":     "azure-audio-" + tt.name,
				"models":   "whisper-azure",
				"base_url": upstream.URL,
				"api_key":  "azure-secret",
			})
			if channelResp.Code != http.StatusOK {
				t.Fatalf("create azure audio channel failed: %d %s", channelResp.Code, channelResp.Body.String())
			}

			var reqBody bytes.Buffer
			writer := multipart.NewWriter(&reqBody)
			if err := writer.WriteField("model", "whisper-azure"); err != nil {
				t.Fatal(err)
			}
			if err := writer.WriteField("prompt", "domain words"); err != nil {
				t.Fatal(err)
			}
			routerxOptions, err := json.Marshal(map[string]interface{}{
				"route": map[string]string{"provider": "azure"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := writer.WriteField("routerx", string(routerxOptions)); err != nil {
				t.Fatal(err)
			}
			fileWriter, err := writer.CreateFormFile("file", "sample.wav")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fileWriter.Write(audioBytes); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}

			req := httptest.NewRequest(http.MethodPost, tt.downstreamPath, &reqBody)
			req.Header.Set("Authorization", "Bearer "+tokenPayload.Data.Key)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			resp := httptest.NewRecorder()
			r.ServeHTTP(resp, req)

			if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"text":"`+tt.wantText+`"`) {
				t.Fatalf("azure audio %s should return upstream response, got %d %s", tt.name, resp.Code, resp.Body.String())
			}
			if upstreamCalls != 1 || upstreamPath != tt.wantUpstreamPath || upstreamAPIVersion != "preview" {
				t.Fatalf("azure audio %s should use Azure v1 path and preview api-version, calls=%d path=%q api-version=%q", tt.name, upstreamCalls, upstreamPath, upstreamAPIVersion)
			}
			if upstreamAPIKey != "azure-secret" || upstreamAuth != "" {
				t.Fatalf("azure audio %s should use api-key header only, api-key=%q authorization=%q", tt.name, upstreamAPIKey, upstreamAuth)
			}
			if upstreamModel != "whisper-azure" || upstreamPrompt != "domain words" || !bytes.Equal(upstreamFile, audioBytes) {
				t.Fatalf("azure audio %s should preserve multipart fields without routerx, model=%q prompt=%q file=%q", tt.name, upstreamModel, upstreamPrompt, string(upstreamFile))
			}
			var storedToken model.Token
			if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
				t.Fatal(err)
			}
			if storedToken.RemainQuota != 49 {
				t.Fatalf("azure audio %s without usage should deduct minimum token budget charge, got %d", tt.name, storedToken.RemainQuota)
			}
			var root model.User
			if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
				t.Fatal(err)
			}
			if root.Quota != 99 {
				t.Fatalf("azure audio %s without usage should deduct minimum user quota charge, got %d", tt.name, root.Quota)
			}
			var callLog model.Log
			if err := internal.DB.First(&callLog).Error; err != nil {
				t.Fatal(err)
			}
			if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 1 || callLog.TotalTokens != 0 || callLog.UsageSource != common.LogUsageSourceMinimum {
				t.Fatalf("unexpected azure audio %s success log: %+v", tt.name, callLog)
			}
		})
	}
}

func TestResponsesPassthroughExtractsUsageAndDeductsQuota(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamPath := ""
	upstreamBody := map[string]interface{}{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPath = req.URL.Path
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(req.Body)
		if err := json.Unmarshal(raw.Bytes(), &upstreamBody); err != nil {
			t.Errorf("upstream body should be json: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-test","object":"response","model":"gpt-test","output":[],"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "responses",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "responses",
		"models":   "gpt-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	resp := performJSON(r, http.MethodPost, "/v1/responses", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-test",
		"input": "hello",
		"routerx": map[string]interface{}{
			"route": map[string]string{"provider": "openai-compatible"},
		},
	})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"id":"resp-test"`) {
		t.Fatalf("responses passthrough should return upstream response, got %d %s", resp.Code, resp.Body.String())
	}
	if upstreamCalls != 1 || upstreamPath != "/v1/responses" {
		t.Fatalf("responses should call upstream endpoint once, calls=%d path=%q", upstreamCalls, upstreamPath)
	}
	if upstreamBody["model"] != "gpt-test" || upstreamBody["input"] != "hello" {
		t.Fatalf("responses request should preserve model and input, got %#v", upstreamBody)
	}
	if _, ok := upstreamBody["routerx"]; ok {
		t.Fatalf("routerx private field leaked to upstream: %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 43 {
		t.Fatalf("responses usage should deduct token budget by 7, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 93 {
		t.Fatalf("responses usage should deduct user quota by 7, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 7 || callLog.TotalTokens != 7 || callLog.PromptTokens != 3 || callLog.CompletionTokens != 4 {
		t.Fatalf("unexpected responses success log: %+v", callLog)
	}
}

func TestEmbeddingsPassthroughExtractsUsageAndDeductsQuota(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamPath := ""
	upstreamBody := map[string]interface{}{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPath = req.URL.Path
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(req.Body)
		if err := json.Unmarshal(raw.Bytes(), &upstreamBody); err != nil {
			t.Errorf("upstream body should be json: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"embed-test","usage":{"prompt_tokens":8,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "embeddings",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "embeddings",
		"models":   "embed-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	resp := performJSON(r, http.MethodPost, "/v1/embeddings", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "embed-test",
		"input": []string{"hello"},
		"routerx": map[string]interface{}{
			"route": map[string]string{"provider": "openai-compatible"},
		},
	})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"object":"embedding"`) {
		t.Fatalf("embeddings passthrough should return upstream response, got %d %s", resp.Code, resp.Body.String())
	}
	if upstreamCalls != 1 || upstreamPath != "/v1/embeddings" {
		t.Fatalf("embeddings should call upstream endpoint once, calls=%d path=%q", upstreamCalls, upstreamPath)
	}
	if upstreamBody["model"] != "embed-test" {
		t.Fatalf("embeddings request should preserve model, got %#v", upstreamBody)
	}
	if input, ok := upstreamBody["input"].([]interface{}); !ok || len(input) != 1 || input[0] != "hello" {
		t.Fatalf("embeddings request should preserve input array, got %#v", upstreamBody["input"])
	}
	if _, ok := upstreamBody["routerx"]; ok {
		t.Fatalf("routerx private field leaked to upstream: %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 42 {
		t.Fatalf("embeddings usage should deduct token budget by 8, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 92 {
		t.Fatalf("embeddings usage should deduct user quota by 8, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 8 || callLog.TotalTokens != 8 || callLog.PromptTokens != 8 || callLog.CompletionTokens != 0 {
		t.Fatalf("unexpected embeddings success log: %+v", callLog)
	}
}

func TestModerationsPassthroughUsesMinimumChargeWithoutUsage(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamPath := ""
	upstreamBody := map[string]interface{}{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPath = req.URL.Path
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(req.Body)
		if err := json.Unmarshal(raw.Bytes(), &upstreamBody); err != nil {
			t.Errorf("upstream body should be json: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"modr-test","model":"omni-moderation-test","results":[{"flagged":false,"categories":{},"category_scores":{}}]}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "moderations",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "moderations",
		"models":   "omni-moderation-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	resp := performJSON(r, http.MethodPost, "/v1/moderations", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "omni-moderation-test",
		"input": "hello",
		"routerx": map[string]interface{}{
			"route": map[string]string{"provider": "openai-compatible"},
		},
	})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"id":"modr-test"`) {
		t.Fatalf("moderations passthrough should return upstream response, got %d %s", resp.Code, resp.Body.String())
	}
	if upstreamCalls != 1 || upstreamPath != "/v1/moderations" {
		t.Fatalf("moderations should call upstream endpoint once, calls=%d path=%q", upstreamCalls, upstreamPath)
	}
	if upstreamBody["model"] != "omni-moderation-test" || upstreamBody["input"] != "hello" {
		t.Fatalf("moderations request should preserve model and input, got %#v", upstreamBody)
	}
	if _, ok := upstreamBody["routerx"]; ok {
		t.Fatalf("routerx private field leaked to upstream: %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 49 {
		t.Fatalf("moderations without usage should use minimum token budget charge, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 99 {
		t.Fatalf("moderations without usage should use minimum user quota charge, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 1 || callLog.TotalTokens != 0 || callLog.PromptTokens != 0 || callLog.CompletionTokens != 0 {
		t.Fatalf("unexpected moderations success log: %+v", callLog)
	}
	if callLog.UsageSource != common.LogUsageSourceMinimum {
		t.Fatalf("moderations log should record minimum usage source, got %+v", callLog)
	}
	var billingSnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(callLog.BillingSnapshot), &billingSnapshot); err != nil {
		t.Fatalf("moderations log should store billing snapshot JSON, got %q: %v", callLog.BillingSnapshot, err)
	}
	expressionSnapshot, ok := billingSnapshot["billing_expression_snapshot"].(map[string]interface{})
	if !ok || expressionSnapshot["source"] != "minimum" || expressionSnapshot["expression"] != "minimum_charge" || expressionSnapshot["base_quota"] != float64(1) {
		t.Fatalf("minimum billing snapshot should record minimum expression: %+v", billingSnapshot)
	}
}

func TestUsageMissingStrategyRejectsWithoutDeductingQuota(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"modr-no-usage","model":"omni-moderation-test","results":[{"flagged":false,"categories":{},"category_scores":{}}]}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("billing.usage_missing_strategy", "reject"); err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "missing-usage-reject",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "missing-usage-reject",
		"models":   "omni-moderation-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	resp := performJSON(r, http.MethodPost, "/v1/moderations", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "omni-moderation-test",
		"input": "hello",
	})
	if resp.Code != http.StatusBadGateway || !strings.Contains(resp.Body.String(), `"code":"usage_missing"`) {
		t.Fatalf("missing usage reject strategy should return usage_missing, got %d %s", resp.Code, resp.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("missing usage reject should happen after exactly one upstream call, calls=%d", upstreamCalls)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 50 {
		t.Fatalf("missing usage reject should not deduct token budget, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("missing usage reject should not deduct user quota, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusFailed ||
		callLog.QuotaUsed != 0 ||
		callLog.ErrorCode != "usage_missing" ||
		callLog.ErrorSource != common.LogErrorSourceBilling {
		t.Fatalf("unexpected missing usage failure log: %+v", callLog)
	}
}

func TestImageGenerationsPassthroughUsesMinimumChargeWithoutUsage(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamPath := ""
	upstreamBody := map[string]interface{}{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPath = req.URL.Path
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(req.Body)
		if err := json.Unmarshal(raw.Bytes(), &upstreamBody); err != nil {
			t.Errorf("upstream body should be json: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1710000000,"data":[{"url":"https://example.invalid/image.png"}]}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "image-generations",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "image-generations",
		"models":   "gpt-image-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	resp := performJSON(r, http.MethodPost, "/v1/images/generations", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model":  "gpt-image-test",
		"prompt": "draw a small router",
		"size":   "1024x1024",
		"routerx": map[string]interface{}{
			"route": map[string]string{"provider": "openai-compatible"},
		},
	})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"url":"https://example.invalid/image.png"`) {
		t.Fatalf("image generation passthrough should return upstream response, got %d %s", resp.Code, resp.Body.String())
	}
	if upstreamCalls != 1 || upstreamPath != "/v1/images/generations" {
		t.Fatalf("image generation should call upstream endpoint once, calls=%d path=%q", upstreamCalls, upstreamPath)
	}
	if upstreamBody["model"] != "gpt-image-test" || upstreamBody["prompt"] != "draw a small router" || upstreamBody["size"] != "1024x1024" {
		t.Fatalf("image generation request should preserve model, prompt and size, got %#v", upstreamBody)
	}
	if _, ok := upstreamBody["routerx"]; ok {
		t.Fatalf("routerx private field leaked to upstream: %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 49 {
		t.Fatalf("image generation without usage should use minimum token budget charge, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 99 {
		t.Fatalf("image generation without usage should use minimum user quota charge, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 1 || callLog.TotalTokens != 0 || callLog.PromptTokens != 0 || callLog.CompletionTokens != 0 {
		t.Fatalf("unexpected image generation success log: %+v", callLog)
	}
}

func TestImageMultipartPassthroughUsesRouteAndMinimumCharge(t *testing.T) {
	cases := []struct {
		name         string
		endpoint     string
		expectedPath string
		withPrompt   bool
		withMask     bool
	}{
		{
			name:         "edits",
			endpoint:     "/v1/images/edits",
			expectedPath: "/v1/images/edits",
			withPrompt:   true,
			withMask:     true,
		},
		{
			name:         "variations",
			endpoint:     "/v1/images/variations",
			expectedPath: "/v1/images/variations",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("JWT_SECRET", "test-jwt-secret")
			t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

			imageBytes := []byte("PNG-routerx-image")
			maskBytes := []byte("PNG-routerx-mask")
			paidCalls := 0
			freeCalls := 0
			upstreamPath := ""
			upstreamAuth := ""
			upstreamModel := ""
			upstreamPrompt := ""
			var upstreamImage []byte
			var upstreamMask []byte
			upstreamHandler := func(label string, calls *int) http.HandlerFunc {
				return func(w http.ResponseWriter, req *http.Request) {
					*calls++
					if label == "paid" {
						upstreamPath = req.URL.Path
						upstreamAuth = req.Header.Get("Authorization")
					}
					if !strings.Contains(req.Header.Get("Content-Type"), "multipart/form-data") {
						t.Errorf("%s upstream should receive multipart content type, got %q", label, req.Header.Get("Content-Type"))
					}
					if err := req.ParseMultipartForm(20 << 20); err != nil {
						t.Errorf("%s upstream received invalid multipart body: %v", label, err)
					}
					if label == "paid" {
						upstreamModel = req.FormValue("model")
						upstreamPrompt = req.FormValue("prompt")
						if leaked := req.FormValue("routerx"); leaked != "" {
							t.Errorf("routerx private form field leaked to upstream: %q", leaked)
						}
						file, _, err := req.FormFile("image")
						if err != nil {
							t.Errorf("paid upstream missing image file: %v", err)
						} else {
							defer file.Close()
							raw := new(bytes.Buffer)
							_, _ = raw.ReadFrom(file)
							upstreamImage = raw.Bytes()
						}
						if tc.withMask {
							mask, _, err := req.FormFile("mask")
							if err != nil {
								t.Errorf("paid upstream missing mask file: %v", err)
							} else {
								defer mask.Close()
								raw := new(bytes.Buffer)
								_, _ = raw.ReadFrom(mask)
								upstreamMask = raw.Bytes()
							}
						}
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"created":1710000000,"data":[{"url":"https://example.invalid/` + label + `.png"}]}`))
				}
			}
			freeUpstream := httptest.NewServer(upstreamHandler("free", &freeCalls))
			defer freeUpstream.Close()
			paidUpstream := httptest.NewServer(upstreamHandler("paid", &paidCalls))
			defer paidUpstream.Close()

			r := newTestRouter(t)
			initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
				"username": "root",
				"password": "password123",
			})
			if initResp.Code != http.StatusOK {
				t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
			}
			rootJWT := loginBearer(t, r, "root", "password123")
			if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
				t.Fatal(err)
			}
			if err := service.NewSettingService().Set("billing.default_user_channel_group_access", `["free","paid"]`); err != nil {
				t.Fatal(err)
			}
			tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
				"name":         "image-" + tc.name,
				"remain_quota": 50,
			})
			var tokenPayload struct {
				Data struct {
					ID  uint   `json:"id"`
					Key string `json:"key"`
				} `json:"data"`
			}
			if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
				t.Fatal(err)
			}
			if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
				t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
			}
			createChannel := func(name, group, baseURL, apiKey string, priority int) {
				t.Helper()
				resp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
					"type":     common.ChannelTypeOpenAICompat,
					"name":     name,
					"models":   "gpt-image-test",
					"base_url": baseURL,
					"api_key":  apiKey,
					"group":    group,
					"priority": priority,
				})
				if resp.Code != http.StatusOK {
					t.Fatalf("create %s channel failed: %d %s", name, resp.Code, resp.Body.String())
				}
			}
			createChannel("free", "free", freeUpstream.URL, "upstream-secret-free", 50)
			createChannel("paid", "paid", paidUpstream.URL, "upstream-secret-paid", 1)

			var reqBody bytes.Buffer
			writer := multipart.NewWriter(&reqBody)
			if err := writer.WriteField("model", "gpt-image-test"); err != nil {
				t.Fatal(err)
			}
			if tc.withPrompt {
				if err := writer.WriteField("prompt", "draw a tidy router"); err != nil {
					t.Fatal(err)
				}
			}
			routerxOptions, err := json.Marshal(map[string]interface{}{
				"route": map[string]string{"channel_group": "paid"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := writer.WriteField("routerx", string(routerxOptions)); err != nil {
				t.Fatal(err)
			}
			imageWriter, err := writer.CreateFormFile("image", "input.png")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := imageWriter.Write(imageBytes); err != nil {
				t.Fatal(err)
			}
			if tc.withMask {
				maskWriter, err := writer.CreateFormFile("mask", "mask.png")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := maskWriter.Write(maskBytes); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}

			req := httptest.NewRequest(http.MethodPost, tc.endpoint, &reqBody)
			req.Header.Set("Authorization", "Bearer "+tokenPayload.Data.Key)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			resp := httptest.NewRecorder()
			r.ServeHTTP(resp, req)

			if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"url":"https://example.invalid/paid.png"`) {
				t.Fatalf("image multipart passthrough should return paid upstream response, got %d %s", resp.Code, resp.Body.String())
			}
			if paidCalls != 1 || freeCalls != 0 || upstreamPath != tc.expectedPath {
				t.Fatalf("routerx.route should select paid image upstream, paid=%d free=%d path=%q", paidCalls, freeCalls, upstreamPath)
			}
			if upstreamAuth != "Bearer upstream-secret-paid" {
				t.Fatalf("upstream authorization should use selected channel secret, got %q", upstreamAuth)
			}
			if upstreamModel != "gpt-image-test" || !bytes.Equal(upstreamImage, imageBytes) {
				t.Fatalf("multipart image fields should be preserved without routerx, model=%q image=%q", upstreamModel, string(upstreamImage))
			}
			if tc.withPrompt && upstreamPrompt != "draw a tidy router" {
				t.Fatalf("multipart prompt should be preserved, got %q", upstreamPrompt)
			}
			if tc.withMask && !bytes.Equal(upstreamMask, maskBytes) {
				t.Fatalf("multipart mask should be preserved, got %q", string(upstreamMask))
			}
			var storedToken model.Token
			if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
				t.Fatal(err)
			}
			if storedToken.RemainQuota != 49 {
				t.Fatalf("image multipart without usage should use minimum token budget charge, got %d", storedToken.RemainQuota)
			}
			var root model.User
			if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
				t.Fatal(err)
			}
			if root.Quota != 99 {
				t.Fatalf("image multipart without usage should use minimum user quota charge, got %d", root.Quota)
			}
			var callLog model.Log
			if err := internal.DB.First(&callLog).Error; err != nil {
				t.Fatal(err)
			}
			if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 1 || callLog.TotalTokens != 0 || callLog.PromptTokens != 0 || callLog.CompletionTokens != 0 {
				t.Fatalf("unexpected image multipart success log: %+v", callLog)
			}
		})
	}
}

func TestAudioSpeechPassthroughReturnsBinaryAndUsesMinimumCharge(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	audioBytes := []byte{0x49, 0x44, 0x33, 0x04, 0x00, 0x00, 0x00, 0x00}
	upstreamCalls := 0
	upstreamPath := ""
	upstreamBody := map[string]interface{}{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamPath = req.URL.Path
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(req.Body)
		if err := json.Unmarshal(raw.Bytes(), &upstreamBody); err != nil {
			t.Errorf("upstream body should be json: %v", err)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(audioBytes)
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "audio-speech",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "audio-speech",
		"models":   "tts-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	resp := performJSON(r, http.MethodPost, "/v1/audio/speech", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "tts-test",
		"input": "hello",
		"voice": "alloy",
		"routerx": map[string]interface{}{
			"route": map[string]string{"provider": "openai-compatible"},
		},
	})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Header().Get("Content-Type"), "audio/mpeg") || !bytes.Equal(resp.Body.Bytes(), audioBytes) {
		t.Fatalf("audio speech should return upstream binary response, got %d %s %#v", resp.Code, resp.Header().Get("Content-Type"), resp.Body.Bytes())
	}
	if upstreamCalls != 1 || upstreamPath != "/v1/audio/speech" {
		t.Fatalf("audio speech should call upstream endpoint once, calls=%d path=%q", upstreamCalls, upstreamPath)
	}
	if upstreamBody["model"] != "tts-test" || upstreamBody["input"] != "hello" || upstreamBody["voice"] != "alloy" {
		t.Fatalf("audio speech request should preserve model, input and voice, got %#v", upstreamBody)
	}
	if _, ok := upstreamBody["routerx"]; ok {
		t.Fatalf("routerx private field leaked to upstream: %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 49 {
		t.Fatalf("audio speech without usage should use minimum token budget charge, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 99 {
		t.Fatalf("audio speech without usage should use minimum user quota charge, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 1 || callLog.TotalTokens != 0 || callLog.PromptTokens != 0 || callLog.CompletionTokens != 0 {
		t.Fatalf("unexpected audio speech success log: %+v", callLog)
	}
}

func TestAudioTranscriptionsMultipartPassthroughUsesRouteAndMinimumCharge(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	audioBytes := []byte("RIFF-routerx-audio")
	paidCalls := 0
	freeCalls := 0
	upstreamPath := ""
	upstreamAuth := ""
	upstreamModel := ""
	upstreamPrompt := ""
	var upstreamFile []byte
	upstreamHandler := func(label string, calls *int) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			*calls++
			if label == "paid" {
				upstreamPath = req.URL.Path
				upstreamAuth = req.Header.Get("Authorization")
			}
			if !strings.Contains(req.Header.Get("Content-Type"), "multipart/form-data") {
				t.Errorf("%s upstream should receive multipart content type, got %q", label, req.Header.Get("Content-Type"))
			}
			if err := req.ParseMultipartForm(20 << 20); err != nil {
				t.Errorf("%s upstream received invalid multipart body: %v", label, err)
			}
			if label == "paid" {
				upstreamModel = req.FormValue("model")
				upstreamPrompt = req.FormValue("prompt")
				if leaked := req.FormValue("routerx"); leaked != "" {
					t.Errorf("routerx private form field leaked to upstream: %q", leaked)
				}
				file, _, err := req.FormFile("file")
				if err != nil {
					t.Errorf("paid upstream missing audio file: %v", err)
				} else {
					defer file.Close()
					raw := new(bytes.Buffer)
					_, _ = raw.ReadFrom(file)
					upstreamFile = raw.Bytes()
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"` + label + ` transcript"}`))
		}
	}
	freeUpstream := httptest.NewServer(upstreamHandler("free", &freeCalls))
	defer freeUpstream.Close()
	paidUpstream := httptest.NewServer(upstreamHandler("paid", &paidCalls))
	defer paidUpstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("billing.default_user_channel_group_access", `["free","paid"]`); err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "audio-transcriptions",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	createChannel := func(name, group, baseURL, apiKey string, priority int) {
		t.Helper()
		resp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
			"type":     common.ChannelTypeOpenAICompat,
			"name":     name,
			"models":   "whisper-test",
			"base_url": baseURL,
			"api_key":  apiKey,
			"group":    group,
			"priority": priority,
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("create %s channel failed: %d %s", name, resp.Code, resp.Body.String())
		}
	}
	createChannel("free", "free", freeUpstream.URL, "upstream-secret-free", 50)
	createChannel("paid", "paid", paidUpstream.URL, "upstream-secret-paid", 1)

	var reqBody bytes.Buffer
	writer := multipart.NewWriter(&reqBody)
	if err := writer.WriteField("model", "whisper-test"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("prompt", "domain words"); err != nil {
		t.Fatal(err)
	}
	routerxOptions, err := json.Marshal(map[string]interface{}{
		"route": map[string]string{"channel_group": "paid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("routerx", string(routerxOptions)); err != nil {
		t.Fatal(err)
	}
	fileWriter, err := writer.CreateFormFile("file", "sample.wav")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fileWriter.Write(audioBytes); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &reqBody)
	req.Header.Set("Authorization", "Bearer "+tokenPayload.Data.Key)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"text":"paid transcript"`) {
		t.Fatalf("audio transcription multipart passthrough should return paid upstream response, got %d %s", resp.Code, resp.Body.String())
	}
	if paidCalls != 1 || freeCalls != 0 || upstreamPath != "/v1/audio/transcriptions" {
		t.Fatalf("routerx.route should select paid transcription upstream, paid=%d free=%d path=%q", paidCalls, freeCalls, upstreamPath)
	}
	if upstreamAuth != "Bearer upstream-secret-paid" {
		t.Fatalf("upstream authorization should use selected channel secret, got %q", upstreamAuth)
	}
	if upstreamModel != "whisper-test" || upstreamPrompt != "domain words" || !bytes.Equal(upstreamFile, audioBytes) {
		t.Fatalf("multipart fields should be preserved without routerx, model=%q prompt=%q file=%q", upstreamModel, upstreamPrompt, string(upstreamFile))
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 49 {
		t.Fatalf("audio transcription without usage should use minimum token budget charge, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 99 {
		t.Fatalf("audio transcription without usage should use minimum user quota charge, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 1 || callLog.TotalTokens != 0 || callLog.PromptTokens != 0 || callLog.CompletionTokens != 0 {
		t.Fatalf("unexpected audio transcription success log: %+v", callLog)
	}
}

func TestRouterXOptionsHeaderRoutesMultipartRequest(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	paidCalls := 0
	freeCalls := 0
	upstreamHandler := func(label string, calls *int) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			*calls++
			if err := req.ParseMultipartForm(20 << 20); err != nil {
				t.Errorf("%s upstream received invalid multipart body: %v", label, err)
			}
			if leaked := req.FormValue("routerx"); leaked != "" {
				t.Errorf("routerx private form field leaked to upstream: %q", leaked)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"` + label + ` transcript"}`))
		}
	}
	freeUpstream := httptest.NewServer(upstreamHandler("free", &freeCalls))
	defer freeUpstream.Close()
	paidUpstream := httptest.NewServer(upstreamHandler("paid", &paidCalls))
	defer paidUpstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("billing.default_user_channel_group_access", `["free","paid"]`); err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "routerx-header",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	createChannel := func(name, group, baseURL string, priority int) {
		t.Helper()
		resp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
			"type":     common.ChannelTypeOpenAICompat,
			"name":     name,
			"models":   "whisper-test",
			"base_url": baseURL,
			"api_key":  "upstream-secret-" + group,
			"group":    group,
			"priority": priority,
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("create %s channel failed: %d %s", name, resp.Code, resp.Body.String())
		}
	}
	createChannel("free", "free", freeUpstream.URL, 50)
	createChannel("paid", "paid", paidUpstream.URL, 1)

	var reqBody bytes.Buffer
	writer := multipart.NewWriter(&reqBody)
	if err := writer.WriteField("model", "whisper-test"); err != nil {
		t.Fatal(err)
	}
	fileWriter, err := writer.CreateFormFile("file", "sample.wav")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fileWriter.Write([]byte("RIFF-routerx-header-audio")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &reqBody)
	req.Header.Set("Authorization", "Bearer "+tokenPayload.Data.Key)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-RouterX-Options", `{"route":{"channel_group":"paid"}}`)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"text":"paid transcript"`) {
		t.Fatalf("X-RouterX-Options should route multipart request to paid upstream, got %d %s", resp.Code, resp.Body.String())
	}
	if paidCalls != 1 || freeCalls != 0 {
		t.Fatalf("X-RouterX-Options should select paid channel only, paid=%d free=%d", paidCalls, freeCalls)
	}
}

func TestChatCompletionStreamForwardsSSEAndDeductsUsage(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamAuth := ""
	var upstreamBody map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamAuth = req.Header.Get("Authorization")
		if err := json.NewDecoder(req.Body).Decode(&upstreamBody); err != nil {
			t.Errorf("upstream received invalid JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"he\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"llo\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "stream",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "stream",
		"models":   "gpt-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-test",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"stream": true,
		"routerx": map[string]interface{}{
			"route": map[string]string{"channel": "stream"},
		},
	})
	if chatResp.Code != http.StatusOK {
		t.Fatalf("stream chat should succeed, got %d %s", chatResp.Code, chatResp.Body.String())
	}
	if ct := chatResp.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("stream response should be SSE, got content-type %q", ct)
	}
	body := chatResp.Body.String()
	if !strings.Contains(body, "chat.completion.chunk") || !strings.Contains(body, "data: [DONE]") || strings.Contains(body, `"success"`) {
		t.Fatalf("stream body should forward OpenAI SSE chunks without RouterX wrapper: %s", body)
	}
	if upstreamCalls != 1 || upstreamAuth != "Bearer upstream-secret" {
		t.Fatalf("stream should call upstream once with channel secret, calls=%d auth=%q", upstreamCalls, upstreamAuth)
	}
	if upstreamBody["stream"] != true || upstreamBody["model"] != "gpt-test" {
		t.Fatalf("stream request should preserve stream=true and model, got %#v", upstreamBody)
	}
	if _, ok := upstreamBody["routerx"]; ok {
		t.Fatalf("routerx private field leaked to upstream: %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 43 {
		t.Fatalf("stream usage should deduct token budget by 7, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 93 {
		t.Fatalf("stream usage should deduct user quota by 7, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 7 || callLog.TotalTokens != 7 || callLog.PromptTokens != 3 || callLog.CompletionTokens != 4 {
		t.Fatalf("unexpected stream success log: %+v", callLog)
	}
}

func TestCompletionsStreamForwardsSSEAndDeductsUsage(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamAuth := ""
	upstreamPath := ""
	var upstreamBody map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamAuth = req.Header.Get("Authorization")
		upstreamPath = req.URL.Path
		if err := json.NewDecoder(req.Body).Decode(&upstreamBody); err != nil {
			t.Errorf("upstream received invalid JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"cmpl-stream\",\"object\":\"text_completion\",\"choices\":[{\"index\":0,\"text\":\"he\"}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"cmpl-stream\",\"object\":\"text_completion\",\"choices\":[{\"index\":0,\"text\":\"llo\",\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "completion-stream",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "completion-stream",
		"models":   "gpt-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	completionResp := performJSON(r, http.MethodPost, "/v1/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model":  "gpt-test",
		"prompt": "hello",
		"stream": true,
		"routerx": map[string]interface{}{
			"route": map[string]string{"provider": "openai-compatible"},
		},
	})
	if completionResp.Code != http.StatusOK || completionResp.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream completion should return SSE, got %d headers=%v body=%s", completionResp.Code, completionResp.Header(), completionResp.Body.String())
	}
	body := completionResp.Body.String()
	if !strings.Contains(body, "data: {\"id\":\"cmpl-stream\"") || !strings.Contains(body, "data: [DONE]") || strings.Contains(body, `"success"`) {
		t.Fatalf("completion stream body should forward OpenAI SSE chunks without RouterX wrapper: %s", body)
	}
	if upstreamCalls != 1 || upstreamAuth != "Bearer upstream-secret" || upstreamPath != "/v1/completions" {
		t.Fatalf("stream should call completions upstream once with channel secret, calls=%d auth=%q path=%q", upstreamCalls, upstreamAuth, upstreamPath)
	}
	if upstreamBody["stream"] != true || upstreamBody["model"] != "gpt-test" || upstreamBody["prompt"] != "hello" {
		t.Fatalf("stream request should preserve stream=true, model and prompt, got %#v", upstreamBody)
	}
	if _, ok := upstreamBody["routerx"]; ok {
		t.Fatalf("routerx private field leaked to upstream: %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 43 {
		t.Fatalf("completion stream usage should deduct token budget by 7, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 93 {
		t.Fatalf("completion stream usage should deduct user quota by 7, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 7 || callLog.TotalTokens != 7 || callLog.PromptTokens != 3 || callLog.CompletionTokens != 4 {
		t.Fatalf("unexpected completion stream success log: %+v", callLog)
	}
}

func TestChatCompletionStreamCancelsUpstreamWhenClientWriteFails(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-cancel\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-req.Context().Done():
			close(upstreamCancelled)
		case <-time.After(2 * time.Second):
		}
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "stream-cancel",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "stream-cancel",
		"models":   "gpt-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	relaySvc := service.NewRelayService(service.NewChannelService(), service.NewTokenService(), service.NewLogService(), service.NewSettingService())
	body, err := json.Marshal(map[string]interface{}{
		"model": "gpt-test",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"stream": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := relaySvc.RelayStream(context.Background(), &storedToken, relay.APIChatCompletions, body, "192.0.2.1")
	if err != nil {
		t.Fatalf("stream setup failed: %v", err)
	}
	clientClosed := errors.New("client closed")
	_, err = result.Forward(func(chunk []byte) error {
		if !bytes.Contains(chunk, []byte("chatcmpl-cancel")) {
			t.Fatalf("unexpected first stream chunk: %s", string(chunk))
		}
		return clientClosed
	}, func() {})
	if !errors.Is(err, clientClosed) {
		t.Fatalf("stream forwarding should return client write error, got %v", err)
	}
	select {
	case <-upstreamCancelled:
	case <-time.After(time.Second):
		t.Fatal("upstream request was not cancelled after client write failure")
	}
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 50 {
		t.Fatalf("failed stream should not deduct token budget, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("failed stream should not deduct user quota, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusFailed || callLog.QuotaUsed != 0 || !strings.Contains(callLog.ErrorMsg, "stream forwarding failed") {
		t.Fatalf("unexpected stream cancellation log: %+v", callLog)
	}
}

func TestChatCompletionStreamRejectsNonOpenAISSEUpstream(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\"}\n\n"))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "stream-non-openai",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeClaude,
		"name":     "claude-stream",
		"models":   "gpt-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-test",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"stream": true,
	})
	if chatResp.Code != http.StatusBadGateway || !strings.Contains(chatResp.Body.String(), `"code":"unsupported_stream_channel"`) {
		t.Fatalf("non OpenAI SSE stream should be rejected before upstream, got %d %s", chatResp.Code, chatResp.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("unsupported stream channel must not call upstream, got %d calls", upstreamCalls)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 50 {
		t.Fatalf("unsupported stream should not deduct token budget, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("unsupported stream should not deduct user quota, got %d", root.Quota)
	}
}

func TestChatCompletionRetriesRetryableUpstreamAndDeductsOnce(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	firstCalls := 0
	firstUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		firstCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"temporary"}}`))
	}))
	defer firstUpstream.Close()

	secondCalls := 0
	secondAuth := ""
	secondUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		secondCalls++
		secondAuth = req.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-retry","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer secondUpstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.Model(&model.Setting{}).Where("key = ?", "relay.retry_count").Update("value", "1").Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "retry",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	firstChannelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "primary",
		"models":   "gpt-test",
		"base_url": firstUpstream.URL,
		"api_key":  "first-secret",
		"priority": 20,
		"idx":      1,
	})
	var firstChannelPayload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(firstChannelResp.Body.Bytes(), &firstChannelPayload); err != nil {
		t.Fatal(err)
	}
	if firstChannelResp.Code != http.StatusOK || firstChannelPayload.Data.ID == 0 {
		t.Fatalf("create first channel failed: %d %s", firstChannelResp.Code, firstChannelResp.Body.String())
	}
	secondChannelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "backup",
		"models":   "gpt-test",
		"base_url": secondUpstream.URL,
		"api_key":  "second-secret",
		"priority": 10,
		"idx":      2,
	})
	var secondChannelPayload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(secondChannelResp.Body.Bytes(), &secondChannelPayload); err != nil {
		t.Fatal(err)
	}
	if secondChannelResp.Code != http.StatusOK || secondChannelPayload.Data.ID == 0 {
		t.Fatalf("create second channel failed: %d %s", secondChannelResp.Code, secondChannelResp.Body.String())
	}

	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-test",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if chatResp.Code != http.StatusOK {
		t.Fatalf("retry should succeed through backup channel, got %d %s", chatResp.Code, chatResp.Body.String())
	}
	if firstCalls != 1 || secondCalls != 1 {
		t.Fatalf("expected one call to each upstream, first=%d second=%d", firstCalls, secondCalls)
	}
	if secondAuth != "Bearer second-secret" {
		t.Fatalf("backup request should use backup secret, got %q", secondAuth)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 45 {
		t.Fatalf("retry success should deduct once by usage total 5, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 95 {
		t.Fatalf("retry success should deduct user quota once by 5, got %d", root.Quota)
	}
	var firstChannel, secondChannel model.Channel
	if err := internal.DB.First(&firstChannel, firstChannelPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.First(&secondChannel, secondChannelPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if firstChannel.ErrorCount != 1 || secondChannel.ErrorCount != 0 {
		t.Fatalf("retry should mark failed channel once and successful channel healthy, first=%d second=%d", firstChannel.ErrorCount, secondChannel.ErrorCount)
	}
	var logs []model.Log
	if err := internal.DB.Order("id ASC").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 || logs[0].Status != common.LogStatusFailed || logs[1].Status != common.LogStatusSuccess || logs[1].QuotaUsed != 5 {
		t.Fatalf("retry should record failed attempt and final success, logs=%+v", logs)
	}
	var firstRouteSnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(logs[0].RouteSnapshot), &firstRouteSnapshot); err != nil {
		t.Fatalf("failed retry attempt should store route snapshot JSON, got %q: %v", logs[0].RouteSnapshot, err)
	}
	if selectedChannelID, ok := firstRouteSnapshot["selected_channel_id"].(float64); !ok || uint(selectedChannelID) != firstChannelPayload.Data.ID {
		t.Fatalf("failed attempt snapshot should reference failed primary channel: %+v", firstRouteSnapshot)
	}
	var secondRouteSnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(logs[1].RouteSnapshot), &secondRouteSnapshot); err != nil {
		t.Fatalf("successful retry attempt should store route snapshot JSON, got %q: %v", logs[1].RouteSnapshot, err)
	}
	if selectedChannelID, ok := secondRouteSnapshot["selected_channel_id"].(float64); !ok || uint(selectedChannelID) != secondChannelPayload.Data.ID {
		t.Fatalf("successful retry snapshot should reference backup channel: %+v", secondRouteSnapshot)
	}
	retryAttempts, ok := secondRouteSnapshot["retry_attempts"].([]interface{})
	if !ok || len(retryAttempts) != 1 {
		t.Fatalf("successful retry snapshot should record previous retry failure: %+v", secondRouteSnapshot)
	}
	firstAttempt, ok := retryAttempts[0].(map[string]interface{})
	if !ok || firstAttempt["status"] != "failed" || firstAttempt["error_code"] != "upstream_500" || firstAttempt["upstream_status"] != float64(500) {
		t.Fatalf("retry attempt summary should include stable upstream failure facts: %+v", retryAttempts[0])
	}
	if channelID, ok := firstAttempt["channel_id"].(float64); !ok || uint(channelID) != firstChannelPayload.Data.ID {
		t.Fatalf("retry attempt summary should reference failed primary channel: %+v", firstAttempt)
	}
}

func TestChatCompletionUsesConfiguredRetryStatuses(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	firstCalls := 0
	firstUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		firstCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"retryable by config"}}`))
	}))
	defer firstUpstream.Close()

	secondCalls := 0
	secondUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		secondCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-retry-status","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":4,"total_tokens":6}}`))
	}))
	defer secondUpstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	settingSvc := service.NewSettingService()
	if err := settingSvc.Set("relay.retry_count", "1"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("relay.retry_on_status", "[400]"); err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "retry-status",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	firstChannelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "retry-status-primary",
		"models":   "gpt-test",
		"base_url": firstUpstream.URL,
		"api_key":  "first-secret",
		"priority": 20,
		"idx":      1,
	})
	if firstChannelResp.Code != http.StatusOK {
		t.Fatalf("create first channel failed: %d %s", firstChannelResp.Code, firstChannelResp.Body.String())
	}
	secondChannelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "retry-status-backup",
		"models":   "gpt-test",
		"base_url": secondUpstream.URL,
		"api_key":  "second-secret",
		"priority": 10,
		"idx":      2,
	})
	if secondChannelResp.Code != http.StatusOK {
		t.Fatalf("create second channel failed: %d %s", secondChannelResp.Code, secondChannelResp.Body.String())
	}

	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-test",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if chatResp.Code != http.StatusOK {
		t.Fatalf("configured retry status should recover through backup, got %d %s", chatResp.Code, chatResp.Body.String())
	}
	if firstCalls != 1 || secondCalls != 1 {
		t.Fatalf("expected configured retry to call each upstream once, first=%d second=%d", firstCalls, secondCalls)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 44 {
		t.Fatalf("configured retry success should deduct once by usage total 6, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 94 {
		t.Fatalf("configured retry success should deduct user quota once by 6, got %d", root.Quota)
	}
	var logs []model.Log
	if err := internal.DB.Where("model = ?", "gpt-test").Order("id ASC").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 || logs[0].Status != common.LogStatusFailed || logs[0].ErrorCode != "upstream_400" || logs[1].Status != common.LogStatusSuccess || logs[1].QuotaUsed != 6 {
		t.Fatalf("configured retry should log failed upstream_400 then final success, logs=%+v", logs)
	}
	var routeSnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(logs[1].RouteSnapshot), &routeSnapshot); err != nil {
		t.Fatalf("successful retry should store route snapshot JSON, got %q: %v", logs[1].RouteSnapshot, err)
	}
	retryAttempts, ok := routeSnapshot["retry_attempts"].([]interface{})
	if !ok || len(retryAttempts) != 1 {
		t.Fatalf("successful retry snapshot should include configured failed attempt: %+v", routeSnapshot)
	}
	firstAttempt, ok := retryAttempts[0].(map[string]interface{})
	if !ok || firstAttempt["error_code"] != "upstream_400" || firstAttempt["upstream_status"] != float64(400) {
		t.Fatalf("retry attempt summary should preserve configured upstream status: %+v", retryAttempts[0])
	}
}

func TestChatCompletionSkipsTrippedChannelAtConfiguredThreshold(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	firstCalls := 0
	firstUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		firstCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"temporary"}}`))
	}))
	defer firstUpstream.Close()

	secondCalls := 0
	secondUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		secondCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-breaker","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer secondUpstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	settingSvc := service.NewSettingService()
	if err := settingSvc.Set("relay.retry_count", "1"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("relay.error_ban_threshold", "1"); err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "breaker",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	firstChannelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "breaker-primary",
		"models":   "gpt-test",
		"base_url": firstUpstream.URL,
		"api_key":  "first-secret",
		"priority": 20,
		"idx":      1,
	})
	var firstChannelPayload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(firstChannelResp.Body.Bytes(), &firstChannelPayload); err != nil {
		t.Fatal(err)
	}
	if firstChannelResp.Code != http.StatusOK || firstChannelPayload.Data.ID == 0 {
		t.Fatalf("create first channel failed: %d %s", firstChannelResp.Code, firstChannelResp.Body.String())
	}
	secondChannelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "breaker-backup",
		"models":   "gpt-test",
		"base_url": secondUpstream.URL,
		"api_key":  "second-secret",
		"priority": 10,
		"idx":      2,
	})
	if secondChannelResp.Code != http.StatusOK {
		t.Fatalf("create second channel failed: %d %s", secondChannelResp.Code, secondChannelResp.Body.String())
	}

	body := map[string]interface{}{
		"model": "gpt-test",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	first := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, body)
	if first.Code != http.StatusOK {
		t.Fatalf("first request should recover through backup, got %d %s", first.Code, first.Body.String())
	}
	second := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, body)
	if second.Code != http.StatusOK {
		t.Fatalf("second request should skip tripped channel and use backup, got %d %s", second.Code, second.Body.String())
	}
	if firstCalls != 1 || secondCalls != 2 {
		t.Fatalf("tripped primary should be skipped after reaching threshold, first=%d second=%d", firstCalls, secondCalls)
	}
	var firstChannel model.Channel
	if err := internal.DB.First(&firstChannel, firstChannelPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if firstChannel.ErrorCount != 1 {
		t.Fatalf("failed primary should remain at threshold after being skipped, got %d", firstChannel.ErrorCount)
	}
}

func TestChatCompletionHonorsDisabledAutoBanSetting(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-breaker-disabled","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	settingSvc := service.NewSettingService()
	if err := settingSvc.Set("relay.error_auto_ban", "false"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("relay.error_ban_threshold", "1"); err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "breaker-disabled",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "breaker-disabled",
		"models":   "gpt-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	var channelPayload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(channelResp.Body.Bytes(), &channelPayload); err != nil {
		t.Fatal(err)
	}
	if channelResp.Code != http.StatusOK || channelPayload.Data.ID == 0 {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}
	if err := internal.DB.Model(&model.Channel{}).Where("id = ?", channelPayload.Data.ID).Update("error_count", 10).Error; err != nil {
		t.Fatal(err)
	}

	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-test",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if chatResp.Code != http.StatusOK {
		t.Fatalf("auto ban disabled should allow high error_count channel, got %d %s", chatResp.Code, chatResp.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("auto ban disabled should call selected upstream, got %d", upstreamCalls)
	}
	var channel model.Channel
	if err := internal.DB.First(&channel, channelPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.ErrorCount != 0 {
		t.Fatalf("successful request should reset channel error_count, got %d", channel.ErrorCount)
	}
}

func TestChatCompletionDoesNotRetryNonRetryableUpstreamStatus(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	firstCalls := 0
	firstUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		firstCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer firstUpstream.Close()

	secondCalls := 0
	secondUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		secondCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-should-not-call","choices":[]}`))
	}))
	defer secondUpstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.Model(&model.Setting{}).Where("key = ?", "relay.retry_count").Update("value", "1").Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "no-retry",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	for _, channel := range []struct {
		name     string
		baseURL  string
		priority int
	}{
		{name: "bad-request", baseURL: firstUpstream.URL, priority: 20},
		{name: "backup", baseURL: secondUpstream.URL, priority: 10},
	} {
		resp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
			"type":     common.ChannelTypeOpenAICompat,
			"name":     channel.name,
			"models":   "gpt-test",
			"base_url": channel.baseURL,
			"api_key":  channel.name + "-secret",
			"priority": channel.priority,
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("create channel %q failed: %d %s", channel.name, resp.Code, resp.Body.String())
		}
	}
	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-test",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if chatResp.Code != http.StatusBadRequest || !strings.Contains(chatResp.Body.String(), `"code":"upstream_400"`) {
		t.Fatalf("upstream 400 should not retry, got %d %s", chatResp.Code, chatResp.Body.String())
	}
	if firstCalls != 1 || secondCalls != 0 {
		t.Fatalf("non-retryable upstream status should not call backup, first=%d second=%d", firstCalls, secondCalls)
	}
}

func TestChatCompletionUpstreamBadRequestMapping(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad upstream request","type":"invalid_request_error","code":"bad_request"}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "limited-sdk",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create limited token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "compat",
		"models":   "gpt-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-test",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if chatResp.Code != http.StatusBadRequest || strings.Contains(chatResp.Body.String(), `"success"`) || !strings.Contains(chatResp.Body.String(), `"code":"upstream_400"`) {
		t.Fatalf("upstream 400 should map to OpenAI-compatible 400, got %d %s", chatResp.Code, chatResp.Body.String())
	}
	if strings.Contains(chatResp.Body.String(), "upstream-secret") {
		t.Fatalf("upstream error leaked secret: %s", chatResp.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("expected one upstream request, got %d", upstreamCalls)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusFailed || callLog.QuotaUsed != 0 || strings.Contains(callLog.ErrorMsg, "upstream-secret") {
		t.Fatalf("unexpected failed call log: %+v", callLog)
	}
}

func TestRelayFailureLogPersistsRequestIDAndErrorCode(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad upstream request","type":"invalid_request_error","code":"bad_request"}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "request-id-log",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create request id token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "request-id-log-channel",
		"models":   "gpt-request-id-log",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create request id channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	requestID := "req-test-relay-failure"
	chatResp := performRawWithHeaders(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, `{"model":"gpt-request-id-log","messages":[{"role":"user","content":"hello"}]}`, map[string]string{
		"X-Request-Id": requestID,
	})
	if chatResp.Code != http.StatusBadRequest || !strings.Contains(chatResp.Body.String(), `"code":"upstream_400"`) {
		t.Fatalf("upstream 400 should map to OpenAI-compatible 400, got %d %s", chatResp.Code, chatResp.Body.String())
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.RequestID != requestID || callLog.ErrorCode != "upstream_400" {
		t.Fatalf("failed relay log should persist request_id and error_code, got %+v", callLog)
	}
	if callLog.ErrorSource != "upstream" || callLog.UpstreamStatus != http.StatusBadRequest {
		t.Fatalf("failed relay log should persist upstream failure facts, got %+v", callLog)
	}
	logResp := performJSON(r, http.MethodGet, "/v0/user/log", rootJWT, nil)
	logBody := logResp.Body.String()
	if logResp.Code != http.StatusOK ||
		!strings.Contains(logBody, `"request_id":"`+requestID+`"`) ||
		!strings.Contains(logBody, `"error_code":"upstream_400"`) ||
		!strings.Contains(logBody, `"error_source":"upstream"`) ||
		!strings.Contains(logBody, `"upstream_status":400`) {
		t.Fatalf("user log API should expose request_id, error_code and upstream failure facts, got %d %s", logResp.Code, logBody)
	}
}

func TestRelayMaxResponseBodyBytesRejectsOversizedUpstream(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	oversizedBody := strings.Repeat("oversized-upstream-body", 20)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(oversizedBody))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("relay.max_response_body_bytes", "64"); err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "response-limit",
		"remain_quota": 10,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "response-limit",
		"models":   "gpt-response-limit",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	var channelPayload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(channelResp.Body.Bytes(), &channelPayload); err != nil {
		t.Fatal(err)
	}
	if channelResp.Code != http.StatusOK || channelPayload.Data.ID == 0 {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	resp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-response-limit",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if resp.Code != http.StatusBadGateway || !strings.Contains(resp.Body.String(), `"code":"upstream_response_too_large"`) {
		t.Fatalf("oversized upstream response should return upstream_response_too_large, got %d %s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "oversized-upstream-body") {
		t.Fatalf("oversized upstream response should not be reflected to client: %s", resp.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("response limit should be detected after one upstream call, got %d", upstreamCalls)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 10 {
		t.Fatalf("oversized upstream response should not deduct token budget, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("oversized upstream response should not deduct user quota, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusFailed ||
		callLog.QuotaUsed != 0 ||
		callLog.ErrorCode != "upstream_response_too_large" ||
		callLog.ErrorSource != common.LogErrorSourceUpstream {
		t.Fatalf("unexpected oversized response failure log: %+v", callLog)
	}
	var channel model.Channel
	if err := internal.DB.First(&channel, channelPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.ErrorCount != 1 {
		t.Fatalf("oversized upstream response should mark channel failure, got %d", channel.ErrorCount)
	}
}

func TestChatCompletionUpstreamErrorStatusMapping(t *testing.T) {
	cases := []struct {
		name           string
		upstreamStatus int
		wantStatus     int
		wantCode       string
	}{
		{name: "unauthorized", upstreamStatus: http.StatusUnauthorized, wantStatus: http.StatusBadGateway, wantCode: "upstream_401"},
		{name: "forbidden", upstreamStatus: http.StatusForbidden, wantStatus: http.StatusBadGateway, wantCode: "upstream_403"},
		{name: "rate limited", upstreamStatus: http.StatusTooManyRequests, wantStatus: http.StatusTooManyRequests, wantCode: "upstream_429"},
		{name: "server error", upstreamStatus: http.StatusInternalServerError, wantStatus: http.StatusBadGateway, wantCode: "upstream_500"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("JWT_SECRET", "test-jwt-secret")
			t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

			upstreamCalls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				upstreamCalls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.upstreamStatus)
				_, _ = w.Write([]byte(`{"error":{"message":"upstream failed","type":"upstream_error","code":"provider_error","secret":"upstream-secret"}}`))
			}))
			defer upstream.Close()

			r := newTestRouter(t)
			initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
				"username": "root",
				"password": "password123",
			})
			if initResp.Code != http.StatusOK {
				t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
			}
			rootJWT := loginBearer(t, r, "root", "password123")
			if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
				t.Fatal(err)
			}
			tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
				"name":         "upstream-" + tt.name,
				"remain_quota": 50,
			})
			var tokenPayload struct {
				Data struct {
					ID  uint   `json:"id"`
					Key string `json:"key"`
				} `json:"data"`
			}
			if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
				t.Fatal(err)
			}
			if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
				t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
			}
			channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
				"type":     common.ChannelTypeOpenAICompat,
				"name":     "upstream-" + tt.name,
				"models":   "gpt-test",
				"base_url": upstream.URL,
				"api_key":  "upstream-secret",
			})
			var channelPayload struct {
				Data struct {
					ID uint `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(channelResp.Body.Bytes(), &channelPayload); err != nil {
				t.Fatal(err)
			}
			if channelResp.Code != http.StatusOK || channelPayload.Data.ID == 0 {
				t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
			}

			chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
				"model": "gpt-test",
				"messages": []map[string]string{
					{"role": "user", "content": "hello"},
				},
			})
			if chatResp.Code != tt.wantStatus || !strings.Contains(chatResp.Body.String(), `"code":"`+tt.wantCode+`"`) {
				t.Fatalf("upstream %d should map to %d/%s, got %d %s", tt.upstreamStatus, tt.wantStatus, tt.wantCode, chatResp.Code, chatResp.Body.String())
			}
			if strings.Contains(chatResp.Body.String(), "upstream-secret") {
				t.Fatalf("upstream error leaked secret: %s", chatResp.Body.String())
			}
			if upstreamCalls != 1 {
				t.Fatalf("expected one upstream request, got %d", upstreamCalls)
			}
			var callLog model.Log
			if err := internal.DB.First(&callLog).Error; err != nil {
				t.Fatal(err)
			}
			if callLog.Status != common.LogStatusFailed || callLog.QuotaUsed != 0 || !strings.Contains(callLog.ErrorMsg, strconv.Itoa(tt.upstreamStatus)) || strings.Contains(callLog.ErrorMsg, "upstream-secret") {
				t.Fatalf("unexpected failed call log: %+v", callLog)
			}
			var storedToken model.Token
			if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
				t.Fatal(err)
			}
			if storedToken.RemainQuota != 50 {
				t.Fatalf("upstream failures should not deduct token budget, got %d", storedToken.RemainQuota)
			}
			var root model.User
			if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
				t.Fatal(err)
			}
			if root.Quota != 100 {
				t.Fatalf("upstream failures should not deduct user quota, got %d", root.Quota)
			}
			var channel model.Channel
			if err := internal.DB.First(&channel, channelPayload.Data.ID).Error; err != nil {
				t.Fatal(err)
			}
			if channel.ErrorCount != 1 {
				t.Fatalf("upstream failure should increment channel error_count, got %d", channel.ErrorCount)
			}
		})
	}
}

func TestChatCompletionUpstreamTimeoutMapping(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		time.Sleep(1500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"late","object":"chat.completion","choices":[],"usage":{"total_tokens":1}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	if err := service.NewSettingService().Set("relay.timeout", "1"); err != nil {
		t.Fatal(err)
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "timeout",
		"remain_quota": 50,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "timeout",
		"models":   "gpt-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	var channelPayload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(channelResp.Body.Bytes(), &channelPayload); err != nil {
		t.Fatal(err)
	}
	if channelResp.Code != http.StatusOK || channelPayload.Data.ID == 0 {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	start := time.Now()
	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-test",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout test took too long: %s", elapsed)
	}
	if chatResp.Code != http.StatusGatewayTimeout || !strings.Contains(chatResp.Body.String(), `"code":"upstream_timeout"`) {
		t.Fatalf("upstream timeout should map to 504/upstream_timeout, got %d %s", chatResp.Code, chatResp.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("expected one upstream request, got %d", upstreamCalls)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusFailed || callLog.QuotaUsed != 0 || !strings.Contains(callLog.ErrorMsg, "timeout") {
		t.Fatalf("unexpected timeout log: %+v", callLog)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 50 {
		t.Fatalf("timeout should not deduct token budget, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 100 {
		t.Fatalf("timeout should not deduct user quota, got %d", root.Quota)
	}
	var channel model.Channel
	if err := internal.DB.First(&channel, channelPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.ErrorCount != 1 {
		t.Fatalf("timeout should increment channel error_count, got %d", channel.ErrorCount)
	}
}

func newTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	common.SetRequestIDHeaderName(common.DefaultRequestIDHeader)
	db, err := gorm.Open(sqlite.Open("file:routerx_test_"+time.Now().Format("150405.000000000")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.Group{},
		&model.User{},
		&model.UserIdentity{},
		&model.Token{},
		&model.Channel{},
		&model.Log{},
		&model.LogReplicationOutbox{},
		&model.RedemCode{},
		&model.QuotaTransaction{},
		&model.ModelPrice{},
		&model.ChannelModelPrice{},
		&model.PaymentProduct{},
		&model.PaymentOrder{},
		&model.PaymentEvent{},
		&model.PaymentRefundRequest{},
		&model.PaymentDispute{},
		&model.AdminAuditLog{},
		&model.Setting{},
	); err != nil {
		t.Fatal(err)
	}
	internal.DB = db
	internal.RDB = nil

	adminSvc := service.NewAdminService()
	settingSvc := service.NewSettingService()
	userSvc := service.NewUserService()
	authSvc := service.NewAuthService()
	channelSvc := service.NewChannelService()
	tokenSvc := service.NewTokenService()
	logSvc := service.NewLogService()
	setupSvc := service.NewSetupService(userSvc, settingSvc)
	relaySvc := service.NewRelayService(channelSvc, tokenSvc, logSvc, settingSvc)

	return SetupRouter(
		handler.NewAuthHandler(authSvc),
		handler.NewUserHandler(userSvc),
		handler.NewTokenHandler(tokenSvc),
		handler.NewAdminHandler(adminSvc),
		handler.NewChannelHandler(channelSvc),
		handler.NewRelayHandler(relaySvc),
		handler.NewLogHandler(logSvc),
		handler.NewSettingHandler(settingSvc),
		handler.NewSetupHandler(setupSvc),
	)
}

func newRouterLogDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:routerx_log_"+name+"_"+time.Now().Format("150405.000000000")+"?mode=memory&cache=shared"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Log{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func performJSON(r http.Handler, method, path, bearer string, body interface{}) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func performRaw(r http.Handler, method, path, bearer, body string) *httptest.ResponseRecorder {
	return performRawWithHeaders(r, method, path, bearer, body, nil)
}

func performRawWithHeaders(r http.Handler, method, path, bearer, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func performForm(r http.Handler, method, path string, values url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func performStripeWebhook(r http.Handler, body, secret string) *httptest.ResponseRecorder {
	return performRawWithHeaders(r, http.MethodPost, "/v0/payment/stripe/webhook", "", body, map[string]string{
		"Stripe-Signature": stripeSignature(body, secret),
	})
}

func stripeSignature(body, secret string) string {
	timestamp := time.Now().Unix()
	payload := fmt.Sprintf("%d.%s", timestamp, body)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return fmt.Sprintf("t=%d,v1=%x", timestamp, mac.Sum(nil))
}

func stripeCheckoutCompletedPayload(eventID string, order *model.PaymentOrder, userID uint, amountTotal int64, paymentIntent string) string {
	providerOrderID := ""
	if order.ProviderOrderID != nil {
		providerOrderID = *order.ProviderOrderID
	}
	raw, _ := json.Marshal(map[string]interface{}{
		"id":   eventID,
		"type": "checkout.session.completed",
		"data": map[string]interface{}{
			"object": map[string]interface{}{
				"id":             providerOrderID,
				"amount_total":   amountTotal,
				"currency":       order.Currency,
				"payment_status": "paid",
				"payment_intent": paymentIntent,
				"metadata": map[string]string{
					"order_no":   order.OrderNo,
					"product_id": order.ProductID,
					"user_id":    strconv.FormatUint(uint64(userID), 10),
				},
			},
		},
	})
	return string(raw)
}

func stripeChargeRefundedPayload(eventID string, order model.PaymentOrder, paymentIntent string, amountRefunded int64) string {
	raw, _ := json.Marshal(map[string]interface{}{
		"id":   eventID,
		"type": "charge.refunded",
		"data": map[string]interface{}{
			"object": map[string]interface{}{
				"id":              "ch_" + paymentIntent,
				"amount_refunded": amountRefunded,
				"amount":          amountRefunded,
				"currency":        order.Currency,
				"payment_intent":  paymentIntent,
				"metadata": map[string]string{
					"order_no": order.OrderNo,
				},
			},
		},
	})
	return string(raw)
}

func stripeChargeDisputeCreatedPayload(eventID string, order model.PaymentOrder, paymentIntent string, amount int64) string {
	return stripeChargeDisputePayload(eventID, "charge.dispute.created", order, paymentIntent, "dp_"+paymentIntent, "needs_response", amount)
}

func stripeChargeDisputePayload(eventID, eventType string, order model.PaymentOrder, paymentIntent, disputeID, status string, amount int64) string {
	raw, _ := json.Marshal(map[string]interface{}{
		"id":   eventID,
		"type": eventType,
		"data": map[string]interface{}{
			"object": map[string]interface{}{
				"id":             disputeID,
				"amount":         amount,
				"currency":       order.Currency,
				"payment_intent": paymentIntent,
				"status":         status,
				"reason":         "fraudulent",
				"metadata": map[string]string{
					"order_no": order.OrderNo,
				},
			},
		},
	})
	return string(raw)
}

func createStripePaidOrder(t *testing.T, r http.Handler, bearer, productID, eventID, paymentIntent string) model.PaymentOrder {
	t.Helper()
	createResp := performJSON(r, http.MethodPost, "/v0/user/payment/orders", bearer, map[string]interface{}{
		"provider":   "stripe",
		"product_id": productID,
	})
	if createResp.Code != http.StatusOK {
		t.Fatalf("create stripe order failed: %d %s", createResp.Code, createResp.Body.String())
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	var order model.PaymentOrder
	if err := internal.DB.Where("user_id = ? AND provider = ? AND status = ?", root.ID, common.PaymentProviderStripe, common.PaymentOrderStatusPending).Order("id DESC").First(&order).Error; err != nil {
		t.Fatal(err)
	}
	successBody := stripeCheckoutCompletedPayload(eventID, &order, root.ID, 999, paymentIntent)
	payResp := performStripeWebhook(r, successBody, "whsec_test_secret")
	if payResp.Code != http.StatusOK || strings.TrimSpace(payResp.Body.String()) != "success" {
		t.Fatalf("pay stripe order failed: %d %s", payResp.Code, payResp.Body.String())
	}
	if err := internal.DB.First(&order, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	return order
}

func loginBearer(t *testing.T, r http.Handler, account, password string) string {
	t.Helper()
	resp := performJSON(r, http.MethodPost, "/v0/user/login", "", map[string]interface{}{
		"account":  account,
		"password": password,
	})
	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if resp.Code != http.StatusOK || !payload.Success || payload.Data.Token == "" {
		t.Fatalf("login failed for %s: %d %s", account, resp.Code, resp.Body.String())
	}
	return "Bearer " + payload.Data.Token
}

func uintString(id uint) string {
	return strconv.FormatUint(uint64(id), 10)
}

func epayNotifyValues(orderNo, tradeNo, money, key string) url.Values {
	values := url.Values{
		"pid":          {"merchant-1"},
		"type":         {"alipay"},
		"out_trade_no": {orderNo},
		"trade_no":     {tradeNo},
		"money":        {money},
		"trade_status": {"TRADE_SUCCESS"},
		"name":         {"RouterX quota"},
		"sign_type":    {"MD5"},
	}
	values.Set("sign", epaySign(values, key))
	return values
}

func epaySign(values url.Values, key string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		if k == "sign" || k == "sign_type" || values.Get(k) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+values.Get(k))
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + key))
	return fmt.Sprintf("%x", sum)
}
