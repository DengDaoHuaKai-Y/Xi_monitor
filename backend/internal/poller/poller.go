package poller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"xi_monitor/backend/internal/crypto"
	"xi_monitor/backend/internal/store"
	"xi_monitor/backend/internal/upstream"
	"xi_monitor/backend/internal/upstream/newapi"
	"xi_monitor/backend/internal/upstream/sub2api"
)

type Store interface {
	ListEnabledUpstreams(context.Context) ([]store.Upstream, error)
	GetUpstream(context.Context, int64) (store.Upstream, error)
	UpdateUpstreamSecret(context.Context, int64, string, string, string) error
	SetUpstreamPollResult(context.Context, int64, time.Time, error) error
	RecordUpstreamCheckFailure(context.Context, int64, time.Time, string) error
	UpsertMonitorItem(context.Context, store.ItemUpdate) (int64, error)
	UpsertConfigSnapshot(context.Context, store.UpsertConfigSnapshotParams) error
}

type Poller struct {
	store     Store
	box       *crypto.SecretBox
	interval  time.Duration
	newAPI    *newapi.Client
	sub2api   *sub2api.Client
	mu        sync.Mutex
	running   map[int64]bool
	lastAllMu sync.Mutex
	lastAll   RefreshStatus
}

type RefreshStatus struct {
	Running    bool      `json:"running"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Message    string    `json:"message,omitempty"`
}

func New(store Store, box *crypto.SecretBox, interval time.Duration) *Poller {
	httpClient := &http.Client{Timeout: 45 * time.Second}
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	return &Poller{
		store:    store,
		box:      box,
		interval: interval,
		newAPI:   newapi.New(httpClient),
		sub2api:  sub2api.New(httpClient),
		running:  map[int64]bool{},
	}
}

func (p *Poller) Start(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	p.RefreshAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.RefreshAll(ctx)
		}
	}
}

func (p *Poller) RefreshAll(ctx context.Context) RefreshStatus {
	p.lastAllMu.Lock()
	if p.lastAll.Running {
		status := p.lastAll
		p.lastAllMu.Unlock()
		return status
	}
	status := RefreshStatus{Running: true, StartedAt: time.Now(), Message: "refreshing"}
	p.lastAll = status
	p.lastAllMu.Unlock()

	go func() {
		finished := RefreshStatus{Running: false, StartedAt: status.StartedAt, FinishedAt: time.Now(), Message: "completed"}
		upstreams, err := p.store.ListEnabledUpstreams(ctx)
		if err != nil {
			finished.Message = err.Error()
		} else {
			var errs []error
			var wg sync.WaitGroup
			var errMu sync.Mutex
			for _, u := range upstreams {
				u := u
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := p.RefreshUpstream(ctx, u.ID); err != nil {
						errMu.Lock()
						errs = append(errs, fmt.Errorf("upstream %d: %w", u.ID, err))
						errMu.Unlock()
					}
				}()
			}
			wg.Wait()
			if len(errs) > 0 {
				finished.Message = errors.Join(errs...).Error()
			}
		}
		finished.FinishedAt = time.Now()
		p.lastAllMu.Lock()
		p.lastAll = finished
		p.lastAllMu.Unlock()
	}()

	return status
}

func (p *Poller) RefreshUpstream(ctx context.Context, id int64) error {
	if !p.tryStart(id) {
		return nil
	}
	defer p.finish(id)

	checkedAt := time.Now()
	u, err := p.store.GetUpstream(ctx, id)
	if err != nil {
		return err
	}
	secret, err := p.box.Decrypt(u.AuthSecretCiphertext, u.AuthSecretNonce)
	if err != nil {
		_ = p.recordRefreshError(ctx, id, checkedAt, err)
		return err
	}
	runtimeUpstream, runtimeSecret, err := upstream.ResolveAuth(ctx, p.clientFor(u), u, secret)
	if err != nil {
		_ = p.recordRefreshError(ctx, id, checkedAt, err)
		return err
	}

	result, err := p.fetch(ctx, runtimeUpstream, runtimeSecret)
	if err != nil {
		_ = p.recordRefreshError(ctx, id, checkedAt, err)
		return err
	}

	if result.UpdatedSecret != "" {
		ciphertext, nonce, err := p.box.Encrypt(result.UpdatedSecret)
		if err != nil {
			_ = p.recordRefreshError(ctx, id, checkedAt, err)
			return err
		}
		masked := result.UpdatedMasked
		if masked == "" {
			masked = upstream.MaskBalanceCredentials(result.UpdatedSecret)
		}
		if err := p.store.UpdateUpstreamSecret(ctx, id, ciphertext, nonce, masked); err != nil {
			_ = p.recordRefreshError(ctx, id, checkedAt, err)
			return err
		}
	}

	for _, item := range result.Items {
		if item.ExternalID == "" {
			item.ExternalID = fmt.Sprintf("%s:%s:%s", item.Source, item.ItemType, item.Name)
		}
		if item.Endpoint == "" {
			item.Endpoint = u.BaseURL
		}
		if _, err := p.store.UpsertMonitorItem(ctx, item); err != nil {
			_ = p.recordRefreshError(ctx, id, checkedAt, err)
			return err
		}
	}
	if result.ConfigSummary != nil {
		snapshotBytes, _ := json.Marshal(result.ConfigSummary)
		hash := sha256.Sum256(snapshotBytes)
		_ = p.store.UpsertConfigSnapshot(ctx, store.UpsertConfigSnapshotParams{
			UpstreamID:    id,
			ConfigHash:    hex.EncodeToString(hash[:]),
			ConfigSummary: result.ConfigSummary,
			DiffSummary:   map[string]any{},
			Changed:       false,
			CheckedAt:     checkedAt,
		})
	}

	return p.store.SetUpstreamPollResult(ctx, id, checkedAt, nil)
}

func (p *Poller) RefreshItem(ctx context.Context, item store.MonitorItem) error {
	return p.RefreshUpstream(ctx, item.UpstreamID)
}

func (p *Poller) recordRefreshError(ctx context.Context, id int64, checkedAt time.Time, err error) error {
	_ = p.store.SetUpstreamPollResult(ctx, id, checkedAt, err)
	if err == nil {
		return nil
	}
	return p.store.RecordUpstreamCheckFailure(ctx, id, checkedAt, err.Error())
}

func (p *Poller) fetch(ctx context.Context, u store.Upstream, secret string) (upstream.FetchResult, error) {
	switch u.Kind {
	case store.KindNewAPI:
		return p.newAPI.Fetch(ctx, u, secret)
	case store.KindSub2API:
		return p.sub2api.Fetch(ctx, u, secret)
	default:
		return upstream.FetchResult{}, fmt.Errorf("unsupported upstream kind: %s", u.Kind)
	}
}

func (p *Poller) clientFor(u store.Upstream) *http.Client {
	switch u.Kind {
	case store.KindNewAPI:
		return p.newAPI.HTTP
	case store.KindSub2API:
		return p.sub2api.HTTP
	default:
		return http.DefaultClient
	}
}

func (p *Poller) tryStart(id int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running[id] {
		return false
	}
	p.running[id] = true
	return true
}

func (p *Poller) finish(id int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.running, id)
}
