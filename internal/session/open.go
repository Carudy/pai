package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/Carudy/pai/internal/paths"
)

// ErrLocked means another Store owns the data directory.
var ErrLocked = errors.New("session store is already open")

// Open exclusively owns the data directory until Close. It fails immediately
// with ErrLocked if another store is open, even when only reading sessions.
func Open() (Store, error) {
	dir := paths.DataDir()
	if dir == "" {
		return nil, fmt.Errorf("cannot determine the data directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "sessions.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open session store lock: %w", err)
	}
	if err := lockFile(lock); err != nil {
		lock.Close()
		return nil, fmt.Errorf("lock session store %q: %w", dir, err)
	}
	store, err := openBackend(dir)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open session store: %w", err), lock.Close())
	}
	service, _ := store.(ServiceStore)
	groups, _ := store.(GroupStore)
	tasks, _ := store.(TaskStore)
	activity, _ := store.(ActivityStore)
	return &ownedStore{Store: store, ServiceStore: service, GroupStore: groups, TaskStore: tasks,
		ActivityStore: activity, lock: lock}, nil
}

type ownedStore struct {
	Store
	// ServiceStore is the same backend's resident-session methods. Embedding the
	// interface keeps a type assertion on the returned Store working through the
	// ownership wrapper.
	ServiceStore
	// GroupStore is the group directory behind the same wrapper, for the same
	// reason: `pai group` type-asserts the opened Store.
	GroupStore
	// TaskStore is the group board, same reason again.
	TaskStore
	// ActivityStore is a group's timeline behind the same wrapper.
	ActivityStore
	lock *os.File
	once sync.Once
	err  error
}

var _ ServiceStore = (*ownedStore)(nil)
var _ GroupStore = (*ownedStore)(nil)
var _ TaskStore = (*ownedStore)(nil)
var _ ActivityStore = (*ownedStore)(nil)

func (s *ownedStore) Close() error {
	s.once.Do(func() {
		// Keep ownership until the backend has finished closing. Closing the
		// handle releases the OS lock without unlinking its shared inode.
		s.err = errors.Join(s.Store.Close(), s.lock.Close())
	})
	return s.err
}
