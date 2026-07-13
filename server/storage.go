package server

import (
	"database/sql"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (CGO-free)

	"github.com/Paco5687/autormm/internal/protocol"
)

// History persists per-host metric samples to SQLite for time-series graphs.
// A nil *History is valid and disables persistence.
type History struct {
	db        *sql.DB
	retention time.Duration
	insMu     sync.Mutex
	ins       *sql.Stmt
}

// HistPoint is one (optionally bucket-averaged) sample.
type HistPoint struct {
	TS      int64   `json:"ts"`
	CPU     float64 `json:"cpu"`
	Mem     float64 `json:"mem"`
	Load1   float64 `json:"load1"`
	NetRecv uint64  `json:"net_recv"`
	NetSent uint64  `json:"net_sent"`
	DiskMax float64 `json:"disk_max"`
}

// OpenHistory opens (creating if needed) the SQLite database at path.
func OpenHistory(path string, retention time.Duration) (*History, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One writer avoids lock churn; WAL + busy_timeout keep reads concurrent.
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS metrics (
			agent_id  TEXT    NOT NULL,
			ts        INTEGER NOT NULL,
			cpu       REAL,
			mem       REAL,
			load1     REAL,
			net_recv  INTEGER,
			net_sent  INTEGER,
			disk_max  REAL
		);
		CREATE INDEX IF NOT EXISTS idx_metrics_agent_ts ON metrics(agent_id, ts);
	`); err != nil {
		db.Close()
		return nil, err
	}
	ins, err := db.Prepare(`INSERT INTO metrics
		(agent_id, ts, cpu, mem, load1, net_recv, net_sent, disk_max)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	return &History{db: db, retention: retention, ins: ins}, nil
}

// Insert stores one snapshot. Safe to call concurrently.
func (h *History) Insert(agentID string, m *protocol.Metrics) {
	if h == nil || m == nil {
		return
	}
	var diskMax float64
	for _, d := range m.Disks {
		if d.Percent > diskMax {
			diskMax = d.Percent
		}
	}
	ts := m.Timestamp.Unix()
	if ts <= 0 {
		ts = time.Now().Unix()
	}
	h.insMu.Lock()
	defer h.insMu.Unlock()
	h.ins.Exec(agentID, ts, m.CPUPercent, m.MemPercent, m.Load1, m.NetRecv, m.NetSent, diskMax)
}

// Query returns bucket-averaged points for agentID between from and to.
func (h *History) Query(agentID string, from, to time.Time, buckets int) ([]HistPoint, error) {
	if h == nil {
		return nil, nil
	}
	if buckets <= 0 {
		buckets = 120
	}
	span := to.Unix() - from.Unix()
	bucket := span / int64(buckets)
	if bucket < 1 {
		bucket = 1
	}
	rows, err := h.db.Query(`
		SELECT (ts/?)*? AS b,
		       AVG(cpu), AVG(mem), AVG(load1),
		       AVG(net_recv), AVG(net_sent), MAX(disk_max)
		FROM metrics
		WHERE agent_id=? AND ts>=? AND ts<=?
		GROUP BY b ORDER BY b`,
		bucket, bucket, agentID, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistPoint
	for rows.Next() {
		var p HistPoint
		var recv, sent float64
		if err := rows.Scan(&p.TS, &p.CPU, &p.Mem, &p.Load1, &recv, &sent, &p.DiskMax); err != nil {
			return nil, err
		}
		p.NetRecv, p.NetSent = uint64(recv), uint64(sent)
		out = append(out, p)
	}
	return out, rows.Err()
}

// prune deletes samples older than the retention window.
func (h *History) prune() {
	if h == nil {
		return
	}
	cutoff := time.Now().Add(-h.retention).Unix()
	h.insMu.Lock()
	h.db.Exec(`DELETE FROM metrics WHERE ts < ?`, cutoff)
	h.insMu.Unlock()
}

// DB returns the underlying database handle (shared with the script store).
func (h *History) DB() *sql.DB {
	if h == nil {
		return nil
	}
	return h.db
}

// Close flushes and closes the database.
func (h *History) Close() error {
	if h == nil {
		return nil
	}
	if h.ins != nil {
		h.ins.Close()
	}
	return h.db.Close()
}
