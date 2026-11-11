// AudioMuse-AI Local Bridge: a minimal music server that gives AudioMuse-AI access to a local
// music folder through the OpenSubsonic API calls it uses for Navidrome.
package main

import (
	"bufio"
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
	"syscall"
	"time"
)

const (
	appName     = "AudioMuse-AI-Local-Bridge"
	apiVersion  = "1.16.1"
	defaultPort = 4520 // IANA: 4504-4533 unassigned; far from AudioMuse-AI (8000) and Navidrome (4533)
)

var version = "dev" // set at build time with -ldflags "-X main.version=1.0.0"

func defaultDataDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, appName)
	}
	return appName + "-data"
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
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
	fmt.Println(msg)
	fmt.Print("Press Enter to close.")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(1)
}

func main() {
	port := flag.Int("port", defaultPort, "port for the web page and the API")
	dataDir := flag.String("data", defaultDataDir(), "folder where settings are saved")
	noBrowser := flag.Bool("no-browser", false, "do not open the browser at start")
	flag.Parse()
	pageURL := fmt.Sprintf("http://localhost:%d", *port)

	// IPv4 on all interfaces: reachable from Docker and the local network. Plain ":port" can bind
	// only the IPv6 side on macOS when another program holds the IPv4 port, hiding the conflict.
	ln, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", *port))
	if err != nil {
		if alreadyRunning(*port) {
			fmt.Println("AudioMuse-AI Local Bridge is already running at " + pageURL)
			if !*noBrowser {
				openBrowser(pageURL)
			}
			return
		}
		exitWithMessage(fmt.Sprintf("Port %d is used by another program (%v).\nClose that program, or start the bridge with another port, for example: -port %d", *port, err, *port+1))
	}
	st, err := openStore(*dataDir)
	if err != nil {
		exitWithMessage("Cannot open the settings database in " + *dataDir + ": " + err.Error())
	}
	app := newApp(st, *port)
	srv := &http.Server{Handler: app.handler(), ReadHeaderTimeout: 10 * time.Second}

	fmt.Printf("AudioMuse-AI Local Bridge %s\nOpen %s in your browser. Keep this window open while you use AudioMuse-AI.\n", version, pageURL)
	if dir := app.cfg().MusicDir; dir != "" {
		app.scanner.Start(dir)
	}
	if !*noBrowser {
		go openBrowser(pageURL)
	}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		_ = srv.Close()
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	_ = st.Close()
}
