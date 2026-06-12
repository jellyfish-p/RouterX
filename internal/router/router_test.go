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
