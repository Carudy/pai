package role

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func safetyWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func safetyContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("read %s = %q, %v; want %q", path, data, err, want)
	}
}

func safetySymlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
}

func safetyNoTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".pai-file-") {
			t.Errorf("staging file leaked: %s", entry.Name())
		}
	}
}

func TestFileSnapshotCommitPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	safetyWrite(t, path, "before")
	if err := os.Chmod(path, 0o751); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := captureFileSnapshot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if string(snapshot.Data) != "before" || !filepath.IsAbs(snapshot.Path) {
		t.Fatalf("snapshot = %q, %q", snapshot.Data, snapshot.Path)
	}
	if err := snapshot.commit(context.Background(), []byte("after")); err != nil {
		t.Fatal(err)
	}
	safetyContent(t, path, "after")
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("permissions = %v, want %v", after.Mode().Perm(), before.Mode().Perm())
	}
	safetyNoTemps(t, filepath.Dir(path))
}

func TestFileSnapshotRejectsChanges(t *testing.T) {
	for _, change := range []string{"same-size bytes", "longer bytes", "inode", "mode", "symlink", "removed"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file")
			safetyWrite(t, path, "before")
			snapshot, err := captureFileSnapshot(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			want := "before"
			switch change {
			case "same-size bytes":
				want = "BEFORE"
				safetyWrite(t, path, want)
				// Content comparison must work even when timestamps are restored.
				if err := os.Chtimes(path, snapshot.info.ModTime(), snapshot.info.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "longer bytes":
				want = "external writer"
				safetyWrite(t, path, want)
			case "inode":
				// Keep the old inode alive so it cannot be reused for the replacement.
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				safetyWrite(t, path, want)
			case "mode":
				if runtime.GOOS == "windows" {
					t.Skip("Unix permissions unavailable")
				}
				if err := os.Chmod(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				safetyWrite(t, path+".other", want)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				safetySymlink(t, path+".other", path)
			case "removed":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if err := snapshot.commit(context.Background(), []byte("PAI")); err == nil {
				t.Fatal("commit accepted changed file")
			}
			if change != "removed" {
				safetyContent(t, path, want)
			} else if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("removed file recreated: %v", err)
			}
			safetyNoTemps(t, filepath.Dir(path))
		})
	}
}

func TestFileSnapshotCaptureRestrictions(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular")
	safetyWrite(t, regular, "data")
	for _, dangling := range []bool{false, true} {
		name := filepath.Join(dir, "link")
		if dangling {
			name += "-dangling"
		}
		target := regular
		if dangling {
			target += "-missing"
		}
		safetySymlink(t, target, name)
		if _, err := captureFileSnapshot(context.Background(), name); err == nil {
			t.Fatalf("accepted symlink %s", name)
		}
	}
	if _, err := captureFileSnapshot(context.Background(), dir); err == nil {
		t.Fatal("accepted directory")
	}
	if _, err := captureFileSnapshot(context.Background(), os.DevNull); err == nil {
		t.Fatal("accepted device")
	}
	large := filepath.Join(dir, "large")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxEditableFileBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := captureFileSnapshot(context.Background(), large); err == nil {
		t.Fatal("accepted oversized file")
	}
	if err := os.Truncate(large, maxEditableFileBytes); err != nil {
		t.Fatal(err)
	}
	snapshot, err := captureFileSnapshot(context.Background(), large)
	if err != nil || len(snapshot.Data) != maxEditableFileBytes {
		t.Fatalf("exact limit snapshot: %v", err)
	}
}

func TestFileSnapshotFreshCaptureAndImmutableBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	safetyWrite(t, path, "first")
	first, err := captureFileSnapshot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	copy(first.Data, "other")
	safetyWrite(t, path, "other")
	if err := first.commit(context.Background(), []byte("bad")); err == nil {
		t.Fatal("caller changed the validation baseline")
	}
	second, err := captureFileSnapshot(context.Background(), path)
	if err != nil || string(second.Data) != "other" {
		t.Fatalf("fresh capture = %+v, %v", second, err)
	}
	if err := second.commit(context.Background(), []byte("fresh")); err != nil {
		t.Fatal(err)
	}
	safetyContent(t, path, "fresh")
}

func TestFileSnapshotConcurrentCommits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	safetyWrite(t, path, "before")
	first, err := captureFileSnapshot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := captureFileSnapshot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, snapshot := range []*fileSnapshot{first, second} {
		go func(s *fileSnapshot, data string) {
			<-start
			results <- s.commit(context.Background(), []byte(data))
		}(snapshot, []string{"first", "second"}[i])
	}
	close(start)
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful commits = %d, want 1", successes)
	}
	safetyNoTemps(t, filepath.Dir(path))
}

func TestFileSnapshotAndCreateTargetParentChanges(t *testing.T) {
	for _, change := range []string{"retarget", "identity"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			parent := filepath.Join(dir, "parent")
			other := filepath.Join(dir, "other")
			for _, name := range []string{parent, other} {
				if err := os.Mkdir(name, 0o755); err != nil {
					t.Fatal(err)
				}
				safetyWrite(t, filepath.Join(name, "file"), "before")
			}
			requestedParent := parent
			if change == "retarget" {
				requestedParent = filepath.Join(dir, "link")
				safetySymlink(t, parent, requestedParent)
			}
			snapshot, err := captureFileSnapshot(context.Background(), filepath.Join(requestedParent, "file"))
			if err != nil {
				t.Fatal(err)
			}
			target, err := captureCreateTarget(context.Background(), filepath.Join(requestedParent, "new"))
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := filepath.EvalSymlinks(parent)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Path != filepath.Join(canonical, "file") || target.Path != filepath.Join(canonical, "new") {
				t.Fatal("paths are not canonical")
			}
			if change == "retarget" {
				if err := os.Remove(requestedParent); err != nil {
					t.Fatal(err)
				}
				safetySymlink(t, other, requestedParent)
			} else {
				if err := os.Rename(parent, parent+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(other, parent); err != nil {
					t.Fatal(err)
				}
			}
			if err := snapshot.commit(context.Background(), []byte("bad")); err == nil {
				t.Fatal("edit accepted changed parent")
			}
			if err := target.commit(context.Background(), []byte("bad")); err == nil {
				t.Fatal("create accepted changed parent")
			}
			safetyContent(t, filepath.Join(parent, "file"), "before")
			if _, err := os.Lstat(filepath.Join(parent, "new")); !os.IsNotExist(err) {
				t.Fatalf("created file in changed parent: %v", err)
			}
		})
	}
}

func TestCreateTargetCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new")
	target, err := captureCreateTarget(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.commit(context.Background(), []byte("created")); err != nil {
		t.Fatal(err)
	}
	safetyContent(t, path, "created")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 {
		t.Fatalf("create permissions = %v", info.Mode().Perm())
	}
	if err := target.commit(context.Background(), []byte("overwrite")); err == nil {
		t.Fatal("repeated create succeeded")
	}
	safetyContent(t, path, "created")
	safetyNoTemps(t, filepath.Dir(path))
}

func TestCreateTargetRejectsExistingAndRacedPaths(t *testing.T) {
	for _, kind := range []string{"file", "directory", "symlink", "dangling"} {
		for _, raced := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "-existing", true: "-raced"}[raced], func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "new")
				var target *createTarget
				var err error
				if raced {
					target, err = captureCreateTarget(context.Background(), path)
					if err != nil {
						t.Fatal(err)
					}
				}
				switch kind {
				case "file":
					safetyWrite(t, path, "external")
				case "directory":
					if err := os.Mkdir(path, 0o755); err != nil {
						t.Fatal(err)
					}
				case "symlink", "dangling":
					other := filepath.Join(dir, "other")
					if kind == "symlink" {
						safetyWrite(t, other, "external")
					}
					safetySymlink(t, other, path)
				}
				if raced {
					err = target.commit(context.Background(), []byte("bad"))
				} else {
					_, err = captureCreateTarget(context.Background(), path)
				}
				if !errors.Is(err, os.ErrExist) {
					t.Fatalf("want exists error, got %v", err)
				}
				if kind == "file" || kind == "symlink" {
					safetyContent(t, path, "external")
				}
				if kind == "dangling" {
					info, err := os.Lstat(path)
					if err != nil || info.Mode()&os.ModeSymlink == 0 {
						t.Fatalf("dangling symlink replaced: %v", err)
					}
				}
				safetyNoTemps(t, dir)
			})
		}
	}
}

func TestCreateTargetNoClobberAtPublish(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "dangling"}[symlink], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "new")
			target, err := captureCreateTarget(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			// Inject an external creation after the final validation, exercising
			// the actual no-clobber publication rather than only preflight checks.
			publish := func(stage, destination string) error {
				if symlink {
					safetySymlink(t, destination+".missing", destination)
				} else {
					safetyWrite(t, destination, "external")
				}
				return os.Link(stage, destination)
			}
			err = commitSafeFile(context.Background(), path, []byte("bad"), 0o644, target.verify, publish)
			if !errors.Is(err, os.ErrExist) {
				t.Fatalf("publish = %v, want exists error", err)
			}
			if !symlink {
				safetyContent(t, path, "external")
			} else if _, err := os.Readlink(path); err != nil {
				t.Fatalf("symlink replaced: %v", err)
			}
			safetyNoTemps(t, filepath.Dir(path))
		})
	}
}

func TestCreateTargetConcurrentCommits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new")
	first, err := captureCreateTarget(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := captureCreateTarget(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, target := range []*createTarget{first, second} {
		go func(target *createTarget) {
			<-start
			results <- target.commit(context.Background(), []byte("created"))
		}(target)
	}
	close(start)
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		} else if !errors.Is(err, os.ErrExist) {
			t.Fatalf("unexpected create failure: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful creates = %d, want 1", successes)
	}
	safetyContent(t, path, "created")
	safetyNoTemps(t, filepath.Dir(path))
}

func TestFileSnapshotAndCreateTargetStableSymlinkParent(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "parent")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	safetySymlink(t, parent, link)
	safetyWrite(t, filepath.Join(parent, "file"), "before")
	snapshot, err := captureFileSnapshot(context.Background(), filepath.Join(link, "file"))
	if err != nil {
		t.Fatal(err)
	}
	target, err := captureCreateTarget(context.Background(), filepath.Join(link, "new"))
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.commit(context.Background(), []byte("after")); err != nil {
		t.Fatal(err)
	}
	if err := target.commit(context.Background(), []byte("created")); err != nil {
		t.Fatal(err)
	}
	safetyContent(t, filepath.Join(parent, "file"), "after")
	safetyContent(t, filepath.Join(parent, "new"), "created")
	safetyNoTemps(t, parent)
}

func TestCreateTargetMissingParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "new")
	if _, err := captureCreateTarget(context.Background(), path); err == nil {
		t.Fatal("accepted missing parent")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("parent created: %v", err)
	}
}

func TestFileSnapshotAndCreateTargetCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	safetyWrite(t, path, "before")
	snapshot, err := captureFileSnapshot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	target, err := captureCreateTarget(context.Background(), path+".new")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checks := []func() error{
		func() error { _, err := captureFileSnapshot(ctx, path); return err },
		func() error { _, err := captureCreateTarget(ctx, target.Path); return err },
		func() error { return snapshot.commit(ctx, []byte("bad")) },
		func() error { return target.commit(ctx, []byte("bad")) },
	}
	for i, check := range checks {
		if err := check(); !errors.Is(err, context.Canceled) {
			t.Fatalf("check %d = %v, want canceled", i, err)
		}
	}
	safetyContent(t, path, "before")
	safetyNoTemps(t, filepath.Dir(path))

	// Captures do not take the gate, and waiting for the commit gate is cancellable.
	fileMutationGate <- struct{}{}
	func() {
		defer func() { <-fileMutationGate }()
		if _, err := captureFileSnapshot(context.Background(), path); err != nil {
			t.Fatal(err)
		}
		if _, err := captureCreateTarget(context.Background(), target.Path); err != nil {
			t.Fatal(err)
		}
		waiting, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer stop()
		if err := snapshot.commit(waiting, []byte("bad")); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("waiting commit = %v", err)
		}
	}()
	if err := snapshot.commit(context.Background(), []byte("after")); err != nil {
		t.Fatalf("gate leaked after cancellation: %v", err)
	}
}

func TestFileSnapshotStageCleanupAndLatestValidation(t *testing.T) {
	for _, failure := range []string{"stale", "cancel", "publish"} {
		t.Run(failure, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file")
			safetyWrite(t, path, "before")
			snapshot, err := captureFileSnapshot(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			verify := func(ctx context.Context) error {
				calls++
				if calls == 2 {
					switch failure {
					case "stale":
						safetyWrite(t, path, "external")
					case "cancel":
						cancel()
					}
				}
				return snapshot.verify(ctx)
			}
			publish := func(string, string) error { return os.ErrPermission }
			err = commitSafeFile(ctx, path, []byte("bad"), 0o644, verify, publish)
			if err == nil || calls != 2 {
				t.Fatalf("commit = %v, validations = %d", err, calls)
			}
			if failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("want canceled, got %v", err)
			}
			want := "before"
			if failure == "stale" {
				want = "external"
			}
			safetyContent(t, path, want)
			safetyNoTemps(t, filepath.Dir(path))
		})
	}
}

func TestFileSnapshotSuccessfulPublishDespiteCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	safetyWrite(t, path, "before")
	snapshot, err := captureFileSnapshot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	publish := func(stage, destination string) error {
		err := os.Rename(stage, destination)
		cancel()
		return err
	}
	if err := commitSafeFile(ctx, path, []byte("after"), 0o644, snapshot.verify, publish); err != nil {
		t.Fatalf("successful mutation reported as failure: %v", err)
	}
	safetyContent(t, path, "after")
	safetyNoTemps(t, filepath.Dir(path))
}

// Cancel immediately after the stage appears, without timing-dependent sleeps.
// This exercises the staging helper's own cleanup, before it returns ownership.
type safetyStageCancelContext struct {
	context.Context
	cancel context.CancelFunc
	dir    string
}

func (ctx safetyStageCancelContext) Err() error {
	matches, _ := filepath.Glob(filepath.Join(ctx.dir, ".pai-file-*"))
	if len(matches) != 0 {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestFileSnapshotStageCancellationCleanup(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stageCtx := safetyStageCancelContext{Context: ctx, cancel: cancel, dir: dir}
	name, err := stageFile(stageCtx, filepath.Join(dir, "file"), []byte("data"), 0o644)
	if name == "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("stage = %q, %v; want created stage then cancellation", name, err)
	}
	safetyNoTemps(t, dir)
}

type safetyCancelReader struct {
	cancel context.CancelFunc
}

func (r safetyCancelReader) Read(p []byte) (int, error) {
	r.cancel()
	return copy(p, "data"), nil
}

func TestFileSnapshotBoundedContextReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := fileContextReader{ctx: ctx, r: safetyCancelReader{cancel: cancel}}
	if _, err := io.ReadAll(r); !errors.Is(err, context.Canceled) {
		t.Fatalf("reader did not detect cancellation after read: %v", err)
	}
	r = fileContextReader{ctx: context.Background(), r: bytes.NewReader(make([]byte, maxEditableFileBytes+100))}
	data, err := io.ReadAll(io.LimitReader(r, maxEditableFileBytes+1))
	if err != nil || len(data) != maxEditableFileBytes+1 {
		t.Fatalf("bounded read length = %d, error = %v", len(data), err)
	}
}
