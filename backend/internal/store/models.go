package store

import (
	"encoding/json"
	"time"
)

type UpstreamKind string

const (
	KindNewAPI  UpstreamKind = "new_api"
	KindSub2API UpstreamKind = "sub2api"
)

type AuthType string

const (
	AuthBearer        AuthType = "bearer"
	AuthCookie        AuthType = "cookie"
	AuthAdminAPIKey   AuthType = "admin_api_key"
	AuthPassword      AuthType = "password"
	AuthNewAPIToken   AuthType = "new_api_token"
	AuthNewAPISession AuthType = "new_api_session"
	AuthXAPIKey       AuthType = "x_api_key"
	AuthNewAPIAccess  AuthType = "newapi_access_token"
	AuthSub2Refresh   AuthType = "sub2api_refresh_token"
)

type Upstream struct {
	ID                   int64           `json:"id"`
	Name                 string          `json:"name"`
	Kind                 UpstreamKind    `json:"kind"`
	BaseURL              string          `json:"base_url"`
	AuthType             AuthType        `json:"auth_type"`
	AuthSecretCiphertext string          `json:"-"`
	AuthSecretNonce      string          `json:"-"`
	AuthSecretMasked     string          `json:"auth_secret_masked"`
	Enabled              bool            `json:"enabled"`
	PollIntervalSeconds  int             `json:"poll_interval_seconds"`
	LastPolledAt         *time.Time      `json:"last_polled_at,omitempty"`
	LastError            string          `json:"last_error,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	Groups               []UpstreamGroup `json:"groups,omitempty"`
}

type UpstreamGroup struct {
	ID               int64     `json:"id"`
	UpstreamID       int64     `json:"upstream_id"`
	Name             string    `json:"name"`
	DisplayName      string    `json:"display_name,omitempty"`
	ManualRatio      *float64  `json:"manual_ratio,omitempty"`
	TestModel        string    `json:"test_model,omitempty"`
	TestEndpointType string    `json:"test_endpoint_type,omitempty"`
	TestPrompt       string    `json:"test_prompt,omitempty"`
	TestMode         string    `json:"test_mode,omitempty"`
	TestStream       bool      `json:"test_stream"`
	Enabled          bool      `json:"enabled"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type MonitorItem struct {
	ID                  int64           `json:"id"`
	UpstreamID          int64           `json:"upstream_id"`
	UpstreamGroupID     *int64          `json:"upstream_group_id,omitempty"`
	ExternalID          string          `json:"external_id"`
	ItemType            string          `json:"item_type"`
	Name                string          `json:"name"`
	Endpoint            string          `json:"endpoint"`
	Source              string          `json:"source"`
	GroupName           string          `json:"group_name"`
	Ratio               *string         `json:"ratio,omitempty"`
	Status              string          `json:"status"`
	LatencyMS           *int            `json:"latency_ms,omitempty"`
	AvailabilityPercent *string         `json:"availability_percent,omitempty"`
	Balance             *string         `json:"balance,omitempty"`
	BalanceUnit         string          `json:"balance_unit,omitempty"`
	LastCheckedAt       *time.Time      `json:"last_checked_at,omitempty"`
	LastMessage         string          `json:"last_message,omitempty"`
	Trend               json.RawMessage `json:"trend,omitempty"`
	RawSummary          json.RawMessage `json:"raw_summary,omitempty"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

type ItemUpdate struct {
	UpstreamID          int64
	UpstreamGroupID     *int64
	ExternalID          string
	ItemType            string
	Name                string
	Endpoint            string
	Source              string
	GroupName           string
	Ratio               *float64
	Status              string
	LatencyMS           *int
	AvailabilityPercent *float64
	Balance             *float64
	BalanceUnit         string
	LastCheckedAt       time.Time
	LastMessage         string
	Trend               any
	RawSummary          any
	CheckType           string
	TestParams          any
}

type DashboardSummary struct {
	Total           int64      `json:"total"`
	Available       int64      `json:"available"`
	Unavailable     int64      `json:"unavailable"`
	AvgLatencyMS    *float64   `json:"avg_latency_ms"`
	LastRefreshedAt *time.Time `json:"last_refreshed_at,omitempty"`
}

type Dashboard struct {
	Summary DashboardSummary `json:"summary"`
	Items   []MonitorItem    `json:"items"`
}

type UpsertConfigSnapshotParams struct {
	UpstreamID    int64
	ConfigHash    string
	ConfigSummary any
	DiffSummary   any
	Changed       bool
	CheckedAt     time.Time
}
