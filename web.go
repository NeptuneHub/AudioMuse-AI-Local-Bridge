package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var indexHTML []byte

const sessionCookie = "bridge_session"

type App struct {
	store     *Store
	scanner   *Scanner
	limiter   *limiter
	routes    map[string]restHandler
	port      int
	mu        sync.RWMutex
	settings  Settings
	sessMu    sync.Mutex
	sessions  map[string]time.Time
	plMu      sync.Mutex
	scanEvery time.Duration // how often the music folder is checked for changes, 0 = never
}

func newApp(st *Store, port int) *App {
	a := &App{store: st, scanner: newScanner(st), limiter: newLimiter(), port: port,
		settings: st.loadSettings(), sessions: map[string]time.Time{}}
	a.routes = a.restRoutes()
	return a
}

func (a *App) cfg() Settings {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.settings
}

func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/", a.handleRest)
	mux.HandleFunc("GET /{$}", a.serveIndex)
	mux.HandleFunc("GET /api/state", a.apiState)
	mux.HandleFunc("GET /api/status", a.withSession(a.apiStatus))
	mux.HandleFunc("GET /api/browse", a.apiBrowse)
	mux.HandleFunc("POST /api/setup", a.post(a.apiSetup))
	mux.HandleFunc("POST /api/login", a.post(a.apiLogin))
	mux.HandleFunc("POST /api/logout", a.post(a.apiLogout))
	mux.HandleFunc("POST /api/settings", a.post(a.withSession(a.apiSettings)))
	mux.HandleFunc("POST /api/rescan", a.post(a.withSession(a.apiRescan)))
	return mux
}

func (a *App) serveIndex(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write(indexHTML)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func isLocal(r *http.Request) bool {
	ip := net.ParseIP(clientIP(r))
	return ip != nil && ip.IsLoopback()
}

// post protects POST calls against cross-site requests: JSON body and same-origin only.
func (a *App) post(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			jsonError(w, http.StatusUnsupportedMediaType, "JSON expected")
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			if u, err := url.Parse(o); err != nil || u.Host != r.Host {
				jsonError(w, http.StatusForbidden, "Cross-site request refused")
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		h(w, r)
	}
}

func (a *App) hasSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	a.sessMu.Lock()
	defer a.sessMu.Unlock()
	exp, ok := a.sessions[c.Value]
	if ok && time.Now().After(exp) {
		delete(a.sessions, c.Value)
		return false
	}
	return ok
}

func (a *App) withSession(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.hasSession(r) {
			jsonError(w, http.StatusUnauthorized, "Please log in")
			return
		}
		h(w, r)
	}
}

func (a *App) startSession(w http.ResponseWriter) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	exp := time.Now().Add(30 * 24 * time.Hour)
	a.sessMu.Lock()
	a.sessions[token] = exp
	a.sessMu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", Expires: exp,
		HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func defaultMusicDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if fi, err := os.Stat(filepath.Join(home, "Music")); err == nil && fi.IsDir() {
		return filepath.Join(home, "Music")
	}
	return home
}

// connectURLs lists the addresses AudioMuse-AI can use to reach the bridge.
func (a *App) connectURLs() []map[string]string {
	urls := []map[string]string{
		{"label": "AudioMuse-AI on this computer", "url": fmt.Sprintf("http://127.0.0.1:%d", a.port)},
	}
	if ip := lanIP(); ip != "" {
		urls = append(urls, map[string]string{"label": "AudioMuse-AI on another computer in your network",
			"url": fmt.Sprintf("http://%s:%d", ip, a.port)})
	}
	return urls
}

// lanIP returns the local network address of this computer, or "". A computer can have more
// than one (Wi-Fi and cable, VPN, virtual machines), so it takes the one the system uses for
// its main connection. Connecting a UDP socket sends nothing: it only asks the system which
// address it would use (192.0.2.1 is a reserved documentation address).
func lanIP() string {
	if c, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		ip := c.LocalAddr().(*net.UDPAddr).IP
		c.Close()
		if ip.IsPrivate() {
			return ip.String()
		}
	}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, ad := range addrs {
			if n, ok := ad.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() {
				return n.IP.String()
			}
		}
	}
	return ""
}

func (a *App) apiState(w http.ResponseWriter, r *http.Request) {
	cfg := a.cfg()
	logged := cfg.ready() && a.hasSession(r)
	out := map[string]any{"app": appName, "version": version, "setupDone": cfg.ready(), "loggedIn": logged, "local": isLocal(r)}
	if logged {
		out["musicDir"], out["username"], out["urls"] = cfg.MusicDir, cfg.Username, a.connectURLs()
		out["scanEvery"] = int(a.scanEvery.Seconds())
	} else if !cfg.ready() && isLocal(r) {
		out["musicDir"] = defaultMusicDir()
	}
	writeJSON(w, http.StatusOK, out)
}

type settingsBody struct {
	MusicDir string `json:"musicDir"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// validate checks the form and returns the cleaned settings. An empty password keeps the old one.
func validate(b settingsBody, old Settings) (Settings, string) {
	dir := strings.TrimSpace(b.MusicDir)
	if dir == "" {
		return Settings{}, "Please choose your music folder."
	}
	dir, _ = filepath.Abs(filepath.Clean(dir))
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return Settings{}, "This music folder does not exist: " + dir
	}
	user := strings.TrimSpace(b.Username)
	if user == "" || len(user) > 64 {
		return Settings{}, "Please enter a username (at most 64 characters)."
	}
	pass := b.Password
	if pass == "" {
		pass = old.Password
	}
	if len(pass) < 4 {
		return Settings{}, "The password must have at least 4 characters."
	}
	return Settings{MusicDir: dir, Username: user, Password: pass}, ""
}

func (a *App) save(s Settings) error {
	if err := a.store.saveSettings(s); err != nil {
		return err
	}
	a.mu.Lock()
	a.settings = s
	a.mu.Unlock()
	return nil
}

func (a *App) apiSetup(w http.ResponseWriter, r *http.Request) {
	if a.cfg().ready() {
		jsonError(w, http.StatusForbidden, "The setup is already done. Please log in.")
		return
	}
	if !isLocal(r) {
		jsonError(w, http.StatusForbidden, fmt.Sprintf("For your safety, the first setup works only on the computer where the bridge runs. Open http://localhost:%d there.", a.port))
		return
	}
	var b settingsBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	s, msg := validate(b, Settings{})
	if msg != "" {
		jsonError(w, http.StatusBadRequest, msg)
		return
	}
	if err := a.save(s); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.startSession(w)
	a.scanner.Start(s.MusicDir)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) apiLogin(w http.ResponseWriter, r *http.Request) {
	var b settingsBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	cfg := a.cfg()
	key := "web|" + clientIP(r) + "|" + strings.ToLower(b.Username)
	if a.limiter.blocked(key) {
		jsonError(w, http.StatusTooManyRequests, "Too many wrong attempts. Please wait one minute.")
		return
	}
	userOK := secureEq(strings.ToLower(strings.TrimSpace(b.Username)), strings.ToLower(cfg.Username))
	if !cfg.ready() || !secureEq(b.Password, cfg.Password) || !userOK {
		a.limiter.fail(key)
		jsonError(w, http.StatusUnauthorized, "Wrong username or password.")
		return
	}
	a.startSession(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) apiLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.sessMu.Lock()
		delete(a.sessions, c.Value)
		a.sessMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) apiSettings(w http.ResponseWriter, r *http.Request) {
	var b settingsBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	old := a.cfg()
	s, msg := validate(b, old)
	if msg != "" {
		jsonError(w, http.StatusBadRequest, msg)
		return
	}
	if err := a.save(s); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.MusicDir != old.MusicDir {
		a.scanner.Start(s.MusicDir)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) apiRescan(w http.ResponseWriter, r *http.Request) {
	dir := a.cfg().MusicDir
	if dir == "" {
		jsonError(w, http.StatusBadRequest, "Please choose your music folder first.")
		return
	}
	a.scanner.Start(dir)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) apiStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.scanner.Status())
}

// apiBrowse lists the subfolders of a folder, for the folder picker.
func (a *App) apiBrowse(w http.ResponseWriter, r *http.Request) {
	if !a.hasSession(r) && (a.cfg().ready() || !isLocal(r)) {
		jsonError(w, http.StatusUnauthorized, "Please log in")
		return
	}
	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = defaultMusicDir()
	}
	dir, _ = filepath.Abs(filepath.Clean(dir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "Cannot open this folder: "+dir)
		return
	}
	dirs := []string{}
	for _, e := range entries {
		if isDotEntry(e.Name()) {
			continue
		}
		if fi, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && fi.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i]) < strings.ToLower(dirs[j]) })
	roots := []string{}
	if runtime.GOOS == "windows" {
		for c := 'A'; c <= 'Z'; c++ {
			if _, err := os.Stat(string(c) + `:\`); err == nil {
				roots = append(roots, string(c)+`:\`)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": dir, "parent": filepath.Dir(dir), "dirs": dirs, "roots": roots})
}
