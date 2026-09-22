//go:build filestore

package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/paths"
)

// Backend name, shown by `pai session` for transparency.
func Backend() string { return "file" }

const fileExt = ".jsonl"

// Open returns the dependency-free JSONL file store. Build with -tags filestore
// to select it; SQLite is the default backend.
func Open() (Store, error) {
	dir := paths.DataDir()
	if dir == "" {
		return nil, fmt.Errorf("cannot determine the data directory")
	}
	dir = filepath.Join(dir, "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create sessions dir: %w", err)
	}
	return &fileStore{dir: dir}, nil
}

// fileStore keeps one JSONL file per session: line 1 is the Meta, the rest are
// turns. The filename is the session name.
//
// Writes are plain appends, so a process killed mid-write can leave a truncated
// final line. Reads tolerate exactly that (see readLines) rather than letting a
// single bad byte brick the whole session.
type fileStore struct{ dir string }

func (s *fileStore) path(name string) string { return filepath.Join(s.dir, name+fileExt) }

func (s *fileStore) Create(meta Meta) (*Session, error) {
	if err := validate(meta.Name); err != nil {
		return nil, err
	}
	path := s.path(meta.Name)

	// A zero-byte file is the leftover of a create that died before the header
	// was written. Reuse it rather than reporting ErrExists forever, which would
	// otherwise make the name permanently unusable.
	if fi, err := os.Stat(path); err == nil && fi.Size() == 0 {
		_ = os.Remove(path)
	}

	now := time.Now()
	meta.CreatedAt, meta.UpdatedAt = now, now

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrExists, meta.Name)
		}
		return nil, err
	}
	defer f.Close()
	if err := writeJSONLine(f, meta); err != nil {
		return nil, err
	}
	return &Session{Meta: meta}, nil
}

func (s *fileStore) Get(name string) (*Session, error) {
	raw, err := os.ReadFile(s.path(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return nil, err
	}

	lines := nonEmptyLines(raw)
	if len(lines) == 0 {
		return nil, fmt.Errorf("%w: %s (empty)", ErrNotFound, name)
	}

	var meta Meta
	if err := json.Unmarshal(lines[0], &meta); err != nil {
		// An incomplete header means the session was never fully created.
		return nil, fmt.Errorf("%w: %s (incomplete header)", ErrNotFound, name)
	}

	sess := &Session{Meta: meta}
	for i, line := range lines[1:] {
		var t core.Turn
		if err := json.Unmarshal(line, &t); err != nil {
			// A malformed *last* line is the signature of an append interrupted
			// mid-write: those bytes never landed, so drop them. Corruption
			// anywhere earlier is a real error worth surfacing.
			if i == len(lines)-2 {
				continue
			}
			return nil, fmt.Errorf("parse session %q turn %d: %w", name, i+1, err)
		}
		sess.Turns = append(sess.Turns, t)
	}

	sess.Meta.Name = name
	sess.Meta.Turns = len(sess.Turns)
	if fi, err := os.Stat(s.path(name)); err == nil {
		sess.Meta.UpdatedAt = fi.ModTime()
	}
	return sess, nil
}

func (s *fileStore) Latest(cwd string) (*Session, error) {
	metas, err := s.List()
	if err != nil {
		return nil, err
	}
	best := ""
	var bestAt time.Time
	for _, m := range metas {
		if cwd != "" && m.Cwd != cwd {
			continue
		}
		if best == "" || m.UpdatedAt.After(bestAt) {
			best, bestAt = m.Name, m.UpdatedAt
		}
	}
	if best == "" {
		return nil, fmt.Errorf("%w (for %s)", ErrNotFound, cwd)
	}
	return s.Get(best)
}

func (s *fileStore) List() ([]Meta, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	metas := make([]Meta, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), fileExt) {
			continue
		}
		name := strings.TrimSuffix(e.Name(), fileExt)

		var m Meta
		turns := 0
		if raw, err := os.ReadFile(s.path(name)); err == nil {
			lines := nonEmptyLines(raw)
			if len(lines) > 0 {
				_ = json.Unmarshal(lines[0], &m)
				turns = len(lines) - 1
			}
		}
		m.Name = name
		m.Turns = turns
		if fi, err := e.Info(); err == nil {
			m.UpdatedAt = fi.ModTime()
		}
		metas = append(metas, m)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].UpdatedAt.After(metas[j].UpdatedAt) })
	return metas, nil
}

func (s *fileStore) Append(name string, turns ...core.Turn) error {
	if len(turns) == 0 {
		return nil
	}
	f, err := os.OpenFile(s.path(name), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return err
	}
	defer f.Close()

	now := time.Now()
	for _, t := range turns {
		if t.At.IsZero() {
			t.At = now
		}
		if err := writeJSONLine(f, t); err != nil {
			return err
		}
	}
	return nil
}

func (s *fileStore) Rename(oldName, newName string) error {
	if err := validate(newName); err != nil {
		return err
	}
	if _, err := os.Stat(s.path(newName)); err == nil {
		return fmt.Errorf("%w: %s", ErrExists, newName)
	}
	if err := os.Rename(s.path(oldName), s.path(newName)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, oldName)
		}
		return err
	}
	return nil
}

func (s *fileStore) Delete(name string) error {
	if err := os.Remove(s.path(name)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return err
	}
	return nil
}

func (s *fileStore) Close() error { return nil }

// nonEmptyLines splits b into trimmed, non-empty lines.
func nonEmptyLines(b []byte) [][]byte {
	var out [][]byte
	for _, ln := range bytes.Split(b, []byte{'\n'}) {
		if ln = bytes.TrimSpace(ln); len(ln) > 0 {
			out = append(out, ln)
		}
	}
	return out
}

func writeJSONLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}
