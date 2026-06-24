package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xi_monitor/backend/internal/store"
)

func TestApplyAuthNewAPITokenAndXAPIKey(t *testing.T) {
	newAPISecret, err := EncodeNewAPITokenCredentials("access-token", "42")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ApplyAuth(req, store.AuthNewAPIToken, newAPISecret)
	if got := req.Header.Get("Authorization"); got != "Bearer access-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("New-Api-User"); got != "42" {
		t.Fatalf("New-Api-User = %q", got)
	}

	apiKeySecret, err := EncodeAPIKeyCredentials("admin-key")
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	ApplyAuth(req, store.AuthXAPIKey, apiKeySecret)
	if got := req.Header.Get("X-API-Key"); got != "admin-key" {
		t.Fatalf("X-API-Key = %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization should be empty, got %q", got)
	}
}

func TestRuntimeGroupNameUsesStableInternalName(t *testing.T) {
	group := RuntimeGroup{Group: &store.UpstreamGroup{
		Name:        "default",
		DisplayName: "显示分组",
	}}
	if got := group.Name(); got != "default" {
		t.Fatalf("runtime group name = %q, want internal name", got)
	}
}

func TestResolveAuthPasswordModes(t *testing.T) {
	newAPIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/login" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc"})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"id": 7,
			},
		})
	}))
	defer newAPIServer.Close()

	passwordSecret, err := EncodePasswordCredentials("admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	u, secret, err := ResolveAuth(context.Background(), newAPIServer.Client(), store.Upstream{
		Kind:     store.KindNewAPI,
		BaseURL:  newAPIServer.URL,
		AuthType: store.AuthPassword,
	}, passwordSecret)
	if err != nil {
		t.Fatal(err)
	}
	if u.AuthType != store.AuthNewAPISession {
		t.Fatalf("new-api runtime auth type = %s", u.AuthType)
	}
	sessionCreds, err := DecodeNewAPISessionCredentials(secret)
	if err != nil {
		t.Fatal(err)
	}
	if sessionCreds.UserID != "7" || sessionCreds.Cookie != "session=abc" {
		t.Fatalf("unexpected new-api session creds: %#v", sessionCreds)
	}

	sub2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/login" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"access_token": "jwt-token",
				"token_type":   "Bearer",
				"user": map[string]any{
					"role": "admin",
				},
			},
		})
	}))
	defer sub2Server.Close()

	u, secret, err = ResolveAuth(context.Background(), sub2Server.Client(), store.Upstream{
		Kind:     store.KindSub2API,
		BaseURL:  sub2Server.URL,
		AuthType: store.AuthPassword,
	}, passwordSecret)
	if err != nil {
		t.Fatal(err)
	}
	if u.AuthType != store.AuthBearer || secret != "jwt-token" {
		t.Fatalf("sub2api runtime auth = %s %q", u.AuthType, secret)
	}
}

func TestRequestJSONTreatsBusinessFailureAsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"message": "Unauthorized, insufficient privileges",
		})
	}))
	defer server.Close()

	_, status, _, err := RequestJSON(context.Background(), server.Client(), store.Upstream{
		BaseURL:  server.URL,
		AuthType: store.AuthBearer,
	}, "token", http.MethodGet, "/api/channel/", nil, nil)
	if err == nil {
		t.Fatal("expected business failure error")
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if got := err.Error(); got != "GET /api/channel/ failed: Unauthorized, insufficient privileges" {
		t.Fatalf("error = %q", got)
	}
}

func TestCheckOpenAICompatibleCallSuccessAndFailureRedactsKey(t *testing.T) {
	successServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-sensitive-call-key" {
			t.Fatalf("Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "gpt-test" || body["stream"] != true {
			t.Fatalf("unexpected body: %#v", body)
		}
		messages := body["messages"].([]any)
		if messages[0].(map[string]any)["content"] != "hi" {
			t.Fatalf("unexpected prompt: %#v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer successServer.Close()

	status, _, message, summary := CheckOpenAICompatibleCall(context.Background(), successServer.Client(), CallCredentials{
		URL: successServer.URL,
		Key: "sk-sensitive-call-key",
	}, CallTestOptions{Model: "gpt-test", EndpointType: "responses", Prompt: "not-hi", Stream: false})
	if status != "available" || message != "call key verified via chat_completions" {
		t.Fatalf("status=%s message=%q", status, message)
	}
	if summary["endpoint_type"] != "chat_completions" || summary["stream"] != true {
		t.Fatalf("summary = %#v", summary)
	}

	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": "bad key sk-sensitive-call-key",
			},
		})
	}))
	defer failServer.Close()
	status, _, message, summary = CheckOpenAICompatibleCall(context.Background(), failServer.Client(), CallCredentials{
		URL: failServer.URL,
		Key: "sk-sensitive-call-key",
	}, CallTestOptions{Model: "gpt-test"})
	if status != "error" {
		t.Fatalf("status = %s", status)
	}
	if !strings.Contains(message, "returned 403") || !strings.Contains(message, "bad key") {
		t.Fatalf("message = %q", message)
	}
	if strings.Contains(message, "sk-sensitive-call-key") {
		t.Fatalf("message leaked key: %q", message)
	}
	if b, _ := json.Marshal(summary); strings.Contains(string(b), "sk-sensitive-call-key") {
		t.Fatalf("summary leaked key: %s", b)
	}
}
