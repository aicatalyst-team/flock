// Package store provides persistent state for the control plane.
//
// The default backend is SQLite (pure Go via modernc.org/sqlite — no CGO).
// All schemas are managed by inline migrations applied at Open() time.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the persistence abstraction. Implementations must be safe for
// concurrent use.
type Store interface {
	APIKeys() APIKeyStore
	Models() ModelStore
	Nodes() NodeStore
	Usage() UsageStore
	Audit() AuditStore
	Close() error
}

// ---- types ----

// APIKey represents a Flock API key. Plain key text is never stored; only Hash.
type APIKey struct {
	ID               string
	Hash             string
	Name             string
	Scope            string // "admin" | "user"
	UserID           string // free-form owner identifier; empty for legacy keys
	QuotaDailyTokens int64  // 0 = unlimited
	CreatedAt        time.Time
	Revoked          bool
}

type APIKeyStore interface {
	Create(ctx context.Context, k APIKey) error
	GetByHash(ctx context.Context, hash string) (*APIKey, error)
	GetByID(ctx context.Context, id string) (*APIKey, error)
	List(ctx context.Context) ([]APIKey, error)
	Revoke(ctx context.Context, id string) error
}

type Model struct {
	ID          string
	CatalogID   string
	Source      string
	Status      string
	SizeBytes   int64
	InstalledAt time.Time
}

type ModelStore interface {
	Upsert(ctx context.Context, m Model) error
	Get(ctx context.Context, id string) (*Model, error)
	List(ctx context.Context) ([]Model, error)
	Delete(ctx context.Context, id string) error
}

type Node struct {
	ID            string
	Hostname      string
	OS            string
	Arch          string
	RAMGB         int
	Address       string // host:port reachable from the leader
	HardwareJSON  string
	LastHeartbeat time.Time
	State         string
}

type NodeStore interface {
	Upsert(ctx context.Context, n Node) error
	Get(ctx context.Context, id string) (*Node, error)
	List(ctx context.Context) ([]Node, error)
	Delete(ctx context.Context, id string) error
}

// Usage records a single completed inference request.
type Usage struct {
	ID               int64
	TS               time.Time
	APIKeyID         string
	UserID           string
	Model            string
	Protocol         string // openai | anthropic
	PromptTokens     int
	CompletionTokens int
	LatencyMS        int
	Outcome          string // ok | error | rate_limited
}

type UsageStore interface {
	Record(ctx context.Context, u Usage) error
	SumTokensSince(ctx context.Context, apiKeyID string, since time.Time) (int64, error)
	RecentByUser(ctx context.Context, userID string, limit int) ([]Usage, error)
	Recent(ctx context.Context, limit int) ([]Usage, error)
}

// AuditEntry records an action taken via the admin API or gateway.
type AuditEntry struct {
	ID       int64
	TS       time.Time
	Actor    string // user id or "system"
	Action   string
	Target   string
	Metadata string // JSON-encoded extras
}

type AuditStore interface {
	Record(ctx context.Context, e AuditEntry) error
	Recent(ctx context.Context, limit int) ([]AuditEntry, error)
}

// ---- open ----

func OpenSQLite(dsn string) (Store, error) {
	db, err := sql.Open("sqlite", dsn+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := applySchema(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &sqliteStore{db: db}, nil
}

type sqliteStore struct {
	db *sql.DB
}

func (s *sqliteStore) APIKeys() APIKeyStore { return &sqliteAPIKeys{db: s.db} }
func (s *sqliteStore) Models() ModelStore   { return &sqliteModels{db: s.db} }
func (s *sqliteStore) Nodes() NodeStore     { return &sqliteNodes{db: s.db} }
func (s *sqliteStore) Usage() UsageStore    { return &sqliteUsage{db: s.db} }
func (s *sqliteStore) Audit() AuditStore    { return &sqliteAudit{db: s.db} }
func (s *sqliteStore) Close() error         { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS api_keys (
    id                  TEXT PRIMARY KEY,
    hash                TEXT NOT NULL UNIQUE,
    name                TEXT NOT NULL,
    scope               TEXT NOT NULL,
    user_id             TEXT NOT NULL DEFAULT '',
    quota_daily_tokens  INTEGER NOT NULL DEFAULT 0,
    created_at          INTEGER NOT NULL,
    revoked             INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(hash);

CREATE TABLE IF NOT EXISTS models (
    id           TEXT PRIMARY KEY,
    catalog_id   TEXT NOT NULL,
    source       TEXT NOT NULL,
    status       TEXT NOT NULL,
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    installed_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS nodes (
    id             TEXT PRIMARY KEY,
    hostname       TEXT NOT NULL,
    os             TEXT NOT NULL,
    arch           TEXT NOT NULL,
    ram_gb         INTEGER NOT NULL,
    address        TEXT NOT NULL DEFAULT '',
    hardware_json  TEXT NOT NULL,
    last_heartbeat INTEGER NOT NULL,
    state          TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS usage (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    ts                INTEGER NOT NULL,
    api_key_id        TEXT NOT NULL,
    user_id           TEXT NOT NULL,
    model             TEXT NOT NULL,
    protocol          TEXT NOT NULL,
    prompt_tokens     INTEGER NOT NULL,
    completion_tokens INTEGER NOT NULL,
    latency_ms        INTEGER NOT NULL,
    outcome           TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_usage_key_ts ON usage(api_key_id, ts);
CREATE INDEX IF NOT EXISTS idx_usage_user_ts ON usage(user_id, ts);

CREATE TABLE IF NOT EXISTS audit_log (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    ts            INTEGER NOT NULL,
    actor         TEXT NOT NULL,
    action        TEXT NOT NULL,
    target        TEXT NOT NULL,
    metadata_json TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_log(ts);
`

func applySchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, schema)
	return err
}

// ---- api_keys ----

type sqliteAPIKeys struct{ db *sql.DB }

func (s *sqliteAPIKeys) Create(ctx context.Context, k APIKey) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys(id, hash, name, scope, user_id, quota_daily_tokens, created_at, revoked)
		 VALUES(?,?,?,?,?,?,?,?)`,
		k.ID, k.Hash, k.Name, k.Scope, k.UserID, k.QuotaDailyTokens, k.CreatedAt.Unix(), boolToInt(k.Revoked))
	if err != nil {
		return fmt.Errorf("insert api_key: %w", err)
	}
	return nil
}

func (s *sqliteAPIKeys) GetByHash(ctx context.Context, hash string) (*APIKey, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, hash, name, scope, user_id, quota_daily_tokens, created_at, revoked
		 FROM api_keys WHERE hash = ?`, hash)
	return scanKey(row)
}

func (s *sqliteAPIKeys) GetByID(ctx context.Context, id string) (*APIKey, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, hash, name, scope, user_id, quota_daily_tokens, created_at, revoked
		 FROM api_keys WHERE id = ?`, id)
	return scanKey(row)
}

func scanKey(row *sql.Row) (*APIKey, error) {
	var k APIKey
	var ts int64
	var rev int
	if err := row.Scan(&k.ID, &k.Hash, &k.Name, &k.Scope, &k.UserID, &k.QuotaDailyTokens, &ts, &rev); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scan api_key: %w", err)
	}
	k.CreatedAt = time.Unix(ts, 0)
	k.Revoked = rev != 0
	return &k, nil
}

func (s *sqliteAPIKeys) List(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, hash, name, scope, user_id, quota_daily_tokens, created_at, revoked
		 FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query api_keys: %w", err)
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		var ts int64
		var rev int
		if err := rows.Scan(&k.ID, &k.Hash, &k.Name, &k.Scope, &k.UserID, &k.QuotaDailyTokens, &ts, &rev); err != nil {
			return nil, fmt.Errorf("scan api_key: %w", err)
		}
		k.CreatedAt = time.Unix(ts, 0)
		k.Revoked = rev != 0
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *sqliteAPIKeys) Revoke(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET revoked = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("revoke api_key: %w", err)
	}
	return nil
}

// ---- models ----

type sqliteModels struct{ db *sql.DB }

func (s *sqliteModels) Upsert(ctx context.Context, m Model) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO models(id, catalog_id, source, status, size_bytes, installed_at)
		 VALUES(?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   catalog_id=excluded.catalog_id,
		   source=excluded.source,
		   status=excluded.status,
		   size_bytes=excluded.size_bytes`,
		m.ID, m.CatalogID, m.Source, m.Status, m.SizeBytes, m.InstalledAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert model: %w", err)
	}
	return nil
}

func (s *sqliteModels) Get(ctx context.Context, id string) (*Model, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, catalog_id, source, status, size_bytes, installed_at FROM models WHERE id = ?`, id)
	var m Model
	var ts int64
	if err := row.Scan(&m.ID, &m.CatalogID, &m.Source, &m.Status, &m.SizeBytes, &ts); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scan model: %w", err)
	}
	m.InstalledAt = time.Unix(ts, 0)
	return &m, nil
}

func (s *sqliteModels) List(ctx context.Context) ([]Model, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, catalog_id, source, status, size_bytes, installed_at FROM models ORDER BY installed_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query models: %w", err)
	}
	defer rows.Close()
	var out []Model
	for rows.Next() {
		var m Model
		var ts int64
		if err := rows.Scan(&m.ID, &m.CatalogID, &m.Source, &m.Status, &m.SizeBytes, &ts); err != nil {
			return nil, fmt.Errorf("scan model: %w", err)
		}
		m.InstalledAt = time.Unix(ts, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *sqliteModels) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM models WHERE id = ?`, id)
	return err
}

// ---- nodes ----

type sqliteNodes struct{ db *sql.DB }

func (s *sqliteNodes) Upsert(ctx context.Context, n Node) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO nodes(id, hostname, os, arch, ram_gb, address, hardware_json, last_heartbeat, state)
		 VALUES(?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   hostname=excluded.hostname,
		   os=excluded.os,
		   arch=excluded.arch,
		   ram_gb=excluded.ram_gb,
		   address=excluded.address,
		   hardware_json=excluded.hardware_json,
		   last_heartbeat=excluded.last_heartbeat,
		   state=excluded.state`,
		n.ID, n.Hostname, n.OS, n.Arch, n.RAMGB, n.Address, n.HardwareJSON, n.LastHeartbeat.Unix(), n.State)
	return err
}

func (s *sqliteNodes) Get(ctx context.Context, id string) (*Node, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, hostname, os, arch, ram_gb, address, hardware_json, last_heartbeat, state FROM nodes WHERE id = ?`, id)
	var n Node
	var ts int64
	if err := row.Scan(&n.ID, &n.Hostname, &n.OS, &n.Arch, &n.RAMGB, &n.Address, &n.HardwareJSON, &ts, &n.State); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	n.LastHeartbeat = time.Unix(ts, 0)
	return &n, nil
}

func (s *sqliteNodes) List(ctx context.Context) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, hostname, os, arch, ram_gb, address, hardware_json, last_heartbeat, state FROM nodes ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		var ts int64
		if err := rows.Scan(&n.ID, &n.Hostname, &n.OS, &n.Arch, &n.RAMGB, &n.Address, &n.HardwareJSON, &ts, &n.State); err != nil {
			return nil, err
		}
		n.LastHeartbeat = time.Unix(ts, 0)
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *sqliteNodes) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, id)
	return err
}

// ---- usage ----

type sqliteUsage struct{ db *sql.DB }

func (s *sqliteUsage) Record(ctx context.Context, u Usage) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage(ts, api_key_id, user_id, model, protocol,
		    prompt_tokens, completion_tokens, latency_ms, outcome)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		u.TS.Unix(), u.APIKeyID, u.UserID, u.Model, u.Protocol,
		u.PromptTokens, u.CompletionTokens, u.LatencyMS, u.Outcome)
	return err
}

func (s *sqliteUsage) SumTokensSince(ctx context.Context, apiKeyID string, since time.Time) (int64, error) {
	var sum sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(prompt_tokens + completion_tokens), 0)
		 FROM usage WHERE api_key_id = ? AND ts >= ?`,
		apiKeyID, since.Unix()).Scan(&sum)
	if err != nil {
		return 0, err
	}
	return sum.Int64, nil
}

func (s *sqliteUsage) Recent(ctx context.Context, limit int) ([]Usage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ts, api_key_id, user_id, model, protocol,
		        prompt_tokens, completion_tokens, latency_ms, outcome
		 FROM usage ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsage(rows)
}

func (s *sqliteUsage) RecentByUser(ctx context.Context, userID string, limit int) ([]Usage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ts, api_key_id, user_id, model, protocol,
		        prompt_tokens, completion_tokens, latency_ms, outcome
		 FROM usage WHERE user_id = ? ORDER BY ts DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsage(rows)
}

func scanUsage(rows *sql.Rows) ([]Usage, error) {
	var out []Usage
	for rows.Next() {
		var u Usage
		var ts int64
		if err := rows.Scan(&u.ID, &ts, &u.APIKeyID, &u.UserID, &u.Model, &u.Protocol,
			&u.PromptTokens, &u.CompletionTokens, &u.LatencyMS, &u.Outcome); err != nil {
			return nil, err
		}
		u.TS = time.Unix(ts, 0)
		out = append(out, u)
	}
	return out, rows.Err()
}

// ---- audit ----

type sqliteAudit struct{ db *sql.DB }

func (s *sqliteAudit) Record(ctx context.Context, e AuditEntry) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log(ts, actor, action, target, metadata_json)
		 VALUES(?,?,?,?,?)`,
		e.TS.Unix(), e.Actor, e.Action, e.Target, e.Metadata)
	return err
}

func (s *sqliteAudit) Recent(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ts, actor, action, target, metadata_json
		 FROM audit_log ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.Actor, &e.Action, &e.Target, &e.Metadata); err != nil {
			return nil, err
		}
		e.TS = time.Unix(ts, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
