package httpapi

import (
	"context"
	"errors"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"xi_monitor/backend/internal/auth"
	"xi_monitor/backend/internal/crypto"
	"xi_monitor/backend/internal/poller"
	"xi_monitor/backend/internal/store"
	"xi_monitor/backend/internal/upstream"
)

type Store interface {
	CreateUpstream(context.Context, store.Upstream, []store.UpstreamGroup) (store.Upstream, error)
	ListUpstreams(context.Context) ([]store.Upstream, error)
	GetUpstream(context.Context, int64) (store.Upstream, error)
	UpdateUpstream(context.Context, store.Upstream, *[]store.UpstreamGroup) (store.Upstream, error)
	SetUpstreamGroupRatio(context.Context, int64, string, *float64) (store.UpstreamGroup, error)
	DeleteUpstream(context.Context, int64) error
	GetDashboard(context.Context) (store.Dashboard, error)
	GetMonitorItem(context.Context, int64) (store.MonitorItem, error)
}

type Poller interface {
	RefreshAll(context.Context) poller.RefreshStatus
	RefreshUpstream(context.Context, int64) error
	RefreshItem(context.Context, store.MonitorItem) error
}

type Server struct {
	auth   *auth.Service
	box    *crypto.SecretBox
	store  Store
	poller Poller
}

func NewRouter(authSvc *auth.Service, box *crypto.SecretBox, st Store, pl Poller) *gin.Engine {
	s := &Server{auth: authSvc, box: box, store: st, poller: pl}
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	api := r.Group("/api")
	api.POST("/auth/login", s.login)
	api.POST("/auth/logout", func(c *gin.Context) { ok(c, gin.H{}) })

	protected := api.Group("")
	protected.Use(s.authMiddleware())
	protected.GET("/auth/me", s.me)
	protected.GET("/dashboard", s.dashboard)
	protected.POST("/dashboard/refresh", s.refreshDashboard)
	protected.POST("/items/:id/refresh", s.refreshItem)
	protected.POST("/upstreams", s.createUpstream)
	protected.GET("/upstreams", s.listUpstreams)
	protected.PUT("/upstreams/:id", s.updateUpstream)
	protected.DELETE("/upstreams/:id", s.deleteUpstream)
	protected.POST("/upstreams/:id/refresh", s.refreshUpstream)
	protected.POST("/upstreams/:id/test", s.testUpstream)
	protected.PUT("/upstreams/:id/groups/:group_name/ratio", s.setGroupRatio)

	return r
}

func ServeFrontend(r *gin.Engine, distDir string) {
	distDir = strings.TrimSpace(distDir)
	if distDir == "" {
		return
	}

	fs := http.Dir(distDir)
	fileServer := http.FileServer(fs)
	r.NoRoute(func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			fail(c, http.StatusNotFound, "not found")
			return
		}
		if c.Request.URL.Path == "/api" || strings.HasPrefix(c.Request.URL.Path, "/api/") {
			fail(c, http.StatusNotFound, "not found")
			return
		}

		cleanPath := strings.TrimPrefix(path.Clean(c.Request.URL.Path), "/")
		if cleanPath != "" && cleanPath != "." {
			if f, err := fs.Open(cleanPath); err == nil {
				defer f.Close()
				if stat, err := f.Stat(); err == nil && !stat.IsDir() {
					fileServer.ServeHTTP(c.Writer, c.Request)
					return
				}
			}
		}

		c.File(filepath.Join(distDir, "index.html"))
	})
}

func (s *Server) login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request")
		return
	}
	token, err := s.auth.Login(req.Username, req.Password)
	if err != nil {
		fail(c, http.StatusUnauthorized, "invalid username or password")
		return
	}
	ok(c, gin.H{"token": token})
}

func (s *Server) me(c *gin.Context) {
	username, _ := c.Get("username")
	ok(c, gin.H{"username": username})
}

func (s *Server) dashboard(c *gin.Context) {
	d, err := s.store.GetDashboard(c.Request.Context())
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	ok(c, d)
}

func (s *Server) refreshDashboard(c *gin.Context) {
	status := s.poller.RefreshAll(context.Background())
	ok(c, status)
}

func (s *Server) refreshUpstream(c *gin.Context) {
	id, okID := parseID(c, "id")
	if !okID {
		return
	}
	go s.poller.RefreshUpstream(context.Background(), id)
	ok(c, gin.H{"running": true, "upstream_id": id})
}

func (s *Server) refreshItem(c *gin.Context) {
	id, okID := parseID(c, "id")
	if !okID {
		return
	}
	item, err := s.store.GetMonitorItem(c.Request.Context(), id)
	if err != nil {
		handleStoreErr(c, err)
		return
	}
	go s.poller.RefreshItem(context.Background(), item)
	ok(c, gin.H{"running": true, "item_id": id})
}

func (s *Server) createUpstream(c *gin.Context) {
	var req upstreamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request")
		return
	}
	u, groups, err := s.buildCreate(req)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	created, err := s.store.CreateUpstream(c.Request.Context(), u, groups)
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	go s.poller.RefreshUpstream(context.Background(), created.ID)
	ok(c, created)
}

func (s *Server) listUpstreams(c *gin.Context) {
	upstreams, err := s.store.ListUpstreams(c.Request.Context())
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	ok(c, upstreams)
}

func (s *Server) updateUpstream(c *gin.Context) {
	id, okID := parseID(c, "id")
	if !okID {
		return
	}
	current, err := s.store.GetUpstream(c.Request.Context(), id)
	if err != nil {
		handleStoreErr(c, err)
		return
	}

	var req upstreamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request")
		return
	}

	updated, groups, err := s.buildUpdate(current, req)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	updatedUpstream, err := s.store.UpdateUpstream(c.Request.Context(), updated, groups)
	if err != nil {
		handleStoreErr(c, err)
		return
	}
	ok(c, updatedUpstream)
}

func (s *Server) deleteUpstream(c *gin.Context) {
	id, okID := parseID(c, "id")
	if !okID {
		return
	}
	if err := s.store.DeleteUpstream(c.Request.Context(), id); err != nil {
		handleStoreErr(c, err)
		return
	}
	ok(c, gin.H{"deleted": true})
}

func (s *Server) testUpstream(c *gin.Context) {
	id, okID := parseID(c, "id")
	if !okID {
		return
	}
	err := s.poller.RefreshUpstream(context.Background(), id)
	if err != nil {
		fail(c, http.StatusBadGateway, err.Error())
		return
	}
	ok(c, gin.H{"ok": true})
}

func (s *Server) setGroupRatio(c *gin.Context) {
	id, okID := parseID(c, "id")
	if !okID {
		return
	}
	groupName := strings.TrimSpace(c.Param("group_name"))
	if groupName == "" {
		fail(c, http.StatusBadRequest, "group_name is required")
		return
	}
	var req groupRatioRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Ratio != nil && *req.Ratio <= 0 {
		fail(c, http.StatusBadRequest, "ratio must be positive")
		return
	}
	group, err := s.store.SetUpstreamGroupRatio(c.Request.Context(), id, groupName, req.Ratio)
	if err != nil {
		handleStoreErr(c, err)
		return
	}
	ok(c, group)
}

func (s *Server) buildCreate(req upstreamRequest) (store.Upstream, []store.UpstreamGroup, error) {
	u, groups, err := normalizeUpstream(req)
	if err != nil {
		return store.Upstream{}, nil, err
	}
	secret, masked, err := credentialSecret(req, true)
	if err != nil {
		return store.Upstream{}, nil, err
	}
	ciphertext, nonce, err := s.box.Encrypt(secret)
	if err != nil {
		return store.Upstream{}, nil, err
	}
	u.AuthSecretCiphertext = ciphertext
	u.AuthSecretNonce = nonce
	u.AuthSecretMasked = masked
	return u, groups, nil
}

func (s *Server) buildUpdate(current store.Upstream, req upstreamRequest) (store.Upstream, *[]store.UpstreamGroup, error) {
	req = canonicalizeUpstreamUpdateRequest(req)
	u := current
	if strings.TrimSpace(req.Name) != "" {
		u.Name = strings.TrimSpace(req.Name)
	}
	if strings.TrimSpace(req.Kind) != "" {
		u.Kind = store.UpstreamKind(strings.TrimSpace(req.Kind))
	}
	if strings.TrimSpace(req.BaseURL) != "" {
		u.BaseURL = strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	}
	if strings.TrimSpace(req.AuthType) != "" {
		u.AuthType = store.AuthType(strings.TrimSpace(req.AuthType))
	}
	if req.PollIntervalSeconds != nil {
		u.PollIntervalSeconds = *req.PollIntervalSeconds
	}
	if req.Enabled != nil {
		u.Enabled = *req.Enabled
	}
	if err := validateUpstream(u); err != nil {
		return store.Upstream{}, nil, err
	}
	if shouldAttachCallCredentialsOnly(req) {
		secret, err := s.box.Decrypt(current.AuthSecretCiphertext, current.AuthSecretNonce)
		if err != nil {
			return store.Upstream{}, nil, err
		}
		callURL, callKey := callCredentialsFromRequest(req)
		secret, err = upstream.AttachCallCredentials(u.AuthType, secret, upstream.CallCredentials{URL: callURL, Key: callKey})
		if err != nil {
			return store.Upstream{}, nil, err
		}
		ciphertext, nonce, err := s.box.Encrypt(secret)
		if err != nil {
			return store.Upstream{}, nil, err
		}
		u.AuthSecretCiphertext = ciphertext
		u.AuthSecretNonce = nonce
		u.AuthSecretMasked = appendCallMask(current.AuthSecretMasked, callKey)
	} else if shouldUpdateCredential(req) {
		secret, masked, err := credentialSecret(req, false)
		if err != nil {
			return store.Upstream{}, nil, err
		}
		ciphertext, nonce, err := s.box.Encrypt(secret)
		if err != nil {
			return store.Upstream{}, nil, err
		}
		u.AuthSecretCiphertext = ciphertext
		u.AuthSecretNonce = nonce
		u.AuthSecretMasked = masked
	} else {
		u.AuthSecretCiphertext = ""
		u.AuthSecretNonce = ""
	}

	if req.Groups == nil {
		return u, nil, nil
	}
	groups := normalizeGroups(*req.Groups)
	if len(groups) == 0 {
		return store.Upstream{}, nil, errors.New("at least one group is required when groups is provided")
	}
	return u, &groups, nil
}

func normalizeUpstream(req upstreamRequest) (store.Upstream, []store.UpstreamGroup, error) {
	req = canonicalizeUpstreamRequest(req)
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	interval := 1800
	if req.PollIntervalSeconds != nil && *req.PollIntervalSeconds > 0 {
		interval = *req.PollIntervalSeconds
	}
	u := store.Upstream{
		Name:                strings.TrimSpace(req.Name),
		Kind:                store.UpstreamKind(strings.TrimSpace(req.Kind)),
		BaseURL:             strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"),
		AuthType:            store.AuthType(strings.TrimSpace(req.AuthType)),
		Enabled:             enabled,
		PollIntervalSeconds: interval,
	}
	if err := validateUpstream(u); err != nil {
		return store.Upstream{}, nil, err
	}
	groups := normalizeGroups(valueOrEmpty(req.Groups))
	if len(groups) == 0 {
		return store.Upstream{}, nil, errors.New("at least one group is required")
	}
	return u, groups, nil
}

func canonicalizeUpstreamRequest(req upstreamRequest) upstreamRequest {
	if strings.TrimSpace(req.BaseURL) == "" {
		req.BaseURL = req.URL
	}
	if strings.TrimSpace(req.AuthType) == "" {
		if strings.TrimSpace(req.BalanceAuthType) != "" {
			req.AuthType = req.BalanceAuthType
		} else if strings.TrimSpace(req.AuthUsername) != "" || req.AuthPassword != "" {
			req.AuthType = string(store.AuthPassword)
		} else if strings.TrimSpace(req.APIKey) != "" {
			req.AuthType = string(store.AuthXAPIKey)
		} else if strings.TrimSpace(req.BalanceRefreshToken) != "" {
			req.AuthType = upstream.BalanceAuthSub2APIRefreshToken
		} else if strings.TrimSpace(req.BalanceAccessToken) != "" || strings.TrimSpace(req.AccessToken) != "" {
			req.AuthType = upstream.BalanceAuthNewAPIAccessToken
		} else {
			switch strings.TrimSpace(req.Kind) {
			case string(store.KindNewAPI):
				req.AuthType = upstream.BalanceAuthNewAPIAccessToken
			case string(store.KindSub2API):
				req.AuthType = upstream.BalanceAuthSub2APIRefreshToken
			default:
				req.AuthType = string(store.AuthBearer)
			}
		}
	}
	if strings.TrimSpace(req.AuthSecret) == "" {
		req.AuthSecret = req.Secret
	}
	if strings.TrimSpace(req.Name) == "" {
		req.Name = defaultNameFromURL(req.BaseURL)
	}
	if req.Groups == nil {
		req.Groups = &[]groupRequest{{Name: "default", DisplayName: "default", Enabled: boolPtr(true)}}
	}
	return req
}

func canonicalizeUpstreamUpdateRequest(req upstreamRequest) upstreamRequest {
	if strings.TrimSpace(req.BaseURL) == "" {
		req.BaseURL = req.URL
	}
	if strings.TrimSpace(req.AuthSecret) == "" {
		req.AuthSecret = req.Secret
	}
	if strings.TrimSpace(req.AuthType) == "" {
		switch {
		case strings.TrimSpace(req.BalanceAuthType) != "":
			req.AuthType = req.BalanceAuthType
		case strings.TrimSpace(req.AuthUsername) != "" || req.AuthPassword != "":
			req.AuthType = string(store.AuthPassword)
		case strings.TrimSpace(req.BalanceAccessToken) != "" || strings.TrimSpace(req.AccessToken) != "":
			req.AuthType = upstream.BalanceAuthNewAPIAccessToken
		case strings.TrimSpace(req.BalanceRefreshToken) != "":
			req.AuthType = upstream.BalanceAuthSub2APIRefreshToken
		case strings.TrimSpace(req.APIKey) != "":
			req.AuthType = string(store.AuthXAPIKey)
		}
	}
	return req
}

func validateUpstream(u store.Upstream) error {
	if u.Name == "" {
		return errors.New("name is required")
	}
	if u.BaseURL == "" {
		return errors.New("base_url is required")
	}
	if u.Kind != store.KindNewAPI && u.Kind != store.KindSub2API {
		return errors.New("kind must be new_api or sub2api")
	}
	if u.AuthType != store.AuthBearer && u.AuthType != store.AuthCookie && u.AuthType != store.AuthAdminAPIKey && u.AuthType != store.AuthPassword && u.AuthType != store.AuthNewAPIToken && u.AuthType != store.AuthNewAPISession && u.AuthType != store.AuthXAPIKey && u.AuthType != store.AuthType(upstream.BalanceAuthNewAPIAccessToken) && u.AuthType != store.AuthType(upstream.BalanceAuthSub2APIRefreshToken) {
		return errors.New("auth_type must be bearer, cookie, admin_api_key, password, new_api_token, new_api_session, x_api_key, newapi_access_token, or sub2api_refresh_token")
	}
	if u.PollIntervalSeconds <= 0 {
		return errors.New("poll_interval_seconds must be positive")
	}
	return nil
}

func shouldUpdateCredential(req upstreamRequest) bool {
	return strings.TrimSpace(req.AuthSecret) != "" ||
		strings.TrimSpace(req.Secret) != "" ||
		strings.TrimSpace(req.AccessToken) != "" ||
		strings.TrimSpace(req.UserID) != "" ||
		strings.TrimSpace(req.BalanceAuthType) != "" ||
		strings.TrimSpace(req.BalanceUserID) != "" ||
		strings.TrimSpace(req.BalanceAccessToken) != "" ||
		strings.TrimSpace(req.BalanceRefreshToken) != "" ||
		strings.TrimSpace(req.APIKey) != "" ||
		strings.TrimSpace(req.Key) != "" ||
		strings.TrimSpace(req.CallURL) != "" ||
		strings.TrimSpace(req.CallKey) != "" ||
		strings.TrimSpace(req.AuthUsername) != "" ||
		req.AuthPassword != ""
}

func shouldAttachCallCredentialsOnly(req upstreamRequest) bool {
	if strings.TrimSpace(req.CallKey) == "" && strings.TrimSpace(req.Key) == "" {
		return false
	}
	return strings.TrimSpace(req.AuthSecret) == "" &&
		strings.TrimSpace(req.Secret) == "" &&
		strings.TrimSpace(req.AccessToken) == "" &&
		strings.TrimSpace(req.UserID) == "" &&
		strings.TrimSpace(req.BalanceAuthType) == "" &&
		strings.TrimSpace(req.BalanceUserID) == "" &&
		strings.TrimSpace(req.BalanceAccessToken) == "" &&
		strings.TrimSpace(req.BalanceRefreshToken) == "" &&
		strings.TrimSpace(req.APIKey) == "" &&
		strings.TrimSpace(req.AuthUsername) == "" &&
		req.AuthPassword == ""
}

func appendCallMask(masked, callKey string) string {
	masked = strings.TrimSpace(masked)
	if strings.TrimSpace(callKey) == "" {
		return masked
	}
	callMask := "call:" + crypto.MaskSecret(callKey)
	if strings.Contains(masked, "call:") {
		parts := strings.Fields(masked)
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if strings.HasPrefix(part, "call:") {
				out = append(out, callMask)
			} else {
				out = append(out, part)
			}
		}
		return strings.Join(out, " ")
	}
	if masked == "" {
		return callMask
	}
	return masked + " " + callMask
}

func credentialSecret(req upstreamRequest, required bool) (string, string, error) {
	req = canonicalizeUpstreamRequest(req)
	switch strings.TrimSpace(req.AuthType) {
	case upstream.BalanceAuthNewAPIAccessToken:
		if strings.TrimSpace(req.BalanceAccessToken) == "" {
			req.BalanceAccessToken = req.AccessToken
		}
		if strings.TrimSpace(req.BalanceAccessToken) == "" && strings.TrimSpace(req.AuthSecret) != "" {
			req.BalanceAccessToken = req.AuthSecret
		}
		if strings.TrimSpace(req.BalanceUserID) == "" {
			req.BalanceUserID = req.UserID
		}
		callURL, callKey := callCredentialsFromRequest(req)
		secret, err := upstream.EncodeBalanceCredentials(upstream.BalanceCredentials{
			BalanceAuthType:    upstream.BalanceAuthNewAPIAccessToken,
			BalanceUserID:      req.BalanceUserID,
			BalanceAccessToken: req.BalanceAccessToken,
			CallURL:            callURL,
			CallKey:            callKey,
		})
		if err != nil {
			if required || shouldUpdateCredential(req) {
				return "", "", err
			}
			return "", "", nil
		}
		return secret, upstream.MaskBalanceCredentials(secret), nil
	case upstream.BalanceAuthSub2APIRefreshToken:
		if strings.TrimSpace(req.BalanceAccessToken) == "" {
			req.BalanceAccessToken = req.AccessToken
		}
		callURL, callKey := callCredentialsFromRequest(req)
		secret, err := upstream.EncodeBalanceCredentials(upstream.BalanceCredentials{
			BalanceAuthType:       upstream.BalanceAuthSub2APIRefreshToken,
			BalanceAccessToken:    req.BalanceAccessToken,
			BalanceRefreshToken:   req.BalanceRefreshToken,
			BalanceTokenExpiresAt: req.BalanceTokenExpiresAt,
			CallURL:               callURL,
			CallKey:               callKey,
		})
		if err != nil {
			if required || shouldUpdateCredential(req) {
				return "", "", err
			}
			return "", "", nil
		}
		return secret, upstream.MaskBalanceCredentials(secret), nil
	case string(store.AuthNewAPIToken):
		if strings.TrimSpace(req.AccessToken) == "" && strings.TrimSpace(req.AuthSecret) != "" {
			req.AccessToken = req.AuthSecret
		}
		callURL, callKey := callCredentialsFromRequest(req)
		secret, err := upstream.EncodeNewAPITokenCredentialsWithCall(req.AccessToken, req.UserID, callURL, callKey)
		if err != nil {
			if required || shouldUpdateCredential(req) {
				return "", "", err
			}
			return "", "", nil
		}
		masked := "user:" + strings.TrimSpace(req.UserID) + " " + crypto.MaskSecret(req.AccessToken)
		if callKey != "" {
			masked += " call:" + crypto.MaskSecret(callKey)
		}
		return secret, masked, nil
	case string(store.AuthXAPIKey):
		if strings.TrimSpace(req.APIKey) == "" && strings.TrimSpace(req.AuthSecret) != "" {
			req.APIKey = req.AuthSecret
		}
		callURL, callKey := callCredentialsFromRequest(req)
		secret, err := upstream.EncodeAPIKeyCredentialsWithCall(req.APIKey, callURL, callKey)
		if err != nil {
			if required || shouldUpdateCredential(req) {
				return "", "", err
			}
			return "", "", nil
		}
		masked := crypto.MaskSecret(req.APIKey)
		if callKey != "" {
			masked += " call:" + crypto.MaskSecret(callKey)
		}
		return secret, masked, nil
	case string(store.AuthPassword):
		callURL, callKey := callCredentialsFromRequest(req)
		secret, err := upstream.EncodeBalanceCredentials(upstream.BalanceCredentials{
			BalanceAuthType: upstream.BalanceAuthPassword,
			AuthUsername:    req.AuthUsername,
			AuthPassword:    req.AuthPassword,
			CallURL:         callURL,
			CallKey:         callKey,
		})
		if err != nil {
			if required || shouldUpdateCredential(req) {
				return "", "", err
			}
			return "", "", nil
		}
		return secret, upstream.MaskBalanceCredentials(secret), nil
	}
	if strings.TrimSpace(req.AuthSecret) == "" {
		if required {
			return "", "", errors.New("auth_secret is required")
		}
		return "", "", nil
	}
	return req.AuthSecret, crypto.MaskSecret(req.AuthSecret), nil
}

func callCredentialsFromRequest(req upstreamRequest) (string, string) {
	callURL := strings.TrimRight(strings.TrimSpace(req.CallURL), "/")
	if callURL == "" {
		callURL = strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	}
	callKey := strings.TrimSpace(req.CallKey)
	if callKey == "" {
		callKey = strings.TrimSpace(req.Key)
	}
	if callKey == "" && strings.TrimSpace(req.AuthType) == string(store.AuthPassword) {
		callKey = strings.TrimSpace(req.APIKey)
	}
	if callKey == "" || callURL == "" {
		return "", ""
	}
	return callURL, callKey
}

func defaultNameFromURL(rawURL string) string {
	name := strings.TrimSpace(rawURL)
	name = strings.TrimPrefix(name, "https://")
	name = strings.TrimPrefix(name, "http://")
	name = strings.TrimRight(name, "/")
	if name == "" {
		return ""
	}
	if idx := strings.Index(name, "/"); idx >= 0 {
		name = name[:idx]
	}
	return name
}

func boolPtr(v bool) *bool {
	return &v
}

func normalizeGroups(reqs []groupRequest) []store.UpstreamGroup {
	groups := make([]store.UpstreamGroup, 0, len(reqs))
	seen := map[string]bool{}
	for _, req := range reqs {
		name := strings.TrimSpace(req.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		groups = append(groups, store.UpstreamGroup{
			Name:             name,
			DisplayName:      strings.TrimSpace(req.DisplayName),
			ManualRatio:      req.ManualRatio,
			TestModel:        strings.TrimSpace(req.TestModel),
			TestEndpointType: strings.TrimSpace(req.TestEndpointType),
			TestPrompt:       req.TestPrompt,
			TestMode:         strings.TrimSpace(req.TestMode),
			TestStream:       req.TestStream,
			Enabled:          enabled,
		})
	}
	return groups
}

func (s *Server) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		tokenText := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		if tokenText == "" {
			fail(c, http.StatusUnauthorized, "missing token")
			c.Abort()
			return
		}
		claims, err := s.auth.Parse(tokenText)
		if err != nil {
			fail(c, http.StatusUnauthorized, "invalid token")
			c.Abort()
			return
		}
		c.Set("username", claims.Username)
		c.Next()
	}
}

func ok(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

func fail(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"success": false, "message": msg})
}

func parseID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

func handleStoreErr(c *gin.Context, err error) {
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "not found")
		return
	}
	fail(c, http.StatusInternalServerError, err.Error())
}

func valueOrEmpty[T any](v *[]T) []T {
	if v == nil {
		return nil
	}
	return *v
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type groupRatioRequest struct {
	Ratio *float64 `json:"ratio"`
}

type upstreamRequest struct {
	Name                  string          `json:"name"`
	Kind                  string          `json:"kind"`
	URL                   string          `json:"url"`
	BaseURL               string          `json:"base_url"`
	Secret                string          `json:"secret"`
	AccessToken           string          `json:"access_token"`
	UserID                string          `json:"user_id"`
	APIKey                string          `json:"api_key"`
	Key                   string          `json:"key"`
	CallURL               string          `json:"call_url"`
	CallKey               string          `json:"call_key"`
	BalanceAuthType       string          `json:"balance_auth_type"`
	BalanceUserID         string          `json:"balance_user_id"`
	BalanceAccessToken    string          `json:"balance_access_token"`
	BalanceRefreshToken   string          `json:"balance_refresh_token"`
	BalanceTokenExpiresAt *time.Time      `json:"balance_token_expires_at"`
	AuthType              string          `json:"auth_type"`
	AuthSecret            string          `json:"auth_secret"`
	AuthUsername          string          `json:"auth_username"`
	AuthPassword          string          `json:"auth_password"`
	Groups                *[]groupRequest `json:"groups"`
	Enabled               *bool           `json:"enabled"`
	PollIntervalSeconds   *int            `json:"poll_interval_seconds"`
}

type groupRequest struct {
	Name             string   `json:"name"`
	DisplayName      string   `json:"display_name"`
	ManualRatio      *float64 `json:"manual_ratio"`
	TestModel        string   `json:"test_model"`
	TestEndpointType string   `json:"test_endpoint_type"`
	TestPrompt       string   `json:"test_prompt"`
	TestMode         string   `json:"test_mode"`
	TestStream       bool     `json:"test_stream"`
	Enabled          *bool    `json:"enabled"`
}
