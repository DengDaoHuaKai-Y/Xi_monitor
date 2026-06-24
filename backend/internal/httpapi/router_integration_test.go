package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xi_monitor/backend/internal/auth"
	"xi_monitor/backend/internal/config"
	"xi_monitor/backend/internal/crypto"
	"xi_monitor/backend/internal/httpapi"
	"xi_monitor/backend/internal/poller"
	"xi_monitor/backend/internal/store"
)

type fakePoller struct{}

func (fakePoller) RefreshAll(context.Context) poller.RefreshStatus {
	return poller.RefreshStatus{Running: false, Message: "ok"}
}

func (fakePoller) RefreshUpstream(context.Context, int64) error {
	return nil
}

func (fakePoller) RefreshItem(context.Context, store.MonitorItem) error {
	return nil
}

func TestHTTPRoutesWithPostgres(t *testing.T) {
	cfg, err := config.Load("../../config.yaml")
	if err != nil {
		t.Skip("create backend/config.yaml to run HTTP integration test")
	}

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Database.DSN)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	passwordHash, err := auth.HashPassword("test-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	authSvc := auth.NewService("admin", passwordHash, "test-session-secret")
	box, err := crypto.NewSecretBox("test-32-byte-encryption-key-0000")
	if err != nil {
		t.Fatalf("new secretbox: %v", err)
	}
	router := httpapi.NewRouter(authSvc, box, st, fakePoller{})

	token := loginAndToken(t, router)

	createBody := map[string]any{
		"name":         "__http_integration_test__",
		"kind":         "new_api",
		"url":          "https://example.invalid",
		"access_token": "sk-http-integration-plaintext",
		"user_id":      "1",
		"enabled":      true,
	}
	createResp := requestJSON(t, router, http.MethodPost, "/api/upstreams", token, createBody)
	id := int64(createResp["data"].(map[string]any)["id"].(float64))
	t.Cleanup(func() {
		_ = st.DeleteUpstream(context.Background(), id)
	})

	got, err := st.GetUpstream(ctx, id)
	if err != nil {
		t.Fatalf("get created upstream: %v", err)
	}
	if got.AuthType != store.AuthNewAPIAccess {
		t.Fatalf("auth type = %s", got.AuthType)
	}
	if len(got.Groups) != 1 || got.Groups[0].Name != "default" {
		t.Fatalf("default group not created: %#v", got.Groups)
	}
	if strings.Contains(got.AuthSecretCiphertext, "sk-http-integration-plaintext") {
		t.Fatal("stored ciphertext contains plaintext")
	}
	if got.AuthSecretMasked == "sk-http-integration-plaintext" {
		t.Fatal("stored masked secret is plaintext")
	}

	listResp := requestJSON(t, router, http.MethodGet, "/api/upstreams", token, nil)
	listBytes, _ := json.Marshal(listResp)
	if bytes.Contains(listBytes, []byte("sk-http-integration-plaintext")) {
		t.Fatal("GET /api/upstreams leaked plaintext secret")
	}

	requestJSON(t, router, http.MethodGet, "/api/dashboard", token, nil)
	requestJSON(t, router, http.MethodPost, "/api/dashboard/refresh", token, nil)
	requestJSON(t, router, http.MethodDelete, "/api/upstreams/"+jsonNumber(id), token, nil)
}

func TestHTTPCreateSub2APIWithAPIKey(t *testing.T) {
	cfg, err := config.Load("../../config.yaml")
	if err != nil {
		t.Skip("create backend/config.yaml to run HTTP integration test")
	}

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Database.DSN)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	passwordHash, err := auth.HashPassword("test-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	authSvc := auth.NewService("admin", passwordHash, "test-session-secret")
	box, err := crypto.NewSecretBox("test-32-byte-encryption-key-0000")
	if err != nil {
		t.Fatalf("new secretbox: %v", err)
	}
	router := httpapi.NewRouter(authSvc, box, st, fakePoller{})
	token := loginAndToken(t, router)

	createResp := requestJSON(t, router, http.MethodPost, "/api/upstreams", token, map[string]any{
		"name":    "__http_sub2api_test__",
		"kind":    "sub2api",
		"url":     "https://sub2api.example.invalid",
		"api_key": "sub2api-admin-key",
	})
	id := int64(createResp["data"].(map[string]any)["id"].(float64))
	t.Cleanup(func() {
		_ = st.DeleteUpstream(context.Background(), id)
	})
	got, err := st.GetUpstream(ctx, id)
	if err != nil {
		t.Fatalf("get created upstream: %v", err)
	}
	if got.AuthType != store.AuthXAPIKey {
		t.Fatalf("auth type = %s", got.AuthType)
	}
	if strings.Contains(got.AuthSecretCiphertext, "sub2api-admin-key") {
		t.Fatal("stored ciphertext contains plaintext")
	}
}

func TestHTTPCreateWithUsernamePasswordDefaultsToPassword(t *testing.T) {
	cfg, err := config.Load("../../config.yaml")
	if err != nil {
		t.Skip("create backend/config.yaml to run HTTP integration test")
	}

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Database.DSN)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	passwordHash, err := auth.HashPassword("test-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	authSvc := auth.NewService("admin", passwordHash, "test-session-secret")
	box, err := crypto.NewSecretBox("test-32-byte-encryption-key-0000")
	if err != nil {
		t.Fatalf("new secretbox: %v", err)
	}
	router := httpapi.NewRouter(authSvc, box, st, fakePoller{})
	token := loginAndToken(t, router)

	createResp := requestJSON(t, router, http.MethodPost, "/api/upstreams", token, map[string]any{
		"name":          "__http_password_test__",
		"kind":          "new_api",
		"url":           "https://example.invalid",
		"auth_username": "admin",
		"auth_password": "secret-password",
		"call_url":      "https://call.example.invalid",
		"call_key":      "sk-call-plaintext",
	})
	id := int64(createResp["data"].(map[string]any)["id"].(float64))
	t.Cleanup(func() {
		_ = st.DeleteUpstream(context.Background(), id)
	})
	got, err := st.GetUpstream(ctx, id)
	if err != nil {
		t.Fatalf("get created upstream: %v", err)
	}
	if got.AuthType != store.AuthPassword {
		t.Fatalf("auth type = %s", got.AuthType)
	}
	if strings.Contains(got.AuthSecretCiphertext, "secret-password") {
		t.Fatal("stored ciphertext contains plaintext password")
	}
	if strings.Contains(got.AuthSecretCiphertext, "sk-call-plaintext") {
		t.Fatal("stored ciphertext contains plaintext call key")
	}
	if !strings.Contains(got.AuthSecretMasked, "call:") {
		t.Fatalf("masked secret should mention call key presence: %q", got.AuthSecretMasked)
	}

	requestJSON(t, router, http.MethodPut, "/api/upstreams/"+jsonNumber(id)+"/groups/default/ratio", token, map[string]any{
		"ratio": 0.25,
	})
	got, err = st.GetUpstream(ctx, id)
	if err != nil {
		t.Fatalf("get upstream after ratio update: %v", err)
	}
	if len(got.Groups) == 0 || got.Groups[0].ManualRatio == nil || *got.Groups[0].ManualRatio != 0.25 {
		t.Fatalf("manual ratio not saved: %#v", got.Groups)
	}
}

func loginAndToken(t *testing.T, router http.Handler) string {
	t.Helper()
	resp := requestJSON(t, router, http.MethodPost, "/api/auth/login", "", map[string]any{
		"username": "admin",
		"password": "test-password",
	})
	data := resp["data"].(map[string]any)
	token, ok := data["token"].(string)
	if !ok || token == "" {
		t.Fatal("login response did not include token")
	}
	return token
}

func requestJSON(t *testing.T, router http.Handler, method, path, token string, body any) map[string]any {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("%s %s returned %d: %s", method, path, rec.Code, rec.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if success, _ := decoded["success"].(bool); !success {
		t.Fatalf("%s %s returned unsuccessful response: %s", method, path, rec.Body.String())
	}
	return decoded
}

func jsonNumber(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
