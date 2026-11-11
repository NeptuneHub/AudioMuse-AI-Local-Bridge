package main

import (
	"log"
	"os"
	"path/filepath"
	"sync"
)

// The log goes to the terminal, when there is one, and to bridge.log in the data folder, which
// the tray menu opens. At 10 MB the file is renamed to bridge.log.old and a new one is started.

const maxLogSize = 10 << 20

type logWriter struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func setupLog(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	w := &logWriter{path: path}
	log.SetOutput(w)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.open()
}

func (w *logWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, info.Size()
	return nil
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = os.Stderr.Write(p) // fails silently when there is no terminal (the macOS and Windows apps)
	if w.f != nil && w.size+int64(len(p)) > maxLogSize {
		_ = w.f.Close()
		w.f = nil
		_ = os.Rename(w.path, w.path+".old")
		_ = w.open()
	}
	if w.f != nil {
		n, _ := w.f.Write(p)
		w.size += int64(n)
	}
	return len(p), nil
}
