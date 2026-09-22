package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingFileRotatesPastTheCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "daemon.log")
	f, err := openRotatingFile(path, 40)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Four 16-byte lines under a 40-byte cap: the third write rotates, so the
	// current file holds lines 3-4 and the previous file lines 1-2. Only one
	// previous file is kept, by design.
	line := strings.Repeat("x", 15) + "\n"
	for i := 0; i < 4; i++ {
		if _, err := f.Write([]byte(line)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	prev, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("no rotated file: %v", err)
	}
	if len(cur) != 2*len(line) || len(prev) != 2*len(line) {
		t.Fatalf("rotation split = current %d + previous %d, want %d each", len(cur), len(prev), 2*len(line))
	}
	if len(cur) > 40 {
		t.Fatalf("current log %d bytes exceeds the cap", len(cur))
	}
}

func TestDaemonLogWriterFallsBackToStderr(t *testing.T) {
	if w, closer := daemonLogWriter(""); w != os.Stderr || closer != nil {
		t.Fatal("empty data dir must log to stderr only")
	}
	dir := t.TempDir()
	w, closer := daemonLogWriter(dir)
	if closer == nil {
		t.Fatal("data dir given: expected a log file")
	}
	defer closer.Close()
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "logs", "daemon.log"))
	if err != nil || string(got) != "hello\n" {
		t.Fatalf("log file = %q, %v", got, err)
	}
}
