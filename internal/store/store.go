package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("роутер не найден")

type Router struct {
	ID          int
	Name        string
	DisplayName string
	Firmware    string
	TunnelPort  int
	SSHUser     string
	AuthType    string
	AuthSecret  string
	HostKey     string
	SubURL      string
	NodesHash   string // ноды на роутере

	Owner  string  // номер учётки в панели
	Access []int64 // telegram id

	Primary    string
	Preferred  string             // приоритетная нода, пусто значит по замеру
	Speeds     map[string]float64 // мбит/с
	MeasuredAt time.Time
}

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS routers (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL,
	firmware    TEXT NOT NULL,
	tunnel_port INTEGER NOT NULL UNIQUE,
	ssh_user    TEXT NOT NULL,
	auth_type   TEXT NOT NULL,
	auth_secret TEXT NOT NULL,
	host_key    TEXT NOT NULL DEFAULT '',
	added_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS state_cache (
	router_id  INTEGER NOT NULL,
	op         TEXT NOT NULL,
	value      INTEGER NOT NULL,
	checked_at TEXT NOT NULL,
	PRIMARY KEY (router_id, op)
);

CREATE TABLE IF NOT EXISTS audit_log (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	ts        TEXT NOT NULL,
	router_id INTEGER NOT NULL,
	actor     TEXT NOT NULL,
	op        TEXT NOT NULL,
	value     INTEGER NOT NULL,
	result    TEXT NOT NULL,
	detail    TEXT NOT NULL DEFAULT ''
);`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	// одно соединение
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("схема базы: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("миграция базы: %w", err)
	}
	return &Store{db: db}, nil
}

var addedColumns = []string{"display_name", "sub_url", "nodes_hash",
	"owner", "access", "primary_tag", "speeds", "measured_at", "pinned"}

func migrate(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(routers)`)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		have[name] = true
	}
	rows.Close()
	for _, col := range addedColumns {
		if have[col] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE routers ADD COLUMN ` + col + ` TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Routers() ([]Router, error) {
	rows, err := s.db.Query(`SELECT ` + routerColumns + ` FROM routers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Router
	for rows.Next() {
		r, err := scanRouter(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Router(id int) (Router, error) {
	r, err := scanRouter(s.db.QueryRow(`SELECT `+routerColumns+` FROM routers WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Router{}, ErrNotFound
	}
	return r, err
}

const routerColumns = `id, name, display_name, firmware, tunnel_port, ssh_user,
	auth_type, auth_secret, host_key, sub_url, nodes_hash,
	owner, access, primary_tag, speeds, measured_at, pinned`

func scanRouter(row interface{ Scan(...any) error }) (Router, error) {
	var r Router
	var access, speeds, measured, pinned string
	err := row.Scan(&r.ID, &r.Name, &r.DisplayName, &r.Firmware, &r.TunnelPort,
		&r.SSHUser, &r.AuthType, &r.AuthSecret, &r.HostKey, &r.SubURL, &r.NodesHash,
		&r.Owner, &access, &r.Primary, &speeds, &measured, &pinned)
	if err != nil {
		return r, err
	}
	for _, f := range strings.Split(access, ",") {
		if id, err := strconv.ParseInt(f, 10, 64); err == nil {
			r.Access = append(r.Access, id)
		}
	}
	if speeds != "" {
		_ = json.Unmarshal([]byte(speeds), &r.Speeds)
	}
	r.MeasuredAt, _ = time.Parse(time.RFC3339, measured)
	// "1" от первой версии закрепления
	if pinned == "1" {
		r.Preferred = r.Primary
	} else {
		r.Preferred = pinned
	}
	return r, nil
}

func (s *Store) SetOwner(id int, owner string, access []int64) error {
	ids := make([]string, len(access))
	for i, a := range access {
		ids[i] = strconv.FormatInt(a, 10)
	}
	res, err := s.db.Exec(`UPDATE routers SET owner = ?, access = ? WHERE id = ?`,
		owner, strings.Join(ids, ","), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetPrimary(id int, primary string) error {
	_, err := s.db.Exec(`UPDATE routers SET primary_tag = ? WHERE id = ?`, primary, id)
	return err
}

func (s *Store) SetPreferred(id int, primary, preferred string) error {
	res, err := s.db.Exec(`UPDATE routers SET primary_tag = ?, pinned = ? WHERE id = ?`, primary, preferred, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetSpeeds(id int, speeds map[string]float64) error {
	b, _ := json.Marshal(speeds)
	_, err := s.db.Exec(`UPDATE routers SET speeds = ?, measured_at = ? WHERE id = ?`,
		string(b), time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func (s *Store) SetSubURL(id int, url string) error {
	res, err := s.db.Exec(`UPDATE routers SET sub_url = ?, nodes_hash = '' WHERE id = ?`, url, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetNodesHash(id int, hash string) error {
	_, err := s.db.Exec(`UPDATE routers SET nodes_hash = ? WHERE id = ?`, hash, id)
	return err
}

func (s *Store) AddRouter(r Router) (int, error) {
	res, err := s.db.Exec(`INSERT INTO routers
		(name, display_name, firmware, tunnel_port, ssh_user, auth_type, auth_secret, host_key, added_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Name, r.DisplayName, r.Firmware, r.TunnelPort, r.SSHUser,
		r.AuthType, r.AuthSecret, r.HostKey, now())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return int(id), err
}

func (s *Store) SetDisplayName(id int, name string) error {
	res, err := s.db.Exec(`UPDATE routers SET display_name = ? WHERE id = ?`, name, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CachePut(routerID int, op string, value bool) error {
	_, err := s.db.Exec(`INSERT INTO state_cache (router_id, op, value, checked_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(router_id, op) DO UPDATE SET value = excluded.value, checked_at = excluded.checked_at`,
		routerID, op, boolToInt(value), now())
	return err
}

func (s *Store) CacheGet(routerID int, op string) (bool, time.Time, bool) {
	var v int
	var ts string
	err := s.db.QueryRow(`SELECT value, checked_at FROM state_cache
		WHERE router_id = ? AND op = ?`, routerID, op).Scan(&v, &ts)
	if err != nil {
		return false, time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return false, time.Time{}, false
	}
	return v == 1, at, true
}

func (s *Store) Log(routerID int, actor, op string, value bool, result, detail string) {
	// журнал не должен ронять операцию
	_, _ = s.db.Exec(`INSERT INTO audit_log (ts, router_id, actor, op, value, result, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		now(), routerID, actor, op, boolToInt(value), result, detail)
}

type LogEntry struct {
	TS       string
	RouterID int
	Actor    string
	Op       string
	Value    bool
	Result   string
	Detail   string
}

func (s *Store) RecentLog(n int) ([]LogEntry, error) {
	rows, err := s.db.Query(`SELECT ts, router_id, actor, op, value, result, detail
		FROM audit_log ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LogEntry
	for rows.Next() {
		var e LogEntry
		var v int
		if err := rows.Scan(&e.TS, &e.RouterID, &e.Actor, &e.Op, &v, &e.Result, &e.Detail); err != nil {
			return nil, err
		}
		e.Value = v == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
