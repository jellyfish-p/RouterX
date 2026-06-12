package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/handler"
	"routerx/internal/model"
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
		"ready.production_strict",
		"billing.bootstrap_admin_quota",
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

	if err := internal.DB.Model(&model.Setting{}).Where("key = ?", "relay.timeout").Update("value", "0").Error; err != nil {
		t.Fatal(err)
	}
	notReady := performJSON(r, http.MethodGet, "/ready", "", nil)
	if notReady.Code != http.StatusServiceUnavailable || !strings.Contains(notReady.Body.String(), "relay.timeout") {
		t.Fatalf("invalid relay.timeout should make ready fail, got %d %s", notReady.Code, notReady.Body.String())
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
		{name: "stream unsupported", body: `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}],"stream":true}`, code: "unsupported_stream"},
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
	if callLog.TokenID == nil || *callLog.TokenID != tokenPayload.Data.ID || callLog.ChannelID == nil {
		t.Fatalf("success log should reference token and channel: %+v", callLog)
	}

	billingResp := performJSON(r, http.MethodGet, "/v0/user/billing", rootJWT, nil)
	if billingResp.Code != http.StatusOK || !strings.Contains(billingResp.Body.String(), `"call_count":1`) || !strings.Contains(billingResp.Body.String(), `"total_quota":5`) || !strings.Contains(billingResp.Body.String(), `"total_tokens":5`) {
		t.Fatalf("billing should aggregate successful logs, got %d %s", billingResp.Code, billingResp.Body.String())
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
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
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
