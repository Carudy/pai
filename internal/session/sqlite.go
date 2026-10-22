package session

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: keeps cross-compilation toolchain-free

	"github.com/Carudy/pai/internal/core"
)

// Backend name, shown by `pai session` for transparency.
func Backend() string { return "sqlite" }

func openBackend(dir string) (Store, error) {
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

var _ Store = (*sqliteStore)(nil)

func (s *sqliteStore) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS sessions (
  name       TEXT PRIMARY KEY,
  role       TEXT NOT NULL,
  model      TEXT NOT NULL,
  cwd        TEXT NOT NULL,
  title      TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  prompt_tokens   INTEGER NOT NULL DEFAULT 0,
  prompt_estimate INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS turns (
  name    TEXT NOT NULL REFERENCES sessions(name) ON DELETE CASCADE ON UPDATE CASCADE,
  seq     INTEGER NOT NULL,
  role    TEXT NOT NULL,
  kind    TEXT NOT NULL,
  content TEXT NOT NULL,
  at      INTEGER NOT NULL,
  essential INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (name, seq)
);
CREATE TABLE IF NOT EXISTS turn_overrides (
  name       TEXT NOT NULL,
  seq        INTEGER NOT NULL,
  action     TEXT NOT NULL,
  content    TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (name, seq),
  FOREIGN KEY (name, seq) REFERENCES turns(name, seq) ON DELETE CASCADE ON UPDATE CASCADE
);`)
	if err != nil {
		return err
	}
	// Additive columns for databases created before they existed: CREATE TABLE IF NOT
	// EXISTS only covers a fresh file, and there is no schema version to bump.
	for _, col := range [][2]string{
		{"prompt_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"prompt_estimate", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := s.ensureColumn("sessions", col[0], col[1]); err != nil {
			return err
		}
	}
	if err := s.ensureColumn("turns", "essential", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	return nil
}

// ensureColumn adds a column when an older database predates it. table and decl are
// literals from this package, never user input.
func (s *sqliteStore) ensureColumn(table, column, decl string) error {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if found {
		return nil
	}
	_, err = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl))
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
		`SELECT name, role, model, cwd, title, created_at, updated_at, prompt_tokens, prompt_estimate
		 FROM sessions WHERE name = ?`, name).
		Scan(&m.Name, &m.Role, &m.Model, &m.Cwd, &m.Title, &createdAt, &updatedAt, &m.PromptTokens, &m.PromptEstimate)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	m.CreatedAt, m.UpdatedAt = time.Unix(createdAt, 0), time.Unix(updatedAt, 0)

	rows, err := s.db.Query(`SELECT seq, role, kind, content, at, essential FROM turns WHERE name = ? ORDER BY seq`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sess := &Session{Meta: m}
	for rows.Next() {
		var (
			t         core.Turn
			at        int64
			essential int
		)
		if err := rows.Scan(&t.Seq, &t.Role, &t.Kind, &t.Content, &at, &essential); err != nil {
			return nil, err
		}
		t.At = time.Unix(at, 0)
		t.Essential = essential != 0
		sess.Turns = append(sess.Turns, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sess.Meta.Turns = len(sess.Turns)

	// The override layer rides along with the session: it is small, keyed by the
	// same seq, and every reader that wants the turns wants the surgery on them.
	orows, err := s.db.Query(`SELECT seq, action, content FROM turn_overrides WHERE name = ?`, name)
	if err != nil {
		return nil, err
	}
	defer orows.Close()
	for orows.Next() {
		var o Override
		if err := orows.Scan(&o.Seq, &o.Action, &o.Content); err != nil {
			return nil, err
		}
		if sess.Overrides == nil {
			sess.Overrides = make(map[int]Override)
		}
		sess.Overrides[o.Seq] = o
	}
	if err := orows.Err(); err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *sqliteStore) Latest(cwd string) (*Session, error) {
	// Timestamps have one-second resolution, so ties are common; name breaks them
	// so "latest" and list order do not depend on row insertion order.
	query := `SELECT name FROM sessions ORDER BY updated_at DESC, name LIMIT 1`
	args := []any{}
	if cwd != "" {
		query = `SELECT name FROM sessions WHERE cwd = ? ORDER BY updated_at DESC, name LIMIT 1`
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
FROM sessions s ORDER BY s.updated_at DESC, s.name`)
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
			`INSERT INTO turns(name, seq, role, kind, content, at, essential) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			name, seq, t.Role, t.Kind, t.Content, at.Unix(), boolInt(t.Essential)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at = ? WHERE name = ?`, now.Unix(), name); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqliteStore) SetModel(name, model string) error { return s.setField(name, "model", model) }
func (s *sqliteStore) SetRole(name, role string) error   { return s.setField(name, "role", role) }
func (s *sqliteStore) SetWorkingDir(name, cwd string) error {
	return s.setField(name, "cwd", cwd)
}

// SetUsage records the prompt size a provider reported next to the estimate made for
// that same prompt. It does not touch updated_at: accounting is not conversation
// activity, and bumping the timestamp would reorder the session list on every turn.
func (s *sqliteStore) SetUsage(name string, reported, estimate int) error {
	res, err := s.db.Exec(`UPDATE sessions SET prompt_tokens = ?, prompt_estimate = ? WHERE name = ?`,
		reported, estimate, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return nil
}

// SetOverride stores or replaces the user's surgery on one turn. It upserts, so a
// turn has at most one override, and it reports ErrNotFound when the session or
// the turn it names does not exist rather than leaking a foreign-key error.
func (s *sqliteStore) SetOverride(name string, o Override) error {
	if err := validate(name); err != nil {
		return err
	}
	if o.Action != core.OverrideReplace && o.Action != core.OverrideDrop {
		return fmt.Errorf("invalid override action %q", o.Action)
	}
	// A drop carries no text, so normalize here and keep the row shape one kind.
	content := o.Content
	if o.Action == core.OverrideDrop {
		content = ""
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	exists, err := turnExists(tx, name, o.Seq)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: %s turn %d", ErrNotFound, name, o.Seq)
	}

	_, err = tx.Exec(
		`INSERT INTO turn_overrides(name, seq, action, content, updated_at) VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(name, seq) DO UPDATE SET action = excluded.action, content = excluded.content, updated_at = excluded.updated_at`,
		name, o.Seq, o.Action, content, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("set override: %w", err)
	}
	return tx.Commit()
}

// DeleteOverride clears the surgery on one turn, restoring its stored text. A
// turn with no override is ErrNotFound, so reset is not silently ambiguous.
func (s *sqliteStore) DeleteOverride(name string, seq int) error {
	if err := validate(name); err != nil {
		return err
	}
	res, err := s.db.Exec(`DELETE FROM turn_overrides WHERE name = ? AND seq = ?`, name, seq)
	if err != nil {
		return fmt.Errorf("delete override: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: override on %s turn %d", ErrNotFound, name, seq)
	}
	return nil
}

// SetEssential marks or unmarks one stored turn, returning ErrNotFound when the
// session or the turn does not exist. It is independent of the turn's override:
// a turn can be both edited/dropped and marked essential, and either is
// revocable without touching the other.
func (s *sqliteStore) SetEssential(name string, seq int, on bool) error {
	if err := validate(name); err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE turns SET essential = ? WHERE name = ? AND seq = ?`, boolInt(on), name, seq)
	if err != nil {
		return fmt.Errorf("set essential: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: turn %d in %s", ErrNotFound, seq, name)
	}
	return nil
}

// boolInt stores a Go bool in SQLite's INTEGER column.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// turnExists reports whether the named session holds that turn, distinguishing
// "no such session or turn" (ErrNotFound) from a real query failure.
func turnExists(tx *sql.Tx, name string, seq int) (bool, error) {
	var sessionExists int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM sessions WHERE name = ?`, name).Scan(&sessionExists); err != nil {
		return false, err
	}
	if sessionExists == 0 {
		return false, nil
	}
	var turnExists int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM turns WHERE name = ? AND seq = ?`, name, seq).Scan(&turnExists); err != nil {
		return false, err
	}
	return turnExists > 0, nil
}

// Truncate keeps the first keep turns and drops the rest. seq is dense and
// 1-based and only the tail is removed, so keep is the last surviving seq.
func (s *sqliteStore) Truncate(name string, keep int) error {
	if err := validate(name); err != nil {
		return err
	}
	if keep < 0 {
		keep = 0
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
	if _, err := tx.Exec(`DELETE FROM turns WHERE name = ? AND seq > ?`, name, keep); err != nil {
		return fmt.Errorf("truncate session: %w", err)
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at = ? WHERE name = ?`, time.Now().Unix(), name); err != nil {
		return err
	}
	return tx.Commit()
}

// setField rewrites one metadata column. column is a compile-time constant, never
// caller input, so concatenating it into the statement is safe.
func (s *sqliteStore) setField(name, column, value string) error {
	if err := validate(name); err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE sessions SET `+column+` = ?, updated_at = ? WHERE name = ?`, value, time.Now().Unix(), name)
	if err != nil {
		return fmt.Errorf("update session %s: %w", column, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return nil
}

// Rename renames a session in one transaction, including its turns. A name that
// is already taken is ErrExists rather than a raw UNIQUE-constraint error, which
// is what Store promises and what callers translate into a user-facing message.
func (s *sqliteStore) Rename(oldName, newName string) error {
	if err := validate(newName); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Renaming onto the session's own name stays a successful no-op, so only a
	// different, already-taken name is a conflict.
	var taken int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM sessions WHERE name = ?`, newName).Scan(&taken); err != nil {
		return err
	}
	if taken > 0 && newName != oldName {
		return fmt.Errorf("%w: %s", ErrExists, newName)
	}

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
