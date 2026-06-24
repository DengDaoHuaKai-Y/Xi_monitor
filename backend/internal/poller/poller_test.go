package poller

import (
	"context"
	"errors"
	"testing"
	"time"

	"xi_monitor/backend/internal/crypto"
	"xi_monitor/backend/internal/store"
)

type fakeStore struct {
	upstream      store.Upstream
	pollErr       error
	failureCalled bool
	failureMsg    string
}

func (f *fakeStore) ListEnabledUpstreams(context.Context) ([]store.Upstream, error) {
	return []store.Upstream{f.upstream}, nil
}

func (f *fakeStore) GetUpstream(context.Context, int64) (store.Upstream, error) {
	return f.upstream, nil
}

func (f *fakeStore) UpdateUpstreamSecret(context.Context, int64, string, string, string) error {
	return nil
}

func (f *fakeStore) SetUpstreamPollResult(_ context.Context, _ int64, _ time.Time, err error) error {
	f.pollErr = err
	return nil
}

func (f *fakeStore) RecordUpstreamCheckFailure(_ context.Context, _ int64, _ time.Time, message string) error {
	f.failureCalled = true
	f.failureMsg = message
	return nil
}

func (f *fakeStore) UpsertMonitorItem(context.Context, store.ItemUpdate) (int64, error) {
	return 0, nil
}

func (f *fakeStore) UpsertConfigSnapshot(context.Context, store.UpsertConfigSnapshotParams) error {
	return nil
}

func TestRefreshUpstreamRecordsFailureLog(t *testing.T) {
	box, err := crypto.NewSecretBox("test-32-byte-encryption-key-0000")
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, nonce, err := box.Encrypt("not-json")
	if err != nil {
		t.Fatal(err)
	}
	st := &fakeStore{
		upstream: store.Upstream{
			ID:                   7,
			Kind:                 store.KindNewAPI,
			BaseURL:              "https://example.invalid",
			AuthType:             store.AuthPassword,
			AuthSecretCiphertext: ciphertext,
			AuthSecretNonce:      nonce,
			Enabled:              true,
		},
	}
	pl := New(st, box, time.Minute)

	err = pl.RefreshUpstream(context.Background(), 7)
	if err == nil {
		t.Fatal("expected refresh error")
	}
	if st.pollErr == nil {
		t.Fatal("expected poll result error to be recorded")
	}
	if !st.failureCalled {
		t.Fatal("expected failure check log to be recorded")
	}
	if st.failureMsg == "" || !errors.Is(err, st.pollErr) {
		t.Fatalf("unexpected failure state: err=%v pollErr=%v msg=%q", err, st.pollErr, st.failureMsg)
	}
}
