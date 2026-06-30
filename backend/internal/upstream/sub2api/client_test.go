package sub2api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xi_monitor/backend/internal/store"
	"xi_monitor/backend/internal/upstream"
)

func TestFetchFallsBackToUserProfileBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/admin/accounts", "/api/admin/accounts":
			http.Error(w, "forbidden", http.StatusForbidden)
		case "/api/v1/user/profile":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":    0,
				"message": "success",
				"data": map[string]any{
					"id":             77,
					"email":          "user@example.com",
					"role":           "user",
					"balance":        20.82170124,
					"allowed_groups": []any{"default"},
				},
			})
		case "/v1/chat/completions":
			if got := r.Header.Get("Authorization"); got != "Bearer call-key" {
				t.Fatalf("Authorization = %q", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["model"] != "claude-test" || body["stream"] != true {
				t.Fatalf("unexpected call test body: %#v", body)
			}
			messages := body["messages"].([]any)
			first := messages[0].(map[string]any)
			if first["content"] != "hi" {
				t.Fatalf("unexpected prompt: %#v", body)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	secret, err := upstream.EncodeBearerCredentials("jwt-token", server.URL, "call-key")
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(server.Client()).Fetch(context.Background(), store.Upstream{
		ID:       11,
		Kind:     store.KindSub2API,
		BaseURL:  server.URL,
		AuthType: store.AuthBearer,
		Groups: []store.UpstreamGroup{
			{Name: "default", ManualRatio: floatPtr(0.25), TestModel: "claude-test", Enabled: true},
		},
	}, secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items len = %d", len(result.Items))
	}
	item := result.Items[0]
	if item.ItemType != "account" || item.ExternalID != "user:77" {
		t.Fatalf("unexpected item: %#v", item)
	}
	if item.Balance == nil || *item.Balance != 20.82170124 {
		t.Fatalf("balance = %#v", item.Balance)
	}
	if item.Ratio == nil || *item.Ratio != 0.25 {
		t.Fatalf("ratio = %#v", item.Ratio)
	}
	if item.GroupName != "default" {
		t.Fatalf("group = %s", item.GroupName)
	}
	if item.LastMessage != "user balance fetched via /api/v1/user/profile; call key verified via chat_completions" {
		t.Fatalf("message = %q", item.LastMessage)
	}
}

func TestFetchSub2APIRefreshTokenRefreshesAndSavesToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["refresh_token"] != "old-refresh-token" {
				t.Fatalf("refresh token = %#v", body["refresh_token"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"access_token":  "new-access-token",
					"refresh_token": "new-refresh-token",
					"expires_in":    3600,
				},
			})
		case "/api/v1/auth/me":
			if got := r.Header.Get("Authorization"); got != "Bearer new-access-token" {
				t.Fatalf("Authorization = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"id":             77,
					"email":          "user@example.com",
					"balance":        12.5,
					"allowed_groups": []any{"default"},
				},
			})
		case "/v1/chat/completions":
			if got := r.Header.Get("Authorization"); got != "Bearer call-key" {
				t.Fatalf("Authorization = %q", got)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	expiresAt := time.Now().Add(-time.Hour)
	secret, err := upstream.EncodeBalanceCredentials(upstream.BalanceCredentials{
		BalanceAuthType:       upstream.BalanceAuthSub2APIRefreshToken,
		BalanceAccessToken:    "old-access-token",
		BalanceRefreshToken:   "old-refresh-token",
		BalanceTokenExpiresAt: &expiresAt,
		CallURL:               server.URL,
		CallKey:               "call-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(server.Client()).Fetch(context.Background(), store.Upstream{
		ID:       11,
		Kind:     store.KindSub2API,
		BaseURL:  server.URL,
		AuthType: store.AuthSub2Refresh,
		Groups: []store.UpstreamGroup{
			{Name: "default", TestModel: "claude-test", Enabled: true},
		},
	}, secret)
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdatedSecret == "" {
		t.Fatal("expected updated secret")
	}
	updated, err := upstream.DecodeBalanceCredentials(result.UpdatedSecret)
	if err != nil {
		t.Fatal(err)
	}
	if updated.BalanceAccessToken != "new-access-token" || updated.BalanceRefreshToken != "new-refresh-token" || updated.BalanceTokenExpiresAt == nil {
		t.Fatalf("updated credentials = %#v", updated)
	}
	if strings.Contains(result.UpdatedMasked, "new-access-token") || strings.Contains(result.UpdatedMasked, "new-refresh-token") {
		t.Fatalf("updated mask leaked secret: %q", result.UpdatedMasked)
	}
	item := result.Items[0]
	if item.Balance == nil || *item.Balance != 12.5 {
		t.Fatalf("balance = %#v", item.Balance)
	}
}

func TestFetchSub2APIRefreshTokenUsesValidCachedAccessToken(t *testing.T) {
	refreshCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			refreshCalled = true
			http.Error(w, "refresh should not be called", http.StatusTeapot)
		case "/api/v1/auth/me":
			if got := r.Header.Get("Authorization"); got != "Bearer cached-access-token" {
				t.Fatalf("Authorization = %q", got)
			}
			if got := r.Header.Get("Cookie"); got != "cf_clearance=ok; session=abc" {
				t.Fatalf("Cookie = %q", got)
			}
			if got := r.Header.Get("User-Agent"); got != "Mozilla/5.0 Test" {
				t.Fatalf("User-Agent = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"id":             77,
					"email":          "user@example.com",
					"balance":        15.25,
					"allowed_groups": []any{"default"},
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	expiresAt := time.Now().Add(time.Hour)
	secret, err := upstream.EncodeBalanceCredentials(upstream.BalanceCredentials{
		BalanceAuthType:       upstream.BalanceAuthSub2APIRefreshToken,
		BalanceAccessToken:    "cached-access-token",
		BalanceRefreshToken:   "refresh-token",
		BalanceTokenExpiresAt: &expiresAt,
		BalanceCookie:         "cf_clearance=ok; session=abc",
		BalanceUserAgent:      "Mozilla/5.0 Test",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(server.Client()).Fetch(context.Background(), store.Upstream{
		ID:       11,
		Kind:     store.KindSub2API,
		BaseURL:  server.URL,
		AuthType: store.AuthSub2Refresh,
	}, secret)
	if err != nil {
		t.Fatal(err)
	}
	if refreshCalled {
		t.Fatal("refresh endpoint was called")
	}
	if result.UpdatedSecret != "" {
		t.Fatalf("UpdatedSecret = %q", result.UpdatedSecret)
	}
	item := result.Items[0]
	if item.Balance == nil || *item.Balance != 15.25 {
		t.Fatalf("balance = %#v", item.Balance)
	}
}

func TestFetchSub2APIAllowsCachedAccessTokenWithoutRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			if got := r.Header.Get("Authorization"); got != "Bearer cached-access-token" {
				t.Fatalf("Authorization = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"id":      77,
					"balance": 15.25,
				},
			})
		case "/api/v1/auth/refresh":
			t.Fatal("refresh endpoint should not be called")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	secret, err := upstream.EncodeBalanceCredentials(upstream.BalanceCredentials{
		BalanceAuthType:    upstream.BalanceAuthSub2APIRefreshToken,
		BalanceAccessToken: "cached-access-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(server.Client()).Fetch(context.Background(), store.Upstream{
		ID:       11,
		Kind:     store.KindSub2API,
		BaseURL:  server.URL,
		AuthType: store.AuthSub2Refresh,
	}, secret)
	if err != nil {
		t.Fatal(err)
	}
	item := result.Items[0]
	if item.Balance == nil || *item.Balance != 15.25 {
		t.Fatalf("balance = %#v", item.Balance)
	}
}

func TestFetchSub2APIRefreshTokenExpiredReturnsCredentialInvalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":    401,
				"message": "refresh token old-refresh-token expired",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	secret, err := upstream.EncodeBalanceCredentials(upstream.BalanceCredentials{
		BalanceAuthType:     upstream.BalanceAuthSub2APIRefreshToken,
		BalanceRefreshToken: "old-refresh-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(server.Client()).Fetch(context.Background(), store.Upstream{
		ID:       11,
		Kind:     store.KindSub2API,
		BaseURL:  server.URL,
		AuthType: store.AuthSub2Refresh,
	}, secret)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "balance credential invalid") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "old-refresh-token") {
		t.Fatalf("error leaked refresh token: %v", err)
	}
}

func TestFetchSub2APIFallsBackToProfileWhenAuthMeHasNoBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "new-access-token",
				"refresh_token": "new-refresh-token",
				"expires_in":    3600,
			})
		case "/api/v1/auth/me":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"id": 77, "email": "user@example.com"},
			})
		case "/api/v1/user/profile":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"id": 77, "email": "user@example.com", "credit": 9.5},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	secret, err := upstream.EncodeBalanceCredentials(upstream.BalanceCredentials{
		BalanceAuthType:     upstream.BalanceAuthSub2APIRefreshToken,
		BalanceRefreshToken: "old-refresh-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(server.Client()).Fetch(context.Background(), store.Upstream{
		ID:       11,
		Kind:     store.KindSub2API,
		BaseURL:  server.URL,
		AuthType: store.AuthSub2Refresh,
	}, secret)
	if err != nil {
		t.Fatal(err)
	}
	item := result.Items[0]
	if item.Balance == nil || *item.Balance != 9.5 {
		t.Fatalf("balance = %#v", item.Balance)
	}
	if !strings.Contains(item.LastMessage, "/api/v1/user/profile") {
		t.Fatalf("message = %q", item.LastMessage)
	}
}

func TestFetchUsesCallTestWhenAccountAPIsFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/admin/accounts", "/api/admin/accounts", "/api/v1/user/profile", "/api/v1/auth/me":
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case "/v1/chat/completions":
			if got := r.Header.Get("Authorization"); got != "Bearer call-key" {
				t.Fatalf("Authorization = %q", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["model"] != "claude-test" || body["stream"] != true {
				t.Fatalf("unexpected call test body: %#v", body)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	secret, err := upstream.EncodeAPIKeyCredentialsWithCall("bad-admin-key", server.URL, "call-key")
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(server.Client()).Fetch(context.Background(), store.Upstream{
		ID:       11,
		Name:     "call-only sub2api",
		Kind:     store.KindSub2API,
		BaseURL:  server.URL,
		AuthType: store.AuthXAPIKey,
		Groups: []store.UpstreamGroup{
			{Name: "default", TestModel: "claude-test", Enabled: true},
		},
	}, secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items len = %d", len(result.Items))
	}
	item := result.Items[0]
	if item.Status != "available" {
		t.Fatalf("status = %s message=%s", item.Status, item.LastMessage)
	}
	if !strings.Contains(item.LastMessage, "account balance unavailable") || !strings.Contains(item.LastMessage, "call key verified via chat_completions") {
		t.Fatalf("message = %q", item.LastMessage)
	}
}
