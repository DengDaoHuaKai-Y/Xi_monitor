package sub2api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"xi_monitor/backend/internal/store"
	"xi_monitor/backend/internal/upstream"
)

type Client struct {
	HTTP *http.Client
}

const sub2APIAccessTokenRefreshSkew = 2 * time.Minute

func New(httpClient *http.Client) *Client {
	return &Client{HTTP: httpClient}
}

func (c *Client) Fetch(ctx context.Context, u store.Upstream, secret string) (upstream.FetchResult, error) {
	now := time.Now()
	if creds, err := upstream.DecodeBalanceCredentials(secret); err == nil && creds.BalanceAuthType == upstream.BalanceAuthSub2APIRefreshToken {
		return c.fetchWithRefreshToken(ctx, u, secret, creds, now)
	}
	accountsResp, _, _, _, err := upstream.RequestFirstJSON(ctx, c.HTTP, u, secret, http.MethodGet, []string{"/api/v1/admin/accounts", "/api/admin/accounts"}, nil, nil)
	if err != nil {
		return c.fetchUserProfile(ctx, u, secret, now, err)
	}

	configSummary := map[string]any{}
	if channelsResp, _, _, _, channelsErr := upstream.RequestFirstJSON(ctx, c.HTTP, u, secret, http.MethodGet, []string{"/api/v1/admin/channels", "/api/admin/channels"}, nil, nil); channelsErr == nil {
		configSummary["channels"] = channelsResp
	} else {
		configSummary["channels_error"] = channelsErr.Error()
	}
	if groupsResp, _, _, _, groupsErr := upstream.RequestFirstJSON(ctx, c.HTTP, u, secret, http.MethodGet, []string{"/api/v1/admin/groups", "/api/admin/groups"}, nil, nil); groupsErr == nil {
		configSummary["groups"] = groupsResp
	} else {
		configSummary["groups_error"] = groupsErr.Error()
	}

	var items []store.ItemUpdate
	accounts := upstream.AsList(accountsResp)
	for _, account := range accounts {
		externalID := upstream.StringField(account, "id", "account_id", "uuid")
		if externalID == "" {
			externalID = upstream.StringField(account, "name", "email", "username")
		}
		name := upstream.StringField(account, "name", "email", "username", "display_name")
		if name == "" {
			name = "account " + externalID
		}
		accountGroup := upstream.StringField(account, "group", "group_name", "groupName")
		endpoint := upstream.StringField(account, "endpoint", "base_url", "url")
		if endpoint == "" {
			endpoint = u.BaseURL
		}
		baseBalance := upstream.FloatField(account, "balance", "remaining_balance", "quota", "credit")
		for _, group := range upstream.EnabledGroups(u.Groups) {
			if group.Group != nil && accountGroup != "" && group.Group.Name != accountGroup && group.Group.DisplayName != accountGroup {
				continue
			}

			status, latency, message, callSummary := c.checkGroupCall(ctx, u, secret, group.Group)
			balance := baseBalance
			var groupRatio *float64
			if group.Group != nil && group.Group.ManualRatio != nil {
				groupRatio = group.Group.ManualRatio
			}
			if message == "call key not configured" {
				if msg := upstream.StringField(account, "message", "status_message"); msg != "" {
					message = msg
				}
			}

			if externalID != "" {
				usageQuery := url.Values{}
				usageQuery.Set("source", "active")
				usageQuery.Set("force", "true")
				usagePaths := []string{
					"/api/v1/admin/accounts/" + url.PathEscape(externalID) + "/usage",
					"/api/admin/accounts/" + url.PathEscape(externalID) + "/usage",
				}
				if usageResp, _, _, _, usageErr := upstream.RequestFirstJSON(ctx, c.HTTP, u, secret, http.MethodGet, usagePaths, usageQuery, nil); usageErr == nil {
					if b := balanceFromUsage(usageResp); b != nil {
						balance = b
					}
				} else if message == "" {
					message = usageErr.Error()
				}
			}

			groupName := group.Name()
			if accountGroup != "" && group.Group == nil {
				groupName = accountGroup
			}

			items = append(items, store.ItemUpdate{
				UpstreamID:          u.ID,
				UpstreamGroupID:     group.IDPtr(),
				ExternalID:          externalID,
				ItemType:            "account",
				Name:                name,
				Endpoint:            endpoint,
				Source:              "sub2api",
				GroupName:           groupName,
				Ratio:               groupRatio,
				Status:              status,
				LatencyMS:           latency,
				AvailabilityPercent: availabilityFromStatus(status),
				Balance:             balance,
				BalanceUnit:         upstream.StringField(account, "balance_unit", "currency", "unit"),
				LastCheckedAt:       now,
				LastMessage:         message,
				Trend:               upstream.TrendValue(latency, status),
				RawSummary:          withCallSummary(sanitizeAccount(account), callSummary),
				CheckType:           "availability",
				TestParams:          map[string]any{"call_test": callSummary},
			})
		}
	}

	if monitorsResp, _, _, _, monitorErr := upstream.RequestFirstJSON(ctx, c.HTTP, u, secret, http.MethodGet, []string{"/api/v1/admin/channel-monitors", "/api/admin/channel-monitors"}, nil, nil); monitorErr == nil {
		items = append(items, monitorItems(u, monitorsResp, now)...)
	} else {
		configSummary["monitors_error"] = monitorErr.Error()
	}

	return upstream.FetchResult{Items: items, ConfigSummary: configSummary}, nil
}

func (c *Client) checkGroupCall(ctx context.Context, u store.Upstream, secret string, group *store.UpstreamGroup) (string, *int, string, map[string]any) {
	return upstream.CheckOpenAICompatibleCall(ctx, c.HTTP, upstream.ExtractCallCredentials(secret), upstream.CallTestOptionsFromGroup(group))
}

func (c *Client) fetchUserProfile(ctx context.Context, u store.Upstream, secret string, now time.Time, fallbackErr error) (upstream.FetchResult, error) {
	profileResp, _, latency, path, err := upstream.RequestFirstJSON(ctx, c.HTTP, u, secret, http.MethodGet, []string{"/api/v1/user/profile", "/api/v1/auth/me"}, nil, nil)
	if err != nil {
		if result, ok := c.fetchCallOnly(ctx, u, secret, now, fmt.Errorf("sub2api admin accounts failed: %w; user profile failed: %v", fallbackErr, err)); ok {
			return result, nil
		}
		return upstream.FetchResult{}, fmt.Errorf("sub2api admin accounts failed: %w; user profile failed: %v", fallbackErr, err)
	}

	profile := asMap(profileResp)
	balance := upstream.FloatField(profile, "balance", "remaining_balance", "credit", "credits")
	latencyMS := upstream.LatencyMS(latency)
	groupName := firstAllowedGroup(profile)
	if groupName == "" {
		groupName = "default"
	}
	group := upstream.GroupForName(u, groupName)
	callStatus, callLatency, callMessage, callSummary := upstream.CheckOpenAICompatibleCall(ctx, c.HTTP, upstream.ExtractCallCredentials(secret), upstream.CallTestOptionsFromGroup(group))
	status := "available"
	if callStatus != "unknown" {
		status = callStatus
	}
	itemLatency := &latencyMS
	if callLatency != nil {
		itemLatency = callLatency
	}
	availability := floatPtr(100)
	if status != "available" {
		availability = floatPtr(0)
	}
	groupRatio := upstream.ManualRatioForGroup(u, groupName)
	userID := upstream.StringField(profile, "id", "user_id")
	if userID == "" {
		userID = "self"
	}
	name := upstream.StringField(profile, "username", "email", "display_name")
	if name == "" {
		name = "sub2api user " + userID
	}

	item := store.ItemUpdate{
		UpstreamID:          u.ID,
		UpstreamGroupID:     upstream.RuntimeGroup{Group: group}.IDPtr(),
		ExternalID:          "user:" + userID,
		ItemType:            "account",
		Name:                name,
		Endpoint:            u.BaseURL,
		Source:              "sub2api",
		GroupName:           groupName,
		Ratio:               groupRatio,
		Status:              status,
		LatencyMS:           itemLatency,
		AvailabilityPercent: availability,
		Balance:             balance,
		BalanceUnit:         "USD",
		LastCheckedAt:       now,
		LastMessage:         balanceAndCallMessage("user balance fetched via "+path, callMessage),
		Trend:               upstream.TrendValue(itemLatency, status),
		RawSummary:          sanitizeUserProfile(profile, balance, callSummary),
		CheckType:           "balance",
		TestParams:          map[string]any{"endpoint": path, "call_test": callSummary},
	}

	return upstream.FetchResult{
		Items: []store.ItemUpdate{item},
		ConfigSummary: map[string]any{
			"user_profile": map[string]any{
				"group":   groupName,
				"balance": balance,
				"unit":    "USD",
			},
			"admin_accounts_error": fallbackErr.Error(),
		},
	}, nil
}

func (c *Client) fetchWithRefreshToken(ctx context.Context, u store.Upstream, secret string, creds upstream.BalanceCredentials, now time.Time) (upstream.FetchResult, error) {
	var cachedErr error
	if canUseCachedAccessToken(creds, now) {
		result, err := c.fetchWithAccessToken(ctx, u, secret, creds, now)
		if err == nil {
			return result, nil
		}
		cachedErr = err
	}
	if strings.TrimSpace(creds.BalanceRefreshToken) == "" {
		if cachedErr != nil {
			if result, ok := c.fetchCallOnly(ctx, u, secret, now, fmt.Errorf("balance credential invalid: %w", cachedErr)); ok {
				return result, nil
			}
			return upstream.FetchResult{}, fmt.Errorf("balance credential invalid: %w", cachedErr)
		}
		return upstream.FetchResult{}, fmt.Errorf("balance credential invalid: balance_refresh_token or usable balance_access_token is required")
	}

	updated, err := c.refreshAccessToken(ctx, u, creds)
	if err != nil {
		if cachedErr != nil {
			err = fmt.Errorf("%w; cached access token failed: %v", err, cachedErr)
		}
		if result, ok := c.fetchCallOnly(ctx, u, secret, now, fmt.Errorf("balance credential invalid: %w", err)); ok {
			return result, nil
		}
		return upstream.FetchResult{}, fmt.Errorf("balance credential invalid: %w", err)
	}
	updatedSecret, err := upstream.EncodeBalanceCredentials(updated)
	if err != nil {
		return upstream.FetchResult{}, err
	}

	result, err := c.fetchWithAccessToken(ctx, u, updatedSecret, updated, now)
	if err != nil {
		return upstream.FetchResult{}, err
	}
	result.UpdatedSecret = updatedSecret
	result.UpdatedMasked = upstream.MaskBalanceCredentials(updatedSecret)
	return result, nil
}

func (c *Client) fetchWithAccessToken(ctx context.Context, u store.Upstream, secret string, creds upstream.BalanceCredentials, now time.Time) (upstream.FetchResult, error) {
	runtime := u
	runtime.AuthType = store.AuthBearer
	runtimeSecret, err := upstream.EncodeBearerCredentials(creds.BalanceAccessToken, creds.CallURL, creds.CallKey)
	if err != nil {
		return upstream.FetchResult{}, err
	}

	headers := balanceBrowserHeaders(creds)
	resp, _, latency, err := upstream.RequestJSON(ctx, c.HTTP, runtime, runtimeSecret, http.MethodGet, "/api/v1/auth/me", nil, nil, headers)
	path := "/api/v1/auth/me"
	profile := asMap(resp)
	balance := balanceFromUsage(profile)
	if err != nil || balance == nil {
		resp, _, latency, err = upstream.RequestJSON(ctx, c.HTTP, runtime, runtimeSecret, http.MethodGet, "/api/v1/user/profile", nil, nil, headers)
		path = "/api/v1/user/profile"
		if err != nil {
			if result, ok := c.fetchCallOnly(ctx, u, secret, now, fmt.Errorf("user balance failed: %w", err)); ok {
				return result, nil
			}
			return upstream.FetchResult{}, fmt.Errorf("user balance failed: %w", err)
		}
		profile = asMap(resp)
		balance = balanceFromUsage(profile)
	}
	if balance == nil {
		if result, ok := c.fetchCallOnly(ctx, u, secret, now, fmt.Errorf("未找到额度字段")); ok {
			return result, nil
		}
		return upstream.FetchResult{}, fmt.Errorf("未找到额度字段")
	}

	return c.userProfileResult(ctx, u, secret, now, profile, balance, latency, path, ""), nil
}

func canUseCachedAccessToken(creds upstream.BalanceCredentials, now time.Time) bool {
	if strings.TrimSpace(creds.BalanceAccessToken) == "" {
		return false
	}
	if creds.BalanceTokenExpiresAt == nil {
		return true
	}
	return creds.BalanceTokenExpiresAt.After(now.Add(sub2APIAccessTokenRefreshSkew))
}

func (c *Client) refreshAccessToken(ctx context.Context, u store.Upstream, creds upstream.BalanceCredentials) (upstream.BalanceCredentials, error) {
	if c.HTTP == nil {
		c.HTTP = http.DefaultClient
	}
	payload, err := json.Marshal(map[string]any{"refresh_token": creds.BalanceRefreshToken})
	if err != nil {
		return upstream.BalanceCredentials{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(u.BaseURL, "/")+"/api/v1/auth/refresh", bytes.NewReader(payload))
	if err != nil {
		return upstream.BalanceCredentials{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	applyBrowserHeaders(req, balanceBrowserHeaders(creds))
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return upstream.BalanceCredentials{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return upstream.BalanceCredentials{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return upstream.BalanceCredentials{}, fmt.Errorf("/api/v1/auth/refresh returned %d: %s", resp.StatusCode, upstream.RedactSensitive(string(respBody), creds.BalanceRefreshToken))
	}
	var decoded any
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return upstream.BalanceCredentials{}, fmt.Errorf("decode refresh response: %w", err)
	}
	if ok, msg := refreshResponseSuccess(decoded); !ok {
		if msg == "" {
			msg = "refresh token invalid"
		}
		return upstream.BalanceCredentials{}, fmt.Errorf("%s", upstream.RedactSensitive(msg, creds.BalanceRefreshToken))
	}
	data := refreshDataMap(decoded)
	if ok, msg := refreshResponseSuccess(data); !ok {
		if msg == "" {
			msg = "refresh token invalid"
		}
		return upstream.BalanceCredentials{}, fmt.Errorf("%s", upstream.RedactSensitive(msg, creds.BalanceRefreshToken))
	}
	accessToken := upstream.StringField(data, "access_token", "accessToken", "token")
	refreshToken := upstream.StringField(data, "refresh_token", "refreshToken")
	if refreshToken == "" {
		refreshToken = creds.BalanceRefreshToken
	}
	if accessToken == "" {
		return upstream.BalanceCredentials{}, fmt.Errorf("refresh response did not include access_token")
	}
	expiresIn := 86400.0
	if v, ok := upstream.ToFloat(data["expires_in"]); ok && v > 0 {
		expiresIn = v
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)
	creds.BalanceAccessToken = accessToken
	creds.BalanceRefreshToken = refreshToken
	creds.BalanceTokenExpiresAt = &expiresAt
	return creds, nil
}

func balanceBrowserHeaders(creds upstream.BalanceCredentials) map[string]string {
	headers := map[string]string{}
	if strings.TrimSpace(creds.BalanceCookie) != "" {
		headers["Cookie"] = strings.TrimSpace(creds.BalanceCookie)
	}
	if strings.TrimSpace(creds.BalanceUserAgent) != "" {
		headers["User-Agent"] = strings.TrimSpace(creds.BalanceUserAgent)
	}
	return headers
}

func applyBrowserHeaders(req *http.Request, headers map[string]string) {
	for key, value := range headers {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		req.Header.Set(key, value)
	}
}

func (c *Client) userProfileResult(ctx context.Context, u store.Upstream, secret string, now time.Time, profile map[string]any, balance *float64, latency time.Duration, path string, fallbackMessage string) upstream.FetchResult {
	latencyMS := upstream.LatencyMS(latency)
	groupName := firstAllowedGroup(profile)
	if groupName == "" {
		groupName = "default"
	}
	group := upstream.GroupForName(u, groupName)
	callStatus, callLatency, callMessage, callSummary := upstream.CheckOpenAICompatibleCall(ctx, c.HTTP, upstream.ExtractCallCredentials(secret), upstream.CallTestOptionsFromGroup(group))
	status := "available"
	if callStatus != "unknown" {
		status = callStatus
	}
	itemLatency := &latencyMS
	if callLatency != nil {
		itemLatency = callLatency
	}
	availability := floatPtr(100)
	if status != "available" {
		availability = floatPtr(0)
	}
	groupRatio := upstream.ManualRatioForGroup(u, groupName)
	userID := upstream.StringField(profile, "id", "user_id")
	if userID == "" {
		userID = "self"
	}
	name := upstream.StringField(profile, "username", "email", "display_name")
	if name == "" {
		name = "sub2api user " + userID
	}
	balanceMsg := "user balance fetched via " + path
	if strings.TrimSpace(fallbackMessage) != "" {
		balanceMsg = fallbackMessage
	}
	item := store.ItemUpdate{
		UpstreamID:          u.ID,
		UpstreamGroupID:     upstream.RuntimeGroup{Group: group}.IDPtr(),
		ExternalID:          "user:" + userID,
		ItemType:            "account",
		Name:                name,
		Endpoint:            u.BaseURL,
		Source:              "sub2api",
		GroupName:           groupName,
		Ratio:               groupRatio,
		Status:              status,
		LatencyMS:           itemLatency,
		AvailabilityPercent: availability,
		Balance:             balance,
		BalanceUnit:         "USD",
		LastCheckedAt:       now,
		LastMessage:         balanceAndCallMessage(balanceMsg, callMessage),
		Trend:               upstream.TrendValue(itemLatency, status),
		RawSummary:          sanitizeUserProfile(profile, balance, callSummary),
		CheckType:           "balance",
		TestParams:          map[string]any{"endpoint": path, "call_test": callSummary},
	}
	return upstream.FetchResult{
		Items: []store.ItemUpdate{item},
		ConfigSummary: map[string]any{
			"user_profile": map[string]any{
				"group":   groupName,
				"balance": balance,
				"unit":    "USD",
			},
		},
	}
}

func (c *Client) fetchCallOnly(ctx context.Context, u store.Upstream, secret string, now time.Time, accountErr error) (upstream.FetchResult, bool) {
	call := upstream.ExtractCallCredentials(secret)
	if call.URL == "" || call.Key == "" {
		return upstream.FetchResult{}, false
	}

	var items []store.ItemUpdate
	var summaries []map[string]any
	for _, group := range upstream.EnabledGroups(u.Groups) {
		status, latency, message, summary := upstream.CheckOpenAICompatibleCall(ctx, c.HTTP, call, upstream.CallTestOptionsFromGroup(group.Group))
		if status == "unknown" {
			continue
		}
		groupName := group.Name()
		if groupName == "" {
			groupName = "default"
		}
		summary["account_error"] = accountErr.Error()
		summaries = append(summaries, summary)
		items = append(items, store.ItemUpdate{
			UpstreamID:          u.ID,
			UpstreamGroupID:     group.IDPtr(),
			ExternalID:          "user:self",
			ItemType:            "account",
			Name:                u.Name,
			Endpoint:            call.URL,
			Source:              "sub2api",
			GroupName:           groupName,
			Ratio:               upstream.ManualRatioForGroup(u, groupName),
			Status:              status,
			LatencyMS:           latency,
			AvailabilityPercent: availabilityFromStatus(status),
			BalanceUnit:         "USD",
			LastCheckedAt:       now,
			LastMessage:         balanceAndCallMessage("account balance unavailable: "+accountErr.Error(), message),
			Trend:               upstream.TrendValue(latency, status),
			RawSummary:          map[string]any{"call_check": summary},
			CheckType:           "availability",
			TestParams:          map[string]any{"call_test": summary},
		})
	}
	if len(items) == 0 {
		return upstream.FetchResult{}, false
	}
	return upstream.FetchResult{
		Items: items,
		ConfigSummary: map[string]any{
			"call_only":            summaries,
			"admin_accounts_error": accountErr.Error(),
			"account_balance_mode": "unavailable",
		},
	}, true
}

func balanceFromUsage(resp any) *float64 {
	m := asMap(resp)
	if b := upstream.FloatField(m, "balance", "remaining_balance", "remaining", "quota", "credit", "credits", "available", "available_balance", "left_quota", "remain_quota"); b != nil {
		return b
	}
	if total := upstream.FloatField(m, "total", "limit", "quota_limit", "total_quota"); total != nil {
		if used := upstream.FloatField(m, "used", "usage", "current", "total_used", "used_quota"); used != nil {
			remaining := *total - *used
			return &remaining
		}
	}
	for _, key := range []string{"data", "usage", "summary", "quota", "profile", "user"} {
		if child, ok := m[key]; ok {
			if b := balanceFromUsage(child); b != nil {
				return b
			}
		}
	}
	return nil
}

func refreshDataMap(v any) map[string]any {
	m := asMap(v)
	if data, ok := m["data"].(map[string]any); ok {
		return data
	}
	return m
}

func refreshResponseSuccess(v any) (bool, string) {
	m, ok := v.(map[string]any)
	if !ok {
		return true, ""
	}
	if code, ok := upstream.ToFloat(m["code"]); ok && code != 0 {
		return false, upstream.StringField(m, "message", "error", "detail")
	}
	if success, ok := m["success"].(bool); ok && !success {
		return false, upstream.StringField(m, "message", "error", "detail")
	}
	return true, ""
}

func firstAllowedGroup(m map[string]any) string {
	if group := upstream.StringField(m, "group", "group_name"); group != "" {
		return group
	}
	if groups, ok := m["allowed_groups"].([]any); ok && len(groups) > 0 {
		if s, ok := groups[0].(string); ok {
			return s
		}
		if child, ok := groups[0].(map[string]any); ok {
			return upstream.StringField(child, "name", "display_name", "id")
		}
	}
	return ""
}

func floatPtr(v float64) *float64 {
	return &v
}

func balanceAndCallMessage(balanceMsg, callMsg string) string {
	if callMsg == "" || callMsg == "call key not configured" {
		return balanceMsg
	}
	return balanceMsg + "; " + callMsg
}

func monitorItems(u store.Upstream, resp any, now time.Time) []store.ItemUpdate {
	var items []store.ItemUpdate
	for _, monitor := range upstream.AsList(resp) {
		externalID := upstream.StringField(monitor, "id", "monitor_id", "channel_id")
		name := upstream.StringField(monitor, "name", "channel_name", "model", "id")
		if name == "" {
			name = "monitor " + externalID
		}
		status := upstream.StatusFrom(upstream.FirstValue(monitor, "status", "available", "enabled"), "unknown")
		latency := upstream.IntField(monitor, "latency_ms", "latency", "response_time")
		groupName := upstream.StringField(monitor, "group", "group_name")
		items = append(items, store.ItemUpdate{
			UpstreamID:          u.ID,
			ExternalID:          externalID,
			ItemType:            "monitor",
			Name:                name,
			Endpoint:            upstream.StringField(monitor, "endpoint", "url", "base_url"),
			Source:              "sub2api",
			GroupName:           groupName,
			Ratio:               upstream.ManualRatioForGroup(u, groupName),
			Status:              status,
			LatencyMS:           latency,
			AvailabilityPercent: availabilityFromStatus(status),
			LastCheckedAt:       now,
			LastMessage:         upstream.StringField(monitor, "message", "last_message"),
			Trend:               upstream.TrendValue(latency, status),
			RawSummary:          sanitizeAccount(monitor),
			CheckType:           "sync",
			TestParams:          map[string]any{},
		})
	}
	return items
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		if data, ok := m["data"].(map[string]any); ok {
			for k, v := range data {
				if _, exists := m[k]; !exists {
					m[k] = v
				}
			}
		}
		return m
	}
	return map[string]any{}
}

func availabilityFromStatus(status string) *float64 {
	var v float64
	if status == "available" {
		v = 100
	} else if status == "unavailable" || status == "error" {
		v = 0
	} else {
		return nil
	}
	return &v
}

func testParams(g *store.UpstreamGroup) map[string]any {
	if g == nil {
		return map[string]any{}
	}
	return map[string]any{
		"group":    g.Name,
		"model_id": g.TestModel,
		"prompt":   g.TestPrompt,
		"mode":     g.TestMode,
	}
}

func sanitizeAccount(m map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"id", "name", "email", "username", "status", "enabled", "group", "group_name", "balance", "models"} {
		if v, ok := m[key]; ok {
			out[key] = v
		}
	}
	return out
}

func withCallSummary(out map[string]any, callSummary map[string]any) map[string]any {
	if len(callSummary) > 0 {
		out["call_check"] = callSummary
	}
	return out
}

func sanitizeUserProfile(m map[string]any, balance *float64, callSummary map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"id", "username", "email", "role", "status", "allowed_groups", "concurrency", "rpm_limit", "total_recharged"} {
		if v, ok := m[key]; ok {
			out[key] = v
		}
	}
	if balance != nil {
		out["balance"] = *balance
	}
	out["unit"] = "USD"
	if len(callSummary) > 0 {
		out["call_check"] = callSummary
	}
	return out
}
