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
	DesiredPlacements() DesiredPlacementStore
	Shards() ShardStore
	Usage() UsageStore
	Audit() AuditStore
	Budgets() BudgetStore
	// Cache is the persistent backend for the response cache. The
	// cache package wraps this with its driver-shape API.
	Cache() CacheStore
	Close() error
}

// CacheStore is the persistent cache surface. Values are opaque
// bytes; expiry is a unix-epoch second.
type CacheStore interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key, namespace string, value []byte, expiresAt time.Time) error
	Delete(ctx context.Context, key string) error
	DeleteNamespace(ctx context.Context, namespace string) error
	// SweepExpired deletes rows whose expires_at < now. Called by the
	// cache package's reaper.
	SweepExpired(ctx context.Context, now time.Time) (int64, error)
	Count(ctx context.Context) (int, int64, error) // entries, total bytes
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
//
// RPMLimit and TPMLimit are per-minute token-bucket ceilings. 0 means
// unlimited (the default for legacy keys). Enforced by
// api.RateLimitMiddleware via in-memory leaky buckets; resets on
// leader restart.
//
// ExpiresAt, when non-zero, makes the key time-limited: the auth
// middleware refuses requests with HTTP 401 `key_expired` once `now >
// expires_at`. Zero (the default for legacy keys) means "never
// expires". Useful for short-lived per-PR keys and external-contractor
// access.
type APIKey struct {
	ID               string
	Hash             string
	Name             string
	Scope            string // "admin" | "user" | "node"
	UserID           string
	QuotaDailyTokens int64
	RPMLimit         int
	TPMLimit         int
	AllowedModels    []string
	CreatedAt        time.Time
	ExpiresAt        time.Time
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
	// UpdateRateLimits replaces the RPM/TPM ceilings on the given key.
	// Passing 0 means "unlimited" — restores the legacy behavior. Both
	// fields are set atomically so a partial edit can't accidentally
	// leave one ceiling set and the other clear.
	UpdateRateLimits(ctx context.Context, id string, rpm, tpm int) error
	// UpdateExpiresAt sets the key's expiry. A zero time clears the
	// expiry ("never expires"); a past time effectively expires the
	// key immediately.
	UpdateExpiresAt(ctx context.Context, id string, expiresAt time.Time) error
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
	// SetStatus flips a single placement's status in place (e.g. ready ↔
	// draining during an eviction) without touching last_seen semantics.
	// A draining placement is invisible to GetByModel, so the router
	// stops sending new requests to it.
	SetStatus(ctx context.Context, nodeID, modelID, status string) error
}

// DesiredPlacement is the operator's declared intent that a node should
// keep a model resident in memory. Distinct from Placement (which is
// observational: "this node can serve this model right now").
//
// ModelID is the CATALOG id — surfaces map it to the engine-native name
// (Ollama tag, HF repo) via the models table when talking to an engine.
//
// Priority orders both restore-on-boot (high first) and eviction (low
// first). Pinned placements are never chosen as eviction victims and are
// loaded with Ollama keep_alive=-1 so the engine's idle TTL skips them.
type DesiredPlacement struct {
	NodeID    string    `json:"node_id"`
	ModelID   string    `json:"model_id"`
	Priority  int       `json:"priority"`
	Pinned    bool      `json:"pinned"`
	CreatedAt time.Time `json:"created_at"`
}

type DesiredPlacementStore interface {
	Upsert(ctx context.Context, d DesiredPlacement) error
	Get(ctx context.Context, nodeID, modelID string) (*DesiredPlacement, error)
	// ListByNode returns the node's desired set ordered by priority DESC,
	// created_at ASC — the order Restore loads them in.
	ListByNode(ctx context.Context, nodeID string) ([]DesiredPlacement, error)
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
//
// CostUSD is the dollar cost computed from the catalog/vendor pricing
// table at write time (`recordUsage`). 0 means no cost was tracked —
// either the row predates the column, or the model has no pricing
// configured (typical for operator-owned open-weight models). Storing
// the snapshot lets historical totals stay correct even when pricing
// changes later.
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
	CostUSD          float64
}

// Budget is one rolling spend / token allowance attached to an API key.
// Multiple budgets can apply to a single key — every request must clear
// all of them to be admitted. Two simultaneously-valid configurations:
//
//   - tokens / day  (e.g. 1M tokens/day)
//   - usd / month   (e.g. $100/month)
//
// CurrentValue accumulates as requests run (incremented from the
// recordUsage path); a request is refused when CurrentValue >=
// LimitValue. ResetAt is the unix timestamp at which the window
// rolls — checked lazily on every read so we don't need a cron.
type Budget struct {
	ID           int64
	APIKeyID     string
	Window       string // "day" | "week" | "month"
	LimitUnit    string // "tokens" | "usd"
	LimitValue   float64
	CurrentValue float64
	ResetAt      time.Time
	CreatedAt    time.Time
}

// BudgetStore is the persistence surface for per-key budgets.
type BudgetStore interface {
	Create(ctx context.Context, b Budget) (int64, error)
	ListByKey(ctx context.Context, apiKeyID string) ([]Budget, error)
	Delete(ctx context.Context, id int64) error
	// Increment atomically adds delta to a single budget. Used from
	// recordUsage after the response is known.
	Increment(ctx context.Context, id int64, delta float64) error
	// ResetExpired rolls every budget whose reset_at has passed:
	// current_value = 0, reset_at = next boundary. Called from the
	// middleware so admission decisions always see fresh state.
	ResetExpired(ctx context.Context, apiKeyID string, now time.Time) error
}

type UsageStore interface {
	Record(ctx context.Context, u Usage) error
	SumTokensSince(ctx context.Context, apiKeyID string, since time.Time) (int64, error)
	// LastUsedByModel returns the most recent request timestamp per model
	// id. Models with no usage rows are absent from the map — eviction
	// treats them as least-recently-used. Used by the lifecycle manager's
	// LRU victim ordering.
	LastUsedByModel(ctx context.Context) (map[string]time.Time, error)
	RecentByUser(ctx context.Context, userID string, limit int) ([]Usage, error)
	Recent(ctx context.Context, limit int) ([]Usage, error)
	// Breakdown aggregates the usage table by time bucket and the
	// chosen grouping columns. Each non-empty group_by entry adds a
	// SELECT column and a GROUP BY term. Returns the rows + the
	// totals across all rows in the same bucket range.
	Breakdown(ctx context.Context, opts BreakdownOpts) ([]BreakdownRow, BreakdownTotals, error)
}

// BreakdownOpts describes the time-bucketed query.
type BreakdownOpts struct {
	// Bucket selects the time-rollup granularity. Valid: "hour",
	// "day", "month", "total". Defaults to "day" when empty.
	Bucket string
	// Since / Until bound the time range. Zero values mean "open
	// ended" — Since defaults to 30 days ago, Until to now.
	Since time.Time
	Until time.Time
	// GroupBy is an ordered subset of {"user","model","protocol","outcome"}.
	// Empty groups by the time bucket only.
	GroupBy []string
	// Limit caps the number of returned rows. 0 = no limit.
	Limit int
}

// BreakdownRow is one aggregated row.
type BreakdownRow struct {
	Bucket           string  `json:"bucket"`
	User             string  `json:"user,omitempty"`
	Model            string  `json:"model,omitempty"`
	Protocol         string  `json:"protocol,omitempty"`
	Outcome          string  `json:"outcome,omitempty"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	Requests         int64   `json:"requests"`
	CostUSD          float64 `json:"cost_usd"`
}

// BreakdownTotals sums everything in the bucket range — useful for the
// dashboard "totals" footer and as a sanity check that group_by didn't
// double-count.
type BreakdownTotals struct {
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	Requests         int64   `json:"requests"`
	CostUSD          float64 `json:"cost_usd"`
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
func (s *sqliteStore) DesiredPlacements() DesiredPlacementStore {
	return &sqliteDesiredPlacements{db: s.db}
}
func (s *sqliteStore) Shards() ShardStore   { return &sqliteShards{db: s.db} }
func (s *sqliteStore) Usage() UsageStore    { return &sqliteUsage{db: s.db} }
func (s *sqliteStore) Audit() AuditStore    { return &sqliteAudit{db: s.db} }
func (s *sqliteStore) Budgets() BudgetStore { return &sqliteBudgets{db: s.db} }
func (s *sqliteStore) Cache() CacheStore    { return &sqliteCache{db: s.db} }
func (s *sqliteStore) Close() error         { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS api_keys (
    id                  TEXT PRIMARY KEY,
    hash                TEXT NOT NULL UNIQUE,
    name                TEXT NOT NULL,
    scope               TEXT NOT NULL,
    user_id             TEXT NOT NULL DEFAULT '',
    quota_daily_tokens  INTEGER NOT NULL DEFAULT 0,
    rpm_limit           INTEGER NOT NULL DEFAULT 0,
    tpm_limit           INTEGER NOT NULL DEFAULT 0,
    allowed_models      TEXT,
    expires_at          INTEGER NOT NULL DEFAULT 0,
    created_at          INTEGER NOT NULL,
    revoked             INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(hash);
CREATE INDEX IF NOT EXISTS idx_api_keys_expires ON api_keys(expires_at) WHERE expires_at > 0;

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

CREATE TABLE IF NOT EXISTS desired_placements (
    node_id    TEXT NOT NULL,
    model_id   TEXT NOT NULL,
    priority   INTEGER NOT NULL DEFAULT 0,
    pinned     INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (node_id, model_id)
);

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
    outcome           TEXT NOT NULL,
    cost_usd          REAL NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_usage_key_ts ON usage(api_key_id, ts);
CREATE INDEX IF NOT EXISTS idx_usage_user_ts ON usage(user_id, ts);
CREATE INDEX IF NOT EXISTS idx_usage_ts ON usage(ts);
CREATE INDEX IF NOT EXISTS idx_usage_model_ts ON usage(model, ts);

CREATE TABLE IF NOT EXISTS budgets (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    api_key_id     TEXT    NOT NULL,
    window         TEXT    NOT NULL,                -- 'day' | 'week' | 'month'
    limit_unit     TEXT    NOT NULL,                -- 'tokens' | 'usd'
    limit_value    REAL    NOT NULL,
    current_value  REAL    NOT NULL DEFAULT 0,
    reset_at       INTEGER NOT NULL,
    created_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_budgets_key ON budgets(api_key_id);

CREATE TABLE IF NOT EXISTS cache (
    key        TEXT PRIMARY KEY,
    namespace  TEXT NOT NULL DEFAULT '',
    value      BLOB NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cache_expires_at ON cache(expires_at);
CREATE INDEX IF NOT EXISTS idx_cache_namespace ON cache(namespace);

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
		// v0.8 — per-key RPM / TPM ceilings. 0 = unlimited.
		{table: "api_keys", column: "rpm_limit", ddl: `ALTER TABLE api_keys ADD COLUMN rpm_limit INTEGER NOT NULL DEFAULT 0`},
		{table: "api_keys", column: "tpm_limit", ddl: `ALTER TABLE api_keys ADD COLUMN tpm_limit INTEGER NOT NULL DEFAULT 0`},
		// v0.8 — per-call $ cost. 0 default — pre-migration rows have no
		// cost recorded.
		{table: "usage", column: "cost_usd", ddl: `ALTER TABLE usage ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0`},
		// v0.8 — per-key expiry. 0 = never expires (legacy default).
		{table: "api_keys", column: "expires_at", ddl: `ALTER TABLE api_keys ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0`},
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
		`INSERT INTO api_keys(id, hash, name, scope, user_id, quota_daily_tokens, rpm_limit, tpm_limit, allowed_models, expires_at, created_at, revoked)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		k.ID, k.Hash, k.Name, k.Scope, k.UserID, k.QuotaDailyTokens, k.RPMLimit, k.TPMLimit, allowed,
		unixOrZero(k.ExpiresAt), k.CreatedAt.Unix(), boolToInt(k.Revoked)); err != nil {
		return fmt.Errorf("insert api_key: %w", err)
	}
	return nil
}

func (s *sqliteAPIKeys) GetByHash(ctx context.Context, hash string) (*APIKey, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, hash, name, scope, user_id, quota_daily_tokens, rpm_limit, tpm_limit, allowed_models, expires_at, created_at, revoked
		 FROM api_keys WHERE hash = ?`, hash)
	return scanKey(row)
}

func (s *sqliteAPIKeys) GetByID(ctx context.Context, id string) (*APIKey, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, hash, name, scope, user_id, quota_daily_tokens, rpm_limit, tpm_limit, allowed_models, expires_at, created_at, revoked
		 FROM api_keys WHERE id = ?`, id)
	return scanKey(row)
}

func scanKey(row *sql.Row) (*APIKey, error) {
	var k APIKey
	var ts int64
	var expiresAt int64
	var rev int
	var allowed sql.NullString
	if err := row.Scan(&k.ID, &k.Hash, &k.Name, &k.Scope, &k.UserID, &k.QuotaDailyTokens, &k.RPMLimit, &k.TPMLimit, &allowed, &expiresAt, &ts, &rev); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan api_key: %w", err)
	}
	k.CreatedAt = time.Unix(ts, 0)
	if expiresAt > 0 {
		k.ExpiresAt = time.Unix(expiresAt, 0)
	}
	k.Revoked = rev != 0
	list, err := unmarshalAllowed(allowed)
	if err != nil {
		return nil, fmt.Errorf("decode allowed_models: %w", err)
	}
	k.AllowedModels = list
	return &k, nil
}

func (s *sqliteAPIKeys) List(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, hash, name, scope, user_id, quota_daily_tokens, rpm_limit, tpm_limit, allowed_models, expires_at, created_at, revoked
		 FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query api_keys: %w", err)
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		var ts int64
		var expiresAt int64
		var rev int
		var allowed sql.NullString
		if err := rows.Scan(&k.ID, &k.Hash, &k.Name, &k.Scope, &k.UserID, &k.QuotaDailyTokens, &k.RPMLimit, &k.TPMLimit, &allowed, &expiresAt, &ts, &rev); err != nil {
			return nil, fmt.Errorf("scan api_key: %w", err)
		}
		k.CreatedAt = time.Unix(ts, 0)
		if expiresAt > 0 {
			k.ExpiresAt = time.Unix(expiresAt, 0)
		}
		k.Revoked = rev != 0
		list, err := unmarshalAllowed(allowed)
		if err != nil {
			return nil, fmt.Errorf("decode allowed_models: %w", err)
		}
		k.AllowedModels = list
		out = append(out, k)
	}
	return out, rows.Err()
}

// unixOrZero converts a time to its unix timestamp, returning 0 for the
// zero time. Used at write time so a "never expires" record stores 0
// in the column rather than a sentinel like INT_MAX.
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func (s *sqliteAPIKeys) Revoke(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET revoked = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("revoke api_key: %w", err)
	}
	return nil
}

func (s *sqliteAPIKeys) UpdateExpiresAt(ctx context.Context, id string, expiresAt time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET expires_at = ? WHERE id = ?`,
		unixOrZero(expiresAt), id)
	if err != nil {
		return fmt.Errorf("update expires_at: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("api_key %s not found", id)
	}
	return nil
}

func (s *sqliteAPIKeys) UpdateRateLimits(ctx context.Context, id string, rpm, tpm int) error {
	if rpm < 0 || tpm < 0 {
		return fmt.Errorf("rpm/tpm must be >= 0 (0 = unlimited)")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET rpm_limit = ?, tpm_limit = ? WHERE id = ?`,
		rpm, tpm, id)
	if err != nil {
		return fmt.Errorf("update rate limits: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("api_key %s not found", id)
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

func (s *sqlitePlacements) SetStatus(ctx context.Context, nodeID, modelID, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE model_placements SET status = ?, last_seen = ? WHERE node_id = ? AND model_id = ?`,
		status, time.Now().Unix(), nodeID, modelID)
	if err != nil {
		return fmt.Errorf("set placement status: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no placement for %s on %s", modelID, nodeID)
	}
	return nil
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

// ---- desired placements ----

type sqliteDesiredPlacements struct{ db *sql.DB }

func (s *sqliteDesiredPlacements) Upsert(ctx context.Context, d DesiredPlacement) error {
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO desired_placements(node_id, model_id, priority, pinned, created_at)
		 VALUES(?,?,?,?,?)
		 ON CONFLICT(node_id, model_id) DO UPDATE SET
		   priority=excluded.priority,
		   pinned=excluded.pinned`,
		d.NodeID, d.ModelID, d.Priority, boolToInt(d.Pinned), d.CreatedAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert desired placement: %w", err)
	}
	return nil
}

func (s *sqliteDesiredPlacements) Get(ctx context.Context, nodeID, modelID string) (*DesiredPlacement, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT node_id, model_id, priority, pinned, created_at
		 FROM desired_placements WHERE node_id = ? AND model_id = ?`, nodeID, modelID)
	var d DesiredPlacement
	var pinned int
	var created int64
	if err := row.Scan(&d.NodeID, &d.ModelID, &d.Priority, &pinned, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan desired placement: %w", err)
	}
	d.Pinned = pinned != 0
	d.CreatedAt = time.Unix(created, 0)
	return &d, nil
}

func (s *sqliteDesiredPlacements) ListByNode(ctx context.Context, nodeID string) ([]DesiredPlacement, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT node_id, model_id, priority, pinned, created_at
		 FROM desired_placements WHERE node_id = ?
		 ORDER BY priority DESC, created_at ASC`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("query desired placements: %w", err)
	}
	defer rows.Close()
	var out []DesiredPlacement
	for rows.Next() {
		var d DesiredPlacement
		var pinned int
		var created int64
		if err := rows.Scan(&d.NodeID, &d.ModelID, &d.Priority, &pinned, &created); err != nil {
			return nil, fmt.Errorf("scan desired placement: %w", err)
		}
		d.Pinned = pinned != 0
		d.CreatedAt = time.Unix(created, 0)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *sqliteDesiredPlacements) Delete(ctx context.Context, nodeID, modelID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM desired_placements WHERE node_id = ? AND model_id = ?`, nodeID, modelID)
	return err
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
		    prompt_tokens, completion_tokens, latency_ms, outcome, cost_usd)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		u.TS.Unix(), u.APIKeyID, u.UserID, u.Model, u.Protocol,
		u.PromptTokens, u.CompletionTokens, u.LatencyMS, u.Outcome, u.CostUSD)
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

func (s *sqliteUsage) LastUsedByModel(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT model, MAX(ts) FROM usage GROUP BY model`)
	if err != nil {
		return nil, fmt.Errorf("last used by model: %w", err)
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var model string
		var ts int64
		if err := rows.Scan(&model, &ts); err != nil {
			return nil, fmt.Errorf("scan last used: %w", err)
		}
		out[model] = time.Unix(ts, 0)
	}
	return out, rows.Err()
}

func (s *sqliteUsage) Recent(ctx context.Context, limit int) ([]Usage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ts, api_key_id, user_id, model, protocol,
		        prompt_tokens, completion_tokens, latency_ms, outcome, cost_usd
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
		        prompt_tokens, completion_tokens, latency_ms, outcome, cost_usd
		 FROM usage WHERE user_id = ? ORDER BY ts DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsage(rows)
}

// bucketExpr returns the SQL expression that maps the unix ts column to
// a bucket label. "total" collapses all rows into a single bucket so
// the grouping helpers below stay uniform.
func bucketExpr(bucket string) string {
	switch bucket {
	case "hour":
		return `strftime('%Y-%m-%d %H:00', ts, 'unixepoch')`
	case "month":
		return `strftime('%Y-%m', ts, 'unixepoch')`
	case "total":
		return `'all'`
	default: // "day" + unspecified
		return `strftime('%Y-%m-%d', ts, 'unixepoch')`
	}
}

// groupColumn maps the user-facing group_by token to the actual SQL
// column. Returning "", false signals an unknown token and the caller
// rejects the request with a 400.
func groupColumn(token string) (sqlCol string, ok bool) {
	switch token {
	case "user":
		return "user_id", true
	case "model":
		return "model", true
	case "protocol":
		return "protocol", true
	case "outcome":
		return "outcome", true
	}
	return "", false
}

func (s *sqliteUsage) Breakdown(ctx context.Context, opts BreakdownOpts) ([]BreakdownRow, BreakdownTotals, error) {
	if opts.Bucket == "" {
		opts.Bucket = "day"
	}
	// Normalize since/until with sensible defaults.
	now := time.Now()
	if opts.Since.IsZero() {
		opts.Since = now.AddDate(0, 0, -30)
	}
	if opts.Until.IsZero() {
		opts.Until = now
	}

	groupCols := make([]string, 0, len(opts.GroupBy))
	for _, g := range opts.GroupBy {
		col, ok := groupColumn(g)
		if !ok {
			return nil, BreakdownTotals{}, fmt.Errorf("unsupported group_by token %q (try user|model|protocol|outcome)", g)
		}
		groupCols = append(groupCols, col)
	}
	bucket := bucketExpr(opts.Bucket)

	// SELECT list: bucket label, each group col, then the aggregates.
	selectList := []string{bucket + " AS bucket"}
	selectList = append(selectList, groupCols...)
	selectList = append(selectList,
		"SUM(prompt_tokens) AS pt",
		"SUM(completion_tokens) AS ct",
		"COUNT(*) AS reqs",
		"COALESCE(SUM(cost_usd), 0) AS cost",
	)
	groupList := append([]string{"bucket"}, groupCols...)

	query := "SELECT " + strings.Join(selectList, ", ") +
		" FROM usage WHERE ts >= ? AND ts < ?" +
		" GROUP BY " + strings.Join(groupList, ", ") +
		" ORDER BY bucket DESC, reqs DESC"
	if opts.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", opts.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, opts.Since.Unix(), opts.Until.Unix())
	if err != nil {
		return nil, BreakdownTotals{}, fmt.Errorf("breakdown query: %w", err)
	}
	defer rows.Close()

	out := []BreakdownRow{}
	var totals BreakdownTotals
	for rows.Next() {
		var r BreakdownRow
		dest := []any{&r.Bucket}
		// One scan slot per group column; we route the value into the
		// matching BreakdownRow field in the same order as opts.GroupBy.
		groupVals := make([]string, len(opts.GroupBy))
		for i := range opts.GroupBy {
			dest = append(dest, &groupVals[i])
		}
		dest = append(dest, &r.PromptTokens, &r.CompletionTokens, &r.Requests, &r.CostUSD)
		if err := rows.Scan(dest...); err != nil {
			return nil, BreakdownTotals{}, fmt.Errorf("scan breakdown: %w", err)
		}
		for i, g := range opts.GroupBy {
			switch g {
			case "user":
				r.User = groupVals[i]
			case "model":
				r.Model = groupVals[i]
			case "protocol":
				r.Protocol = groupVals[i]
			case "outcome":
				r.Outcome = groupVals[i]
			}
		}
		totals.PromptTokens += r.PromptTokens
		totals.CompletionTokens += r.CompletionTokens
		totals.Requests += r.Requests
		totals.CostUSD += r.CostUSD
		out = append(out, r)
	}
	return out, totals, rows.Err()
}

func scanUsage(rows *sql.Rows) ([]Usage, error) {
	var out []Usage
	for rows.Next() {
		var u Usage
		var ts int64
		if err := rows.Scan(&u.ID, &ts, &u.APIKeyID, &u.UserID, &u.Model, &u.Protocol,
			&u.PromptTokens, &u.CompletionTokens, &u.LatencyMS, &u.Outcome, &u.CostUSD); err != nil {
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

// ---- budgets ----

type sqliteBudgets struct{ db *sql.DB }

func (s *sqliteBudgets) Create(ctx context.Context, b Budget) (int64, error) {
	if b.ResetAt.IsZero() {
		b.ResetAt = NextBudgetReset(b.Window, time.Now())
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now()
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO budgets(api_key_id, window, limit_unit, limit_value, current_value, reset_at, created_at)
		 VALUES(?,?,?,?,?,?,?)`,
		b.APIKeyID, b.Window, b.LimitUnit, b.LimitValue, b.CurrentValue,
		b.ResetAt.Unix(), b.CreatedAt.Unix())
	if err != nil {
		return 0, fmt.Errorf("insert budget: %w", err)
	}
	return res.LastInsertId()
}

func (s *sqliteBudgets) ListByKey(ctx context.Context, apiKeyID string) ([]Budget, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, api_key_id, window, limit_unit, limit_value, current_value, reset_at, created_at
		 FROM budgets WHERE api_key_id = ? ORDER BY id`, apiKeyID)
	if err != nil {
		return nil, fmt.Errorf("query budgets: %w", err)
	}
	defer rows.Close()
	var out []Budget
	for rows.Next() {
		var b Budget
		var resetAt, createdAt int64
		if err := rows.Scan(&b.ID, &b.APIKeyID, &b.Window, &b.LimitUnit, &b.LimitValue,
			&b.CurrentValue, &resetAt, &createdAt); err != nil {
			return nil, fmt.Errorf("scan budget: %w", err)
		}
		b.ResetAt = time.Unix(resetAt, 0)
		b.CreatedAt = time.Unix(createdAt, 0)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *sqliteBudgets) Delete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM budgets WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete budget: %w", err)
	}
	return nil
}

func (s *sqliteBudgets) Increment(ctx context.Context, id int64, delta float64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE budgets SET current_value = current_value + ? WHERE id = ?`,
		delta, id)
	if err != nil {
		return fmt.Errorf("increment budget: %w", err)
	}
	return nil
}

// ResetExpired walks every budget for this key and, if its reset_at is
// in the past, zeroes current_value and advances reset_at to the next
// window boundary. The lazy-on-read pattern keeps us out of cron
// territory — fresh state is observed on the very next admission
// check, however long the leader was offline.
func (s *sqliteBudgets) ResetExpired(ctx context.Context, apiKeyID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx,
		`SELECT id, window, reset_at FROM budgets WHERE api_key_id = ? AND reset_at <= ?`,
		apiKeyID, now.Unix())
	if err != nil {
		return fmt.Errorf("scan expired budgets: %w", err)
	}
	type expiry struct {
		ID     int64
		Window string
	}
	var toReset []expiry
	for rows.Next() {
		var e expiry
		var ts int64
		if err := rows.Scan(&e.ID, &e.Window, &ts); err != nil {
			rows.Close()
			return err
		}
		toReset = append(toReset, e)
	}
	rows.Close()
	for _, e := range toReset {
		next := NextBudgetReset(e.Window, now)
		if _, err := tx.ExecContext(ctx,
			`UPDATE budgets SET current_value = 0, reset_at = ? WHERE id = ?`,
			next.Unix(), e.ID); err != nil {
			return fmt.Errorf("reset budget %d: %w", e.ID, err)
		}
	}
	return tx.Commit()
}

// NextBudgetReset returns the unix time at which a budget with the
// given window should next roll. UTC is used so resets land at the
// same wall-clock moment for all admins; future work can make the
// timezone configurable per-budget.
//
//	day   → next 00:00 UTC
//	week  → next Monday 00:00 UTC
//	month → next 1st of month 00:00 UTC
//	(default) treated as "day" — same logic as misconfigured rows
func NextBudgetReset(window string, now time.Time) time.Time {
	now = now.UTC()
	switch window {
	case "month":
		return time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	case "week":
		// Days until next Monday (1..7).
		offset := int(time.Monday-now.Weekday()+7) % 7
		if offset == 0 {
			offset = 7
		}
		next := time.Date(now.Year(), now.Month(), now.Day()+offset, 0, 0, 0, 0, time.UTC)
		return next
	default: // "day"
		return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	}
}

// ---- cache ----

type sqliteCache struct{ db *sql.DB }

func (s *sqliteCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	var value []byte
	var expiresAt int64
	err := s.db.QueryRowContext(ctx,
		`SELECT value, expires_at FROM cache WHERE key = ?`, key).Scan(&value, &expiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("cache get: %w", err)
	}
	if expiresAt > 0 && time.Now().Unix() > expiresAt {
		return nil, false, nil
	}
	return value, true, nil
}

func (s *sqliteCache) Set(ctx context.Context, key, namespace string, value []byte, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cache(key, namespace, value, expires_at)
		 VALUES(?,?,?,?)
		 ON CONFLICT(key) DO UPDATE SET
		   namespace=excluded.namespace,
		   value=excluded.value,
		   expires_at=excluded.expires_at`,
		key, namespace, value, expiresAt.Unix())
	if err != nil {
		return fmt.Errorf("cache set: %w", err)
	}
	return nil
}

func (s *sqliteCache) Delete(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM cache WHERE key = ?`, key)
	return err
}

func (s *sqliteCache) DeleteNamespace(ctx context.Context, namespace string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM cache WHERE namespace = ?`, namespace)
	return err
}

func (s *sqliteCache) SweepExpired(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM cache WHERE expires_at > 0 AND expires_at < ?`, now.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *sqliteCache) Count(ctx context.Context) (int, int64, error) {
	var n int
	var bytes int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(LENGTH(value)), 0) FROM cache`).Scan(&n, &bytes)
	return n, bytes, err
}
