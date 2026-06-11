// Package store provides persistent state for the control plane.
//
// The default backend is SQLite (pure Go via modernc.org/sqlite — no CGO).
// All schemas are managed by inline migrations applied at Open() time.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the persistence abstraction. Implementations must be safe for
// concurrent use.
type Store interface {
	APIKeys() APIKeyStore
	Models() ModelStore
	Nodes() NodeStore
	Placements() PlacementStore
	Shards() ShardStore
	Usage() UsageStore
	Audit() AuditStore
	Close() error
}

// ---- types ----

// APIKey represents a Flock API key. Plain key text is never stored; only Hash.
//
// AllowedModels, when non-nil, restricts the key to the listed model ids
// — requests for any other model are refused with HTTP 403
// `model_not_allowed`. A nil slice (the default, and the value for keys
// created before this column existed) means "no restriction". An empty
// slice means "no model is allowed" — useful for hard-disabling a key
// without revoking it. Entries support glob suffix wildcards (e.g.
// `claude-*`, `gpt-*`) so vendor families can be approved in one row.
type APIKey struct {
	ID               string
	Hash             string
	Name             string
	Scope            string // "admin" | "user" | "node"
	UserID           string
	QuotaDailyTokens int64
	AllowedModels    []string
	CreatedAt        time.Time
	Revoked          bool
}

type APIKeyStore interface {
	Create(ctx context.Context, k APIKey) error
	GetByHash(ctx context.Context, hash string) (*APIKey, error)
	GetByID(ctx context.Context, id string) (*APIKey, error)
	List(ctx context.Context) ([]APIKey, error)
	Revoke(ctx context.Context, id string) error
	// UpdateAllowedModels replaces the allowlist for the given key id.
	// Pass nil for "unrestricted", an empty slice for "deny all", or a
	// list (each entry may include a `*` suffix wildcard).
	UpdateAllowedModels(ctx context.Context, id string, allowed []string) error
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

// Node represents a worker registered with the control plane.
//
// WorkerToken is a long-lived shared secret used for BOTH directions of
// communication between the leader and this worker. v0.3 stores it in
// plaintext for simplicity — a future revision will replace this with
// per-direction HMAC keys.
type Node struct {
	ID            string
	Hostname      string
	OS            string
	Arch          string
	RAMGB         int
	Address       string
	WorkerToken   string
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

// Placement records that a given node currently hosts a given model.
// The special node id "local" represents the leader's own engine.
type Placement struct {
	NodeID   string
	ModelID  string
	Status   string // ready | loading | error
	LastSeen time.Time
}

type PlacementStore interface {
	Upsert(ctx context.Context, p Placement) error
	GetByModel(ctx context.Context, modelID string) ([]Placement, error)
	GetByNode(ctx context.Context, nodeID string) ([]Placement, error)
	ReplaceForNode(ctx context.Context, nodeID string, ps []Placement) error
	Delete(ctx context.Context, nodeID, modelID string) error
}

// Shard is one piece of a model that has been split across multiple nodes.
// A sharded model has N "rpc" shards (one per node hosting a piece of the
// model) plus exactly one "coordinator" shard (the node running
// llama-server --rpc <list>). The router routes requests to the coordinator
// only; the coordinator talks to the rpc shards internally.
type Shard struct {
	ID         string
	ModelID    string
	Role       string // "coordinator" | "rpc"
	NodeID     string
	Address    string // host:port reachable by the coordinator (rpc) or by the leader (coordinator)
	ProcessID  string // id assigned by the supervisor that launched the process
	Status     string // starting | ready | failed | stopped
	ConfigJSON string
	CreatedAt  time.Time
	LastSeen   time.Time
}

type ShardStore interface {
	Create(ctx context.Context, s Shard) error
	Get(ctx context.Context, id string) (*Shard, error)
	GetByModel(ctx context.Context, modelID string) ([]Shard, error)
	UpdateStatus(ctx context.Context, id, status string) error
	List(ctx context.Context) ([]Shard, error)
	Delete(ctx context.Context, id string) error
	DeleteByModel(ctx context.Context, modelID string) error
}

// Usage records a single completed inference request.
type Usage struct {
	ID               int64
	TS               time.Time
	APIKeyID         string
	UserID           string
	Model            string
	Protocol         string
	PromptTokens     int
	CompletionTokens int
	LatencyMS        int
	Outcome          string
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
	Actor    string
	Action   string
	Target   string
	Metadata string
}

type AuditStore interface {
	Record(ctx context.Context, e AuditEntry) error
	Recent(ctx context.Context, limit int) ([]AuditEntry, error)
}

// ---- open ----

func OpenSQLite(dsn string) (Store, error) {
	db, err := sql.Open("sqlite", appendPragmas(dsn))
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

func (s *sqliteStore) APIKeys() APIKeyStore       { return &sqliteAPIKeys{db: s.db} }
func (s *sqliteStore) Models() ModelStore         { return &sqliteModels{db: s.db} }
func (s *sqliteStore) Nodes() NodeStore           { return &sqliteNodes{db: s.db} }
func (s *sqliteStore) Placements() PlacementStore { return &sqlitePlacements{db: s.db} }
func (s *sqliteStore) Shards() ShardStore         { return &sqliteShards{db: s.db} }
func (s *sqliteStore) Usage() UsageStore          { return &sqliteUsage{db: s.db} }
func (s *sqliteStore) Audit() AuditStore          { return &sqliteAudit{db: s.db} }
func (s *sqliteStore) Close() error               { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS api_keys (
    id                  TEXT PRIMARY KEY,
    hash                TEXT NOT NULL UNIQUE,
    name                TEXT NOT NULL,
    scope               TEXT NOT NULL,
    user_id             TEXT NOT NULL DEFAULT '',
    quota_daily_tokens  INTEGER NOT NULL DEFAULT 0,
    allowed_models      TEXT,
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
    worker_token   TEXT NOT NULL DEFAULT '',
    hardware_json  TEXT NOT NULL,
    last_heartbeat INTEGER NOT NULL,
    state          TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS model_placements (
    node_id    TEXT NOT NULL,
    model_id   TEXT NOT NULL,
    status     TEXT NOT NULL,
    last_seen  INTEGER NOT NULL,
    PRIMARY KEY (node_id, model_id)
);
CREATE INDEX IF NOT EXISTS idx_placements_model ON model_placements(model_id);

CREATE TABLE IF NOT EXISTS shards (
    id          TEXT PRIMARY KEY,
    model_id    TEXT NOT NULL,
    role        TEXT NOT NULL,
    node_id     TEXT NOT NULL,
    address     TEXT NOT NULL,
    process_id  TEXT NOT NULL,
    status      TEXT NOT NULL,
    config_json TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    last_seen   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_shards_model ON shards(model_id);

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
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return err
	}
	return runColumnMigrations(ctx, db)
}

// runColumnMigrations brings older databases up to the current column set.
// Each migration is idempotent — checks whether the column exists before
// adding it. SQLite does not support `ADD COLUMN IF NOT EXISTS`, so the
// presence check uses PRAGMA table_info.
//
// Currently empty (cost_micros was added in v0.6 and removed in v0.7 —
// the column will remain in upgraded-from-v0.6 databases as an unused
// nullable field, which is harmless). Add new column migrations here as
// the schema evolves.
func runColumnMigrations(ctx context.Context, db *sql.DB) error {
	type colMigration struct {
		table, column, ddl string
	}
	migrations := []colMigration{
		// v0.8 — per-key model allowlist. NULL preserves the existing
		// "any model" behavior for keys created before this column.
		{table: "api_keys", column: "allowed_models", ddl: `ALTER TABLE api_keys ADD COLUMN allowed_models TEXT`},
	}
	for _, m := range migrations {
		exists, err := columnExists(ctx, db, m.table, m.column)
		if err != nil {
			return fmt.Errorf("check column %s.%s: %w", m.table, m.column, err)
		}
		if exists {
			continue
		}
		if _, err := db.ExecContext(ctx, m.ddl); err != nil {
			return fmt.Errorf("migrate %s.%s: %w", m.table, m.column, err)
		}
	}
	return nil
}

func columnExists(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// appendPragmas safely appends the required pragma settings to a DSN that
// may already contain a query string.
func appendPragmas(dsn string) string {
	pragmas := "_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + pragmas
}

// ---- api_keys ----

type sqliteAPIKeys struct{ db *sql.DB }

func (s *sqliteAPIKeys) Create(ctx context.Context, k APIKey) error {
	allowed, err := marshalAllowed(k.AllowedModels)
	if err != nil {
		return fmt.Errorf("encode allowed_models: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys(id, hash, name, scope, user_id, quota_daily_tokens, allowed_models, created_at, revoked)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		k.ID, k.Hash, k.Name, k.Scope, k.UserID, k.QuotaDailyTokens, allowed,
		k.CreatedAt.Unix(), boolToInt(k.Revoked)); err != nil {
		return fmt.Errorf("insert api_key: %w", err)
	}
	return nil
}

func (s *sqliteAPIKeys) GetByHash(ctx context.Context, hash string) (*APIKey, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, hash, name, scope, user_id, quota_daily_tokens, allowed_models, created_at, revoked
		 FROM api_keys WHERE hash = ?`, hash)
	return scanKey(row)
}

func (s *sqliteAPIKeys) GetByID(ctx context.Context, id string) (*APIKey, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, hash, name, scope, user_id, quota_daily_tokens, allowed_models, created_at, revoked
		 FROM api_keys WHERE id = ?`, id)
	return scanKey(row)
}

func scanKey(row *sql.Row) (*APIKey, error) {
	var k APIKey
	var ts int64
	var rev int
	var allowed sql.NullString
	if err := row.Scan(&k.ID, &k.Hash, &k.Name, &k.Scope, &k.UserID, &k.QuotaDailyTokens, &allowed, &ts, &rev); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan api_key: %w", err)
	}
	k.CreatedAt = time.Unix(ts, 0)
	k.Revoked = rev != 0
	if list, err := unmarshalAllowed(allowed); err != nil {
		return nil, fmt.Errorf("decode allowed_models: %w", err)
	} else {
		k.AllowedModels = list
	}
	return &k, nil
}

func (s *sqliteAPIKeys) List(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, hash, name, scope, user_id, quota_daily_tokens, allowed_models, created_at, revoked
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
		var allowed sql.NullString
		if err := rows.Scan(&k.ID, &k.Hash, &k.Name, &k.Scope, &k.UserID, &k.QuotaDailyTokens, &allowed, &ts, &rev); err != nil {
			return nil, fmt.Errorf("scan api_key: %w", err)
		}
		k.CreatedAt = time.Unix(ts, 0)
		k.Revoked = rev != 0
		if list, err := unmarshalAllowed(allowed); err != nil {
			return nil, fmt.Errorf("decode allowed_models: %w", err)
		} else {
			k.AllowedModels = list
		}
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

func (s *sqliteAPIKeys) UpdateAllowedModels(ctx context.Context, id string, allowed []string) error {
	encoded, err := marshalAllowed(allowed)
	if err != nil {
		return fmt.Errorf("encode allowed_models: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE api_keys SET allowed_models = ? WHERE id = ?`, encoded, id)
	if err != nil {
		return fmt.Errorf("update allowed_models: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("api_key %s not found", id)
	}
	return nil
}

// marshalAllowed encodes the allowlist for storage.
//
//   - nil  → SQL NULL ("no restriction") — preserves the pre-allowlist
//     default for keys created before the column existed.
//   - []   → "[]" (JSON empty array) → "deny every model". A real string
//     in the column distinguishes this from the nil case.
//   - list → "[\"id1\",\"id2\"]" — explicit allowlist.
func marshalAllowed(list []string) (sql.NullString, error) {
	if list == nil {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(list)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

func unmarshalAllowed(v sql.NullString) ([]string, error) {
	if !v.Valid {
		return nil, nil
	}
	if v.String == "" {
		return []string{}, nil
	}
	var list []string
	if err := json.Unmarshal([]byte(v.String), &list); err != nil {
		return nil, err
	}
	if list == nil {
		// JSON `null` round-trips to a nil slice, but a stored Valid row
		// represents "explicit empty" — normalize to []string{} so the
		// caller sees deny-all rather than unrestricted.
		return []string{}, nil
	}
	return list, nil
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
		   size_bytes=excluded.size_bytes,
		   installed_at=excluded.installed_at`,
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
		if errors.Is(err, sql.ErrNoRows) {
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
		`INSERT INTO nodes(id, hostname, os, arch, ram_gb, address, worker_token, hardware_json, last_heartbeat, state)
		 VALUES(?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   hostname=excluded.hostname,
		   os=excluded.os,
		   arch=excluded.arch,
		   ram_gb=excluded.ram_gb,
		   address=excluded.address,
		   worker_token=CASE WHEN excluded.worker_token != '' THEN excluded.worker_token ELSE nodes.worker_token END,
		   hardware_json=excluded.hardware_json,
		   last_heartbeat=excluded.last_heartbeat,
		   state=excluded.state`,
		n.ID, n.Hostname, n.OS, n.Arch, n.RAMGB, n.Address, n.WorkerToken, n.HardwareJSON, n.LastHeartbeat.Unix(), n.State)
	return err
}

func (s *sqliteNodes) Get(ctx context.Context, id string) (*Node, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, hostname, os, arch, ram_gb, address, worker_token, hardware_json, last_heartbeat, state FROM nodes WHERE id = ?`, id)
	var n Node
	var ts int64
	if err := row.Scan(&n.ID, &n.Hostname, &n.OS, &n.Arch, &n.RAMGB, &n.Address, &n.WorkerToken, &n.HardwareJSON, &ts, &n.State); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	n.LastHeartbeat = time.Unix(ts, 0)
	return &n, nil
}

func (s *sqliteNodes) List(ctx context.Context) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, hostname, os, arch, ram_gb, address, worker_token, hardware_json, last_heartbeat, state FROM nodes ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		var ts int64
		if err := rows.Scan(&n.ID, &n.Hostname, &n.OS, &n.Arch, &n.RAMGB, &n.Address, &n.WorkerToken, &n.HardwareJSON, &ts, &n.State); err != nil {
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

// ---- placements ----

type sqlitePlacements struct{ db *sql.DB }

func (s *sqlitePlacements) Upsert(ctx context.Context, p Placement) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO model_placements(node_id, model_id, status, last_seen)
		 VALUES(?,?,?,?)
		 ON CONFLICT(node_id, model_id) DO UPDATE SET
		   status=excluded.status,
		   last_seen=excluded.last_seen`,
		p.NodeID, p.ModelID, p.Status, p.LastSeen.Unix())
	return err
}

func (s *sqlitePlacements) GetByModel(ctx context.Context, modelID string) ([]Placement, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT node_id, model_id, status, last_seen FROM model_placements
		 WHERE model_id = ? AND status = 'ready'`, modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlacements(rows)
}

func (s *sqlitePlacements) GetByNode(ctx context.Context, nodeID string) ([]Placement, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT node_id, model_id, status, last_seen FROM model_placements
		 WHERE node_id = ?`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlacements(rows)
}

// ReplaceForNode atomically replaces the placement set for a node — useful
// when a worker reports its full loaded-model list on heartbeat.
func (s *sqlitePlacements) ReplaceForNode(ctx context.Context, nodeID string, ps []Placement) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM model_placements WHERE node_id = ?`, nodeID); err != nil {
		return err
	}
	for _, p := range ps {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO model_placements(node_id, model_id, status, last_seen) VALUES(?,?,?,?)`,
			p.NodeID, p.ModelID, p.Status, p.LastSeen.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *sqlitePlacements) Delete(ctx context.Context, nodeID, modelID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM model_placements WHERE node_id = ? AND model_id = ?`, nodeID, modelID)
	return err
}

func scanPlacements(rows *sql.Rows) ([]Placement, error) {
	var out []Placement
	for rows.Next() {
		var p Placement
		var ts int64
		if err := rows.Scan(&p.NodeID, &p.ModelID, &p.Status, &ts); err != nil {
			return nil, err
		}
		p.LastSeen = time.Unix(ts, 0)
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- shards ----

type sqliteShards struct{ db *sql.DB }

func (s *sqliteShards) Create(ctx context.Context, sh Shard) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO shards(id, model_id, role, node_id, address, process_id, status, config_json, created_at, last_seen)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		sh.ID, sh.ModelID, sh.Role, sh.NodeID, sh.Address, sh.ProcessID,
		sh.Status, sh.ConfigJSON, sh.CreatedAt.Unix(), sh.LastSeen.Unix())
	if err != nil {
		return fmt.Errorf("insert shard: %w", err)
	}
	return nil
}

func (s *sqliteShards) Get(ctx context.Context, id string) (*Shard, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, model_id, role, node_id, address, process_id, status, config_json, created_at, last_seen
		 FROM shards WHERE id = ?`, id)
	return scanShard(row)
}

func scanShard(row *sql.Row) (*Shard, error) {
	var sh Shard
	var created, lastSeen int64
	if err := row.Scan(&sh.ID, &sh.ModelID, &sh.Role, &sh.NodeID, &sh.Address,
		&sh.ProcessID, &sh.Status, &sh.ConfigJSON, &created, &lastSeen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan shard: %w", err)
	}
	sh.CreatedAt = time.Unix(created, 0)
	sh.LastSeen = time.Unix(lastSeen, 0)
	return &sh, nil
}

func (s *sqliteShards) GetByModel(ctx context.Context, modelID string) ([]Shard, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, model_id, role, node_id, address, process_id, status, config_json, created_at, last_seen
		 FROM shards WHERE model_id = ? ORDER BY role, created_at`, modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanShards(rows)
}

func (s *sqliteShards) List(ctx context.Context) ([]Shard, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, model_id, role, node_id, address, process_id, status, config_json, created_at, last_seen
		 FROM shards ORDER BY model_id, role, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanShards(rows)
}

func scanShards(rows *sql.Rows) ([]Shard, error) {
	var out []Shard
	for rows.Next() {
		var sh Shard
		var created, lastSeen int64
		if err := rows.Scan(&sh.ID, &sh.ModelID, &sh.Role, &sh.NodeID, &sh.Address,
			&sh.ProcessID, &sh.Status, &sh.ConfigJSON, &created, &lastSeen); err != nil {
			return nil, err
		}
		sh.CreatedAt = time.Unix(created, 0)
		sh.LastSeen = time.Unix(lastSeen, 0)
		out = append(out, sh)
	}
	return out, rows.Err()
}

func (s *sqliteShards) UpdateStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE shards SET status = ?, last_seen = ? WHERE id = ?`,
		status, time.Now().Unix(), id)
	return err
}

func (s *sqliteShards) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM shards WHERE id = ?`, id)
	return err
}

func (s *sqliteShards) DeleteByModel(ctx context.Context, modelID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM shards WHERE model_id = ?`, modelID)
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
