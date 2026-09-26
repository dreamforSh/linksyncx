package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// clashPortAllocLockKey serializes listener port allocation across instances.
const clashPortAllocLockKey = "clash_pool_port_alloc"

type clashRepository struct {
	db *sql.DB
}

// NewClashRepository returns the SQL-backed Clash pool repository.
func NewClashRepository(db *sql.DB) service.ClashRepository {
	return &clashRepository{db: db}
}

const clashProfileColumns = `id, name, url_encrypted, url_fingerprint, url_masked, user_agent, enabled,
	refresh_interval_minutes, include_pattern, exclude_pattern, fetch_proxy_id, notes,
	last_refresh_at, last_refresh_status, last_refresh_error, last_format,
	upload_bytes, download_bytes, total_bytes, expire_at, node_count, created_at, updated_at`

type clashRowScanner interface {
	Scan(dest ...any) error
}

func scanClashProfile(row clashRowScanner) (*service.ClashProfile, error) {
	var (
		p             service.ClashProfile
		fetchProxyID  sql.NullInt64
		lastRefreshAt sql.NullTime
		expireAt      sql.NullTime
	)
	if err := row.Scan(&p.ID, &p.Name, &p.URLEncrypted, &p.URLFingerprint, &p.URLMasked, &p.UserAgent, &p.Enabled,
		&p.RefreshIntervalMinutes, &p.IncludePattern, &p.ExcludePattern, &fetchProxyID, &p.Notes,
		&lastRefreshAt, &p.LastRefreshStatus, &p.LastRefreshError, &p.LastFormat,
		&p.UploadBytes, &p.DownloadBytes, &p.TotalBytes, &expireAt, &p.NodeCount, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if fetchProxyID.Valid {
		id := fetchProxyID.Int64
		p.FetchProxyID = &id
	}
	p.LastRefreshAt = nullTimePtr(lastRefreshAt)
	p.ExpireAt = nullTimePtr(expireAt)
	return &p, nil
}

func nullTimePtr(v sql.NullTime) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time
	return &t
}

func (r *clashRepository) CreateProfile(ctx context.Context, p *service.ClashProfile) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO clash_profiles (name, url_encrypted, url_fingerprint, url_masked, user_agent, enabled,
			refresh_interval_minutes, include_pattern, exclude_pattern, fetch_proxy_id, notes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, last_refresh_status, created_at, updated_at`,
		p.Name, p.URLEncrypted, p.URLFingerprint, p.URLMasked, p.UserAgent, p.Enabled,
		p.RefreshIntervalMinutes, p.IncludePattern, p.ExcludePattern, p.FetchProxyID, p.Notes,
	).Scan(&p.ID, &p.LastRefreshStatus, &p.CreatedAt, &p.UpdatedAt)
}

func (r *clashRepository) UpdateProfile(ctx context.Context, p *service.ClashProfile) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE clash_profiles SET name = $2, url_encrypted = $3, url_fingerprint = $4, url_masked = $5,
			user_agent = $6, enabled = $7, refresh_interval_minutes = $8, include_pattern = $9,
			exclude_pattern = $10, fetch_proxy_id = $11, notes = $12, updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`,
		p.ID, p.Name, p.URLEncrypted, p.URLFingerprint, p.URLMasked, p.UserAgent, p.Enabled,
		p.RefreshIntervalMinutes, p.IncludePattern, p.ExcludePattern, p.FetchProxyID, p.Notes)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return service.ErrClashProfileNotFound
	}
	return nil
}

func (r *clashRepository) GetProfile(ctx context.Context, id int64) (*service.ClashProfile, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+clashProfileColumns+` FROM clash_profiles WHERE id = $1 AND deleted_at IS NULL`, id)
	p, err := scanClashProfile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrClashProfileNotFound
	}
	return p, err
}

func (r *clashRepository) ListProfiles(ctx context.Context) ([]service.ClashProfile, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+clashProfileColumns+` FROM clash_profiles WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.ClashProfile, 0)
	for rows.Next() {
		p, err := scanClashProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *clashRepository) ListProfileStats(ctx context.Context) (map[int64]service.ClashProfileStats, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT n.profile_id,
			COUNT(*),
			COUNT(*) FILTER (WHERE n.status = 'active'),
			COUNT(*) FILTER (WHERE n.status = 'active' AND n.health_status = 'healthy'),
			COUNT(*) FILTER (WHERE n.health_status = 'unhealthy'),
			COUNT(*) FILTER (WHERE n.status = 'missing'),
			COUNT(*) FILTER (WHERE n.status = 'invalid'),
			COUNT(*) FILTER (WHERE n.status = 'disabled'),
			COUNT(*) FILTER (WHERE EXISTS (
				SELECT 1 FROM accounts a WHERE a.proxy_id = n.proxy_id AND a.deleted_at IS NULL))
		FROM clash_nodes n
		GROUP BY n.profile_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64]service.ClashProfileStats)
	for rows.Next() {
		var id int64
		var s service.ClashProfileStats
		if err := rows.Scan(&id, &s.Total, &s.Active, &s.Healthy, &s.Unhealthy, &s.Missing, &s.Invalid, &s.Disabled, &s.Bound); err != nil {
			return nil, err
		}
		out[id] = s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	accountRows, err := r.db.QueryContext(ctx, `
		SELECT n.profile_id, COUNT(DISTINCT a.id)
		FROM clash_nodes n
		JOIN accounts a ON a.proxy_id = n.proxy_id AND a.deleted_at IS NULL AND a.parent_account_id IS NULL
		GROUP BY n.profile_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = accountRows.Close() }()
	for accountRows.Next() {
		var id int64
		var count int
		if err := accountRows.Scan(&id, &count); err != nil {
			return nil, err
		}
		s := out[id]
		s.BoundAccounts = count
		out[id] = s
	}
	return out, accountRows.Err()
}

func (r *clashRepository) SoftDeleteProfile(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, `UPDATE clash_profiles SET deleted_at = NOW(), updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return service.ErrClashProfileNotFound
	}
	return nil
}

func (r *clashRepository) RecordRefresh(ctx context.Context, profileID int64, rec service.ClashRefreshRecord) error {
	var (
		setUserInfo             bool
		upload, download, total int64
		expire                  *time.Time
		setNodeCount            bool
		nodeCount               int
	)
	if rec.UserInfo != nil {
		setUserInfo = true
		upload, download, total, expire = rec.UserInfo.Upload, rec.UserInfo.Download, rec.UserInfo.Total, rec.UserInfo.Expire
	}
	if rec.NodeCount != nil {
		setNodeCount, nodeCount = true, *rec.NodeCount
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE clash_profiles SET
			last_refresh_at = $2,
			last_refresh_status = $3,
			last_refresh_error = $4,
			last_format = CASE WHEN $5::text = '' THEN last_format ELSE $5::text END,
			upload_bytes = CASE WHEN $6::boolean THEN $7::bigint ELSE upload_bytes END,
			download_bytes = CASE WHEN $6::boolean THEN $8::bigint ELSE download_bytes END,
			total_bytes = CASE WHEN $6::boolean THEN $9::bigint ELSE total_bytes END,
			expire_at = CASE WHEN $6::boolean THEN $10::timestamptz ELSE expire_at END,
			node_count = CASE WHEN $11::boolean THEN $12::integer ELSE node_count END,
			updated_at = NOW()
		WHERE id = $1`,
		profileID, rec.At, rec.Status, truncateClashError(rec.Error), rec.Format,
		setUserInfo, upload, download, total, expire, setNodeCount, nodeCount)
	return err
}

func truncateClashError(message string) string {
	if len(message) <= 2000 {
		return message
	}
	return message[:2000]
}

func (r *clashRepository) ExistsProfileName(ctx context.Context, name string, excludeID int64) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM clash_profiles WHERE name = $1 AND id <> $2 AND deleted_at IS NULL)`, name, excludeID).Scan(&exists)
	return exists, err
}

func (r *clashRepository) ExistsProfileURL(ctx context.Context, fingerprint string, excludeID int64) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM clash_profiles WHERE url_fingerprint = $1 AND id <> $2 AND deleted_at IS NULL)`, fingerprint, excludeID).Scan(&exists)
	return exists, err
}

const clashNodeColumns = `n.id, n.profile_id, n.name, n.type, n.server, n.server_port, n.config, n.config_hash,
	n.status, n.status_reason, n.missing_since, n.listen_port, n.proxy_id, n.health_status, n.latency_ms,
	n.consecutive_failures, n.consecutive_successes, n.last_checked_at, n.last_check_error,
	n.exit_ip, n.exit_country, n.exit_country_code, n.exit_region, n.exit_city, n.exit_status,
	n.exit_checked_at, n.exit_pending_ip, n.exit_changed_at, n.platform_checks, n.created_at, n.updated_at`

func scanClashNode(row clashRowScanner, extra ...any) (*service.ClashNode, error) {
	var (
		n             service.ClashNode
		configRaw     []byte
		platformRaw   []byte
		missingSince  sql.NullTime
		latency       sql.NullInt64
		lastCheckedAt sql.NullTime
		exitCheckedAt sql.NullTime
		exitChangedAt sql.NullTime
	)
	dest := []any{&n.ID, &n.ProfileID, &n.Name, &n.Type, &n.Server, &n.ServerPort, &configRaw, &n.ConfigHash,
		&n.Status, &n.StatusReason, &missingSince, &n.ListenPort, &n.ProxyID, &n.HealthStatus, &latency,
		&n.ConsecutiveFailures, &n.ConsecutiveSuccesses, &lastCheckedAt, &n.LastCheckError,
		&n.ExitIP, &n.ExitCountry, &n.ExitCountryCode, &n.ExitRegion, &n.ExitCity, &n.ExitStatus,
		&exitCheckedAt, &n.ExitPendingIP, &exitChangedAt, &platformRaw, &n.CreatedAt, &n.UpdatedAt}
	dest = append(dest, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	if len(configRaw) > 0 {
		if err := json.Unmarshal(configRaw, &n.Config); err != nil {
			return nil, fmt.Errorf("decode clash node %d config: %w", n.ID, err)
		}
	}
	if len(platformRaw) > 0 {
		_ = json.Unmarshal(platformRaw, &n.PlatformChecks)
	}
	n.MissingSince = nullTimePtr(missingSince)
	if latency.Valid {
		v := int(latency.Int64)
		n.LatencyMs = &v
	}
	n.LastCheckedAt = nullTimePtr(lastCheckedAt)
	n.ExitCheckedAt = nullTimePtr(exitCheckedAt)
	n.ExitChangedAt = nullTimePtr(exitChangedAt)
	return &n, nil
}

func (r *clashRepository) ListNodesByProfile(ctx context.Context, profileID int64) ([]service.ClashNode, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+clashNodeColumns+` FROM clash_nodes n WHERE n.profile_id = $1 ORDER BY n.id`, profileID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.ClashNode, 0)
	for rows.Next() {
		n, err := scanClashNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

const clashNodeViewFrom = ` FROM clash_nodes n JOIN clash_profiles p ON p.id = n.profile_id`

func (r *clashRepository) queryNodeViews(ctx context.Context, where string, args ...any) ([]service.ClashNodeView, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+clashNodeColumns+`, p.name, p.enabled, p.deleted_at IS NOT NULL`+clashNodeViewFrom+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	views := make([]service.ClashNodeView, 0)
	for rows.Next() {
		var view service.ClashNodeView
		node, err := scanClashNode(rows, &view.ProfileName, &view.ProfileEnabled, &view.ProfileDeleted)
		if err != nil {
			return nil, err
		}
		view.ClashNode = *node
		views = append(views, view)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := r.attachBoundAccounts(ctx, views); err != nil {
		return nil, err
	}
	return views, nil
}

func (r *clashRepository) attachBoundAccounts(ctx context.Context, views []service.ClashNodeView) error {
	if len(views) == 0 {
		return nil
	}
	proxyIDs := make([]int64, 0, len(views))
	index := make(map[int64][]int, len(views))
	for i := range views {
		proxyIDs = append(proxyIDs, views[i].ProxyID)
		index[views[i].ProxyID] = append(index[views[i].ProxyID], i)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, platform, parent_account_id, proxy_id FROM accounts
		WHERE proxy_id = ANY($1) AND deleted_at IS NULL ORDER BY id`, pq.Array(proxyIDs))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			acc     service.ClashBoundAccount
			parent  sql.NullInt64
			proxyID int64
		)
		if err := rows.Scan(&acc.ID, &acc.Name, &acc.Platform, &parent, &proxyID); err != nil {
			return err
		}
		if parent.Valid {
			id := parent.Int64
			acc.ParentAccountID = &id
		}
		for _, i := range index[proxyID] {
			views[i].Accounts = append(views[i].Accounts, acc)
		}
	}
	return rows.Err()
}

func (r *clashRepository) ListNodeViews(ctx context.Context, filter service.ClashNodeFilter, params pagination.PaginationParams) ([]service.ClashNodeView, *pagination.PaginationResult, error) {
	conds := []string{"TRUE"}
	args := make([]any, 0, 6)
	add := func(cond string, value any) {
		args = append(args, value)
		conds = append(conds, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if filter.ProfileID != nil {
		add("n.profile_id = ?", *filter.ProfileID)
	}
	if filter.Status != "" {
		add("n.status = ?", filter.Status)
	}
	if filter.Health != "" {
		add("n.health_status = ?", filter.Health)
	}
	if filter.Bound != nil {
		exists := "EXISTS (SELECT 1 FROM accounts a WHERE a.proxy_id = n.proxy_id AND a.deleted_at IS NULL)"
		if *filter.Bound {
			conds = append(conds, exists)
		} else {
			conds = append(conds, "NOT "+exists)
		}
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		args = append(args, "%"+escapeLike(search)+"%")
		ph := fmt.Sprintf("$%d", len(args))
		conds = append(conds, "(n.name ILIKE "+ph+" OR n.server ILIKE "+ph+" OR n.exit_ip ILIKE "+ph+" OR p.name ILIKE "+ph+")")
	}
	where := " WHERE " + strings.Join(conds, " AND ")

	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*)`+clashNodeViewFrom+where, args...).Scan(&total); err != nil {
		return nil, nil, err
	}
	args = append(args, params.Limit(), params.Offset())
	page := fmt.Sprintf(" ORDER BY n.profile_id, n.id LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	views, err := r.queryNodeViews(ctx, where+page, args...)
	if err != nil {
		return nil, nil, err
	}
	return views, paginationResultFromTotal(total, params), nil
}

func (r *clashRepository) ListAllNodeViews(ctx context.Context) ([]service.ClashNodeView, error) {
	return r.queryNodeViews(ctx, " ORDER BY n.id")
}

func (r *clashRepository) GetNodeView(ctx context.Context, id int64) (*service.ClashNodeView, error) {
	views, err := r.queryNodeViews(ctx, " WHERE n.id = $1", id)
	if err != nil {
		return nil, err
	}
	if len(views) == 0 {
		return nil, service.ErrClashNodeNotFound
	}
	return &views[0], nil
}

func (r *clashRepository) GetNodeViewByProxyID(ctx context.Context, proxyID int64) (*service.ClashNodeView, error) {
	views, err := r.queryNodeViews(ctx, " WHERE n.proxy_id = $1", proxyID)
	if err != nil {
		return nil, err
	}
	if len(views) == 0 {
		return nil, service.ErrClashNodeNotFound
	}
	return &views[0], nil
}

func (r *clashRepository) ApplyNodeSync(ctx context.Context, profileID int64, plan *service.ClashNodeSyncPlan, ports service.ClashPortRange, specFactory service.ClashManagedProxySpecFactory) (*service.ClashNodeSyncResult, error) {
	result := &service.ClashNodeSyncResult{}
	if plan == nil {
		return result, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	previous := make(map[int64]struct {
		status  string
		proxyID int64
	})
	rows, err := tx.QueryContext(ctx, `SELECT id, status, proxy_id FROM clash_nodes WHERE profile_id = $1 FOR UPDATE`, profileID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, proxyID int64
		var status string
		if err := rows.Scan(&id, &status, &proxyID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		previous[id] = struct {
			status  string
			proxyID int64
		}{status, proxyID}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	var invalidatedAccounts []int64
	if len(plan.Inserts) > 0 {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, clashPortAllocLockKey); err != nil {
			return nil, err
		}
		used, err := clashUsedPorts(ctx, tx)
		if err != nil {
			return nil, err
		}
		next := ports.Start
		for _, insert := range plan.Inserts {
			for next <= ports.End {
				if _, taken := used[next]; !taken {
					break
				}
				next++
			}
			if next > ports.End {
				return nil, fmt.Errorf("no free clash listener port in %d-%d", ports.Start, ports.End)
			}
			port := next
			used[port] = struct{}{}
			spec := specFactory()
			var proxyID int64
			if err := tx.QueryRowContext(ctx, `
				INSERT INTO proxies (name, protocol, host, port, username, password, status, fallback_mode, expiry_warn_days, source, created_at, updated_at)
				VALUES ($1, 'socks5', $2, $3, $4, $5, 'active', 'none', 7, 'clash', NOW(), NOW())
				RETURNING id`, insert.ProxyName, spec.Host, port, spec.Username, spec.Password).Scan(&proxyID); err != nil {
				return nil, fmt.Errorf("create managed proxy: %w", err)
			}
			configJSON, err := json.Marshal(insert.Config)
			if err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO clash_nodes (profile_id, name, type, server, server_port, config, config_hash, status, status_reason, listen_port, proxy_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
				profileID, insert.Name, insert.Type, insert.Server, insert.ServerPort, configJSON, insert.ConfigHash,
				insert.Status, insert.StatusReason, port, proxyID); err != nil {
				return nil, fmt.Errorf("create clash node: %w", err)
			}
			result.Inserted++
		}
		result.Structural = true
	}

	for _, update := range plan.Updates {
		prev, ok := previous[update.NodeID]
		if !ok {
			continue
		}
		configJSON, err := json.Marshal(update.Config)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE clash_nodes SET name = $2, type = $3, server = $4, server_port = $5, config = $6, config_hash = $7,
				status = $8::text, status_reason = $9::text,
				missing_since = CASE WHEN $8::text = 'missing' THEN missing_since ELSE NULL END,
				exit_status = CASE WHEN $10::boolean AND exit_status = 'ok' THEN 'stale' ELSE exit_status END,
				updated_at = NOW()
			WHERE id = $1`,
			update.NodeID, update.Name, update.Type, update.Server, update.ServerPort, configJSON, update.ConfigHash,
			update.Status, update.StatusReason, update.ConfigChanged); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE proxies SET name = $2, updated_at = NOW() WHERE id = $1 AND name <> $2`, prev.proxyID, update.ProxyName); err != nil {
			return nil, err
		}
		if update.ConfigChanged {
			// The node's egress may have moved; per-proxy billing probes are stale.
			ids, err := invalidateProxyProbeSnapshots(ctx, tx, prev.proxyID)
			if err != nil {
				return nil, err
			}
			invalidatedAccounts = append(invalidatedAccounts, ids...)
		}
		if update.ConfigChanged || update.Status != prev.status {
			result.Structural = true
		}
		result.Updated++
	}

	if len(plan.Missing) > 0 {
		res, err := tx.ExecContext(ctx, `
			UPDATE clash_nodes SET status = 'missing', status_reason = 'removed from subscription',
				missing_since = COALESCE(missing_since, NOW()), updated_at = NOW()
			WHERE profile_id = $1 AND id = ANY($2) AND status IN ('active', 'invalid')`,
			profileID, pq.Array(plan.Missing))
		if err != nil {
			return nil, err
		}
		affected, _ := res.RowsAffected()
		result.Missing = int(affected)
		if affected > 0 {
			result.Structural = true
		}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE clash_profiles SET node_count = (
			SELECT COUNT(*) FROM clash_nodes WHERE profile_id = $1 AND status <> 'missing'), updated_at = NOW()
		WHERE id = $1`, profileID); err != nil {
		return nil, err
	}
	if len(invalidatedAccounts) > 0 {
		if err := enqueueProxyProbeAccountChanges(ctx, tx, invalidatedAccounts); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func clashUsedPorts(ctx context.Context, tx *sql.Tx) (map[int]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT listen_port FROM clash_nodes
		UNION
		SELECT port FROM proxies WHERE source = 'clash' AND deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	used := make(map[int]struct{})
	for rows.Next() {
		var port int
		if err := rows.Scan(&port); err != nil {
			return nil, err
		}
		used[port] = struct{}{}
	}
	return used, rows.Err()
}

func (r *clashRepository) SetNodeStatus(ctx context.Context, nodeID int64, status, reason string) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE clash_nodes SET status = $2, status_reason = $3,
			missing_since = CASE WHEN $2 = 'missing' THEN COALESCE(missing_since, NOW()) ELSE NULL END,
			updated_at = NOW()
		WHERE id = $1`, nodeID, status, reason)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return service.ErrClashNodeNotFound
	}
	return nil
}

func (r *clashRepository) MarkProfileNodes(ctx context.Context, profileID int64, status, reason string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE clash_nodes SET status = $2, status_reason = $3,
			missing_since = CASE WHEN $2 = 'missing' THEN COALESCE(missing_since, NOW()) ELSE NULL END,
			updated_at = NOW()
		WHERE profile_id = $1`, profileID, status, reason)
	return err
}

func (r *clashRepository) UpdateNodeHealth(ctx context.Context, updates []service.ClashNodeHealthUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	ids := make([]int64, len(updates))
	statuses := make([]string, len(updates))
	latencies := make([]sql.NullInt64, len(updates))
	failures := make([]int64, len(updates))
	successes := make([]int64, len(updates))
	errs := make([]string, len(updates))
	checked := make([]string, len(updates))
	for i, u := range updates {
		ids[i], statuses[i], failures[i], successes[i] = u.NodeID, u.HealthStatus, int64(u.ConsecutiveFailures), int64(u.ConsecutiveSuccesses)
		errs[i], checked[i] = truncateClashError(u.Error), u.CheckedAt.UTC().Format(time.RFC3339Nano)
		if u.LatencyMs != nil {
			latencies[i] = sql.NullInt64{Int64: int64(*u.LatencyMs), Valid: true}
		}
	}
	latencyArg := make([]any, len(latencies))
	for i, l := range latencies {
		if l.Valid {
			latencyArg[i] = l.Int64
		}
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE clash_nodes n SET health_status = d.status,
			latency_ms = COALESCE(d.latency, n.latency_ms),
			consecutive_failures = d.failures, consecutive_successes = d.successes,
			last_check_error = d.err, last_checked_at = d.checked, updated_at = NOW()
		FROM unnest($1::bigint[], $2::text[], $3::bigint[], $4::bigint[], $5::bigint[], $6::text[], $7::text[]::timestamptz[])
			AS d(id, status, latency, failures, successes, err, checked)
		WHERE n.id = d.id`,
		pq.Array(ids), pq.Array(statuses), pq.Array(latencyArg), pq.Array(failures), pq.Array(successes), pq.Array(errs), pq.Array(checked))
	return err
}

func (r *clashRepository) UpdateNodeExit(ctx context.Context, u service.ClashNodeExitUpdate) error {
	var platformJSON []byte
	if u.PlatformChecks != nil {
		raw, err := json.Marshal(u.PlatformChecks)
		if err != nil {
			return err
		}
		platformJSON = raw
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE clash_nodes SET exit_ip = $2, exit_country = $3, exit_country_code = $4, exit_region = $5,
			exit_city = $6, exit_status = $7, exit_pending_ip = $8,
			exit_changed_at = COALESCE($9, exit_changed_at), exit_checked_at = $10,
			platform_checks = COALESCE($11::jsonb, platform_checks), updated_at = NOW()
		WHERE id = $1`,
		u.NodeID, u.ExitIP, u.Country, u.CountryCode, u.Region, u.City, u.ExitStatus, u.PendingIP,
		u.ChangedAt, u.CheckedAt, nullableJSON(platformJSON))
	return err
}

func nullableJSON(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

func (r *clashRepository) AcceptNodeExit(ctx context.Context, nodeID int64) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE clash_nodes SET exit_ip = exit_pending_ip, exit_pending_ip = '', exit_status = 'stale',
			exit_changed_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND exit_status = 'changed' AND exit_pending_ip <> ''`, nodeID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return service.ErrClashNodeNotFound
	}
	return nil
}

func (r *clashRepository) ReclaimNodes(ctx context.Context, missingBefore time.Time) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
		WITH victims AS (
			SELECT n.id, n.proxy_id FROM clash_nodes n JOIN clash_profiles p ON p.id = n.profile_id
			WHERE ((n.status = 'missing' AND n.missing_since < $1) OR p.deleted_at IS NOT NULL)
				AND NOT EXISTS (SELECT 1 FROM accounts a WHERE a.proxy_id = n.proxy_id AND a.deleted_at IS NULL)
		), removed AS (
			DELETE FROM clash_nodes WHERE id IN (SELECT id FROM victims) RETURNING proxy_id
		)
		UPDATE proxies SET deleted_at = NOW(), updated_at = NOW()
		WHERE id IN (SELECT proxy_id FROM removed) AND deleted_at IS NULL
		RETURNING id`, missingBefore)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *clashRepository) ListRenderNodes(ctx context.Context) ([]service.ClashRenderNode, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT n.id, n.listen_port, COALESCE(pr.username, ''), COALESCE(pr.password, ''), n.config,
			(n.status = 'active' AND p.enabled AND p.deleted_at IS NULL) AS live,
			EXISTS (SELECT 1 FROM accounts a WHERE a.proxy_id = n.proxy_id AND a.deleted_at IS NULL) AS bound
		FROM clash_nodes n
		JOIN clash_profiles p ON p.id = n.profile_id
		JOIN proxies pr ON pr.id = n.proxy_id
		ORDER BY n.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.ClashRenderNode, 0)
	for rows.Next() {
		var (
			node        service.ClashRenderNode
			configRaw   []byte
			live, bound bool
		)
		if err := rows.Scan(&node.NodeID, &node.ListenPort, &node.Username, &node.Password, &configRaw, &live, &bound); err != nil {
			return nil, err
		}
		switch {
		case live:
			if err := json.Unmarshal(configRaw, &node.Config); err != nil {
				return nil, fmt.Errorf("decode clash node %d config: %w", node.NodeID, err)
			}
		case bound:
			// Keep a REJECT placeholder: the port stays ours and bound
			// accounts fail closed instead of reaching another listener.
		default:
			continue
		}
		out = append(out, node)
	}
	return out, rows.Err()
}

func (r *clashRepository) RehostManagedProxies(ctx context.Context, host string) ([]int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
		WITH moved AS (
			UPDATE proxies SET host = $1, updated_at = NOW()
			WHERE source = 'clash' AND deleted_at IS NULL AND host <> $1
			RETURNING id
		)
		SELECT a.id FROM accounts a WHERE a.proxy_id IN (SELECT id FROM moved) AND a.deleted_at IS NULL`, host)
	if err != nil {
		return nil, err
	}
	accountIDs := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		accountIDs = append(accountIDs, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(accountIDs) > 0 {
		if err := enqueueProxyProbeAccountChanges(ctx, tx, accountIDs); err != nil {
			return nil, err
		}
	}
	return accountIDs, tx.Commit()
}

func (r *clashRepository) PauseAccounts(ctx context.Context, reasons map[int64]string, until, renewBefore time.Time) ([]int64, error) {
	if len(reasons) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(reasons))
	texts := make([]string, 0, len(reasons))
	for id, reason := range reasons {
		ids = append(ids, id)
		texts = append(texts, reason)
	}
	return r.updateAccountsAndNotify(ctx, `
		UPDATE accounts a SET temp_unschedulable_until = $3, temp_unschedulable_reason = d.reason, updated_at = NOW()
		FROM unnest($1::bigint[], $2::text[]) AS d(id, reason)
		WHERE a.id = d.id AND a.deleted_at IS NULL
			AND (a.temp_unschedulable_until IS NULL
				OR a.temp_unschedulable_until < $4
				OR (a.temp_unschedulable_reason LIKE $5 AND a.temp_unschedulable_reason IS DISTINCT FROM d.reason))
		RETURNING a.id`,
		pq.Array(ids), pq.Array(texts), until, renewBefore, service.ClashPauseReasonPrefix+"%")
}

func (r *clashRepository) ClearPauses(ctx context.Context, accountIDs []int64) ([]int64, error) {
	if len(accountIDs) == 0 {
		return nil, nil
	}
	return r.updateAccountsAndNotify(ctx, `
		UPDATE accounts SET temp_unschedulable_until = NULL, temp_unschedulable_reason = NULL, updated_at = NOW()
		WHERE id = ANY($1) AND deleted_at IS NULL AND temp_unschedulable_reason LIKE $2
		RETURNING id`, pq.Array(accountIDs), service.ClashPauseReasonPrefix+"%")
}

func (r *clashRepository) updateAccountsAndNotify(ctx context.Context, query string, args ...any) ([]int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	changed := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		changed = append(changed, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(changed) > 0 {
		if err := enqueueProxyProbeAccountChanges(ctx, tx, changed); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return changed, nil
}

func (r *clashRepository) ListPausedAccounts(ctx context.Context) (map[int64]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, temp_unschedulable_reason FROM accounts
		WHERE deleted_at IS NULL AND temp_unschedulable_reason LIKE $1 AND temp_unschedulable_until > NOW()`,
		service.ClashPauseReasonPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64]string)
	for rows.Next() {
		var (
			id     int64
			reason sql.NullString
		)
		if err := rows.Scan(&id, &reason); err != nil {
			return nil, err
		}
		out[id] = reason.String
	}
	return out, rows.Err()
}
