package main

import (
	"crypto/md5"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.senan.xyz/taglib"
)

// The OpenSubsonic endpoints AudioMuse-AI calls (tasks/mediaserver/navidrome.py), and only those.
// Behaviour follows Navidrome. Responses are JSON only, because AudioMuse-AI always sends f=json.

type apiError struct {
	code int
	msg  string
}

var errAuth = &apiError{40, "Wrong username or password"}

func errParam(name string) *apiError {
	return &apiError{10, fmt.Sprintf("Missing required parameter: '%s'", name)}
}

func errNotFound(what string) *apiError { return &apiError{70, what + " not found"} }

func errInternal(err error) *apiError { return &apiError{0, "Internal error: " + err.Error()} }

func send(w http.ResponseWriter, payload map[string]any, e *apiError) {
	resp := map[string]any{"status": "ok", "version": apiVersion, "type": appName, "serverVersion": version, "openSubsonic": true}
	if e != nil {
		resp["status"] = "failed"
		resp["error"] = map[string]any{"code": e.code, "message": e.msg}
	}
	for k, v := range payload {
		resp[k] = v
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": resp})
}

type restHandler func(r *http.Request) (map[string]any, *apiError)

func (a *App) restRoutes() map[string]restHandler {
	return map[string]restHandler{
		"getMusicFolders":   a.getMusicFolders,
		"getAlbumList2":     a.getAlbumList2,
		"getAlbum":          a.getAlbum,
		"getSong":           a.getSong,
		"search3":           a.search3,
		"getPlaylists":      a.getPlaylists,
		"getPlaylist":       a.getPlaylist,
		"createPlaylist":    a.createPlaylistAPI,
		"updatePlaylist":    a.updatePlaylistAPI,
		"deletePlaylist":    a.deletePlaylistAPI,
		"getLyricsBySongId": a.getLyricsBySongID,
	}
}

// handleRest serves /rest/<name> and /rest/<name>.view, GET or POST (query string or form body).
func (a *App) handleRest(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	if err := r.ParseForm(); err != nil {
		send(w, nil, &apiError{0, "Invalid request"})
		return
	}
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/rest/"), ".view")
	h, ok := a.routes[name]
	if !ok && name != "stream" {
		http.NotFound(w, r)
		return
	}
	if e := a.authenticate(r); e != nil {
		send(w, nil, e)
		return
	}
	if name == "stream" {
		a.stream(w, r)
		return
	}
	payload, e := h(r)
	send(w, payload, e)
}

// ---- authentication ----

func secureEq(x, y string) bool {
	hx, hy := sha256.Sum256([]byte(x)), sha256.Sum256([]byte(y))
	return subtle.ConstantTimeCompare(hx[:], hy[:]) == 1
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// authenticate accepts u+p (plain or "enc:" hex) and u+t+s (md5 token), like Navidrome.
// API keys are not supported, so every key is invalid.
func (a *App) authenticate(r *http.Request) *apiError {
	q := r.Form
	if q.Get("apiKey") != "" {
		for _, k := range []string{"u", "p", "t", "s"} {
			if q.Has(k) {
				return &apiError{43, "Multiple conflicting authentication mechanisms provided"}
			}
		}
		return &apiError{44, "Invalid API key"}
	}
	for _, k := range []string{"u", "v", "c"} {
		if q.Get(k) == "" {
			return errParam(k)
		}
	}
	cfg := a.cfg()
	user := q.Get("u")
	key := "api|" + clientIP(r) + "|" + strings.ToLower(user)
	if !cfg.ready() || a.limiter.blocked(key) {
		return errAuth
	}
	passOK := false
	if p := q.Get("p"); p != "" {
		if strings.HasPrefix(p, "enc:") {
			b, err := hex.DecodeString(p[4:])
			p = string(b)
			if err != nil {
				p = ""
			}
		}
		passOK = p != "" && secureEq(p, cfg.Password)
	} else if t, s := q.Get("t"), q.Get("s"); t != "" && s != "" {
		sum := md5.Sum([]byte(cfg.Password + s))
		passOK = secureEq(strings.ToLower(t), hex.EncodeToString(sum[:]))
	}
	userOK := secureEq(strings.ToLower(user), strings.ToLower(cfg.Username))
	if !userOK || !passOK {
		a.limiter.fail(key)
		return errAuth
	}
	return nil
}

// limiter blocks a client for one minute after 5 failed logins within one minute.
type limiter struct {
	mu sync.Mutex
	m  map[string]*attempts
}

type attempts struct {
	n            int
	first, until time.Time
}

func newLimiter() *limiter { return &limiter{m: map[string]*attempts{}} }

func (l *limiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	at := l.m[key]
	return at != nil && time.Now().Before(at.until)
}

func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.m) > 10000 {
		for k, v := range l.m {
			if now.Sub(v.first) > time.Minute && now.After(v.until) {
				delete(l.m, k)
			}
		}
	}
	at := l.m[key]
	if at == nil || now.Sub(at.first) > time.Minute {
		at = &attempts{first: now}
		l.m[key] = at
	}
	if at.n++; at.n >= 5 {
		at.until, at.n, at.first = now.Add(time.Minute), 0, now
	}
}

// ---- response shapes ----
// Rules (OpenSubsonic): never null; required fields always present; classic optional fields without
// data are omitted; OpenSubsonic fields the bridge supports are always present, empty when unknown.

type genreResp struct {
	Name string `json:"name"`
}

type childResp struct {
	ID          string `json:"id"`
	Parent      string `json:"parent,omitempty"`
	IsDir       bool   `json:"isDir"`
	Title       string `json:"title"`
	Album       string `json:"album,omitempty"`
	Artist      string `json:"artist,omitempty"`
	Track       int    `json:"track,omitempty"`
	Year        int    `json:"year,omitempty"`
	Genre       string `json:"genre,omitempty"`
	Size        int64  `json:"size,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Suffix      string `json:"suffix,omitempty"`
	Duration    int    `json:"duration,omitempty"`
	BitRate     int    `json:"bitRate,omitempty"`
	Path        string `json:"path,omitempty"`
	DiscNumber  int    `json:"discNumber,omitempty"`
	Created     string `json:"created,omitempty"`
	AlbumID     string `json:"albumId,omitempty"`
	ArtistID    string `json:"artistId,omitempty"`
	Type        string `json:"type"`
	// OpenSubsonic
	MediaType          string      `json:"mediaType"`
	Genres             []genreResp `json:"genres"`
	Artists            []IDName    `json:"artists"`
	DisplayArtist      string      `json:"displayArtist"`
	AlbumArtists       []IDName    `json:"albumArtists"`
	DisplayAlbumArtist string      `json:"displayAlbumArtist"`
	SamplingRate       int         `json:"samplingRate"`
	ChannelCount       int         `json:"channelCount"`
}

type albumResp struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Artist    string `json:"artist,omitempty"`
	ArtistID  string `json:"artistId,omitempty"`
	SongCount int    `json:"songCount"`
	Duration  int    `json:"duration"`
	Created   string `json:"created"`
	Year      int    `json:"year,omitempty"`
	Genre     string `json:"genre,omitempty"`
	// OpenSubsonic
	DisplayArtist string      `json:"displayArtist"`
	Artists       []IDName    `json:"artists"`
	Genres        []genreResp `json:"genres"`
	IsCompilation bool        `json:"isCompilation"`
	Song          []childResp `json:"song,omitempty"`
}

type artistResp struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	AlbumCount int    `json:"albumCount,omitempty"`
}

type playlistResp struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Owner     string      `json:"owner,omitempty"`
	SongCount int         `json:"songCount"`
	Duration  int         `json:"duration"`
	Created   string      `json:"created"`
	Changed   string      `json:"changed"`
	Entry     []childResp `json:"entry,omitempty"`
}

func isoTime(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z") }

func genreList(g []string) []genreResp {
	out := make([]genreResp, 0, len(g))
	for _, n := range g {
		out = append(out, genreResp{n})
	}
	return out
}

func nonNil(v []IDName) []IDName {
	if v == nil {
		return []IDName{}
	}
	return v
}

// songPath returns the absolute path of a song, checking it stays inside the music folder.
func songPath(root string, s *Song) (string, bool) {
	if root == "" {
		return "", false
	}
	abs := filepath.Join(root, filepath.FromSlash(s.Path))
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return abs, true
}

func toChild(root string, s *Song) childResp {
	abs, _ := songPath(root, s)
	c := childResp{
		ID: s.ID, Parent: s.AlbumID, Title: s.Title, Album: s.Album, Artist: s.Artist, Track: s.Track,
		Year: s.Year, Size: s.Size, ContentType: audioTypes["."+s.Suffix], Suffix: s.Suffix,
		Duration: int(s.Duration), BitRate: s.BitRate, Path: abs, DiscNumber: s.Disc,
		Created: isoTime(s.Created), AlbumID: s.AlbumID, ArtistID: s.ArtistID, Type: "music",
		MediaType: "song", Genres: genreList(s.Genres), Artists: nonNil(s.Artists), DisplayArtist: s.Artist,
		AlbumArtists: nonNil(s.AlbumArtists), DisplayAlbumArtist: s.AlbumArtist,
		SamplingRate: s.SampleRate, ChannelCount: s.Channels,
	}
	if len(s.Genres) > 0 {
		c.Genre = s.Genres[0]
	}
	return c
}

func toChildren(root string, songs []*Song) []childResp {
	out := make([]childResp, 0, len(songs))
	for _, s := range songs {
		out = append(out, toChild(root, s))
	}
	return out
}

func toAlbum(a *Album) albumResp {
	r := albumResp{
		ID: a.ID, Name: a.Name, Artist: a.Artist, ArtistID: a.ArtistID, SongCount: a.SongCount,
		Duration: int(a.Duration), Created: isoTime(a.Created), Year: a.Year, DisplayArtist: a.Artist,
		Artists: nonNil(a.Artists), Genres: genreList(a.Genres), IsCompilation: a.Compilation,
	}
	if len(a.Genres) > 0 {
		r.Genre = a.Genres[0]
	}
	return r
}

func toAlbums(albums []*Album) []albumResp {
	out := make([]albumResp, 0, len(albums))
	for _, a := range albums {
		out = append(out, toAlbum(a))
	}
	return out
}

func (a *App) toPlaylist(p *playlist, withEntries bool) playlistResp {
	vis, cfg := p.visible(), a.cfg()
	r := playlistResp{ID: playlistID(p.file), Name: p.name, Owner: cfg.Username, SongCount: len(vis),
		Created: isoTime(p.created), Changed: isoTime(p.changed)}
	var dur float64
	for _, e := range vis {
		dur += e.song.Duration
		if withEntries {
			r.Entry = append(r.Entry, toChild(cfg.MusicDir, e.song))
		}
	}
	r.Duration = int(dur)
	return r
}

// ---- parameters ----

func intParam(r *http.Request, name string, def int) int {
	if n, err := strconv.Atoi(r.Form.Get(name)); err == nil {
		return n
	}
	return def
}

// checkFolder accepts only the single music folder (id 1), like a one-library Navidrome.
func checkFolder(r *http.Request) *apiError {
	for _, v := range r.Form["musicFolderId"] {
		if v != "1" {
			return &apiError{70, fmt.Sprintf("Library %s not found or not accessible", v)}
		}
	}
	return nil
}

// ---- handlers ----

func (a *App) getMusicFolders(r *http.Request) (map[string]any, *apiError) {
	return map[string]any{"musicFolders": map[string]any{
		"musicFolder": []map[string]any{{"id": 1, "name": "Music Library"}},
	}}, nil
}

func (a *App) getAlbumList2(r *http.Request) (map[string]any, *apiError) {
	typ := r.Form.Get("type")
	if typ == "" {
		return nil, errParam("type")
	}
	if e := checkFolder(r); e != nil {
		return nil, e
	}
	size := max(min(intParam(r, "size", 10), 500), 0)
	offset := max(intParam(r, "offset", 0), 0)
	var albums []*Album
	switch typ {
	case "newest":
		var err error
		if albums, err = a.store.albumsNewest(offset, size); err != nil {
			return nil, errInternal(err)
		}
	case "frequent":
		// Only played albums are listed. Nothing records plays here, so the list is empty.
	default:
		return nil, &apiError{0, fmt.Sprintf("type '%s' not implemented", typ)}
	}
	return map[string]any{"albumList2": struct {
		Album []albumResp `json:"album,omitempty"`
	}{toAlbums(albums)}}, nil
}

func (a *App) getAlbum(r *http.Request) (map[string]any, *apiError) {
	id := r.Form.Get("id")
	if id == "" {
		return nil, errParam("id")
	}
	al, err := a.store.album(id)
	if err != nil {
		return nil, errInternal(err)
	} else if al == nil {
		return nil, errNotFound("Album")
	}
	songs, err := a.store.songsByAlbum(id)
	if err != nil {
		return nil, errInternal(err)
	}
	resp := toAlbum(al)
	resp.Song = toChildren(a.cfg().MusicDir, songs)
	return map[string]any{"album": resp}, nil
}

func (a *App) getSong(r *http.Request) (map[string]any, *apiError) {
	id := r.Form.Get("id")
	if id == "" {
		return nil, errParam("id")
	}
	s, err := a.store.song(id)
	if err != nil {
		return nil, errInternal(err)
	} else if s == nil {
		return nil, errNotFound("Song")
	}
	return map[string]any{"song": toChild(a.cfg().MusicDir, s)}, nil
}

// search3: an empty query (or "") lists everything in natural order, which AudioMuse-AI uses to
// page through all songs. Other queries match the start of words in title, album and artist.
func (a *App) search3(r *http.Request) (map[string]any, *apiError) {
	if e := checkFolder(r); e != nil {
		return nil, e
	}
	query := strings.TrimSuffix(strings.TrimSpace(r.Form.Get("query")), "*")
	var tokens []string
	if query != "" && query != `""` {
		if tokens = searchTokens(query); len(tokens) == 0 {
			return map[string]any{"searchResult3": map[string]any{}}, nil
		}
	}
	var result struct {
		Artist []artistResp `json:"artist,omitempty"`
		Album  []albumResp  `json:"album,omitempty"`
		Song   []childResp  `json:"song,omitempty"`
	}
	if n := intParam(r, "artistCount", 20); n > 0 {
		artists, err := a.store.searchArtists(tokens, max(intParam(r, "artistOffset", 0), 0), n)
		if err != nil {
			return nil, errInternal(err)
		}
		for _, ar := range artists {
			result.Artist = append(result.Artist, artistResp{ar.ID, ar.Name, ar.AlbumCount})
		}
	}
	if n := intParam(r, "albumCount", 20); n > 0 {
		albums, err := a.store.searchAlbums(tokens, max(intParam(r, "albumOffset", 0), 0), n)
		if err != nil {
			return nil, errInternal(err)
		}
		result.Album = toAlbums(albums)
	}
	if n := intParam(r, "songCount", 20); n > 0 {
		songs, err := a.store.searchSongs(tokens, max(intParam(r, "songOffset", 0), 0), n)
		if err != nil {
			return nil, errInternal(err)
		}
		result.Song = toChildren(a.cfg().MusicDir, songs)
	}
	return map[string]any{"searchResult3": result}, nil
}

// stream sends the original file (no transcoding). http.ServeContent handles Range requests.
func (a *App) stream(w http.ResponseWriter, r *http.Request) {
	id := r.Form.Get("id")
	if id == "" {
		send(w, nil, errParam("id"))
		return
	}
	s, err := a.store.song(id)
	if err != nil {
		send(w, nil, errInternal(err))
		return
	}
	abs, ok := "", false
	if s != nil {
		abs, ok = songPath(a.cfg().MusicDir, s)
	}
	if !ok {
		send(w, nil, &apiError{70, "data not found"})
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		send(w, nil, &apiError{70, "data not found"})
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		send(w, nil, errInternal(err))
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Content-Duration", strconv.FormatFloat(s.Duration, 'f', 2, 64))
	if ct := audioTypes["."+s.Suffix]; ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, s.Title+"."+s.Suffix, info.ModTime(), f)
}

func (a *App) musicRoot() (string, *apiError) {
	if root := a.cfg().MusicDir; root != "" {
		return root, nil
	}
	return "", &apiError{0, "The music folder is not set"}
}

func (a *App) getPlaylists(r *http.Request) (map[string]any, *apiError) {
	var list []playlistResp
	if root := a.cfg().MusicDir; root != "" {
		files, err := playlistFiles(root)
		if err != nil {
			return nil, errInternal(err)
		}
		for _, f := range files {
			if p, err := a.loadPlaylist(root, f); err == nil {
				list = append(list, a.toPlaylist(p, false))
			}
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	return map[string]any{"playlists": struct {
		Playlist []playlistResp `json:"playlist,omitempty"`
	}{list}}, nil
}

func (a *App) getPlaylist(r *http.Request) (map[string]any, *apiError) {
	id := r.Form.Get("id")
	if id == "" {
		return nil, errParam("id")
	}
	root, e := a.musicRoot()
	if e != nil {
		return nil, e
	}
	p, err := a.findPlaylist(root, id)
	if err != nil {
		return nil, errInternal(err)
	} else if p == nil {
		return nil, errNotFound("Playlist")
	}
	return map[string]any{"playlist": a.toPlaylist(p, true)}, nil
}

func (a *App) createPlaylistAPI(r *http.Request) (map[string]any, *apiError) {
	id, name := r.Form.Get("playlistId"), r.Form.Get("name")
	if id == "" && strings.TrimSpace(name) == "" {
		return nil, errParam("name")
	}
	root, e := a.musicRoot()
	if e != nil {
		return nil, e
	}
	p, e := a.createPlaylist(root, id, name, r.Form["songId"])
	if e != nil {
		return nil, e
	}
	return map[string]any{"playlist": a.toPlaylist(p, true)}, nil
}

func (a *App) updatePlaylistAPI(r *http.Request) (map[string]any, *apiError) {
	id := r.Form.Get("playlistId")
	if id == "" {
		return nil, errParam("playlistId")
	}
	root, e := a.musicRoot()
	if e != nil {
		return nil, e
	}
	var name *string
	if r.Form.Has("name") {
		n := r.Form.Get("name")
		name = &n
	}
	var remove []int
	for _, v := range r.Form["songIndexToRemove"] {
		if n, err := strconv.Atoi(v); err == nil {
			remove = append(remove, n)
		}
	}
	// "comment" and "public" are accepted and ignored: an M3U8 file has no place for them.
	return nil, a.updatePlaylist(root, id, name, r.Form["songIdToAdd"], remove)
}

func (a *App) deletePlaylistAPI(r *http.Request) (map[string]any, *apiError) {
	id := r.Form.Get("id")
	if id == "" {
		return nil, errParam("id")
	}
	root, e := a.musicRoot()
	if e != nil {
		return nil, e
	}
	return nil, a.deletePlaylist(root, id)
}

// ---- lyrics ----

type lyricLine struct {
	Start *int64 `json:"start,omitempty"`
	Value string `json:"value"`
}

type lyricsResp struct {
	DisplayArtist string      `json:"displayArtist,omitempty"`
	DisplayTitle  string      `json:"displayTitle,omitempty"`
	Lang          string      `json:"lang"`
	Offset        int64       `json:"offset,omitempty"`
	Synced        bool        `json:"synced"`
	Line          []lyricLine `json:"line"`
}

var (
	syncRegex = regexp.MustCompile(`(^|\n)\s*\[([0-9]{1,2}:)?([0-9]{1,2}):([0-9]{1,2})(\.[0-9]{1,3})?\]`)
	timeRegex = regexp.MustCompile(`^\[(?:([0-9]{1,2}):)?([0-9]{1,2}):([0-9]{1,2})(?:\.([0-9]{1,3}))?\]`)
	metaRegex = regexp.MustCompile(`^\[([a-zA-Z#]+):(.*)\]$`)
)

// parseLyrics reads LRC or plain text, like Navidrome: synced when a line starts with [mm:ss.xx].
func parseLyrics(text string) *lyricsResp {
	text = strings.ReplaceAll(strings.TrimPrefix(text, "\ufeff"), "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	l := &lyricsResp{Lang: "xxx", Synced: syncRegex.MatchString(text), Line: []lyricLine{}}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if m := metaRegex.FindStringSubmatch(line); m != nil {
			v := strings.TrimSpace(m[2])
			switch strings.ToLower(m[1]) {
			case "ar":
				l.DisplayArtist = v
			case "ti":
				l.DisplayTitle = v
			case "offset":
				l.Offset, _ = strconv.ParseInt(v, 10, 64)
			case "la", "lang":
				l.Lang = v
			}
			continue
		}
		if !l.Synced {
			l.Line = append(l.Line, lyricLine{Value: line})
			continue
		}
		var starts []int64
		for {
			m := timeRegex.FindStringSubmatch(line)
			if m == nil {
				break
			}
			h, _ := strconv.ParseInt(m[1], 10, 64)
			mi, _ := strconv.ParseInt(m[2], 10, 64)
			sec, _ := strconv.ParseInt(m[3], 10, 64)
			ms := ((h*60+mi)*60 + sec) * 1000
			if frac := m[4]; frac != "" {
				f, _ := strconv.ParseInt(frac, 10, 64)
				ms += f * []int64{0, 100, 10, 1}[len(frac)]
			}
			starts = append(starts, ms)
			line = line[len(m[0]):]
		}
		for _, st := range starts {
			st := st
			l.Line = append(l.Line, lyricLine{Start: &st, Value: strings.TrimSpace(line)})
		}
	}
	if l.Synced {
		sort.SliceStable(l.Line, func(i, j int) bool { return *l.Line[i].Start < *l.Line[j].Start })
	}
	if len(l.Line) == 0 {
		return nil
	}
	return l
}

// readLyrics tries a sidecar .lrc, then .txt, then the lyrics embedded in the file.
func readLyrics(abs string) []lyricsResp {
	base := strings.TrimSuffix(abs, filepath.Ext(abs))
	for _, ext := range []string{".lrc", ".txt"} {
		if data, err := os.ReadFile(base + ext); err == nil {
			if l := parseLyrics(string(data)); l != nil {
				return []lyricsResp{*l}
			}
		}
	}
	tags, err := taglib.ReadTags(abs)
	if err != nil {
		return nil
	}
	var out []lyricsResp
	for k, vals := range tags {
		lk := strings.ToLower(k)
		if lk != "lyrics" && lk != "unsyncedlyrics" && lk != "©lyr" && !strings.HasPrefix(lk, "lyrics:") {
			continue
		}
		for _, v := range vals {
			if l := parseLyrics(v); l != nil {
				if lang, ok := strings.CutPrefix(lk, "lyrics:"); ok && lang != "" && l.Lang == "xxx" {
					l.Lang = lang
				}
				out = append(out, *l)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Lang < out[j].Lang })
	return out
}

func (a *App) getLyricsBySongID(r *http.Request) (map[string]any, *apiError) {
	id := r.Form.Get("id")
	if id == "" {
		return nil, errParam("id")
	}
	s, err := a.store.song(id)
	if err != nil {
		return nil, errInternal(err)
	} else if s == nil {
		return nil, errNotFound("Song")
	}
	var lists []lyricsResp
	if abs, ok := songPath(a.cfg().MusicDir, s); ok {
		lists = readLyrics(abs)
	}
	return map[string]any{"lyricsList": struct {
		StructuredLyrics []lyricsResp `json:"structuredLyrics,omitempty"`
	}{lists}}, nil
}
