package newapi

import (
	"context"
	"fmt"
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

func New(httpClient *http.Client) *Client {
	return &Client{HTTP: httpClient}
}

func (c *Client) Fetch(ctx context.Context, u store.Upstream, secret string) (upstream.FetchResult, error) {
	now := time.Now()
	if creds, err := upstream.DecodeBalanceCredentials(secret); err == nil && creds.BalanceAuthType == upstream.BalanceAuthNewAPIAccessToken {
		return c.fetchUserSelfWithAccessToken(ctx, u, secret, creds, now)
	}
	channelsResp, _, _, err := upstream.RequestJSON(ctx, c.HTTP, u, secret, http.MethodGet, "/api/channel/", nil, nil)
	if err != nil {
		return c.fetchUserSelf(ctx, u, secret, now, err)
	}

	var items []store.ItemUpdate
	channels := upstream.AsList(channelsResp)
	for _, channel := range channels {
		externalID := upstream.StringField(channel, "id", "key", "channel_id")
		if externalID == "" {
			externalID = upstream.StringField(channel, "name", "display_name")
		}
		name := upstream.StringField(channel, "name", "display_name", "key")
		if name == "" {
			name = "channel " + externalID
		}
		endpoint := upstream.StringField(channel, "base_url", "base_url_actual", "endpoint", "url")
		if endpoint == "" {
			endpoint = u.BaseURL
		}
		balance := upstream.FloatField(channel, "balance", "remaining_balance", "credit", "quota")
		message := upstream.StringField(channel, "message", "status_message", "test_message")

		for _, group := range upstream.EnabledGroups(u.Groups) {
			groupStatus, groupLatency, groupMessage, callSummary := c.checkGroupCall(ctx, u, secret, group.Group)
			if groupMessage == "call key not configured" && message != "" {
				groupMessage = message
			}
			groupBalance := balance
			var groupRatio *float64
			if group.Group != nil {
				groupRatio = group.Group.ManualRatio
			}

			if externalID != "" {
				if balanceResp, _, _, balErr := upstream.RequestJSON(ctx, c.HTTP, u, secret, http.MethodGet, "/api/channel/update_balance/"+url.PathEscape(externalID), nil, nil); balErr == nil {
					if b := balanceFromResponse(balanceResp); b != nil {
						groupBalance = b
					}
				} else if groupMessage == "" {
					groupMessage = balErr.Error()
				}
			}

			items = append(items, store.ItemUpdate{
				UpstreamID:          u.ID,
				UpstreamGroupID:     group.IDPtr(),
				ExternalID:          externalID,
				ItemType:            "channel",
				Name:                name,
				Endpoint:            endpoint,
				Source:              "new-api",
				GroupName:           group.Name(),
				Ratio:               groupRatio,
				Status:              groupStatus,
				LatencyMS:           groupLatency,
				AvailabilityPercent: availabilityFromStatus(groupStatus),
				Balance:             groupBalance,
				BalanceUnit:         upstream.StringField(channel, "balance_unit", "currency", "unit"),
				LastCheckedAt:       now,
				LastMessage:         groupMessage,
				Trend:               upstream.TrendValue(groupLatency, groupStatus),
				RawSummary:          withCallSummary(sanitizeChannel(channel), callSummary),
				CheckType:           "availability",
				TestParams:          map[string]any{"call_test": callSummary},
			})
		}
	}

	return upstream.FetchResult{Items: items, ConfigSummary: map[string]any{}}, nil
}

func (c *Client) checkGroupCall(ctx context.Context, u store.Upstream, secret string, group *store.UpstreamGroup) (string, *int, string, map[string]any) {
	return upstream.CheckOpenAICompatibleCall(ctx, c.HTTP, upstream.ExtractCallCredentials(secret), upstream.CallTestOptionsFromGroup(group))
}

func (c *Client) fetchUserSelf(ctx context.Context, u store.Upstream, secret string, now time.Time, fallbackErr error) (upstream.FetchResult, error) {
	selfResp, _, latency, err := upstream.RequestJSON(ctx, c.HTTP, u, secret, http.MethodGet, "/api/user/self", nil, nil)
	if err != nil {
		if result, ok := c.fetchCallOnly(ctx, u, secret, now, fmt.Errorf("new-api channel admin failed: %w; user self failed: %v", fallbackErr, err)); ok {
			return result, nil
		}
		return upstream.FetchResult{}, fmt.Errorf("new-api channel admin failed: %w; user self failed: %v", fallbackErr, err)
	}

	self := asMap(selfResp)
	remaining := quotaToUSD(upstream.FloatField(self, "quota"))
	used := quotaToUSD(upstream.FloatField(self, "used_quota"))
	total := sumFloatPtrs(remaining, used)
	latencyMS := upstream.LatencyMS(latency)
	groupName := upstream.StringField(self, "group")
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
	userID := upstream.StringField(self, "id", "user_id")
	if userID == "" {
		if creds, err := upstream.DecodeNewAPISessionCredentials(secret); err == nil {
			userID = creds.UserID
		} else if creds, err := upstream.DecodeNewAPITokenCredentials(secret); err == nil {
			userID = creds.UserID
		}
	}
	if userID == "" {
		userID = "self"
	}
	name := upstream.StringField(self, "username", "display_name", "email")
	if name == "" {
		name = "new-api user " + userID
	}

	item := store.ItemUpdate{
		UpstreamID:          u.ID,
		UpstreamGroupID:     upstream.RuntimeGroup{Group: group}.IDPtr(),
		ExternalID:          "user:" + userID,
		ItemType:            "account",
		Name:                name,
		Endpoint:            u.BaseURL,
		Source:              "new-api",
		GroupName:           groupName,
		Ratio:               groupRatio,
		Status:              status,
		LatencyMS:           itemLatency,
		AvailabilityPercent: availability,
		Balance:             remaining,
		BalanceUnit:         "USD",
		LastCheckedAt:       now,
		LastMessage:         balanceAndCallMessage("user balance fetched via /api/user/self", callMessage),
		Trend:               upstream.TrendValue(itemLatency, status),
		RawSummary:          sanitizeUserSelf(self, remaining, used, total, groupRatio, callSummary),
		CheckType:           "balance",
		TestParams:          map[string]any{"endpoint": "/api/user/self", "call_test": callSummary},
	}

	return upstream.FetchResult{
		Items: []store.ItemUpdate{item},
		ConfigSummary: map[string]any{
			"user_self": map[string]any{
				"group":     groupName,
				"ratio":     groupRatio,
				"remaining": remaining,
				"used":      used,
				"total":     total,
				"unit":      "USD",
			},
			"channel_admin_error": fallbackErr.Error(),
		},
	}, nil
}

func (c *Client) fetchUserSelfWithAccessToken(ctx context.Context, u store.Upstream, secret string, creds upstream.BalanceCredentials, now time.Time) (upstream.FetchResult, error) {
	runtime := u
	runtime.AuthType = store.AuthNewAPIToken
	tokenSecret, err := upstream.EncodeNewAPITokenCredentialsWithCall(creds.BalanceAccessToken, creds.BalanceUserID, creds.CallURL, creds.CallKey)
	if err != nil {
		return upstream.FetchResult{}, err
	}
	selfResp, _, latency, err := upstream.RequestJSON(ctx, c.HTTP, runtime, tokenSecret, http.MethodGet, "/api/user/self", nil, nil)
	if err != nil {
		if result, ok := c.fetchCallOnly(ctx, u, secret, now, fmt.Errorf("balance credential invalid: %w", err)); ok {
			return result, nil
		}
		return upstream.FetchResult{}, fmt.Errorf("balance credential invalid: %w", err)
	}
	return c.userSelfResult(ctx, u, secret, now, selfResp, latency, "")
}

func (c *Client) userSelfResult(ctx context.Context, u store.Upstream, secret string, now time.Time, selfResp any, latency time.Duration, fallbackMessage string) (upstream.FetchResult, error) {
	self := asMap(selfResp)
	remaining := quotaToUSD(upstream.FloatField(self, "quota"))
	used := quotaToUSD(upstream.FloatField(self, "used_quota"))
	total := sumFloatPtrs(remaining, used)
	latencyMS := upstream.LatencyMS(latency)
	groupName := upstream.StringField(self, "group", "planName", "plan_name")
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
	userID := upstream.StringField(self, "id", "user_id")
	if userID == "" {
		if creds, err := upstream.DecodeBalanceCredentials(secret); err == nil {
			userID = creds.BalanceUserID
		} else if creds, err := upstream.DecodeNewAPISessionCredentials(secret); err == nil {
			userID = creds.UserID
		} else if creds, err := upstream.DecodeNewAPITokenCredentials(secret); err == nil {
			userID = creds.UserID
		}
	}
	if userID == "" {
		userID = "self"
	}
	name := upstream.StringField(self, "username", "display_name", "email")
	if name == "" {
		name = "new-api user " + userID
	}

	balanceMsg := "user balance fetched via /api/user/self"
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
		Source:              "new-api",
		GroupName:           groupName,
		Ratio:               groupRatio,
		Status:              status,
		LatencyMS:           itemLatency,
		AvailabilityPercent: availability,
		Balance:             remaining,
		BalanceUnit:         "USD",
		LastCheckedAt:       now,
		LastMessage:         balanceAndCallMessage(balanceMsg, callMessage),
		Trend:               upstream.TrendValue(itemLatency, status),
		RawSummary:          sanitizeUserSelf(self, remaining, used, total, groupRatio, callSummary),
		CheckType:           "balance",
		TestParams:          map[string]any{"endpoint": "/api/user/self", "call_test": callSummary},
	}
	config := map[string]any{
		"user_self": map[string]any{
			"group":     groupName,
			"ratio":     groupRatio,
			"remaining": remaining,
			"used":      used,
			"total":     total,
			"unit":      "USD",
		},
	}
	return upstream.FetchResult{Items: []store.ItemUpdate{item}, ConfigSummary: config}, nil
}

func (c *Client) fetchCallOnly(ctx context.Context, u store.Upstream, secret string, now time.Time, accountErr error) (upstream.FetchResult, bool) {
	call := upstream.ExtractCallCredentials(secret)
	if call.URL == "" || call.Key == "" {
		return upstream.FetchResult{}, false
	}

	userID := "self"
	if creds, err := upstream.DecodeNewAPISessionCredentials(secret); err == nil && creds.UserID != "" {
		userID = creds.UserID
	} else if creds, err := upstream.DecodeNewAPITokenCredentials(secret); err == nil && creds.UserID != "" {
		userID = creds.UserID
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
			ExternalID:          "user:" + userID,
			ItemType:            "account",
			Name:                u.Name,
			Endpoint:            call.URL,
			Source:              "new-api",
			GroupName:           groupName,
			Ratio:               upstream.ManualRatioForGroup(u, groupName),
			Status:              status,
			LatencyMS:           latency,
			AvailabilityPercent: availabilityFromStatus(status),
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
			"channel_admin_error":  accountErr.Error(),
			"account_balance_mode": "unavailable",
		},
	}, true
}

func balanceFromResponse(resp any) *float64 {
	m := asMap(resp)
	if b := upstream.FloatField(m, "balance", "remaining_balance", "credit", "quota", "available", "available_balance"); b != nil {
		return b
	}
	for _, key := range []string{"data", "channel", "result"} {
		if child, ok := m[key]; ok {
			if b := balanceFromResponse(child); b != nil {
				return b
			}
		}
	}
	return nil
}

func quotaToUSD(v *float64) *float64 {
	if v == nil {
		return nil
	}
	out := *v / 500000
	return &out
}

func sumFloatPtrs(values ...*float64) *float64 {
	var sum float64
	var ok bool
	for _, v := range values {
		if v != nil {
			sum += *v
			ok = true
		}
	}
	if !ok {
		return nil
	}
	return &sum
}

func floatPtr(v float64) *float64 {
	return &v
}

func pickLatency(v any, fallback time.Duration) *int {
	m := asMap(v)
	if latency := upstream.IntField(m, "latency_ms", "latency", "response_time", "time_ms"); latency != nil {
		return latency
	}
	l := upstream.LatencyMS(fallback)
	return &l
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
		"group":         g.Name,
		"model":         g.TestModel,
		"endpoint_type": g.TestEndpointType,
		"stream":        g.TestStream,
	}
}

func sanitizeChannel(m map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"id", "name", "type", "status", "enabled", "models", "groups", "base_url", "balance"} {
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

func balanceAndCallMessage(balanceMsg, callMsg string) string {
	if callMsg == "" || callMsg == "call key not configured" {
		return balanceMsg
	}
	return balanceMsg + "; " + callMsg
}

func sanitizeUserSelf(m map[string]any, remaining, used, total, ratio *float64, callSummary map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"id", "username", "display_name", "email", "group", "status", "request_count"} {
		if v, ok := m[key]; ok {
			out[key] = v
		}
	}
	if remaining != nil {
		out["remaining"] = *remaining
	}
	if used != nil {
		out["used"] = *used
	}
	if total != nil {
		out["total"] = *total
	}
	if ratio != nil {
		out["ratio"] = *ratio
	}
	out["unit"] = "USD"
	if len(callSummary) > 0 {
		out["call_check"] = callSummary
	}
	return out
}
