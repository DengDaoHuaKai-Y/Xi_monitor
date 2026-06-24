package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	db *pgxpool.Pool
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() {
	if s != nil && s.db != nil {
		s.db.Close()
	}
}

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.db.Exec(ctx, schemaSQL)
	return err
}

func (s *Store) CreateUpstream(ctx context.Context, u Upstream, groups []UpstreamGroup) (Upstream, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Upstream{}, err
	}
	defer tx.Rollback(ctx)

	if u.PollIntervalSeconds <= 0 {
		u.PollIntervalSeconds = 1800
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO upstreams
			(name, kind, base_url, auth_type, auth_secret_ciphertext, auth_secret_nonce, auth_secret_masked, enabled, poll_interval_seconds)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id, name, kind, base_url, auth_type, auth_secret_ciphertext, auth_secret_nonce, auth_secret_masked,
			enabled, poll_interval_seconds, last_polled_at, COALESCE(last_error, ''), created_at, updated_at
	`, u.Name, u.Kind, u.BaseURL, u.AuthType, u.AuthSecretCiphertext, u.AuthSecretNonce, u.AuthSecretMasked, u.Enabled, u.PollIntervalSeconds)
	created, err := scanUpstream(row)
	if err != nil {
		return Upstream{}, err
	}

	for _, g := range groups {
		g.UpstreamID = created.ID
		createdGroup, err := insertGroup(ctx, tx, g)
		if err != nil {
			return Upstream{}, err
		}
		created.Groups = append(created.Groups, createdGroup)
	}

	if err := tx.Commit(ctx); err != nil {
		return Upstream{}, err
	}
	return created, nil
}

func (s *Store) ListUpstreams(ctx context.Context) ([]Upstream, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, kind, base_url, auth_type, auth_secret_ciphertext, auth_secret_nonce, auth_secret_masked,
			enabled, poll_interval_seconds, last_polled_at, COALESCE(last_error, ''), created_at, updated_at
		FROM upstreams
		ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var upstreams []Upstream
	for rows.Next() {
		u, err := scanUpstream(rows)
		if err != nil {
			return nil, err
		}
		upstreams = append(upstreams, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	groups, err := s.listGroupsForUpstreams(ctx, idsOf(upstreams))
	if err != nil {
		return nil, err
	}
	for i := range upstreams {
		upstreams[i].Groups = groups[upstreams[i].ID]
	}
	return upstreams, nil
}

func (s *Store) ListEnabledUpstreams(ctx context.Context) ([]Upstream, error) {
	all, err := s.ListUpstreams(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, u := range all {
		if u.Enabled {
			out = append(out, u)
		}
	}
	return out, nil
}

func (s *Store) GetUpstream(ctx context.Context, id int64) (Upstream, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id, name, kind, base_url, auth_type, auth_secret_ciphertext, auth_secret_nonce, auth_secret_masked,
			enabled, poll_interval_seconds, last_polled_at, COALESCE(last_error, ''), created_at, updated_at
		FROM upstreams
		WHERE id = $1
	`, id)
	u, err := scanUpstream(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Upstream{}, ErrNotFound
		}
		return Upstream{}, err
	}
	groups, err := s.listGroupsForUpstreams(ctx, []int64{id})
	if err != nil {
		return Upstream{}, err
	}
	u.Groups = groups[id]
	return u, nil
}

func (s *Store) UpdateUpstream(ctx context.Context, u Upstream, groups *[]UpstreamGroup) (Upstream, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Upstream{}, err
	}
	defer tx.Rollback(ctx)

	sets := []string{
		"name = $2",
		"kind = $3",
		"base_url = $4",
		"auth_type = $5",
		"enabled = $6",
		"poll_interval_seconds = $7",
		"updated_at = now()",
	}
	args := []any{u.ID, u.Name, u.Kind, u.BaseURL, u.AuthType, u.Enabled, u.PollIntervalSeconds}
	if u.AuthSecretCiphertext != "" && u.AuthSecretNonce != "" {
		sets = append(sets,
			fmt.Sprintf("auth_secret_ciphertext = $%d", len(args)+1),
			fmt.Sprintf("auth_secret_nonce = $%d", len(args)+2),
			fmt.Sprintf("auth_secret_masked = $%d", len(args)+3),
		)
		args = append(args, u.AuthSecretCiphertext, u.AuthSecretNonce, u.AuthSecretMasked)
	}

	query := `
		UPDATE upstreams SET ` + strings.Join(sets, ", ") + `
		WHERE id = $1
		RETURNING id, name, kind, base_url, auth_type, auth_secret_ciphertext, auth_secret_nonce, auth_secret_masked,
			enabled, poll_interval_seconds, last_polled_at, COALESCE(last_error, ''), created_at, updated_at
	`
	updated, err := scanUpstream(tx.QueryRow(ctx, query, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Upstream{}, ErrNotFound
		}
		return Upstream{}, err
	}

	if groups != nil {
		if _, err := tx.Exec(ctx, `UPDATE upstream_groups SET enabled = false, updated_at = now() WHERE upstream_id = $1`, u.ID); err != nil {
			return Upstream{}, err
		}
		for _, g := range *groups {
			g.UpstreamID = u.ID
			createdGroup, err := upsertGroup(ctx, tx, g)
			if err != nil {
				return Upstream{}, err
			}
			if err := attachOrphanMonitorItemsToGroup(ctx, tx, createdGroup); err != nil {
				return Upstream{}, err
			}
			updated.Groups = append(updated.Groups, createdGroup)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Upstream{}, err
	}
	if groups == nil {
		full, err := s.GetUpstream(ctx, u.ID)
		if err != nil {
			return Upstream{}, err
		}
		return full, nil
	}
	return updated, nil
}

func (s *Store) UpdateUpstreamSecret(ctx context.Context, id int64, ciphertext, nonce, masked string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE upstreams
		SET auth_secret_ciphertext = $2,
			auth_secret_nonce = $3,
			auth_secret_masked = $4,
			updated_at = now()
		WHERE id = $1
	`, id, ciphertext, nonce, masked)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteUpstream(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM upstreams WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetUpstreamPollResult(ctx context.Context, id int64, checkedAt time.Time, lastErr error) error {
	var msg any
	if lastErr != nil {
		msg = lastErr.Error()
	}
	_, err := s.db.Exec(ctx, `
		UPDATE upstreams
		SET last_polled_at = $2, last_error = $3, updated_at = now()
		WHERE id = $1
	`, id, checkedAt, msg)
	return err
}

func (s *Store) RecordUpstreamCheckFailure(ctx context.Context, upstreamID int64, checkedAt time.Time, message string) error {
	if upstreamID <= 0 {
		return errors.New("upstream_id is required")
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = "refresh failed"
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		UPDATE monitor_items
		SET status = 'error',
			availability_percent = 0,
			last_checked_at = $2,
			last_message = $3,
			trend = '[0]'::jsonb,
			updated_at = now()
		WHERE upstream_id = $1
		RETURNING id, latency_ms, balance::float8, ratio::float8
	`, upstreamID, checkedAt, message)
	if err != nil {
		return err
	}
	type failureLog struct {
		id        int64
		latencyMS *int
		balance   *float64
		ratio     *float64
	}
	var logs []failureLog
	for rows.Next() {
		var log failureLog
		if err := rows.Scan(&log.id, &log.latencyMS, &log.balance, &log.ratio); err != nil {
			rows.Close()
			return err
		}
		logs = append(logs, log)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, log := range logs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO check_logs
				(monitor_item_id, check_type, status, test_params, latency_ms, balance, ratio, message, checked_at)
			VALUES ($1,'refresh_error','error','{}'::jsonb,$2,$3,$4,$5,$6)
		`, log.id, log.latencyMS, log.balance, log.ratio, message, checkedAt); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) UpsertMonitorItem(ctx context.Context, item ItemUpdate) (int64, error) {
	if item.ItemType == "account" {
		if existingExternalID, err := s.existingAccountExternalID(ctx, item.UpstreamID, item.UpstreamGroupID); err == nil && existingExternalID != "" {
			item.ExternalID = existingExternalID
		} else if strings.TrimSpace(item.ExternalID) == "" || strings.HasPrefix(item.ExternalID, "user:") {
			item.ExternalID = "account"
		}
	}
	trendJSON, err := marshalJSON(item.Trend, []int{})
	if err != nil {
		return 0, err
	}
	rawJSON, err := marshalJSON(item.RawSummary, map[string]any{})
	if err != nil {
		return 0, err
	}

	var ratio any
	if item.Ratio != nil && !math.IsNaN(*item.Ratio) && !math.IsInf(*item.Ratio, 0) {
		ratio = *item.Ratio
	}
	var availability any
	if item.AvailabilityPercent != nil && !math.IsNaN(*item.AvailabilityPercent) && !math.IsInf(*item.AvailabilityPercent, 0) {
		availability = *item.AvailabilityPercent
	}
	var balance any
	if item.Balance != nil && !math.IsNaN(*item.Balance) && !math.IsInf(*item.Balance, 0) {
		balance = *item.Balance
	}

	query := `
		INSERT INTO monitor_items
			(upstream_id, upstream_group_id, external_id, item_type, name, endpoint, source, group_name, ratio, status,
			 latency_ms, availability_percent, balance, balance_unit, last_checked_at, last_message, trend, raw_summary)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17::jsonb,$18::jsonb)
		ON CONFLICT (upstream_id, upstream_group_id, item_type, external_id)
		DO UPDATE SET
			name = EXCLUDED.name,
			endpoint = EXCLUDED.endpoint,
			source = EXCLUDED.source,
			group_name = EXCLUDED.group_name,
			ratio = EXCLUDED.ratio,
			status = EXCLUDED.status,
			latency_ms = EXCLUDED.latency_ms,
			availability_percent = EXCLUDED.availability_percent,
			balance = EXCLUDED.balance,
			balance_unit = EXCLUDED.balance_unit,
			last_checked_at = EXCLUDED.last_checked_at,
			last_message = EXCLUDED.last_message,
			trend = EXCLUDED.trend,
			raw_summary = EXCLUDED.raw_summary,
			updated_at = now()
		RETURNING id
	`
	if item.UpstreamGroupID == nil {
		query = `
			INSERT INTO monitor_items
				(upstream_id, upstream_group_id, external_id, item_type, name, endpoint, source, group_name, ratio, status,
				 latency_ms, availability_percent, balance, balance_unit, last_checked_at, last_message, trend, raw_summary)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17::jsonb,$18::jsonb)
			ON CONFLICT (upstream_id, item_type, external_id) WHERE upstream_group_id IS NULL
			DO UPDATE SET
				name = EXCLUDED.name,
				endpoint = EXCLUDED.endpoint,
				source = EXCLUDED.source,
				group_name = EXCLUDED.group_name,
				ratio = EXCLUDED.ratio,
				status = EXCLUDED.status,
				latency_ms = EXCLUDED.latency_ms,
				availability_percent = EXCLUDED.availability_percent,
				balance = EXCLUDED.balance,
				balance_unit = EXCLUDED.balance_unit,
				last_checked_at = EXCLUDED.last_checked_at,
				last_message = EXCLUDED.last_message,
				trend = EXCLUDED.trend,
				raw_summary = EXCLUDED.raw_summary,
				updated_at = now()
			RETURNING id
		`
	}

	row := s.db.QueryRow(ctx, query, item.UpstreamID, item.UpstreamGroupID, item.ExternalID, item.ItemType, item.Name, item.Endpoint, item.Source,
		item.GroupName, ratio, item.Status, item.LatencyMS, availability, balance, item.BalanceUnit,
		item.LastCheckedAt, item.LastMessage, string(trendJSON), string(rawJSON))
	var id int64
	if err := row.Scan(&id); err != nil {
		return 0, err
	}

	testParams, err := marshalJSON(item.TestParams, map[string]any{})
	if err != nil {
		return 0, err
	}
	if item.CheckType == "" {
		item.CheckType = "sync"
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO check_logs
			(monitor_item_id, check_type, status, test_params, latency_ms, balance, ratio, message, checked_at)
		VALUES ($1,$2,$3,$4::jsonb,$5,$6,$7,$8,$9)
	`, id, item.CheckType, item.Status, string(testParams), item.LatencyMS, balance, ratio, item.LastMessage, item.LastCheckedAt)
	return id, err
}

func (s *Store) existingAccountExternalID(ctx context.Context, upstreamID int64, upstreamGroupID *int64) (string, error) {
	var externalID string
	var row pgx.Row
	if upstreamGroupID == nil {
		row = s.db.QueryRow(ctx, `
			SELECT external_id
			FROM monitor_items
			WHERE upstream_id = $1 AND item_type = 'account' AND upstream_group_id IS NULL
			ORDER BY updated_at DESC, id DESC
			LIMIT 1
		`, upstreamID)
	} else {
		row = s.db.QueryRow(ctx, `
			SELECT external_id
			FROM monitor_items
			WHERE upstream_id = $1 AND item_type = 'account' AND upstream_group_id = $2
			ORDER BY updated_at DESC, id DESC
			LIMIT 1
		`, upstreamID, *upstreamGroupID)
	}
	if err := row.Scan(&externalID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return externalID, nil
}

func (s *Store) GetDashboard(ctx context.Context) (Dashboard, error) {
	d := Dashboard{Items: []MonitorItem{}}
	rows, err := s.db.Query(ctx, `
		SELECT mi.id, mi.upstream_id, mi.upstream_group_id, mi.external_id, mi.item_type,
			COALESCE(NULLIF(u.name, ''), mi.name) AS display_name,
			mi.endpoint, mi.source, mi.group_name, mi.ratio::text, mi.status, mi.latency_ms,
			mi.availability_percent::text, mi.balance::text, mi.balance_unit,
			mi.last_checked_at, COALESCE(mi.last_message, ''),
			COALESCE((
				SELECT jsonb_agg(
					jsonb_build_object(
						'value', CASE
							WHEN cl.latency_ms IS NOT NULL AND cl.latency_ms > 0 THEN cl.latency_ms
							WHEN cl.status = 'available' THEN 1
							ELSE 0
						END,
						'status', cl.status,
						'checked_at', cl.checked_at
					)
					ORDER BY cl.checked_at
				)
				FROM (
					SELECT latency_ms, status, checked_at
					FROM check_logs
					WHERE monitor_item_id = mi.id
					ORDER BY checked_at DESC
					LIMIT 60
				) cl
			), mi.trend, '[]'::jsonb) AS trend,
			mi.raw_summary, mi.created_at, mi.updated_at
		FROM monitor_items mi
		JOIN upstreams u ON u.id = mi.upstream_id
		ORDER BY mi.source, mi.group_name, display_name, mi.id
	`)
	if err != nil {
		return Dashboard{}, err
	}
	defer rows.Close()

	for rows.Next() {
		item, err := scanMonitorItem(rows)
		if err != nil {
			return Dashboard{}, err
		}
		d.Items = append(d.Items, item)
	}
	if err := rows.Err(); err != nil {
		return Dashboard{}, err
	}

	upstreams, err := s.ListUpstreams(ctx)
	if err != nil {
		return Dashboard{}, err
	}
	d.Items = appendMissingUpstreamPlaceholders(d.Items, upstreams)
	d.Summary = summarizeDashboard(d.Items)
	return d, nil
}

func (s *Store) GetMonitorItem(ctx context.Context, id int64) (MonitorItem, error) {
	item, err := scanMonitorItem(s.db.QueryRow(ctx, `
		SELECT id, upstream_id, upstream_group_id, external_id, item_type, name, endpoint, source, group_name,
			ratio::text, status, latency_ms, availability_percent::text, balance::text, balance_unit,
			last_checked_at, COALESCE(last_message, ''), trend, raw_summary, created_at, updated_at
		FROM monitor_items
		WHERE id = $1
	`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MonitorItem{}, ErrNotFound
		}
		return MonitorItem{}, err
	}
	return item, nil
}

func (s *Store) UpsertConfigSnapshot(ctx context.Context, p UpsertConfigSnapshotParams) error {
	summary, err := marshalJSON(p.ConfigSummary, map[string]any{})
	if err != nil {
		return err
	}
	diff, err := marshalJSON(p.DiffSummary, map[string]any{})
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO upstream_config_snapshots
			(upstream_id, config_hash, config_summary, diff_summary, changed, checked_at)
		VALUES ($1,$2,$3::jsonb,$4::jsonb,$5,$6)
	`, p.UpstreamID, p.ConfigHash, string(summary), string(diff), p.Changed, p.CheckedAt)
	return err
}

func (s *Store) listGroupsForUpstreams(ctx context.Context, upstreamIDs []int64) (map[int64][]UpstreamGroup, error) {
	result := map[int64][]UpstreamGroup{}
	if len(upstreamIDs) == 0 {
		return result, nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, upstream_id, name, COALESCE(display_name, ''), manual_ratio::float8, COALESCE(test_model, ''),
			COALESCE(test_endpoint_type, ''), COALESCE(test_prompt, ''), COALESCE(test_mode, ''),
			test_stream, enabled, created_at, updated_at
		FROM upstream_groups
		WHERE upstream_id = ANY($1)
		ORDER BY upstream_id, id
	`, upstreamIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		result[g.UpstreamID] = append(result[g.UpstreamID], g)
	}
	return result, rows.Err()
}

func insertGroup(ctx context.Context, tx pgx.Tx, g UpstreamGroup) (UpstreamGroup, error) {
	return scanGroup(tx.QueryRow(ctx, `
		INSERT INTO upstream_groups
			(upstream_id, name, display_name, manual_ratio, test_model, test_endpoint_type, test_prompt, test_mode, test_stream, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id, upstream_id, name, COALESCE(display_name, ''), manual_ratio::float8, COALESCE(test_model, ''),
			COALESCE(test_endpoint_type, ''), COALESCE(test_prompt, ''), COALESCE(test_mode, ''),
			test_stream, enabled, created_at, updated_at
	`, g.UpstreamID, g.Name, nullableString(g.DisplayName), g.ManualRatio, nullableString(g.TestModel),
		nullableString(g.TestEndpointType), nullableString(g.TestPrompt), nullableString(g.TestMode), g.TestStream, g.Enabled))
}

func upsertGroup(ctx context.Context, tx pgx.Tx, g UpstreamGroup) (UpstreamGroup, error) {
	return scanGroup(tx.QueryRow(ctx, `
		INSERT INTO upstream_groups
			(upstream_id, name, display_name, manual_ratio, test_model, test_endpoint_type, test_prompt, test_mode, test_stream, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (upstream_id, name)
		DO UPDATE SET
			display_name = EXCLUDED.display_name,
			manual_ratio = EXCLUDED.manual_ratio,
			test_model = EXCLUDED.test_model,
			test_endpoint_type = EXCLUDED.test_endpoint_type,
			test_prompt = EXCLUDED.test_prompt,
			test_mode = EXCLUDED.test_mode,
			test_stream = EXCLUDED.test_stream,
			enabled = EXCLUDED.enabled,
			updated_at = now()
		RETURNING id, upstream_id, name, COALESCE(display_name, ''), manual_ratio::float8, COALESCE(test_model, ''),
			COALESCE(test_endpoint_type, ''), COALESCE(test_prompt, ''), COALESCE(test_mode, ''),
			test_stream, enabled, created_at, updated_at
	`, g.UpstreamID, g.Name, nullableString(g.DisplayName), g.ManualRatio, nullableString(g.TestModel),
		nullableString(g.TestEndpointType), nullableString(g.TestPrompt), nullableString(g.TestMode), g.TestStream, g.Enabled))
}

func attachOrphanMonitorItemsToGroup(ctx context.Context, tx pgx.Tx, g UpstreamGroup) error {
	_, err := tx.Exec(ctx, `
		UPDATE monitor_items
		SET upstream_group_id = $2,
			group_name = $3,
			updated_at = now()
		WHERE upstream_id = $1
			AND upstream_group_id IS NULL
			AND (group_name = $3 OR group_name = $4)
	`, g.UpstreamID, g.ID, g.Name, g.DisplayName)
	return err
}

func (s *Store) SetUpstreamGroupRatio(ctx context.Context, upstreamID int64, groupName string, ratio *float64) (UpstreamGroup, error) {
	groupName = strings.TrimSpace(groupName)
	if upstreamID <= 0 || groupName == "" {
		return UpstreamGroup{}, errors.New("upstream_id and group_name are required")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return UpstreamGroup{}, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		INSERT INTO upstream_groups
			(upstream_id, name, display_name, manual_ratio, enabled)
		VALUES ($1,$2,$2,$3,true)
		ON CONFLICT (upstream_id, name)
		DO UPDATE SET manual_ratio = EXCLUDED.manual_ratio, updated_at = now()
		RETURNING id, upstream_id, name, COALESCE(display_name, ''), manual_ratio::float8, COALESCE(test_model, ''),
			COALESCE(test_endpoint_type, ''), COALESCE(test_prompt, ''), COALESCE(test_mode, ''),
			test_stream, enabled, created_at, updated_at
	`, upstreamID, groupName, ratio)
	group, err := scanGroup(row)
	if err != nil {
		if strings.Contains(err.Error(), "violates foreign key constraint") {
			return UpstreamGroup{}, ErrNotFound
		}
		return UpstreamGroup{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE monitor_items
		SET ratio = $3, updated_at = now()
		WHERE upstream_id = $1
			AND (
				upstream_group_id = $2
				OR group_name = $4
				OR group_name = $5
			)
	`, upstreamID, group.ID, ratio, group.Name, group.DisplayName); err != nil {
		return UpstreamGroup{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return UpstreamGroup{}, err
	}
	return group, nil
}

func scanUpstream(row pgx.Row) (Upstream, error) {
	var u Upstream
	err := row.Scan(&u.ID, &u.Name, &u.Kind, &u.BaseURL, &u.AuthType, &u.AuthSecretCiphertext, &u.AuthSecretNonce,
		&u.AuthSecretMasked, &u.Enabled, &u.PollIntervalSeconds, &u.LastPolledAt, &u.LastError, &u.CreatedAt, &u.UpdatedAt)
	return u, err
}

func scanGroup(row pgx.Row) (UpstreamGroup, error) {
	var g UpstreamGroup
	err := row.Scan(&g.ID, &g.UpstreamID, &g.Name, &g.DisplayName, &g.ManualRatio, &g.TestModel, &g.TestEndpointType,
		&g.TestPrompt, &g.TestMode, &g.TestStream, &g.Enabled, &g.CreatedAt, &g.UpdatedAt)
	return g, err
}

func scanMonitorItem(row pgx.Row) (MonitorItem, error) {
	var item MonitorItem
	var groupID *int64
	var ratio, availability, balance *string
	var trend, raw []byte
	err := row.Scan(&item.ID, &item.UpstreamID, &groupID, &item.ExternalID, &item.ItemType, &item.Name,
		&item.Endpoint, &item.Source, &item.GroupName, &ratio, &item.Status, &item.LatencyMS, &availability,
		&balance, &item.BalanceUnit, &item.LastCheckedAt, &item.LastMessage, &trend, &raw,
		&item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return MonitorItem{}, err
	}
	item.UpstreamGroupID = groupID
	item.Ratio = ratio
	item.AvailabilityPercent = availability
	item.Balance = balance
	item.Trend = json.RawMessage(trend)
	item.RawSummary = json.RawMessage(raw)
	return item, nil
}

func idsOf(upstreams []Upstream) []int64 {
	ids := make([]int64, 0, len(upstreams))
	for _, u := range upstreams {
		ids = append(ids, u.ID)
	}
	return ids
}

func marshalJSON(v any, fallback any) ([]byte, error) {
	if v == nil {
		v = fallback
	}
	return json.Marshal(v)
}

func appendMissingUpstreamPlaceholders(items []MonitorItem, upstreams []Upstream) []MonitorItem {
	seen := map[int64]bool{}
	for _, item := range items {
		seen[item.UpstreamID] = true
	}
	for _, u := range upstreams {
		if seen[u.ID] {
			continue
		}
		status := "unknown"
		message := "已添加，等待首次刷新"
		if u.LastError != "" {
			status = "error"
			message = u.LastError
		}
		groupName := "default"
		if len(u.Groups) > 0 {
			names := make([]string, 0, len(u.Groups))
			for _, group := range u.Groups {
				name := group.DisplayName
				if name == "" {
					name = group.Name
				}
				if name != "" {
					names = append(names, name)
				}
			}
			if len(names) > 0 {
				groupName = strings.Join(names, ", ")
			}
		}
		items = append(items, MonitorItem{
			UpstreamID:    u.ID,
			ExternalID:    fmt.Sprintf("upstream:%d", u.ID),
			ItemType:      "upstream",
			Name:          u.Name,
			Endpoint:      u.BaseURL,
			Source:        string(u.Kind),
			GroupName:     groupName,
			Status:        status,
			LastCheckedAt: u.LastPolledAt,
			LastMessage:   message,
			Trend:         json.RawMessage(`[]`),
			RawSummary:    json.RawMessage(`{}`),
			CreatedAt:     u.CreatedAt,
			UpdatedAt:     u.UpdatedAt,
		})
	}
	return items
}

func summarizeDashboard(items []MonitorItem) DashboardSummary {
	var summary DashboardSummary
	var latencySum float64
	var latencyCount int64
	for _, item := range items {
		summary.Total++
		switch item.Status {
		case "available":
			summary.Available++
		case "unavailable", "error":
			summary.Unavailable++
		}
		if item.LatencyMS != nil {
			latencySum += float64(*item.LatencyMS)
			latencyCount++
		}
		if item.LastCheckedAt != nil && (summary.LastRefreshedAt == nil || item.LastCheckedAt.After(*summary.LastRefreshedAt)) {
			t := *item.LastCheckedAt
			summary.LastRefreshedAt = &t
		}
	}
	if latencyCount > 0 {
		avg := latencySum / float64(latencyCount)
		summary.AvgLatencyMS = &avg
	}
	return summary
}

func nullableString(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

var ErrNotFound = errors.New("not found")

const schemaSQL = `
CREATE TABLE IF NOT EXISTS upstreams (
	id BIGSERIAL PRIMARY KEY,
	name VARCHAR(100) NOT NULL,
	kind VARCHAR(32) NOT NULL CHECK (kind IN ('new_api', 'sub2api')),
	base_url TEXT NOT NULL,
	auth_type VARCHAR(32) NOT NULL CHECK (auth_type IN ('bearer', 'cookie', 'admin_api_key', 'password', 'new_api_token', 'new_api_session', 'x_api_key', 'newapi_access_token', 'sub2api_refresh_token')),
	auth_secret_ciphertext TEXT NOT NULL,
	auth_secret_nonce TEXT NOT NULL,
	auth_secret_masked VARCHAR(120) NOT NULL,
	enabled BOOLEAN NOT NULL DEFAULT true,
	poll_interval_seconds INT NOT NULL DEFAULT 1800,
	last_polled_at TIMESTAMPTZ,
	last_error TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE upstreams DROP CONSTRAINT IF EXISTS upstreams_auth_type_check;
ALTER TABLE upstreams ADD CONSTRAINT upstreams_auth_type_check CHECK (auth_type IN ('bearer', 'cookie', 'admin_api_key', 'password', 'new_api_token', 'new_api_session', 'x_api_key', 'newapi_access_token', 'sub2api_refresh_token'));

CREATE TABLE IF NOT EXISTS upstream_groups (
	id BIGSERIAL PRIMARY KEY,
	upstream_id BIGINT NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
	name VARCHAR(100) NOT NULL,
	display_name VARCHAR(100),
	manual_ratio NUMERIC(12,6),
	test_model VARCHAR(200),
	test_endpoint_type VARCHAR(64),
	test_prompt TEXT,
	test_mode VARCHAR(64),
	test_stream BOOLEAN NOT NULL DEFAULT false,
	enabled BOOLEAN NOT NULL DEFAULT true,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (upstream_id, name)
);

ALTER TABLE upstream_groups ADD COLUMN IF NOT EXISTS manual_ratio NUMERIC(12,6);

CREATE TABLE IF NOT EXISTS monitor_items (
	id BIGSERIAL PRIMARY KEY,
	upstream_id BIGINT NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
	upstream_group_id BIGINT REFERENCES upstream_groups(id) ON DELETE SET NULL,
	external_id VARCHAR(128) NOT NULL,
	item_type VARCHAR(32) NOT NULL,
	name VARCHAR(200) NOT NULL,
	endpoint TEXT NOT NULL,
	source VARCHAR(32) NOT NULL,
	group_name VARCHAR(100) NOT NULL,
	ratio NUMERIC(12,6),
	status VARCHAR(32) NOT NULL DEFAULT 'unknown',
	latency_ms INT,
	availability_percent NUMERIC(6,2),
	balance NUMERIC(20,8),
	balance_unit VARCHAR(32),
	last_checked_at TIMESTAMPTZ,
	last_message TEXT,
	trend JSONB NOT NULL DEFAULT '[]'::jsonb,
	raw_summary JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (upstream_id, upstream_group_id, item_type, external_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_monitor_items_null_group
	ON monitor_items (upstream_id, item_type, external_id)
	WHERE upstream_group_id IS NULL;

CREATE TABLE IF NOT EXISTS check_logs (
	id BIGSERIAL PRIMARY KEY,
	monitor_item_id BIGINT NOT NULL REFERENCES monitor_items(id) ON DELETE CASCADE,
	check_type VARCHAR(32) NOT NULL,
	status VARCHAR(32) NOT NULL,
	test_params JSONB NOT NULL DEFAULT '{}'::jsonb,
	latency_ms INT,
	balance NUMERIC(20,8),
	ratio NUMERIC(12,6),
	message TEXT,
	checked_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_check_logs_item_checked_at ON check_logs (monitor_item_id, checked_at DESC);
CREATE INDEX IF NOT EXISTS idx_check_logs_checked_at ON check_logs (checked_at DESC);

CREATE TABLE IF NOT EXISTS upstream_config_snapshots (
	id BIGSERIAL PRIMARY KEY,
	upstream_id BIGINT NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
	config_hash CHAR(64) NOT NULL,
	config_summary JSONB NOT NULL DEFAULT '{}'::jsonb,
	diff_summary JSONB NOT NULL DEFAULT '{}'::jsonb,
	changed BOOLEAN NOT NULL DEFAULT false,
	checked_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`
