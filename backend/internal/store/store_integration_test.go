package store_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"xi_monitor/backend/internal/config"
	"xi_monitor/backend/internal/crypto"
	"xi_monitor/backend/internal/store"
)

func TestPostgresMigrationAndEncryptedUpstream(t *testing.T) {
	dsn := integrationDSN(t)
	if dsn == "" {
		t.Skip("set MID_DATABASE_DSN or create backend/config.yaml to run PostgreSQL integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	box, err := crypto.NewSecretBox("test-32-byte-encryption-key-0000")
	if err != nil {
		t.Fatalf("new secretbox: %v", err)
	}
	plainSecret := "sk-test-secret-plaintext"
	ciphertext, nonce, err := box.Encrypt(plainSecret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if strings.Contains(ciphertext, plainSecret) {
		t.Fatal("ciphertext contains plaintext")
	}

	created, err := st.CreateUpstream(ctx, store.Upstream{
		Name:                 "__integration_test__",
		Kind:                 store.KindNewAPI,
		BaseURL:              "https://example.invalid",
		AuthType:             store.AuthBearer,
		AuthSecretCiphertext: ciphertext,
		AuthSecretNonce:      nonce,
		AuthSecretMasked:     crypto.MaskSecret(plainSecret),
		Enabled:              true,
		PollIntervalSeconds:  1800,
	}, []store.UpstreamGroup{
		{
			Name:             "default",
			DisplayName:      "Default",
			TestModel:        "gpt-4o-mini",
			TestEndpointType: "chat_completions",
			TestPrompt:       "ping",
			TestMode:         "chat",
			Enabled:          true,
		},
		{
			Name:             "pro",
			DisplayName:      "Pro",
			TestModel:        "gpt-4o",
			TestEndpointType: "responses",
			TestPrompt:       "ping",
			TestMode:         "chat",
			TestStream:       true,
			Enabled:          true,
		},
	})
	if err != nil {
		t.Fatalf("create upstream: %v", err)
	}
	t.Cleanup(func() {
		_ = st.DeleteUpstream(context.Background(), created.ID)
	})

	got, err := st.GetUpstream(ctx, created.ID)
	if err != nil {
		t.Fatalf("get upstream: %v", err)
	}
	if got.AuthSecretMasked == plainSecret {
		t.Fatal("masked secret returned plaintext")
	}
	if len(got.Groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(got.Groups))
	}
	decrypted, err := box.Decrypt(got.AuthSecretCiphertext, got.AuthSecretNonce)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if decrypted != plainSecret {
		t.Fatalf("decrypted secret mismatch")
	}

	latency := 123
	ratio := 0.12
	balance := 12.5
	availability := 100.0
	if _, err := st.UpsertMonitorItem(ctx, store.ItemUpdate{
		UpstreamID:          created.ID,
		UpstreamGroupID:     &got.Groups[0].ID,
		ExternalID:          "channel-1",
		ItemType:            "channel",
		Name:                "Test Channel",
		Endpoint:            "https://example.invalid",
		Source:              "new-api",
		GroupName:           "default",
		Ratio:               &ratio,
		Status:              "available",
		LatencyMS:           &latency,
		AvailabilityPercent: &availability,
		Balance:             &balance,
		BalanceUnit:         "USD",
		LastCheckedAt:       time.Now(),
		LastMessage:         "ok",
		Trend:               []int{123},
		RawSummary:          map[string]any{"id": "channel-1"},
		CheckType:           "availability",
		TestParams:          map[string]any{"model": "gpt-4o-mini"},
	}); err != nil {
		t.Fatalf("upsert monitor item: %v", err)
	}

	firstAccountID, err := st.UpsertMonitorItem(ctx, store.ItemUpdate{
		UpstreamID:          created.ID,
		ExternalID:          "user:1143",
		ItemType:            "account",
		Name:                "Account",
		Endpoint:            "https://example.invalid",
		Source:              "new-api",
		GroupName:           "default",
		Status:              "available",
		AvailabilityPercent: &availability,
		LastCheckedAt:       time.Now(),
		LastMessage:         "ok",
		Trend:               []int{1},
		RawSummary:          map[string]any{"id": "1143"},
		CheckType:           "availability",
		TestParams:          map[string]any{"model": "gpt-test"},
	})
	if err != nil {
		t.Fatalf("upsert first account item: %v", err)
	}
	secondAccountID, err := st.UpsertMonitorItem(ctx, store.ItemUpdate{
		UpstreamID:          created.ID,
		ExternalID:          "user:self",
		ItemType:            "account",
		Name:                "Account",
		Endpoint:            "https://example.invalid",
		Source:              "new-api",
		GroupName:           "default",
		Status:              "available",
		AvailabilityPercent: &availability,
		LastCheckedAt:       time.Now(),
		LastMessage:         "ok",
		Trend:               []int{1},
		RawSummary:          map[string]any{"id": "self"},
		CheckType:           "availability",
		TestParams:          map[string]any{"model": "gpt-test"},
	})
	if err != nil {
		t.Fatalf("upsert second account item should not duplicate null group account: %v", err)
	}
	if secondAccountID != firstAccountID {
		t.Fatalf("account monitor item was recreated: first=%d second=%d", firstAccountID, secondAccountID)
	}

	dashboard, err := st.GetDashboard(ctx)
	if err != nil {
		t.Fatalf("get dashboard: %v", err)
	}
	found := false
	for _, item := range dashboard.Items {
		if item.UpstreamID == created.ID && item.ExternalID == "channel-1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("dashboard did not include inserted monitor item")
	}

	manualRatio := 0.25
	if _, err := st.SetUpstreamGroupRatio(ctx, created.ID, "default", &manualRatio); err != nil {
		t.Fatalf("set group ratio: %v", err)
	}
	dashboard, err = st.GetDashboard(ctx)
	if err != nil {
		t.Fatalf("get dashboard after ratio update: %v", err)
	}
	found = false
	for _, item := range dashboard.Items {
		if item.UpstreamID == created.ID && item.ExternalID == "channel-1" {
			found = true
			if item.Ratio == nil || *item.Ratio != "0.250000" {
				t.Fatalf("dashboard ratio not updated, got %#v", item.Ratio)
			}
			break
		}
	}
	if !found {
		t.Fatal("dashboard did not include monitor item after ratio update")
	}

	updated, err := st.UpdateUpstream(ctx, store.Upstream{
		ID:                  created.ID,
		Name:                "__integration_test__",
		Kind:                store.KindNewAPI,
		BaseURL:             "https://example.invalid",
		AuthType:            store.AuthBearer,
		Enabled:             true,
		PollIntervalSeconds: 1800,
	}, &[]store.UpstreamGroup{
		{
			Name:        "default",
			DisplayName: "Visible Default",
			TestModel:   "gpt-test",
			Enabled:     true,
		},
	})
	if err != nil {
		t.Fatalf("update upstream groups: %v", err)
	}
	if len(updated.Groups) != 1 || updated.Groups[0].ID != got.Groups[0].ID {
		t.Fatalf("default group was recreated: before=%#v after=%#v", got.Groups[0], updated.Groups)
	}
	dashboard, err = st.GetDashboard(ctx)
	if err != nil {
		t.Fatalf("get dashboard after group update: %v", err)
	}
	found = false
	for _, item := range dashboard.Items {
		if item.UpstreamID == created.ID && item.ExternalID == "channel-1" {
			found = true
			if item.UpstreamGroupID == nil || *item.UpstreamGroupID != got.Groups[0].ID {
				t.Fatalf("monitor item group link changed: %#v", item.UpstreamGroupID)
			}
			if item.GroupName != "default" {
				t.Fatalf("monitor item internal group changed: %q", item.GroupName)
			}
			break
		}
	}
	if !found {
		t.Fatal("dashboard did not include monitor item after group update")
	}
}

func integrationDSN(t *testing.T) string {
	t.Helper()
	if dsn := strings.TrimSpace(os.Getenv("MID_DATABASE_DSN")); dsn != "" {
		return dsn
	}
	cfg, err := config.Load("../../config.yaml")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.Database.DSN)
}
