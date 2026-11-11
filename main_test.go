package main

import (
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"
)

// The tray menu's Stop Server and Start Server.
func TestBridgeStopAndStart(t *testing.T) {
	a, music := testApp(t)
	for i := range 30 {
		writeWAV(t, filepath.Join(music, fmt.Sprintf("s%02d.wav", i)), tags("TITLE", fmt.Sprint(i)))
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	b := &bridge{app: a, port: port}
	up := func() bool {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/state", port))
		if err == nil {
			resp.Body.Close()
		}
		return err == nil
	}

	if err := b.start(); err != nil || !b.running() || !up() {
		t.Fatalf("start: %v", err)
	}
	b.stop() // also cancels the scan that start began
	if b.running() || up() || b.status() != "Stopped" {
		t.Fatalf("still reachable after stop: %s", b.status())
	}
	if s := a.scanner.Status(); s.State == "scanning" {
		t.Fatalf("scan still shown as running after stop: %+v", s)
	}
	if err := b.start(); err != nil || !up() {
		t.Fatalf("start again: %v", err)
	}
	a.scanner.Wait()
	if s := a.scanner.Status(); s.State != "done" || s.Songs != 30 {
		t.Fatalf("scan after restart: %+v", s)
	}

	// A second copy cannot take the port: the tray shows why.
	other := &bridge{app: a, port: port}
	if err := other.start(); err == nil || other.status() != fmt.Sprintf("Error: port %d is used by another program", port) {
		t.Fatalf("second copy: %v, %s", err, other.status())
	}
	b.stop()
}
