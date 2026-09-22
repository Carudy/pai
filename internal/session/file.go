//go:build !sqlite

package session

import (
	"bufio"
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

// Open returns the default, dependency-free file store.
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
type fileStore struct{ dir string }

func (s *fileStore) path(name string) string { return filepath.Join(s.dir, name+fileExt) }

func (s *fileStore) Create(meta Meta) (*Session, error) {
	if err := validate(meta.Name); err != nil {
		return nil, err
	}
	now := time.Now()
	meta.CreatedAt, meta.UpdatedAt = now, now

	f, err := os.OpenFile(s.path(meta.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
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
	f, err := os.Open(s.path(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return nil, err
	}
	defer f.Close()

	sc := newScanner(f)
	sess := &Session{}
	sawMeta := false
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if !sawMeta {
			if err := json.Unmarshal(line, &sess.Meta); err != nil {
				return nil, fmt.Errorf("parse session %q: %w", name, err)
			}
			sawMeta = true
			continue
		}
		var t core.Turn
		if err := json.Unmarshal(line, &t); err != nil {
			return nil, fmt.Errorf("parse session %q turn: %w", name, err)
		}
		sess.Turns = append(sess.Turns, t)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !sawMeta {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
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
		if f, err := os.Open(s.path(name)); err == nil {
			sc := newScanner(f)
			sawMeta := false
			for sc.Scan() {
				if len(bytes.TrimSpace(sc.Bytes())) == 0 {
					continue
				}
				if !sawMeta {
					_ = json.Unmarshal(bytes.TrimSpace(sc.Bytes()), &m)
					sawMeta = true
					continue
				}
				turns++
			}
			f.Close()
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

// newScanner returns a scanner with a generous line limit (turns can be long).
func newScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return sc
}

func writeJSONLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}
