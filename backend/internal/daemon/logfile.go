package daemon

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const (
	// daemonLogDir and daemonLogName place the daemon's log under the data
	// directory, next to the database, so a wedged session can be explained
	// after the fact: the desktop app captures stderr only for its own console.
	daemonLogDir  = "logs"
	daemonLogName = "daemon.log"
	// daemonLogMaxBytes caps the current log; the previous one is kept as
	// daemon.log.1, so at most twice this much disk is ever used.
	daemonLogMaxBytes = 32 << 20
)

// rotatingFile is an io.Writer over a log file that renames itself to
// <name>.1 and starts afresh once it grows past max bytes. Writes are
// serialized; a failure to rotate keeps writing to the current file.
type rotatingFile struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func openRotatingFile(path string, maxBytes int64) (*rotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // daemon-owned path under the data dir
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat log: %w", err)
	}
	return &rotatingFile{path: path, max: maxBytes, f: f, size: info.Size()}, nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(p)) > r.max && r.size > 0 {
		r.rotateLocked()
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) rotateLocked() {
	_ = r.f.Close()
	_ = os.Rename(r.path, r.path+".1")
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // same path as opened above
	if err != nil {
		// Keep the old handle's path usable: reopen in append mode failed, so
		// fall back to the renamed file rather than losing lines.
		f, err = os.OpenFile(r.path+".1", os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // fallback to the file just renamed
		if err != nil {
			r.f, _ = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
			r.size = 0
			return
		}
		r.f = f
		return
	}
	r.f = f
	r.size = 0
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

// daemonLogWriter returns stderr plus, when the data dir is usable, the
// rotating daemon.log under it. The returned closer is nil when only stderr
// is in use.
func daemonLogWriter(dataDir string) (io.Writer, io.Closer) {
	if dataDir == "" {
		return os.Stderr, nil
	}
	file, err := openRotatingFile(filepath.Join(dataDir, daemonLogDir, daemonLogName), daemonLogMaxBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: log file unavailable, logging to stderr only: %v\n", err)
		return os.Stderr, nil
	}
	return io.MultiWriter(os.Stderr, file), file
}
