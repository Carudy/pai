//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package session

import (
	"fmt"
	"os"
)

func lockFile(*os.File) error {
	// Fail closed rather than silently permitting unsafe concurrent stores.
	return fmt.Errorf("exclusive session store locking is unsupported on this platform")
}
