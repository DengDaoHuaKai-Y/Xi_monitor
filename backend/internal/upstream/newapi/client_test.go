package newapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xi_monitor/backend/internal/store"
	"xi_monitor/backend/internal/upstream"
)

func TestFetchFallsBackToUserSelfBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/channel/":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": false,
				"message": "Unauthorized, insufficient privileges",
			})
		case "/api/user/self":
			if got := r.Header.Get("New-Api-User"); got != "382" {
				t.Fatalf("New-Api-User = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"id":         382,
					"username":   "demo-user",
					"group":      "default",
					"quota":      1554890,
					"used_quota": 13445110,
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
			if body["model"] != "gpt-test" || body["stream"] != true {
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

	secret, err := upstream.EncodeNewAPISessionCredentials("session=abc", "382")
	if err != nil {
		t.Fatal(err)
	}
	secret, err = upstream.AttachCallCredentials(store.AuthNewAPISession, secret, upstream.CallCredentials{URL: server.URL, Key: "call-key"})
	if err != nil {
		t.Fatal(err)
	}

	result, err := New(server.Client()).Fetch(context.Background(), store.Upstream{
		ID:       9,
		Kind:     store.KindNewAPI,
		BaseURL:  server.URL,
		AuthType: store.AuthNewAPISession,
		Groups: []store.UpstreamGroup{
			{Name: "default", ManualRatio: floatPtr(0.25), TestModel: "gpt-test", Enabled: true},
		},
	}, secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items len = %d", len(result.Items))
	}
	item := result.Items[0]
	if item.ItemType != "account" || item.ExternalID != "user:382" {
		t.Fatalf("unexpected item: %#v", item)
	}
	if item.Balance == nil || *item.Balance != 3.10978 {
		t.Fatalf("balance = %#v", item.Balance)
	}
	if item.Ratio == nil || *item.Ratio != 0.25 {
		t.Fatalf("ratio = %#v", item.Ratio)
	}
	if item.Status != "available" {
		t.Fatalf("status = %s", item.Status)
	}
	if item.LastMessage != "user balance fetched via /api/user/self; call key verified via chat_completions" {
		t.Fatalf("message = %q", item.LastMessage)
	}
}

func TestFetchNewAPIAccessTokenBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/self":
			if got := r.Header.Get("Authorization"); got != "Bearer user-access-token" {
				t.Fatalf("Authorization = %q", got)
			}
			if got := r.Header.Get("New-Api-User"); got != "382" {
				t.Fatalf("New-Api-User = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"id":         382,
					"username":   "user",
					"planName":   "default",
					"quota":      500000,
					"used_quota": 1000000,
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

	secret, err := upstream.EncodeBalanceCredentials(upstream.BalanceCredentials{
		BalanceAuthType:    upstream.BalanceAuthNewAPIAccessToken,
		BalanceUserID:      "382",
		BalanceAccessToken: "user-access-token",
		CallURL:            server.URL,
		CallKey:            "call-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(server.Client()).Fetch(context.Background(), store.Upstream{
		ID:       9,
		Kind:     store.KindNewAPI,
		BaseURL:  server.URL,
		AuthType: store.AuthNewAPIAccess,
		Groups: []store.UpstreamGroup{
			{Name: "default", TestModel: "gpt-test", Enabled: true},
		},
	}, secret)
	if err != nil {
		t.Fatal(err)
	}
	item := result.Items[0]
	if item.Balance == nil || *item.Balance != 1 {
		t.Fatalf("balance = %#v", item.Balance)
	}
	raw, _ := json.Marshal(item.RawSummary)
	if strings.Contains(string(raw), "user-access-token") || strings.Contains(string(raw), "call-key") {
		t.Fatalf("raw summary leaked secret: %s", raw)
	}
}

func TestFetchNewAPIAccessTokenInvalidOnlyMarksBalanceFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/self":
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": false,
				"message": "invalid user-access-token",
			})
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	secret, err := upstream.EncodeBalanceCredentials(upstream.BalanceCredentials{
		BalanceAuthType:    upstream.BalanceAuthNewAPIAccessToken,
		BalanceUserID:      "382",
		BalanceAccessToken: "user-access-token",
		CallURL:            server.URL,
		CallKey:            "call-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(server.Client()).Fetch(context.Background(), store.Upstream{
		ID:       9,
		Name:     "bad balance",
		Kind:     store.KindNewAPI,
		BaseURL:  server.URL,
		AuthType: store.AuthNewAPIAccess,
		Groups: []store.UpstreamGroup{
			{Name: "default", TestModel: "gpt-test", Enabled: true},
		},
	}, secret)
	if err != nil {
		t.Fatal(err)
	}
	item := result.Items[0]
	if item.Status != "available" {
		t.Fatalf("status = %s", item.Status)
	}
	if item.Balance != nil {
		t.Fatalf("balance should be nil, got %#v", item.Balance)
	}
	if !strings.Contains(item.LastMessage, "account balance unavailable") || !strings.Contains(item.LastMessage, "call key verified") {
		t.Fatalf("message = %q", item.LastMessage)
	}
	if strings.Contains(item.LastMessage, "user-access-token") || strings.Contains(item.LastMessage, "call-key") {
		t.Fatalf("message leaked secret: %q", item.LastMessage)
	}
}

func TestFetchUsesCallTestWhenAccountAPIsFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/channel/", "/api/user/self":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": false,
				"message": "Unauthorized, invalid access token",
			})
		case "/v1/chat/completions":
			if got := r.Header.Get("Authorization"); got != "Bearer call-key" {
				t.Fatalf("Authorization = %q", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["model"] != "gpt-test" || body["stream"] != true {
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

	secret, err := upstream.EncodeNewAPITokenCredentialsWithCall("bad-access-token", "382", server.URL, "call-key")
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(server.Client()).Fetch(context.Background(), store.Upstream{
		ID:       9,
		Name:     "call-only new-api",
		Kind:     store.KindNewAPI,
		BaseURL:  server.URL,
		AuthType: store.AuthNewAPIToken,
		Groups: []store.UpstreamGroup{
			{Name: "default", TestModel: "gpt-test", Enabled: true},
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
