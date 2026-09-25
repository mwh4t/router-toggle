package store

import (
	"database/sql"
	"errors"
	"fmt"
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

func migrate(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(routers)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "display_name" {
			return nil
		}
	}
	_, err = db.Exec(`ALTER TABLE routers ADD COLUMN display_name TEXT NOT NULL DEFAULT ''`)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Routers() ([]Router, error) {
	rows, err := s.db.Query(`SELECT id, name, display_name, firmware, tunnel_port, ssh_user,
		auth_type, auth_secret, host_key FROM routers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Router
	for rows.Next() {
		var r Router
		if err := rows.Scan(&r.ID, &r.Name, &r.DisplayName, &r.Firmware, &r.TunnelPort,
			&r.SSHUser, &r.AuthType, &r.AuthSecret, &r.HostKey); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Router(id int) (Router, error) {
	var r Router
	err := s.db.QueryRow(`SELECT id, name, display_name, firmware, tunnel_port, ssh_user,
		auth_type, auth_secret, host_key FROM routers WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &r.DisplayName, &r.Firmware, &r.TunnelPort,
			&r.SSHUser, &r.AuthType, &r.AuthSecret, &r.HostKey)
	if errors.Is(err, sql.ErrNoRows) {
		return Router{}, ErrNotFound
	}
	return r, err
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
