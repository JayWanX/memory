package daemon

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

type requestTracker struct {
	mu       sync.Mutex
	active   int
	stopping bool
	zero     chan struct{}
}

func (tracker *requestTracker) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !tracker.begin() {
			writeControlError(writer, newControlError(codeMemorydUnavailable,
				"local Agency authority is unavailable"))
			return
		}
		defer tracker.end()
		next.ServeHTTP(writer, request)
	})
}

func (tracker *requestTracker) begin() bool {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.stopping {
		return false
	}
	if tracker.active == 0 {
		tracker.zero = make(chan struct{})
	}
	tracker.active++
	return true
}

func (tracker *requestTracker) end() {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.active <= 0 {
		panic("daemon request tracker underflow")
	}
	tracker.active--
	if tracker.active == 0 {
		close(tracker.zero)
	}
}

func (tracker *requestTracker) stop() <-chan struct{} {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.stopping = true
	if tracker.active == 0 {
		done := make(chan struct{})
		close(done)
		return done
	}
	return tracker.zero
}

func requireOwnerDirectory(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("path must be absolute and canonical")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != ownerDirectoryMode {
		return errors.New("path must be an owner-only real directory")
	}
	owner, err := fileOwnerUID(info)
	if err != nil || owner != uint32(os.Geteuid()) {
		return errors.New("path is not owned by the daemon user")
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil || realPath != path {
		return errors.New("path has a symlinked ancestor")
	}
	return nil
}
