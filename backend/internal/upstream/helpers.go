package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"xi_monitor/backend/internal/crypto"
	"xi_monitor/backend/internal/store"
)

const (
	BalanceAuthNewAPIAccessToken   = "newapi_access_token"
	BalanceAuthSub2APIRefreshToken = "sub2api_refresh_token"
	BalanceAuthPassword            = "password"
)

type BalanceCredentials struct {
	BalanceAuthType       string     `json:"balance_auth_type"`
	BalanceUserID         string     `json:"balance_user_id,omitempty"`
	BalanceAccessToken    string     `json:"balance_access_token,omitempty"`
	BalanceRefreshToken   string     `json:"balance_refresh_token,omitempty"`
	BalanceTokenExpiresAt *time.Time `json:"balance_token_expires_at,omitempty"`
	BalanceCookie         string     `json:"balance_cookie,omitempty"`
	BalanceUserAgent      string     `json:"balance_user_agent,omitempty"`
	AuthUsername          string     `json:"auth_username,omitempty"`
	AuthPassword          string     `json:"auth_password,omitempty"`
	CallURL               string     `json:"call_url,omitempty"`
	CallKey               string     `json:"call_key,omitempty"`
}

type PasswordCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
	CallURL  string `json:"call_url,omitempty"`
	CallKey  string `json:"call_key,omitempty"`
}

type NewAPITokenCredentials struct {
	AccessToken string `json:"access_token"`
	UserID      string `json:"user_id"`
	CallURL     string `json:"call_url,omitempty"`
	CallKey     string `json:"call_key,omitempty"`
}

type NewAPISessionCredentials struct {
	Cookie  string `json:"cookie"`
	UserID  string `json:"user_id"`
	CallURL string `json:"call_url,omitempty"`
	CallKey string `json:"call_key,omitempty"`
}

type BearerCredentials struct {
	Token   string `json:"token"`
	CallURL string `json:"call_url,omitempty"`
	CallKey string `json:"call_key,omitempty"`
}

type APIKeyCredentials struct {
	APIKey  string `json:"api_key"`
	CallURL string `json:"call_url,omitempty"`
	CallKey string `json:"call_key,omitempty"`
}

type CallCredentials struct {
	URL string
	Key string
}

type CallTestOptions struct {
	Model        string
	EndpointType string
	Prompt       string
	Stream       bool
}

type Client struct {
	HTTP *http.Client
}

type FetchResult struct {
	Items         []store.ItemUpdate
	ConfigSummary any
	UpdatedSecret string
	UpdatedMasked string
}

type RuntimeGroup struct {
	Group *store.UpstreamGroup
}

func (g RuntimeGroup) IDPtr() *int64 {
	if g.Group == nil {
		return nil
	}
	id := g.Group.ID
	return &id
}

func (g RuntimeGroup) Name() string {
	if g.Group == nil {
		return "default"
	}
	return g.Group.Name
}

func EnabledGroups(groups []store.UpstreamGroup) []RuntimeGroup {
	var out []RuntimeGroup
	for i := range groups {
		if groups[i].Enabled {
			out = append(out, RuntimeGroup{Group: &groups[i]})
		}
	}
	if len(out) == 0 {
		out = append(out, RuntimeGroup{})
	}
	return out
}

func ManualRatioForGroup(u store.Upstream, groupName string) *float64 {
	groupName = strings.TrimSpace(groupName)
	for i := range u.Groups {
		group := u.Groups[i]
		if group.ManualRatio == nil {
			continue
		}
		if group.Name == groupName || group.DisplayName == groupName {
			return group.ManualRatio
		}
	}
	return nil
}

func GroupForName(u store.Upstream, groupName string) *store.UpstreamGroup {
	groupName = strings.TrimSpace(groupName)
	for i := range u.Groups {
		group := &u.Groups[i]
		if !group.Enabled {
			continue
		}
		if group.Name == groupName || group.DisplayName == groupName {
			return group
		}
	}
	for i := range u.Groups {
		if u.Groups[i].Enabled {
			return &u.Groups[i]
		}
	}
	return nil
}

func CallTestOptionsFromGroup(group *store.UpstreamGroup) CallTestOptions {
	if group == nil {
		return CallTestOptions{}
	}
	return CallTestOptions{
		Model:        group.TestModel,
		EndpointType: "chat_completions",
		Prompt:       "hi",
		Stream:       true,
	}
}

func RequestJSON(ctx context.Context, client *http.Client, u store.Upstream, secret, method, path string, query url.Values, body any, extraHeaders ...map[string]string) (any, int, time.Duration, error) {
	if client == nil {
		client = http.DefaultClient
	}
	base := strings.TrimRight(u.BaseURL, "/")
	target := base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, 0, err
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, 0, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, headers := range extraHeaders {
		for key, value := range headers {
			if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
				req.Header.Set(key, strings.TrimSpace(value))
			}
		}
	}
	ApplyAuth(req, u.AuthType, secret)

	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return nil, 0, latency, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, latency, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, latency, fmt.Errorf("%s %s returned %d: %s", method, path, resp.StatusCode, RedactSensitive(truncate(string(respBody), 240), secret))
	}
	if len(strings.TrimSpace(string(respBody))) == 0 {
		return map[string]any{}, resp.StatusCode, latency, nil
	}
	var decoded any
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, resp.StatusCode, latency, fmt.Errorf("decode upstream response: %w", err)
	}
	if ok, msg := responseSuccess(decoded); !ok {
		if msg == "" {
			msg = "upstream response reported failure"
		}
		return nil, resp.StatusCode, latency, fmt.Errorf("%s %s failed: %s", method, path, RedactSensitive(msg, secret))
	}
	return decoded, resp.StatusCode, latency, nil
}

func RequestFirstJSON(ctx context.Context, client *http.Client, u store.Upstream, secret, method string, paths []string, query url.Values, body any) (any, int, time.Duration, string, error) {
	var lastErr error
	var lastStatus int
	var lastLatency time.Duration
	for _, path := range paths {
		resp, status, latency, err := RequestJSON(ctx, client, u, secret, method, path, query, body)
		if err == nil {
			return resp, status, latency, path, nil
		}
		lastErr = err
		lastStatus = status
		lastLatency = latency
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no upstream path candidates")
	}
	return nil, lastStatus, lastLatency, "", lastErr
}

func ApplyAuth(req *http.Request, typ store.AuthType, secret string) {
	switch typ {
	case store.AuthCookie:
		req.Header.Set("Cookie", secret)
	case store.AuthNewAPIToken:
		creds, err := DecodeNewAPITokenCredentials(secret)
		if err == nil {
			req.Header.Set("Authorization", bearerValue(creds.AccessToken))
			req.Header.Set("New-Api-User", creds.UserID)
			return
		}
		req.Header.Set("Authorization", bearerValue(secret))
	case store.AuthNewAPISession:
		creds, err := DecodeNewAPISessionCredentials(secret)
		if err == nil {
			req.Header.Set("Cookie", creds.Cookie)
			req.Header.Set("New-Api-User", creds.UserID)
			return
		}
		req.Header.Set("Cookie", secret)
	case store.AuthXAPIKey:
		creds, err := DecodeAPIKeyCredentials(secret)
		if err == nil {
			req.Header.Set("X-API-Key", creds.APIKey)
			return
		}
		req.Header.Set("X-API-Key", secret)
	case store.AuthAdminAPIKey:
		req.Header.Set("X-API-Key", secret)
		req.Header.Set("Authorization", bearerValue(secret))
	case store.AuthBearer:
		if creds, err := DecodeBearerCredentials(secret); err == nil {
			req.Header.Set("Authorization", bearerValue(creds.Token))
			return
		}
		req.Header.Set("Authorization", bearerValue(secret))
	default:
		req.Header.Set("Authorization", bearerValue(secret))
	}
}

func ResolveAuth(ctx context.Context, client *http.Client, u store.Upstream, secret string) (store.Upstream, string, error) {
	if u.AuthType != store.AuthPassword {
		return u, secret, nil
	}
	sessionSecret, sessionAuthType, err := LoginWithPassword(ctx, client, u, secret)
	if err != nil {
		return store.Upstream{}, "", err
	}
	if call := ExtractCallCredentials(secret); call.URL != "" && call.Key != "" {
		sessionSecret, err = AttachCallCredentials(sessionAuthType, sessionSecret, call)
		if err != nil {
			return store.Upstream{}, "", err
		}
	}
	u.AuthType = sessionAuthType
	return u, sessionSecret, nil
}

func LoginWithPassword(ctx context.Context, client *http.Client, u store.Upstream, secret string) (string, store.AuthType, error) {
	if client == nil {
		client = http.DefaultClient
	}
	creds, err := DecodePasswordCredentials(secret)
	if err != nil {
		return "", "", err
	}
	switch u.Kind {
	case store.KindNewAPI:
		return loginNewAPIWithPassword(ctx, client, u.BaseURL, creds)
	case store.KindSub2API:
		return loginSub2APIWithPassword(ctx, client, u.BaseURL, creds)
	default:
		return "", "", fmt.Errorf("password login unsupported for upstream kind %s", u.Kind)
	}
}

func DecodePasswordCredentials(secret string) (PasswordCredentials, error) {
	if balance, err := DecodeBalanceCredentials(secret); err == nil && balance.BalanceAuthType == BalanceAuthPassword {
		return PasswordCredentials{
			Username: balance.AuthUsername,
			Password: balance.AuthPassword,
			CallURL:  balance.CallURL,
			CallKey:  balance.CallKey,
		}, nil
	}
	var creds PasswordCredentials
	if err := json.Unmarshal([]byte(secret), &creds); err != nil {
		return PasswordCredentials{}, fmt.Errorf("decode password credentials: %w", err)
	}
	creds.Username = strings.TrimSpace(creds.Username)
	creds.CallURL = strings.TrimRight(strings.TrimSpace(creds.CallURL), "/")
	creds.CallKey = strings.TrimSpace(creds.CallKey)
	if creds.Username == "" || creds.Password == "" {
		return PasswordCredentials{}, fmt.Errorf("username and password are required")
	}
	return creds, nil
}

func EncodePasswordCredentials(username, password string) (string, error) {
	creds := PasswordCredentials{Username: strings.TrimSpace(username), Password: password}
	if creds.Username == "" || creds.Password == "" {
		return "", fmt.Errorf("username and password are required")
	}
	b, err := json.Marshal(creds)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func EncodePasswordCredentialsWithCall(username, password, callURL, callKey string) (string, error) {
	return EncodeBalanceCredentials(BalanceCredentials{
		BalanceAuthType: BalanceAuthPassword,
		AuthUsername:    username,
		AuthPassword:    password,
		CallURL:         callURL,
		CallKey:         callKey,
	})
}

func DecodeNewAPITokenCredentials(secret string) (NewAPITokenCredentials, error) {
	if balance, err := DecodeBalanceCredentials(secret); err == nil && balance.BalanceAuthType == BalanceAuthNewAPIAccessToken {
		return NewAPITokenCredentials{
			AccessToken: balance.BalanceAccessToken,
			UserID:      balance.BalanceUserID,
			CallURL:     balance.CallURL,
			CallKey:     balance.CallKey,
		}, nil
	}
	var creds NewAPITokenCredentials
	if err := json.Unmarshal([]byte(secret), &creds); err != nil {
		return NewAPITokenCredentials{}, err
	}
	creds.AccessToken = strings.TrimSpace(creds.AccessToken)
	creds.UserID = strings.TrimSpace(creds.UserID)
	creds.CallURL = strings.TrimRight(strings.TrimSpace(creds.CallURL), "/")
	creds.CallKey = strings.TrimSpace(creds.CallKey)
	if creds.AccessToken == "" || creds.UserID == "" {
		return NewAPITokenCredentials{}, fmt.Errorf("access_token and user_id are required")
	}
	return creds, nil
}

func EncodeNewAPITokenCredentials(accessToken, userID string) (string, error) {
	return EncodeNewAPITokenCredentialsWithCall(accessToken, userID, "", "")
}

func EncodeNewAPITokenCredentialsWithCall(accessToken, userID, callURL, callKey string) (string, error) {
	return EncodeBalanceCredentials(BalanceCredentials{
		BalanceAuthType:    BalanceAuthNewAPIAccessToken,
		BalanceAccessToken: accessToken,
		BalanceUserID:      userID,
		CallURL:            callURL,
		CallKey:            callKey,
	})
}

func DecodeNewAPISessionCredentials(secret string) (NewAPISessionCredentials, error) {
	var creds NewAPISessionCredentials
	if err := json.Unmarshal([]byte(secret), &creds); err != nil {
		return NewAPISessionCredentials{}, err
	}
	creds.Cookie = strings.TrimSpace(creds.Cookie)
	creds.UserID = strings.TrimSpace(creds.UserID)
	creds.CallURL = strings.TrimRight(strings.TrimSpace(creds.CallURL), "/")
	creds.CallKey = strings.TrimSpace(creds.CallKey)
	if creds.Cookie == "" || creds.UserID == "" {
		return NewAPISessionCredentials{}, fmt.Errorf("cookie and user_id are required")
	}
	return creds, nil
}

func EncodeNewAPISessionCredentials(cookie, userID string) (string, error) {
	creds := NewAPISessionCredentials{Cookie: strings.TrimSpace(cookie), UserID: strings.TrimSpace(userID)}
	if creds.Cookie == "" || creds.UserID == "" {
		return "", fmt.Errorf("cookie and user_id are required")
	}
	b, err := json.Marshal(creds)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func DecodeBearerCredentials(secret string) (BearerCredentials, error) {
	if balance, err := DecodeBalanceCredentials(secret); err == nil && balance.BalanceAuthType == BalanceAuthSub2APIRefreshToken && balance.BalanceAccessToken != "" {
		return BearerCredentials{
			Token:   balance.BalanceAccessToken,
			CallURL: balance.CallURL,
			CallKey: balance.CallKey,
		}, nil
	}
	var creds BearerCredentials
	if err := json.Unmarshal([]byte(secret), &creds); err != nil {
		return BearerCredentials{}, err
	}
	creds.Token = strings.TrimSpace(creds.Token)
	creds.CallURL = strings.TrimRight(strings.TrimSpace(creds.CallURL), "/")
	creds.CallKey = strings.TrimSpace(creds.CallKey)
	if creds.Token == "" {
		return BearerCredentials{}, fmt.Errorf("token is required")
	}
	return creds, nil
}

func EncodeBearerCredentials(token, callURL, callKey string) (string, error) {
	creds := BearerCredentials{
		Token:   strings.TrimSpace(token),
		CallURL: strings.TrimRight(strings.TrimSpace(callURL), "/"),
		CallKey: strings.TrimSpace(callKey),
	}
	if creds.Token == "" {
		return "", fmt.Errorf("token is required")
	}
	if (creds.CallURL == "") != (creds.CallKey == "") {
		return "", fmt.Errorf("call_url and call_key must be provided together")
	}
	b, err := json.Marshal(creds)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func AttachCallCredentials(authType store.AuthType, secret string, call CallCredentials) (string, error) {
	call.URL = strings.TrimRight(strings.TrimSpace(call.URL), "/")
	call.Key = strings.TrimSpace(call.Key)
	if call.URL == "" || call.Key == "" {
		return secret, nil
	}
	if creds, err := DecodeBalanceCredentials(secret); err == nil {
		creds.CallURL = call.URL
		creds.CallKey = call.Key
		return EncodeBalanceCredentials(creds)
	}
	switch authType {
	case store.AuthNewAPIToken:
		creds, err := DecodeNewAPITokenCredentials(secret)
		if err != nil {
			return "", err
		}
		creds.CallURL = call.URL
		creds.CallKey = call.Key
		b, err := json.Marshal(creds)
		if err != nil {
			return "", err
		}
		return string(b), nil
	case store.AuthNewAPISession:
		creds, err := DecodeNewAPISessionCredentials(secret)
		if err != nil {
			return "", err
		}
		creds.CallURL = call.URL
		creds.CallKey = call.Key
		b, err := json.Marshal(creds)
		if err != nil {
			return "", err
		}
		return string(b), nil
	case store.AuthBearer:
		return EncodeBearerCredentials(secret, call.URL, call.Key)
	case store.AuthXAPIKey:
		creds, err := DecodeAPIKeyCredentials(secret)
		if err != nil {
			return "", err
		}
		creds.CallURL = call.URL
		creds.CallKey = call.Key
		b, err := json.Marshal(creds)
		if err != nil {
			return "", err
		}
		return string(b), nil
	default:
		return secret, nil
	}
}

func ExtractCallCredentials(secret string) CallCredentials {
	for _, candidate := range []func(string) (CallCredentials, bool){
		callFromBalanceCredentials,
		callFromPasswordCredentials,
		callFromNewAPISessionCredentials,
		callFromNewAPITokenCredentials,
		callFromBearerCredentials,
		callFromAPIKeyCredentials,
	} {
		if call, ok := candidate(secret); ok {
			return call
		}
	}
	return CallCredentials{}
}

func callFromBalanceCredentials(secret string) (CallCredentials, bool) {
	creds, err := DecodeBalanceCredentials(secret)
	if err != nil {
		return CallCredentials{}, false
	}
	return callFrom(creds.CallURL, creds.CallKey)
}

func callFromPasswordCredentials(secret string) (CallCredentials, bool) {
	creds, err := DecodePasswordCredentials(secret)
	if err != nil {
		return CallCredentials{}, false
	}
	return callFrom(creds.CallURL, creds.CallKey)
}

func callFromNewAPISessionCredentials(secret string) (CallCredentials, bool) {
	creds, err := DecodeNewAPISessionCredentials(secret)
	if err != nil {
		return CallCredentials{}, false
	}
	return callFrom(creds.CallURL, creds.CallKey)
}

func callFromNewAPITokenCredentials(secret string) (CallCredentials, bool) {
	creds, err := DecodeNewAPITokenCredentials(secret)
	if err != nil {
		return CallCredentials{}, false
	}
	return callFrom(creds.CallURL, creds.CallKey)
}

func callFromBearerCredentials(secret string) (CallCredentials, bool) {
	creds, err := DecodeBearerCredentials(secret)
	if err != nil {
		return CallCredentials{}, false
	}
	return callFrom(creds.CallURL, creds.CallKey)
}

func callFromAPIKeyCredentials(secret string) (CallCredentials, bool) {
	creds, err := DecodeAPIKeyCredentials(secret)
	if err != nil {
		return CallCredentials{}, false
	}
	return callFrom(creds.CallURL, creds.CallKey)
}

func callFrom(callURL, callKey string) (CallCredentials, bool) {
	call := CallCredentials{
		URL: strings.TrimRight(strings.TrimSpace(callURL), "/"),
		Key: strings.TrimSpace(callKey),
	}
	return call, call.URL != "" && call.Key != ""
}

func DecodeAPIKeyCredentials(secret string) (APIKeyCredentials, error) {
	var creds APIKeyCredentials
	if err := json.Unmarshal([]byte(secret), &creds); err != nil {
		return APIKeyCredentials{}, err
	}
	creds.APIKey = strings.TrimSpace(creds.APIKey)
	creds.CallURL = strings.TrimRight(strings.TrimSpace(creds.CallURL), "/")
	creds.CallKey = strings.TrimSpace(creds.CallKey)
	if creds.APIKey == "" {
		return APIKeyCredentials{}, fmt.Errorf("api_key is required")
	}
	return creds, nil
}

func DecodeBalanceCredentials(secret string) (BalanceCredentials, error) {
	var creds BalanceCredentials
	if err := json.Unmarshal([]byte(secret), &creds); err != nil {
		return BalanceCredentials{}, err
	}
	creds.BalanceAuthType = strings.TrimSpace(creds.BalanceAuthType)
	creds.BalanceUserID = strings.TrimSpace(creds.BalanceUserID)
	creds.BalanceAccessToken = strings.TrimSpace(creds.BalanceAccessToken)
	creds.BalanceRefreshToken = strings.TrimSpace(creds.BalanceRefreshToken)
	creds.BalanceCookie = strings.TrimSpace(creds.BalanceCookie)
	creds.BalanceUserAgent = strings.TrimSpace(creds.BalanceUserAgent)
	creds.AuthUsername = strings.TrimSpace(creds.AuthUsername)
	creds.CallURL = strings.TrimRight(strings.TrimSpace(creds.CallURL), "/")
	creds.CallKey = strings.TrimSpace(creds.CallKey)
	if creds.BalanceAuthType == "" {
		return BalanceCredentials{}, fmt.Errorf("balance_auth_type is required")
	}
	switch creds.BalanceAuthType {
	case BalanceAuthNewAPIAccessToken:
		if creds.BalanceAccessToken == "" || creds.BalanceUserID == "" {
			return BalanceCredentials{}, fmt.Errorf("balance_access_token and balance_user_id are required")
		}
	case BalanceAuthSub2APIRefreshToken:
		if creds.BalanceRefreshToken == "" && creds.BalanceAccessToken == "" {
			return BalanceCredentials{}, fmt.Errorf("balance_refresh_token or balance_access_token is required")
		}
	case BalanceAuthPassword:
		if creds.AuthUsername == "" || creds.AuthPassword == "" {
			return BalanceCredentials{}, fmt.Errorf("auth_username and auth_password are required")
		}
	default:
		return BalanceCredentials{}, fmt.Errorf("unsupported balance_auth_type %s", creds.BalanceAuthType)
	}
	return creds, nil
}

func EncodeBalanceCredentials(creds BalanceCredentials) (string, error) {
	creds.BalanceAuthType = strings.TrimSpace(creds.BalanceAuthType)
	creds.BalanceUserID = strings.TrimSpace(creds.BalanceUserID)
	creds.BalanceAccessToken = strings.TrimSpace(creds.BalanceAccessToken)
	creds.BalanceRefreshToken = strings.TrimSpace(creds.BalanceRefreshToken)
	creds.BalanceCookie = strings.TrimSpace(creds.BalanceCookie)
	creds.BalanceUserAgent = strings.TrimSpace(creds.BalanceUserAgent)
	creds.AuthUsername = strings.TrimSpace(creds.AuthUsername)
	creds.CallURL = strings.TrimRight(strings.TrimSpace(creds.CallURL), "/")
	creds.CallKey = strings.TrimSpace(creds.CallKey)
	if (creds.CallURL == "") != (creds.CallKey == "") {
		return "", fmt.Errorf("call_url and call_key must be provided together")
	}
	if _, err := validateBalanceCredentials(creds); err != nil {
		return "", err
	}
	b, err := json.Marshal(creds)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func validateBalanceCredentials(creds BalanceCredentials) (BalanceCredentials, error) {
	switch creds.BalanceAuthType {
	case BalanceAuthNewAPIAccessToken:
		if creds.BalanceAccessToken == "" || creds.BalanceUserID == "" {
			return BalanceCredentials{}, fmt.Errorf("balance_access_token and balance_user_id are required")
		}
	case BalanceAuthSub2APIRefreshToken:
		if creds.BalanceRefreshToken == "" && creds.BalanceAccessToken == "" {
			return BalanceCredentials{}, fmt.Errorf("balance_refresh_token or balance_access_token is required")
		}
	case BalanceAuthPassword:
		if creds.AuthUsername == "" || creds.AuthPassword == "" {
			return BalanceCredentials{}, fmt.Errorf("auth_username and auth_password are required")
		}
	default:
		return BalanceCredentials{}, fmt.Errorf("balance_auth_type must be newapi_access_token, sub2api_refresh_token, or password")
	}
	return creds, nil
}

func MaskBalanceCredentials(secret string) string {
	creds, err := DecodeBalanceCredentials(secret)
	if err != nil {
		return crypto.MaskSecret(secret)
	}
	parts := []string{creds.BalanceAuthType}
	switch creds.BalanceAuthType {
	case BalanceAuthNewAPIAccessToken:
		parts = append(parts, "user:"+creds.BalanceUserID, crypto.MaskSecret(creds.BalanceAccessToken))
	case BalanceAuthSub2APIRefreshToken:
		if creds.BalanceAccessToken != "" {
			parts = append(parts, "access:"+crypto.MaskSecret(creds.BalanceAccessToken))
		}
		parts = append(parts, "refresh:"+crypto.MaskSecret(creds.BalanceRefreshToken))
		if creds.BalanceCookie != "" {
			parts = append(parts, "cookie:"+crypto.MaskSecret(creds.BalanceCookie))
		}
	case BalanceAuthPassword:
		parts = append(parts, strings.TrimSpace(creds.AuthUsername))
	}
	if creds.CallKey != "" {
		parts = append(parts, "call:"+crypto.MaskSecret(creds.CallKey))
	}
	return strings.Join(parts, " ")
}

func EncodeAPIKeyCredentials(apiKey string) (string, error) {
	return EncodeAPIKeyCredentialsWithCall(apiKey, "", "")
}

func EncodeAPIKeyCredentialsWithCall(apiKey, callURL, callKey string) (string, error) {
	creds := APIKeyCredentials{
		APIKey:  strings.TrimSpace(apiKey),
		CallURL: strings.TrimRight(strings.TrimSpace(callURL), "/"),
		CallKey: strings.TrimSpace(callKey),
	}
	if creds.APIKey == "" {
		return "", fmt.Errorf("api_key is required")
	}
	if (creds.CallURL == "") != (creds.CallKey == "") {
		return "", fmt.Errorf("call_url and call_key must be provided together")
	}
	b, err := json.Marshal(creds)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func bearerValue(secret string) string {
	secret = strings.TrimSpace(secret)
	if strings.HasPrefix(strings.ToLower(secret), "bearer ") {
		return secret
	}
	return "Bearer " + secret
}

func CheckOpenAICompatibleCall(ctx context.Context, client *http.Client, call CallCredentials, opts CallTestOptions) (string, *int, string, map[string]any) {
	call.URL = strings.TrimRight(strings.TrimSpace(call.URL), "/")
	call.Key = strings.TrimSpace(call.Key)
	if call.URL == "" || call.Key == "" {
		return "unknown", nil, "call key not configured", map[string]any{"configured": false}
	}

	opts = normalizeCallTestOptions(opts)
	endpoint := buildOpenAICompatibleEndpointURL(call.URL, opts.EndpointType)
	body := openAICompatibleTestPayload(opts)
	payload, err := json.Marshal(body)
	if err != nil {
		return "error", nil, err.Error(), map[string]any{"configured": true}
	}

	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "error", nil, err.Error(), map[string]any{"configured": true, "endpoint": endpoint}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearerValue(call.Key))
	if opts.Stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}

	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	latencyMS := LatencyMS(latency)
	summary := map[string]any{
		"configured":    true,
		"endpoint":      endpoint,
		"endpoint_type": opts.EndpointType,
		"model":         opts.Model,
		"stream":        opts.Stream,
	}
	if err != nil {
		return "error", &latencyMS, RedactSensitive(err.Error(), call.Key), summary
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return "error", &latencyMS, readErr.Error(), summary
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := upstreamErrorMessageFromBody(respBody)
		if message == "" {
			message = truncate(string(respBody), 240)
		}
		return "error", &latencyMS, RedactSensitive(fmt.Sprintf("%s returned %d: %s", opts.EndpointType, resp.StatusCode, message), call.Key), summary
	}

	var validateErr error
	if opts.Stream {
		validateErr = validateOpenAICompatibleStream(respBody, opts.EndpointType)
	} else {
		validateErr = validateOpenAICompatibleJSON(respBody, opts.EndpointType)
	}
	if validateErr != nil {
		return "error", &latencyMS, RedactSensitive(validateErr.Error(), call.Key), summary
	}
	return "available", &latencyMS, "call key verified via " + opts.EndpointType, summary
}

func normalizeCallTestOptions(opts CallTestOptions) CallTestOptions {
	opts.Model = strings.TrimSpace(opts.Model)
	if opts.Model == "" {
		opts.Model = "gpt-4o-mini"
	}
	opts.EndpointType = "chat_completions"
	opts.Prompt = "hi"
	opts.Stream = true
	return opts
}

func buildOpenAICompatibleEndpointURL(base, endpointType string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	path := "/v1/chat/completions"
	if endpointType == "responses" {
		path = "/v1/responses"
	}
	pathNoVersion := strings.TrimPrefix(path, "/v1")
	lowerBase := strings.ToLower(base)
	if strings.HasSuffix(lowerBase, strings.ToLower(path)) || strings.HasSuffix(lowerBase, strings.ToLower(pathNoVersion)) {
		return base
	}
	if strings.HasSuffix(lowerBase, "/v1") {
		return base + pathNoVersion
	}
	return base + path
}

func openAICompatibleTestPayload(opts CallTestOptions) map[string]any {
	if opts.EndpointType == "responses" {
		return map[string]any{
			"model": opts.Model,
			"input": []map[string]any{
				{
					"role": "user",
					"content": []map[string]any{
						{"type": "input_text", "text": opts.Prompt},
					},
				},
			},
			"stream": opts.Stream,
		}
	}
	return map[string]any{
		"model": opts.Model,
		"messages": []map[string]any{
			{"role": "user", "content": opts.Prompt},
		},
		"stream": opts.Stream,
	}
}

func validateOpenAICompatibleStream(body []byte, endpointType string) error {
	seenJSON := false
	for _, rawLine := range bytes.Split(body, []byte("\n")) {
		line := strings.TrimSpace(string(rawLine))
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(data), &decoded); err != nil {
			return fmt.Errorf("invalid %s stream JSON", endpointType)
		}
		seenJSON = true
		if msg := upstreamErrorMessage(decoded); msg != "" {
			return fmt.Errorf("%s stream error: %s", endpointType, msg)
		}
	}
	if !seenJSON {
		return fmt.Errorf("invalid %s response: expected SSE JSON data", endpointType)
	}
	return nil
}

func validateOpenAICompatibleJSON(body []byte, endpointType string) error {
	if len(strings.TrimSpace(string(body))) == 0 {
		return fmt.Errorf("empty %s response", endpointType)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fmt.Errorf("invalid %s JSON response", endpointType)
	}
	if msg := upstreamErrorMessage(decoded); msg != "" {
		return fmt.Errorf("%s error: %s", endpointType, msg)
	}
	if endpointType == "responses" {
		if StringField(decoded, "id", "status", "output_text") != "" {
			return nil
		}
		if _, ok := decoded["output"].([]any); ok {
			return nil
		}
		return fmt.Errorf("invalid responses response")
	}
	if _, ok := decoded["choices"].([]any); ok {
		return nil
	}
	if StringField(decoded, "id", "model") != "" {
		return nil
	}
	return fmt.Errorf("invalid chat_completions response")
}

func upstreamErrorMessage(m map[string]any) string {
	if errMap, ok := m["error"].(map[string]any); ok {
		if msg := StringField(errMap, "message", "error", "detail"); msg != "" {
			return msg
		}
		return "upstream returned error"
	}
	if success, ok := m["success"].(bool); ok && !success {
		if msg := StringField(m, "message", "error", "detail"); msg != "" {
			return msg
		}
		return "upstream response reported failure"
	}
	return ""
}

func upstreamErrorMessageFromBody(body []byte) string {
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return ""
	}
	return upstreamErrorMessage(decoded)
}

func loginNewAPIWithPassword(ctx context.Context, client *http.Client, baseURL string, creds PasswordCredentials) (string, store.AuthType, error) {
	body := map[string]any{
		"username": creds.Username,
		"password": creds.Password,
	}
	decoded, cookies, err := postLogin(ctx, client, baseURL, "/api/user/login", body)
	if err != nil {
		return "", "", err
	}
	userID := StringField(flattenMap(decoded), "id", "user_id")
	if userID == "" {
		return "", "", fmt.Errorf("new-api login response did not include user id")
	}
	cookie := cookieHeader(cookies)
	if cookie == "" {
		return "", "", fmt.Errorf("new-api login did not set session cookie")
	}
	secret, err := EncodeNewAPISessionCredentials(cookie, userID)
	if err != nil {
		return "", "", err
	}
	return secret, store.AuthNewAPISession, nil
}

func loginSub2APIWithPassword(ctx context.Context, client *http.Client, baseURL string, creds PasswordCredentials) (string, store.AuthType, error) {
	body := map[string]any{
		"email":    creds.Username,
		"password": creds.Password,
	}
	decoded, _, err := postLogin(ctx, client, baseURL, "/api/v1/auth/login", body)
	if err != nil {
		return "", "", err
	}
	if requires2FA, ok := flattenMap(decoded)["requires_2fa"].(bool); ok && requires2FA {
		return "", "", fmt.Errorf("sub2api login requires 2FA; use x-api-key instead")
	}
	token := findToken(decoded)
	if token == "" {
		return "", "", fmt.Errorf("sub2api login response did not include access_token")
	}
	return token, store.AuthBearer, nil
}

func postLogin(ctx context.Context, client *http.Client, baseURL, path string, body map[string]any) (any, []*http.Cookie, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("%s returned %d: %s", path, resp.StatusCode, truncate(string(respBody), 160))
	}
	var decoded any
	if len(strings.TrimSpace(string(respBody))) > 0 {
		if err := json.Unmarshal(respBody, &decoded); err != nil {
			return nil, nil, fmt.Errorf("%s decode login response: %w", path, err)
		}
	}
	if ok, msg := responseSuccess(decoded); !ok {
		if msg == "" {
			msg = "login failed"
		}
		return nil, nil, fmt.Errorf("%s: %s", path, msg)
	}
	return decoded, resp.Cookies(), nil
}

func responseSuccess(v any) (bool, string) {
	m, ok := v.(map[string]any)
	if !ok {
		return true, ""
	}
	if code, ok := ToFloat(m["code"]); ok && code != 0 {
		return false, StringField(m, "message", "error", "detail")
	}
	if success, ok := m["success"].(bool); ok && !success {
		return false, StringField(m, "message", "error", "detail")
	}
	return true, ""
}

func findToken(v any) string {
	switch value := v.(type) {
	case map[string]any:
		for _, key := range []string{"token", "access_token", "accessToken", "jwt", "id_token", "key"} {
			if token, ok := value[key].(string); ok && strings.TrimSpace(token) != "" {
				return strings.TrimSpace(token)
			}
		}
		for _, key := range []string{"data", "result", "user"} {
			if token := findToken(value[key]); token != "" {
				return token
			}
		}
	case []any:
		for _, item := range value {
			if token := findToken(item); token != "" {
				return token
			}
		}
	}
	return ""
}

func flattenMap(v any) map[string]any {
	out := map[string]any{}
	var walk func(any)
	walk = func(value any) {
		switch vv := value.(type) {
		case map[string]any:
			for k, child := range vv {
				if _, exists := out[k]; !exists {
					out[k] = child
				}
			}
			for _, key := range []string{"data", "result", "user"} {
				if child, ok := vv[key]; ok {
					walk(child)
				}
			}
		}
	}
	walk(v)
	return out
}

func cookieHeader(cookies []*http.Cookie) string {
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie.Name != "" && cookie.Value != "" {
			parts = append(parts, cookie.Name+"="+cookie.Value)
		}
	}
	return strings.Join(parts, "; ")
}

func AsList(v any) []map[string]any {
	if arr, ok := v.([]any); ok {
		return mapsFromArray(arr)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	for _, key := range []string{"data", "items", "channels", "accounts", "monitors", "rows", "list"} {
		if child, ok := m[key]; ok {
			if arr, ok := child.([]any); ok {
				return mapsFromArray(arr)
			}
			if childMap, ok := child.(map[string]any); ok {
				if nested := AsList(childMap); len(nested) > 0 {
					return nested
				}
			}
		}
	}
	return nil
}

func mapsFromArray(arr []any) []map[string]any {
	out := make([]map[string]any, 0, len(arr))
	for _, v := range arr {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func StringField(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			switch vv := v.(type) {
			case string:
				if strings.TrimSpace(vv) != "" {
					return vv
				}
			case float64:
				return strconv.FormatFloat(vv, 'f', -1, 64)
			case bool:
				return strconv.FormatBool(vv)
			}
		}
	}
	return ""
}

func FloatField(m map[string]any, keys ...string) *float64 {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if f, ok := ToFloat(v); ok {
				return &f
			}
		}
	}
	return nil
}

func IntField(m map[string]any, keys ...string) *int {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if f, ok := ToFloat(v); ok {
				i := int(f)
				return &i
			}
		}
	}
	return nil
}

func ToFloat(v any) (float64, bool) {
	switch vv := v.(type) {
	case float64:
		return vv, true
	case int:
		return float64(vv), true
	case int64:
		return float64(vv), true
	case json.Number:
		f, err := vv.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(vv), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func StatusFrom(v any, fallback string) string {
	if fallback == "" {
		fallback = "unknown"
	}
	switch vv := v.(type) {
	case bool:
		if vv {
			return "available"
		}
		return "unavailable"
	case float64:
		if vv == 1 || vv == 2 {
			return "available"
		}
		if vv < 0 {
			return "unavailable"
		}
	case string:
		s := strings.ToLower(strings.TrimSpace(vv))
		switch s {
		case "available", "ok", "success", "enabled", "active", "true", "normal", "up":
			return "available"
		case "unavailable", "failed", "fail", "error", "disabled", "inactive", "false", "down":
			return "unavailable"
		case "unknown":
			return "unknown"
		}
	}
	return fallback
}

func FirstValue(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return nil
}

func LatencyMS(d time.Duration) int {
	return int(d / time.Millisecond)
}

func TrendValue(latency *int, status string) []int {
	if latency != nil && *latency > 0 {
		return []int{*latency}
	}
	if status == "available" {
		return []int{1}
	}
	return []int{0}
}

func truncate(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "..."
}

func RedactSensitive(s string, secrets ...string) string {
	out := s
	values := map[string]bool{}
	for _, secret := range secrets {
		collectSecretValues(values, secret)
	}
	for value := range values {
		if len(value) < 4 {
			continue
		}
		out = strings.ReplaceAll(out, value, "[REDACTED]")
		out = strings.ReplaceAll(out, bearerValue(value), "Bearer [REDACTED]")
	}
	return out
}

func collectSecretValues(values map[string]bool, secret string) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return
	}
	values[secret] = true
	var raw map[string]any
	if err := json.Unmarshal([]byte(secret), &raw); err != nil {
		return
	}
	for _, key := range []string{
		"password", "auth_password", "token", "access_token", "refresh_token", "api_key", "key",
		"call_key", "balance_access_token", "balance_refresh_token", "cookie",
	} {
		if v, ok := raw[key].(string); ok && strings.TrimSpace(v) != "" {
			values[strings.TrimSpace(v)] = true
		}
	}
}
