// Package store 封装 SQLite 存储与统计查询, 请求明细默认保留 90 天,
// daily_stats 提供永久聚合。整库单连接串行访问, 本地负载足够。
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Record 一次请求的统计记录(cost 由调用方按定价计算)。
type Record struct {
	Ts          int64  `json:"ts"`
	ProviderID  string `json:"provider_id"`
	ProviderNam string `json:"provider_name"`
	Model       string `json:"model"` // 客户端请求的别名
	Upstream    string `json:"upstream_model"`
	Channel     string `json:"channel"` // main|lightweight|subagent|default
	Format      string `json:"format"`  // anthropic|openai
	Stream      bool   `json:"stream"`
	Status      int    `json:"status"`
	OK          bool   `json:"ok"`
	Input       int64  `json:"input_tokens"`
	CacheRead   int64  `json:"cache_read_tokens"`
	CacheCreate int64  `json:"cache_creation_tokens"`
	Output      int64  `json:"output_tokens"`
	Cost        float64 `json:"cost"`
	DurationMs  int64  `json:"duration_ms"`
	TTFBMs      int64  `json:"ttfb_ms"`
	Failover    string `json:"failover"` // 尝试轨迹 JSON
	Err         string `json:"err"`
	Project     string `json:"project"`
	SessionKey  string `json:"session_key"`
}

// Totals 汇总卡片数据。
type Totals struct {
	Requests    int64   `json:"requests"`
	Errors      int64   `json:"errors"`
	Input       int64   `json:"input_tokens"`
	CacheRead   int64   `json:"cache_read_tokens"`
	CacheCreate int64   `json:"cache_creation_tokens"`
	Output      int64   `json:"output_tokens"`
	Cost        float64 `json:"cost"`
}

// GroupAgg 分组聚合行。
type GroupAgg struct {
	Key         string  `json:"key"`
	Requests    int64   `json:"requests"`
	Input       int64   `json:"input_tokens"`
	CacheRead   int64   `json:"cache_read_tokens"`
	CacheCreate int64   `json:"cache_creation_tokens"`
	Output      int64   `json:"output_tokens"`
	Cost        float64 `json:"cost"`
	Errors      int64   `json:"errors"`
}

// Point 时间序列点(bucket 为 Unix 秒)。
type Point struct {
	Bucket      int64   `json:"bucket"`
	Requests    int64   `json:"requests"`
	Input       int64   `json:"input_tokens"`
	CacheRead   int64   `json:"cache_read_tokens"`
	CacheCreate int64   `json:"cache_creation_tokens"`
	Output      int64   `json:"output_tokens"`
	Cost        float64 `json:"cost"`
}

// RequestRow 请求日志行。
type RequestRow struct {
	Record
	ID int64 `json:"id"`
}

// SessionRow 会话元数据。
type SessionRow struct {
	Key       string `json:"key"`
	Project   string `json:"project"`
	Title     string `json:"title"`
	LastModel string `json:"last_model"`
	MsgCount  int64  `json:"msg_count"`
	SizeBytes int64  `json:"size_bytes"`
	UpdatedAt int64  `json:"updated_at"`
}

// SnapshotRow 快照元数据(内容在磁盘文件中)。
type SnapshotRow struct {
	ID        int64  `json:"id"`
	SessionKy string `json:"session_key"`
	Seq       int64  `json:"seq"`
	Ts        int64  `json:"ts"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Model     string `json:"model"`
	MsgCount  int64  `json:"msg_count"`
	Project   string `json:"project"`
}

// Tier 峰/谷档位: enabled 时按时段计价。
// Mode: "absolute" 直接使用四项单价; "multiplier" 按 Rate × 平价四项。
type Tier struct {
	Enabled bool    `json:"enabled"`
	Start   string  `json:"start"`            // "HH:MM" 含
	End     string  `json:"end"`              // "HH:MM" 不含; 支持跨午夜(start>end)
	Mode    string  `json:"mode"`             // absolute | multiplier
	Rate    float64 `json:"rate,omitempty"`   // multiplier 模式倍率
	Input   float64 `json:"input,omitempty"`  // absolute 模式四项单价
	Output  float64 `json:"output,omitempty"`
	CacheRead     float64 `json:"cache_read,omitempty"`
	CacheCreation float64 `json:"cache_creation,omitempty"`
}

// Price 单价(每百万 token)。四项基础字段为「平价」(可选);
// Peak/Valley 为峰/谷档位, 均可空。
type Price struct {
	Model         string  `json:"model"`
	Input         float64 `json:"input"`
	Output        float64 `json:"output"`
	CacheRead     float64 `json:"cache_read"`
	CacheCreation float64 `json:"cache_creation"`
	Peak          *Tier   `json:"peak,omitempty"`
	Valley        *Tier   `json:"valley,omitempty"`
}

// Store SQLite 句柄。
type Store struct {
	db *sql.DB
}

// Open 打开(必要时创建)数据库并执行迁移。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(filepath.Join(dataDir, "stats.db")) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // 本地串行, 避免 SQLITE_BUSY
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS requests (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts INTEGER NOT NULL,
			provider_id TEXT NOT NULL DEFAULT '',
			provider_name TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			upstream_model TEXT NOT NULL DEFAULT '',
			channel TEXT NOT NULL DEFAULT 'main',
			format TEXT NOT NULL DEFAULT '',
			stream INTEGER NOT NULL DEFAULT 0,
			status INTEGER NOT NULL DEFAULT 0,
			ok INTEGER NOT NULL DEFAULT 0,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cost REAL NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			ttfb_ms INTEGER NOT NULL DEFAULT 0,
			failover TEXT NOT NULL DEFAULT '',
			err TEXT NOT NULL DEFAULT '',
			project TEXT NOT NULL DEFAULT '',
			session_key TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_requests_ts ON requests(ts)`,
		`CREATE INDEX IF NOT EXISTS idx_requests_provider_ts ON requests(provider_id, ts)`,
		`CREATE INDEX IF NOT EXISTS idx_requests_model_ts ON requests(model, ts)`,
		`CREATE TABLE IF NOT EXISTS daily_stats (
			day TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			model TEXT NOT NULL,
			requests INTEGER NOT NULL DEFAULT 0,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cost REAL NOT NULL DEFAULT 0,
			PRIMARY KEY (day, provider_id, model)
		)`,
		`CREATE TABLE IF NOT EXISTS pricing (
			model TEXT PRIMARY KEY,
			input REAL NOT NULL DEFAULT 0,
			output REAL NOT NULL DEFAULT 0,
			cache_read REAL NOT NULL DEFAULT 0,
			cache_creation REAL NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			key TEXT PRIMARY KEY,
			project TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			last_model TEXT NOT NULL DEFAULT '',
			msg_count INTEGER NOT NULL DEFAULT 0,
			size_bytes INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at)`,
		`CREATE TABLE IF NOT EXISTS snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_key TEXT NOT NULL,
			seq INTEGER NOT NULL,
			ts INTEGER NOT NULL,
			path TEXT NOT NULL,
			size_bytes INTEGER NOT NULL DEFAULT 0,
			model TEXT NOT NULL DEFAULT '',
			msg_count INTEGER NOT NULL DEFAULT 0,
			project TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_session ON snapshots(session_key, seq)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_ts ON snapshots(ts)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// 增量迁移: pricing 增加 tiers 列(峰谷档位 JSON); 已存在则忽略
	if _, err := s.db.Exec(`ALTER TABLE pricing ADD COLUMN tiers TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate tiers: %w", err)
	}
	return nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// InsertBatch 批量写入请求明细并同步累加日聚合。
func (s *Store) InsertBatch(recs []Record) error {
	if len(recs) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	reqStmt, err := tx.Prepare(`INSERT INTO requests
		(ts,provider_id,provider_name,model,upstream_model,channel,format,stream,status,ok,
		 input_tokens,cache_read_tokens,cache_creation_tokens,output_tokens,cost,
		 duration_ms,ttfb_ms,failover,err,project,session_key)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer reqStmt.Close()
	dayStmt, err := tx.Prepare(`INSERT INTO daily_stats
		(day,provider_id,model,requests,input_tokens,cache_read_tokens,cache_creation_tokens,output_tokens,cost)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(day,provider_id,model) DO UPDATE SET
		  requests=requests+excluded.requests,
		  input_tokens=input_tokens+excluded.input_tokens,
		  cache_read_tokens=cache_read_tokens+excluded.cache_read_tokens,
		  cache_creation_tokens=cache_creation_tokens+excluded.cache_creation_tokens,
		  output_tokens=output_tokens+excluded.output_tokens,
		  cost=cost+excluded.cost`)
	if err != nil {
		return err
	}
	defer dayStmt.Close()
	for _, r := range recs {
		streamInt, okInt := 0, 0
		if r.Stream {
			streamInt = 1
		}
		if r.OK {
			okInt = 1
		}
		if _, err := reqStmt.Exec(r.Ts, r.ProviderID, r.ProviderNam, r.Model, r.Upstream,
			r.Channel, r.Format, streamInt, r.Status, okInt,
			r.Input, r.CacheRead, r.CacheCreate, r.Output, r.Cost,
			r.DurationMs, r.TTFBMs, r.Failover, r.Err, r.Project, r.SessionKey); err != nil {
			return err
		}
		day := time.Unix(r.Ts, 0).Format("2006-01-02")
		if _, err := dayStmt.Exec(day, r.ProviderID, r.Upstream, 1,
			r.Input, r.CacheRead, r.CacheCreate, r.Output, r.Cost); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CleanupRequests 删除超过保留期的明细, 返回删除行数。
func (s *Store) CleanupRequests(retentionDays int) (int64, error) {
	cut := time.Now().AddDate(0, 0, -retentionDays).Unix()
	res, err := s.db.Exec(`DELETE FROM requests WHERE ts < ?`, cut)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const totalsSel = `COUNT(*),
	COALESCE(SUM(CASE WHEN ok=0 THEN 1 ELSE 0 END),0),
	COALESCE(SUM(input_tokens),0), COALESCE(SUM(cache_read_tokens),0),
	COALESCE(SUM(cache_creation_tokens),0), COALESCE(SUM(output_tokens),0),
	COALESCE(SUM(cost),0)`

func scanTotals(row *sql.Row) (Totals, error) {
	var t Totals
	err := row.Scan(&t.Requests, &t.Errors, &t.Input, &t.CacheRead, &t.CacheCreate, &t.Output, &t.Cost)
	return t, err
}

// TotalsByRange 按时间范围与可选通道汇总。
func (s *Store) TotalsByRange(startTs int64, channel string) (Totals, error) {
	q := "SELECT " + totalsSel + " FROM requests WHERE ts >= ?"
	args := []any{startTs}
	if channel != "" {
		q += " AND channel = ?"
		args = append(args, channel)
	}
	return scanTotals(s.db.QueryRow(q, args...))
}

// GroupBy 按字段分组聚合(field: provider_name|model|channel)。
func (s *Store) GroupBy(startTs int64, field string) ([]GroupAgg, error) {
	col := map[string]string{
		"provider_name": "provider_name",
		"model":         "model",
		"channel":       "channel",
	}[field]
	if col == "" {
		return nil, fmt.Errorf("bad group field %q", field)
	}
	q := fmt.Sprintf(`SELECT %s, COUNT(*), COALESCE(SUM(input_tokens),0),
		COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_creation_tokens),0),
		COALESCE(SUM(output_tokens),0), COALESCE(SUM(cost),0),
		COALESCE(SUM(CASE WHEN ok=0 THEN 1 ELSE 0 END),0)
		FROM requests WHERE ts >= ? GROUP BY %s ORDER BY cost DESC`, col, col)
	rows, err := s.db.Query(q, startTs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupAgg
	for rows.Next() {
		var g GroupAgg
		if err := rows.Scan(&g.Key, &g.Requests, &g.Input, &g.CacheRead, &g.CacheCreate,
			&g.Output, &g.Cost, &g.Errors); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Timeseries 按 bucketSec 整除分桶的时间序列。
func (s *Store) Timeseries(startTs, bucketSec int64) ([]Point, error) {
	rows, err := s.db.Query(`SELECT (ts/?)*? AS b, COUNT(*), COALESCE(SUM(input_tokens),0),
		COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_creation_tokens),0),
		COALESCE(SUM(output_tokens),0), COALESCE(SUM(cost),0)
		FROM requests WHERE ts >= ? GROUP BY b ORDER BY b`, bucketSec, bucketSec, startTs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Point
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.Bucket, &p.Requests, &p.Input, &p.CacheRead, &p.CacheCreate,
			&p.Output, &p.Cost); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListRequests 分页查询请求日志; filters 中的切片拼接为参数化条件。
func (s *Store) ListRequests(startTs int64, providerID, model, channel string, status string, limit, offset int) ([]RequestRow, error) {
	q := `SELECT id, ts, provider_id, provider_name, model, upstream_model, channel, format,
		stream, status, ok, input_tokens, cache_read_tokens, cache_creation_tokens,
		output_tokens, cost, duration_ms, ttfb_ms, failover, err, project, session_key
		FROM requests WHERE ts >= ?`
	args := []any{startTs}
	if providerID != "" {
		q += " AND provider_id = ?"
		args = append(args, providerID)
	}
	if model != "" {
		q += " AND model LIKE ?"
		args = append(args, "%"+model+"%")
	}
	if channel != "" {
		q += " AND channel = ?"
		args = append(args, channel)
	}
	if status == "error" {
		q += " AND ok = 0"
	} else if status == "ok" {
		q += " AND ok = 1"
	}
	q += " ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RequestRow
	for rows.Next() {
		var r RequestRow
		var streamInt, okInt int
		if err := rows.Scan(&r.ID, &r.Ts, &r.ProviderID, &r.ProviderNam, &r.Model, &r.Upstream,
			&r.Channel, &r.Format, &streamInt, &r.Status, &okInt,
			&r.Input, &r.CacheRead, &r.CacheCreate, &r.Output, &r.Cost,
			&r.DurationMs, &r.TTFBMs, &r.Failover, &r.Err, &r.Project, &r.SessionKey); err != nil {
			return nil, err
		}
		r.Stream, r.OK = streamInt == 1, okInt == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- 会话与快照 ----

// UpsertSession 更新会话元数据。
func (s *Store) UpsertSession(row SessionRow) error {
	_, err := s.db.Exec(`INSERT INTO sessions(key,project,title,last_model,msg_count,size_bytes,updated_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET project=excluded.project, title=excluded.title,
		  last_model=excluded.last_model, msg_count=excluded.msg_count,
		  size_bytes=excluded.size_bytes, updated_at=excluded.updated_at`,
		row.Key, row.Project, row.Title, row.LastModel, row.MsgCount, row.SizeBytes, row.UpdatedAt)
	return err
}

// ListSessions 全部会话(按更新时间倒序)。
func (s *Store) ListSessions() ([]SessionRow, error) {
	rows, err := s.db.Query(`SELECT key,project,title,last_model,msg_count,size_bytes,updated_at
		FROM sessions ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		if err := rows.Scan(&r.Key, &r.Project, &r.Title, &r.LastModel, &r.MsgCount, &r.SizeBytes, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteSession 删除会话及其快照元数据, 返回需删除的文件路径。
func (s *Store) DeleteSession(key string) ([]string, error) {
	rows, err := s.db.Query(`SELECT path FROM snapshots WHERE session_key = ?`, key)
	if err != nil {
		return nil, err
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil && p != "" {
			paths = append(paths, p)
		}
	}
	rows.Close()
	if _, err := s.db.Exec(`DELETE FROM snapshots WHERE session_key = ?`, key); err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE key = ?`, key); err != nil {
		return nil, err
	}
	return paths, nil
}

// InsertSnapshot 写入快照元数据。
func (s *Store) InsertSnapshot(row SnapshotRow) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO snapshots(session_key,seq,ts,path,size_bytes,model,msg_count,project)
		VALUES(?,?,?,?,?,?,?,?)`, row.SessionKy, row.Seq, row.Ts, row.Path, row.SizeBytes,
		row.Model, row.MsgCount, row.Project)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListSnapshots 会话快照列表(新→旧)。
func (s *Store) ListSnapshots(key string) ([]SnapshotRow, error) {
	rows, err := s.db.Query(`SELECT id,session_key,seq,ts,path,size_bytes,model,msg_count,project
		FROM snapshots WHERE session_key = ? ORDER BY seq DESC`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SnapshotRow
	for rows.Next() {
		var r SnapshotRow
		if err := rows.Scan(&r.ID, &r.SessionKy, &r.Seq, &r.Ts, &r.Path, &r.SizeBytes,
			&r.Model, &r.MsgCount, &r.Project); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetSnapshot 按 ID 取快照元数据。
func (s *Store) GetSnapshot(id int64) (SnapshotRow, error) {
	var r SnapshotRow
	err := s.db.QueryRow(`SELECT id,session_key,seq,ts,path,size_bytes,model,msg_count,project
		FROM snapshots WHERE id = ?`, id).Scan(&r.ID, &r.SessionKy, &r.Seq, &r.Ts, &r.Path,
		&r.SizeBytes, &r.Model, &r.MsgCount, &r.Project)
	return r, err
}

// PruneSessionSnapshots 删除指定会话超出 keep 数的旧快照, 返回文件路径。
func (s *Store) PruneSessionSnapshots(key string, keep int) ([]string, error) {
	rows, err := s.db.Query(`SELECT id, path FROM snapshots WHERE session_key = ?
		ORDER BY seq DESC LIMIT -1 OFFSET ?`, key, keep)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var paths []string
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err == nil {
			ids = append(ids, id)
			if p != "" {
				paths = append(paths, p)
			}
		}
	}
	rows.Close()
	for _, id := range ids {
		if _, err := s.db.Exec(`DELETE FROM snapshots WHERE id = ?`, id); err != nil {
			return paths, err
		}
	}
	return paths, nil
}

// SnapshotSizeSum 全部快照占用字节数。
func (s *Store) SnapshotSizeSum() (int64, error) {
	var n sql.NullInt64
	err := s.db.QueryRow(`SELECT SUM(size_bytes) FROM snapshots`).Scan(&n)
	return n.Int64, err
}

// OldestSnapshots 最旧的 n 份快照(用于全局容量 LRU)。
func (s *Store) OldestSnapshots(n int) ([]SnapshotRow, error) {
	rows, err := s.db.Query(`SELECT id,session_key,seq,ts,path,size_bytes,model,msg_count,project
		FROM snapshots ORDER BY ts ASC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SnapshotRow
	for rows.Next() {
		var r SnapshotRow
		if err := rows.Scan(&r.ID, &r.SessionKy, &r.Seq, &r.Ts, &r.Path, &r.SizeBytes,
			&r.Model, &r.MsgCount, &r.Project); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteSnapshotRow 删除快照元数据, 返回文件路径。
func (s *Store) DeleteSnapshotRow(id int64) (string, error) {
	var p string
	err := s.db.QueryRow(`SELECT path FROM snapshots WHERE id = ?`, id).Scan(&p)
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(`DELETE FROM snapshots WHERE id = ?`, id)
	return p, err
}

// ---- 定价 ----

// UpsertPrice 写入/更新单价(含峰谷档位)。
func (s *Store) UpsertPrice(p Price) error {
	tiersJSON := ""
	if p.Peak != nil || p.Valley != nil {
		b, err := json.Marshal(map[string]*Tier{"peak": p.Peak, "valley": p.Valley})
		if err != nil {
			return err
		}
		tiersJSON = string(b)
	}
	_, err := s.db.Exec(`INSERT INTO pricing(model,input,output,cache_read,cache_creation,updated_at,tiers)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(model) DO UPDATE SET input=excluded.input, output=excluded.output,
		  cache_read=excluded.cache_read, cache_creation=excluded.cache_creation,
		  updated_at=excluded.updated_at, tiers=excluded.tiers`,
		p.Model, p.Input, p.Output, p.CacheRead, p.CacheCreation, time.Now().Unix(), tiersJSON)
	return err
}

// ListPrices 全部单价。
func (s *Store) ListPrices() ([]Price, error) {
	rows, err := s.db.Query(`SELECT model,input,output,cache_read,cache_creation,tiers FROM pricing ORDER BY model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Price
	for rows.Next() {
		var p Price
		var tiers string
		if err := rows.Scan(&p.Model, &p.Input, &p.Output, &p.CacheRead, &p.CacheCreation, &tiers); err != nil {
			return nil, err
		}
		if tiers != "" {
			var t struct {
				Peak   *Tier `json:"peak"`
				Valley *Tier `json:"valley"`
			}
			if json.Unmarshal([]byte(tiers), &t) == nil {
				p.Peak, p.Valley = t.Peak, t.Valley
			}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePrice 删除单价。
func (s *Store) DeletePrice(model string) error {
	_, err := s.db.Exec(`DELETE FROM pricing WHERE model = ?`, model)
	return err
}
