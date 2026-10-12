package role

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxEditableFileBytes = 16 << 20

// These checks protect approval-time snapshots and serialize PAI's own commits.
// They are best-effort against external writers: portable filesystems provide no
// compare-and-swap rename, and a hostile parent-directory race is not sandboxed.
// In particular, an external change between the last check and publish remains
// possible. No gate is held while a caller is presenting an approval prompt.
var fileMutationGate = make(chan struct{}, 1)

type fileParent struct {
	requested string // Absolute original path, independent of subsequent cwd changes.
	path      string
	info      os.FileInfo
}

type fileSnapshot struct {
	Data []byte
	Path string

	parent   fileParent
	info     os.FileInfo
	original []byte // Separate from Data so callers cannot alter the approval baseline.
}

type createTarget struct {
	Path   string
	parent fileParent
}

func fileSafetyResult(ctx context.Context, err error) error {
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	return err
}

func captureFileParent(ctx context.Context, path string) (fileParent, string, error) {
	if err := ctx.Err(); err != nil {
		return fileParent{}, "", err
	}
	if path == "" {
		return fileParent{}, "", fmt.Errorf("file path is empty")
	}
	requested, err := filepath.Abs(path)
	if err = fileSafetyResult(ctx, err); err != nil {
		return fileParent{}, "", fmt.Errorf("resolve file path: %w", err)
	}
	parentPath, err := filepath.EvalSymlinks(filepath.Dir(requested))
	if err = fileSafetyResult(ctx, err); err != nil {
		return fileParent{}, "", fmt.Errorf("resolve parent directory: %w", err)
	}
	info, err := os.Stat(parentPath)
	if err = fileSafetyResult(ctx, err); err != nil {
		return fileParent{}, "", fmt.Errorf("stat parent directory: %w", err)
	}
	if !info.IsDir() {
		return fileParent{}, "", fmt.Errorf("parent is not a directory: %s", parentPath)
	}
	parent := fileParent{requested: requested, path: parentPath, info: info}
	return parent, filepath.Join(parentPath, filepath.Base(requested)), nil
}

func (p fileParent) verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Dir(p.requested))
	if err = fileSafetyResult(ctx, err); err != nil {
		return fmt.Errorf("resolve parent directory again: %w", err)
	}
	if resolved != p.path {
		return fmt.Errorf("parent directory changed: %s", p.requested)
	}
	info, err := os.Stat(p.path)
	if err = fileSafetyResult(ctx, err); err != nil {
		return fmt.Errorf("stat parent directory again: %w", err)
	}
	if !info.IsDir() || !os.SameFile(p.info, info) {
		return fmt.Errorf("parent directory identity changed: %s", p.requested)
	}
	return nil
}

// readSnapshotFile checks the name and the opened handle, then checks the name
// again. The bounded reader also catches files that grow after the first stat.
func readSnapshotFile(ctx context.Context, path string) ([]byte, os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	info, err := os.Lstat(path)
	if err = fileSafetyResult(ctx, err); err != nil {
		return nil, nil, fmt.Errorf("inspect file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("file must be regular and not a symlink: %s", path)
	}
	if info.Size() > maxEditableFileBytes {
		return nil, nil, fmt.Errorf("file exceeds %d bytes: %s", maxEditableFileBytes, path)
	}
	f, err := os.Open(path)
	if err = fileSafetyResult(ctx, err); err != nil {
		if f != nil {
			f.Close()
		}
		return nil, nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err = fileSafetyResult(ctx, err); err != nil {
		return nil, nil, fmt.Errorf("stat open file: %w", err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) || info.Mode() != opened.Mode() {
		return nil, nil, fmt.Errorf("file changed while opening: %s", path)
	}
	data, err := io.ReadAll(io.LimitReader(fileContextReader{ctx: ctx, r: f}, maxEditableFileBytes+1))
	if err = fileSafetyResult(ctx, err); err != nil {
		return nil, nil, fmt.Errorf("read file: %w", err)
	}
	if len(data) > maxEditableFileBytes {
		return nil, nil, fmt.Errorf("file exceeds %d bytes: %s", maxEditableFileBytes, path)
	}
	after, err := f.Stat()
	if err = fileSafetyResult(ctx, err); err != nil {
		return nil, nil, fmt.Errorf("stat read file: %w", err)
	}
	latest, err := os.Lstat(path)
	if err = fileSafetyResult(ctx, err); err != nil {
		return nil, nil, fmt.Errorf("inspect read file: %w", err)
	}
	if !os.SameFile(info, latest) || info.Mode() != latest.Mode() ||
		info.Mode() != after.Mode() || info.Size() != after.Size() ||
		int64(len(data)) != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return nil, nil, fmt.Errorf("file changed while reading: %s", path)
	}
	return data, info, nil
}

type fileContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r fileContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	return n, fileSafetyResult(r.ctx, err)
}

func captureFileSnapshot(ctx context.Context, path string) (*fileSnapshot, error) {
	parent, actual, err := captureFileParent(ctx, path)
	if err != nil {
		return nil, err
	}
	data, info, err := readSnapshotFile(ctx, actual)
	if err != nil {
		return nil, err
	}
	if err := parent.verify(ctx); err != nil {
		return nil, err
	}
	return &fileSnapshot{Data: data, Path: actual, parent: parent, info: info, original: bytes.Clone(data)}, nil
}

func captureCreateTarget(ctx context.Context, path string) (*createTarget, error) {
	parent, actual, err := captureFileParent(ctx, path)
	if err != nil {
		return nil, err
	}
	target := &createTarget{Path: actual, parent: parent}
	if err := target.verify(ctx); err != nil {
		return nil, err
	}
	return target, nil
}

func (s *fileSnapshot) verify(ctx context.Context) error {
	if err := s.parent.verify(ctx); err != nil {
		return err
	}
	data, info, err := readSnapshotFile(ctx, s.Path)
	if err != nil {
		return err
	}
	if !os.SameFile(s.info, info) || s.info.Mode() != info.Mode() || !bytes.Equal(s.original, data) {
		return fmt.Errorf("file changed since capture: %s", s.Path)
	}
	return s.parent.verify(ctx)
}

func (t *createTarget) verify(ctx context.Context) error {
	if err := t.parent.verify(ctx); err != nil {
		return err
	}
	_, err := os.Lstat(t.Path)
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	if err == nil {
		return fmt.Errorf("file already exists: %s: %w", t.Path, os.ErrExist)
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("inspect create target: %w", err)
	}
	return t.parent.verify(ctx)
}

func acquireFileMutation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case fileMutationGate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-fileMutationGate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// stageFile returns a closed, synced file in the destination's directory. The
// caller owns cleanup on success; every failure here removes the partial stage.
func stageFile(ctx context.Context, path string, data []byte, mode os.FileMode) (name string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pai-file-*")
	if f != nil {
		name = f.Name()
		defer func() {
			f.Close()
			if err != nil {
				os.Remove(name)
			}
		}()
	}
	if err = fileSafetyResult(ctx, err); err != nil {
		return name, fmt.Errorf("create staging file: %w", err)
	}
	for len(data) > 0 {
		if err = ctx.Err(); err != nil {
			return name, err
		}
		n := min(len(data), 64<<10)
		written, writeErr := f.Write(data[:n])
		err = fileSafetyResult(ctx, writeErr)
		if err != nil {
			return name, fmt.Errorf("write staging file: %w", err)
		}
		if written != n {
			return name, io.ErrShortWrite
		}
		data = data[n:]
	}
	if err = fileSafetyResult(ctx, f.Chmod(mode)); err != nil {
		return name, fmt.Errorf("set staging permissions: %w", err)
	}
	if err = fileSafetyResult(ctx, f.Sync()); err != nil {
		return name, fmt.Errorf("sync staging file: %w", err)
	}
	if err = fileSafetyResult(ctx, f.Close()); err != nil {
		return name, fmt.Errorf("close staging file: %w", err)
	}
	return name, nil
}

func (s *fileSnapshot) commit(ctx context.Context, data []byte) error {
	return commitSafeFile(ctx, s.Path, data, s.info.Mode().Perm(), s.verify, os.Rename)
}

func (t *createTarget) commit(ctx context.Context, data []byte) error {
	// A hard link is an atomic no-clobber publish, even if an external writer
	// creates a file or dangling symlink after verification. Never fall back to rename.
	return commitSafeFile(ctx, t.Path, data, 0o644, t.verify, os.Link)
}

func commitSafeFile(ctx context.Context, path string, data []byte, mode os.FileMode, verify func(context.Context) error, publish func(string, string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := acquireFileMutation(ctx); err != nil {
		return err
	}
	defer func() { <-fileMutationGate }()
	if err := verify(ctx); err != nil {
		return err
	}
	name, err := stageFile(ctx, path, data, mode)
	if err != nil {
		return err
	}
	defer os.Remove(name)
	if err := verify(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := publish(name, path); err != nil {
		return fmt.Errorf("publish file: %w", fileSafetyResult(ctx, err))
	}
	// Once published, cancellation must not turn a successful mutation into a
	// reported failure (which could encourage a caller to retry it).
	return nil
}
