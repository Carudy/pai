//go:build !filestore

package session

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: keeps `go install` cgo-free

	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/paths"
)

// Backend name, shown by `pai session` for transparency.
func Backend() string { return "sqlite" }

// Open returns the pure-Go SQLite store: the default backend.
func Open() (Store, error) {
	dir := paths.DataDir()
	if dir == "" {
		return nil, fmt.Errorf("cannot determine the data directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	dsn := "file:" + filepath.Join(dir, "sessions.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &sqliteStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

type sqliteStore struct{ db *sql.DB }

func (s *sqliteStore) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS sessions (
  name       TEXT PRIMARY KEY,
  role       TEXT NOT NULL,
  model      TEXT NOT NULL,
  cwd        TEXT NOT NULL,
  title      TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS turns (
  name    TEXT NOT NULL REFERENCES sessions(name) ON DELETE CASCADE ON UPDATE CASCADE,
  seq     INTEGER NOT NULL,
  role    TEXT NOT NULL,
  kind    TEXT NOT NULL,
  content TEXT NOT NULL,
  at      INTEGER NOT NULL,
  PRIMARY KEY (name, seq)
);`)
	return err
}

func (s *sqliteStore) Create(meta Meta) (*Session, error) {
	if err := validate(meta.Name); err != nil {
		return nil, err
	}
	now := time.Now()
	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO sessions(name, role, model, cwd, title, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		meta.Name, meta.Role, meta.Model, meta.Cwd, meta.Title, now.Unix(), now.Unix())
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%w: %s", ErrExists, meta.Name)
	}
	meta.CreatedAt, meta.UpdatedAt = now, now
	return &Session{Meta: meta}, nil
}

func (s *sqliteStore) Get(name string) (*Session, error) {
	var (
		m         Meta
		createdAt int64
		updatedAt int64
	)
	err := s.db.QueryRow(
		`SELECT name, role, model, cwd, title, created_at, updated_at FROM sessions WHERE name = ?`, name).
		Scan(&m.Name, &m.Role, &m.Model, &m.Cwd, &m.Title, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	m.CreatedAt, m.UpdatedAt = time.Unix(createdAt, 0), time.Unix(updatedAt, 0)

	rows, err := s.db.Query(`SELECT role, kind, content, at FROM turns WHERE name = ? ORDER BY seq`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sess := &Session{Meta: m}
	for rows.Next() {
		var (
			t  core.Turn
			at int64
		)
		if err := rows.Scan(&t.Role, &t.Kind, &t.Content, &at); err != nil {
			return nil, err
		}
		t.At = time.Unix(at, 0)
		sess.Turns = append(sess.Turns, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sess.Meta.Turns = len(sess.Turns)
	return sess, nil
}

func (s *sqliteStore) Latest(cwd string) (*Session, error) {
	query := `SELECT name FROM sessions ORDER BY updated_at DESC LIMIT 1`
	args := []any{}
	if cwd != "" {
		query = `SELECT name FROM sessions WHERE cwd = ? ORDER BY updated_at DESC LIMIT 1`
		args = append(args, cwd)
	}
	var name string
	err := s.db.QueryRow(query, args...).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w (for %s)", ErrNotFound, cwd)
	}
	if err != nil {
		return nil, err
	}
	return s.Get(name)
}

func (s *sqliteStore) List() ([]Meta, error) {
	rows, err := s.db.Query(`
SELECT s.name, s.role, s.model, s.cwd, s.title, s.created_at, s.updated_at,
       (SELECT COUNT(1) FROM turns t WHERE t.name = s.name)
FROM sessions s ORDER BY s.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metas []Meta
	for rows.Next() {
		var (
			m         Meta
			createdAt int64
			updatedAt int64
			turns     int
		)
		if err := rows.Scan(&m.Name, &m.Role, &m.Model, &m.Cwd, &m.Title, &createdAt, &updatedAt, &turns); err != nil {
			return nil, err
		}
		m.CreatedAt, m.UpdatedAt, m.Turns = time.Unix(createdAt, 0), time.Unix(updatedAt, 0), turns
		metas = append(metas, m)
	}
	return metas, rows.Err()
}

func (s *sqliteStore) Append(name string, turns ...core.Turn) error {
	if len(turns) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM sessions WHERE name = ?`, name).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	var seq int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM turns WHERE name = ?`, name).Scan(&seq); err != nil {
		return err
	}

	now := time.Now()
	for _, t := range turns {
		seq++
		at := t.At
		if at.IsZero() {
			at = now
		}
		if _, err := tx.Exec(
			`INSERT INTO turns(name, seq, role, kind, content, at) VALUES(?, ?, ?, ?, ?, ?)`,
			name, seq, t.Role, t.Kind, t.Content, at.Unix()); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at = ? WHERE name = ?`, now.Unix(), name); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqliteStore) Rename(oldName, newName string) error {
	if err := validate(newName); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`UPDATE sessions SET name = ? WHERE name = ?`, newName, oldName)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, oldName)
	}
	if _, err := tx.Exec(`UPDATE turns SET name = ? WHERE name = ?`, newName, oldName); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqliteStore) Delete(name string) error {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return nil
}

func (s *sqliteStore) Close() error { return s.db.Close() }
