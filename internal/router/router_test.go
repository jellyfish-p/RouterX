package router

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
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
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "admin").Update("status", common.UserStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	disabledUserModels := performJSON(r, http.MethodGet, "/v1/models", "Bearer "+tokenPayload.Data.Key, nil)
	if disabledUserModels.Code != http.StatusForbidden || strings.Contains(disabledUserModels.Body.String(), `"success"`) {
		t.Fatalf("expected disabled user api key 403 with OpenAI error, got %d %s", disabledUserModels.Code, disabledUserModels.Body.String())
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
		"jwt.secret",
		"relay.timeout",
		"relay.retry_count",
		"relay.log_body_max_bytes",
		"log.body_max_bytes",
		"log.request_body_enabled",
		"log.response_body_enabled",
		"observability.metrics_enabled",
		"observability.audit_enabled",
		"observability.request_id_header",
		"ready.production_strict",
		"billing.bootstrap_admin_quota",
		"billing.default_user_channel_group_access",
		"billing.user_group_channel_group_access",
		"payment.stripe.enabled",
		"payment.epay.enabled",
		"payment.epay.gateway",
		"payment.epay.pid",
		"payment.epay.notify_url",
		"payment.epay.return_url",
		"payment.currency",
		"payment.order_expire_minutes",
		"payment.refund.auto_deduct",
		"payment.refund.allow_negative_balance",
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
	token, err := service.NewTokenService().Create(root.ID, "metrics-key", 100, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	tokenID := token.ID
	now := time.Now()
	logs := []model.Log{
		{UserID: root.ID, TokenID: &tokenID, ChannelID: &channel.ID, Model: "gpt-test", Status: common.LogStatusSuccess, QuotaUsed: 7, TotalTokens: 7, CreatedAt: now.Add(-time.Minute)},
		{UserID: root.ID, TokenID: &tokenID, ChannelID: &channel.ID, Model: "gpt-test", Status: common.LogStatusFailed, ErrorMsg: "upstream 500", CreatedAt: now},
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

	resp := performJSON(r, http.MethodGet, "/metrics", "", nil)
	body := resp.Body.String()
	if resp.Code != http.StatusOK ||
		!strings.Contains(body, "routerx_db_up 1") ||
		!strings.Contains(body, "routerx_redis_up 0") ||
		!strings.Contains(body, `routerx_logs_total{status="success"} 1`) ||
		!strings.Contains(body, `routerx_logs_total{status="failed"} 1`) ||
		!strings.Contains(body, "routerx_quota_used_total 7") ||
		!strings.Contains(body, "routerx_channel_error_count 3") ||
		!strings.Contains(body, `routerx_payment_orders_total{provider="stripe",status="paid"} 1`) ||
		!strings.Contains(body, `routerx_payment_orders_total{provider="epay",status="pending"} 1`) ||
		!strings.Contains(body, `routerx_payment_events_total{provider="stripe",event_type="checkout.session.completed",processed="true"} 1`) ||
		!strings.Contains(body, `routerx_payment_events_total{provider="epay",event_type="notify",processed="false"} 1`) {
		t.Fatalf("metrics should include relay/payment/infrastructure signals, got %d %s", resp.Code, body)
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
	badEpayGateway := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
		"payment.epay.gateway": "pay.example.com/submit.php",
	})
	if badEpayGateway.Code != http.StatusBadRequest {
		t.Fatalf("invalid payment.epay.gateway should be rejected, got %d %s", badEpayGateway.Code, badEpayGateway.Body.String())
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

	_, exhaustedKey := createToken("exhausted", 0)
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
	var failedLogs int64
	if err := internal.DB.Model(&model.Log{}).Where("status = ?", common.LogStatusFailed).Count(&failedLogs).Error; err != nil {
		t.Fatal(err)
	}
	if failedLogs != 1 {
		t.Fatalf("relay precheck should write one failed log for the routed no-channel rejection, got %d", failedLogs)
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
	if paidCalls != 1 || freeCalls != 1 {
		t.Fatalf("invalid or empty route results must not call upstream, paid=%d free=%d", paidCalls, freeCalls)
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

func TestChatCompletionSuccessLogsAndDeductsQuota(t *testing.T) {
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

	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-test",
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
	if strings.Contains(chatResp.Body.String(), "upstream-secret") || strings.Contains(chatResp.Body.String(), tokenPayload.Data.Key) {
		t.Fatalf("chat response leaked secret: %s", chatResp.Body.String())
	}
	if upstreamCalls != 1 || upstreamPath != "/v1/chat/completions" {
		t.Fatalf("expected one upstream chat request, got calls=%d path=%q", upstreamCalls, upstreamPath)
	}
	if upstreamAuth != "Bearer upstream-secret" {
		t.Fatalf("upstream authorization should use channel secret, got %q", upstreamAuth)
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

	billingResp := performJSON(r, http.MethodGet, "/v0/user/billing", rootJWT, nil)
	if billingResp.Code != http.StatusOK || !strings.Contains(billingResp.Body.String(), `"call_count":1`) || !strings.Contains(billingResp.Body.String(), `"total_quota":5`) || !strings.Contains(billingResp.Body.String(), `"total_tokens":5`) {
		t.Fatalf("billing should aggregate successful logs, got %d %s", billingResp.Code, billingResp.Body.String())
	}
	logResp := performJSON(r, http.MethodGet, "/v0/user/log", rootJWT, nil)
	if logResp.Code != http.StatusOK || !strings.Contains(logResp.Body.String(), `"usage_source":"upstream"`) {
		t.Fatalf("user log should expose upstream usage source, got %d %s", logResp.Code, logResp.Body.String())
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
	logResp := performJSON(r, http.MethodGet, "/v0/user/log", rootJWT, nil)
	logBody := logResp.Body.String()
	if logResp.Code != http.StatusOK || !strings.Contains(logBody, `"request_id":"`+requestID+`"`) || !strings.Contains(logBody, `"error_code":"upstream_400"`) {
		t.Fatalf("user log API should expose request_id and error_code, got %d %s", logResp.Code, logBody)
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
		&model.RedemCode{},
		&model.QuotaTransaction{},
		&model.PaymentProduct{},
		&model.PaymentOrder{},
		&model.PaymentEvent{},
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
