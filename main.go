// AudioMuse-AI Local Bridge: a minimal music server that gives AudioMuse-AI access to a local
// music folder through the OpenSubsonic API calls it uses for Navidrome.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
)

const (
	appName     = "AudioMuse-AI-Local-Bridge"
	apiVersion  = "1.16.1"
	defaultPort = 4520 // IANA: 4504-4533 unassigned; far from AudioMuse-AI (8000) and Navidrome (4533)
)

var version = "dev" // set at build time with -ldflags "-X main.version=v1"

func defaultDataDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, appName)
	}
	return appName + "-data"
}

// start runs a command without waiting for it, and collects it when it ends.
func start(cmd *exec.Cmd) {
	if err := cmd.Start(); err == nil {
		go func() { _ = cmd.Wait() }()
	}
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		start(exec.Command("open", url))
	case "windows":
		start(exec.Command("rundll32", "url.dll,FileProtocolHandler", url))
	default:
		start(exec.Command("xdg-open", url))
	}
}

// openLog shows the log file: in Console on macOS, in Notepad on Windows.
func openLog(path string) {
	switch runtime.GOOS {
	case "darwin":
		start(exec.Command("open", "-a", "Console", path))
	case "windows":
		start(exec.Command("notepad.exe", path))
	default:
		start(exec.Command("xdg-open", path))
	}
}

// hasDesktop reports whether a tray icon can be shown. On Linux it needs a desktop session,
// so on a server (for example over SSH) the bridge runs without it.
func hasDesktop() bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return true
	}
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

// alreadyRunning reports whether the bridge itself is the program using the port.
func alreadyRunning(port int) bool {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/api/state", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var s struct {
		App string `json:"app"`
	}
	return json.NewDecoder(resp.Body).Decode(&s) == nil && s.App == appName
}

func exitWithMessage(msg string) {
	log.Print(msg)
	fmt.Print("Press Enter to close.")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(1)
}

// bridge runs the web page and the API. The tray menu can stop and start it while the program
// keeps running.
type bridge struct {
	app  *App // nil when the database could not be opened
	port int
	mu   sync.Mutex
	srv  *http.Server
	err  string // why the bridge cannot run, shown in the tray menu
}

func (b *bridge) start() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.srv != nil {
		return nil
	}
	if b.app == nil {
		return errors.New(b.err)
	}
	// IPv4 on all interfaces: reachable from Docker and the local network. Plain ":port" can bind
	// only the IPv6 side on macOS when another program holds the IPv4 port, hiding the conflict.
	ln, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", b.port))
	if err != nil {
		b.err = fmt.Sprintf("port %d is used by another program", b.port)
		log.Printf("cannot start: %v", err)
		return err
	}
	b.err = ""
	srv := &http.Server{Handler: b.app.handler(), ReadHeaderTimeout: 10 * time.Second}
	b.srv = srv
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server error: %v", err)
		}
	}()
	log.Printf("server started: open http://localhost:%d in your browser", b.port)
	if dir := b.app.cfg().MusicDir; dir != "" {
		b.app.scanner.Start(dir)
	}
	return nil
}

// stop closes the web page and the API, and cancels a running scan. Downloads in progress get
// a few seconds to finish.
func (b *bridge) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.srv.Shutdown(ctx); err != nil {
		_ = b.srv.Close()
	}
	b.srv = nil
	b.app.scanner.Stop()
	log.Print("server stopped")
}

func (b *bridge) running() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.srv != nil
}

func (b *bridge) status() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.srv != nil:
		return fmt.Sprintf("Running on port %d", b.port)
	case b.err != "":
		return "Error: " + b.err
	default:
		return "Stopped"
	}
}

// checkLoop looks for new, changed or removed music files at the chosen interval.
func (b *bridge) checkLoop() {
	if b.app == nil || b.app.scanEvery <= 0 {
		return
	}
	for range time.Tick(b.app.scanEvery) {
		if dir := b.app.cfg().MusicDir; dir != "" && b.running() {
			b.app.scanner.Check(dir)
		}
	}
}

func main() {
	port := flag.Int("port", defaultPort, "port for the web page and the API")
	dataDir := flag.String("data", defaultDataDir(), "folder for the database, the key and the log")
	noBrowser := flag.Bool("no-browser", false, "do not open the browser at start")
	noTray := flag.Bool("no-tray", false, "run in the terminal, without the icon in the menu bar or system tray")
	scanEvery := flag.Duration("scan-every", 5*time.Minute, "how often to look for new, changed or removed music files (0 turns it off)")
	flag.Parse()
	pageURL := fmt.Sprintf("http://localhost:%d", *port)
	useTray := trayAvailable && !*noTray && hasDesktop()

	logPath := filepath.Join(*dataDir, "bridge.log")
	if err := setupLog(logPath); err != nil {
		log.Printf("cannot write the log file: %v", err)
	}
	log.Printf("AudioMuse-AI Local Bridge %s, data folder: %s", version, *dataDir)

	// Starting the program again only opens the page of the copy already running.
	if alreadyRunning(*port) {
		log.Printf("already running at %s", pageURL)
		if !*noBrowser {
			openBrowser(pageURL)
		}
		return
	}

	b := &bridge{port: *port}
	st, err := openStore(*dataDir)
	if err != nil {
		b.err = "cannot open the database, see the log"
		log.Printf("cannot open the database in %s: %v", *dataDir, err)
		if !useTray {
			exitWithMessage("Cannot open the database in " + *dataDir + ": " + err.Error())
		}
	} else {
		defer st.Close()
		b.app = newApp(st, *port)
		b.app.scanEvery = max(*scanEvery, 0)
	}
	if err := b.start(); err == nil {
		if !*noBrowser {
			openBrowser(pageURL)
		}
	} else if !useTray {
		exitWithMessage(fmt.Sprintf("Port %d is used by another program (%v).\nClose that program, or start the bridge with another port, for example: -port %d", *port, err, *port+1))
	}
	go b.checkLoop()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	if useTray {
		runTray(b, pageURL, logPath, sig) // returns after Quit
		return
	}
	fmt.Printf("Open %s in your browser. Keep this window open while you use AudioMuse-AI.\n", pageURL)
	<-sig
	b.stop()
}
