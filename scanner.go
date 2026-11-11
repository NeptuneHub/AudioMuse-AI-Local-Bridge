package main

import (
	"cmp"
	"context"
	"crypto/md5"
	"crypto/rand"
	"fmt"
	"io/fs"
	"log"
	"math/big"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.senan.xyz/taglib"
)

const (
	unknownArtist  = "[Unknown Artist]"
	unknownAlbum   = "[Unknown Album]"
	variousArtists = "Various Artists"
	libraryID      = "1"
)

// Audio extensions and MIME types, from Navidrome resources/mime_types.yaml.
var audioTypes = map[string]string{
	".mp3": "audio/mpeg", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg",
	".aac": "audio/aac", ".alac": "audio/mp4", ".m4a": "audio/mp4", ".m4b": "audio/mp4",
	".flac": "audio/flac", ".wav": "audio/x-wav", ".wma": "audio/x-ms-wma", ".ape": "audio/x-monkeys-audio",
	".mpc": "audio/x-musepack", ".shn": "audio/x-shn", ".aif": "audio/x-aiff", ".aiff": "audio/x-aiff",
	".dsf": "audio/x-dsf", ".wv": "audio/x-wavpack", ".wvp": "audio/x-wavpack", ".tak": "audio/tak",
	".mka": "audio/x-matroska", ".webm": "audio/webm",
}

// Folders Navidrome never scans (scanner/walk_dir_tree.go).
var ignoredDirs = []string{"$RECYCLE.BIN", "#snapshot", "@Recycle", "@eaDir", "@Recently-Snapshot", ".git", ".streams", "lost+found"}

// Tag aliases, lower case, from Navidrome resources/mappings.yaml.
var (
	tagTitle        = []string{"title", "tit2", "©nam", "inam"}
	tagSubtitle     = []string{"subtitle", "tit3", "----:com.apple.itunes:subtitle", "wm/subtitle"}
	tagAlbum        = []string{"album", "talb", "©alb", "wm/albumtitle", "iprd"}
	tagAlbumVersion = []string{"albumversion", "musicbrainz_albumcomment", "musicbrainz album comment", "version"}
	tagArtist       = []string{"artist", "tpe1", "©art", "author", "iart"}
	tagArtists      = []string{"artists", "txxx:artists", "----:com.apple.itunes:artists", "wm/artists"}
	tagAlbumArtist  = []string{"albumartist", "tpe2", "album artist", "album_artist", "aart", "wm/albumartist"}
	tagAlbumArtists = []string{"albumartists", "txxx:album artists"}
	tagTrack        = []string{"tracknumber", "track", "trck", "trkn", "wm/tracknumber", "itrk"}
	tagDisc         = []string{"discnumber", "disc", "tpos", "disk", "wm/partofset"}
	tagDate         = []string{"date", "recordingdate", "tdrc", "icrd", "record date"}
	tagOrigDate     = []string{"originaldate", "tdor", "tory", "originalyear", "origyear"}
	tagReleaseDate  = []string{"releasedate", "tdrl", "wm/year", "year"}
	tagGenre        = []string{"genre", "tcon", "©gen", "wm/genre", "ignr"}
	tagCompilation  = []string{"compilation", "tcmp", "cpil", "wm/iscompilation"}
	tagMbzAlbumID   = []string{"musicbrainz_albumid", "musicbrainz album id"}
)

var (
	artistSplit = regexp.MustCompile(`(?i) / | feat\. | feat | ft\. | ft |; `)
	genreSplit  = regexp.MustCompile(`[;/,]`)
	yearRegex   = regexp.MustCompile(`[12]\d\d\d`)
)

type tagMap map[string][]string

// all returns the trimmed, non-empty values of the first alias that has any.
func (t tagMap) all(aliases []string) []string {
	for _, a := range aliases {
		var out []string
		for _, v := range t[a] {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

func (t tagMap) first(aliases []string) string {
	if v := t.all(aliases); len(v) > 0 {
		return v[0]
	}
	return ""
}

// artistNames follows Navidrome: the multi-valued tag wins, a single value is split on separators.
func (t tagMap) artistNames(single, multi []string) []string {
	if v := t.all(multi); len(v) > 0 {
		return v
	}
	v := t.all(single)
	if len(v) != 1 {
		return v
	}
	var out []string
	for _, p := range artistSplit.Split(v[0], -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func tagNumber(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(strings.SplitN(v, "/", 2)[0]))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// sanitizeDate keeps YYYY-MM-DD, YYYY-MM or only the year, like Navidrome.
func sanitizeDate(v string) string {
	if !yearRegex.MatchString(v) {
		return ""
	}
	if len(v) > 10 {
		v = v[:10]
	}
	for _, layout := range []string{"2006-01-02", "2006-01"} {
		if _, err := time.Parse(layout, v); err == nil {
			return v
		}
	}
	return yearRegex.FindString(v)
}

func dateYear(v string) int {
	y, _ := strconv.Atoi(yearRegex.FindString(v))
	return y
}

// newHash is Navidrome's id.NewHash: MD5 of each input followed by U+200B, as 22 base62 chars.
func newHash(data ...string) string {
	h := md5.New()
	for _, d := range data {
		h.Write([]byte(d))
		h.Write([]byte("\u200b"))
	}
	return fmt.Sprintf("%022s", new(big.Int).SetBytes(h.Sum(nil)).Text(62))
}

func newRandomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%022s", new(big.Int).SetBytes(b).Text(62))
}

func artistID(name string) string { return newHash(strings.ToLower(strings.TrimSpace(name))) }

func participants(names []string) []IDName {
	out := make([]IDName, 0, len(names))
	for _, n := range names {
		out = append(out, IDName{ID: artistID(n), Name: n})
	}
	return out
}

// mapSong turns tags into a song using Navidrome's rules (model/metadata/map_*.go).
// Folders are never used: only tags, with the file name as the title fallback.
func mapSong(t tagMap, rel string) *Song {
	s := &Song{Path: rel}
	base := path.Base(rel)
	s.Title = cmp.Or(t.first(tagTitle), strings.TrimSuffix(base, path.Ext(base)))
	if sub := t.first(tagSubtitle); sub != "" {
		s.Title += " (" + sub + ")"
	}
	albumTag := t.first(tagAlbum)
	version := t.first(tagAlbumVersion)
	s.Album = cmp.Or(albumTag, unknownAlbum)
	if version != "" {
		s.Album += " (" + version + ")"
	}
	s.Compilation, _ = strconv.ParseBool(t.first(tagCompilation))

	artists := t.artistNames(tagArtist, tagArtists)
	if len(artists) == 0 {
		artists = []string{unknownArtist}
	}
	s.Artist = cmp.Or(strings.Join(t.all(tagArtist), " • "), strings.Join(t.all(tagArtists), " • "), unknownArtist)
	s.Artists = participants(artists)
	s.ArtistID = s.Artists[0].ID

	albumArtists := t.artistNames(tagAlbumArtist, tagAlbumArtists)
	fallback := unknownArtist
	if s.Compilation {
		fallback = variousArtists
	}
	if len(albumArtists) == 0 {
		if s.Compilation {
			albumArtists = []string{variousArtists}
		} else {
			albumArtists = artists
		}
	}
	s.AlbumArtist = cmp.Or(strings.Join(t.all(tagAlbumArtist), " • "), albumArtists[0], fallback)
	s.AlbumArtists = participants(albumArtists)
	s.AlbumArtistID = s.AlbumArtists[0].ID

	s.Track = tagNumber(t.first(tagTrack))
	s.Disc = tagNumber(t.first(tagDisc))

	date, orig, release := sanitizeDate(t.first(tagDate)), sanitizeDate(t.first(tagOrigDate)), sanitizeDate(t.first(tagReleaseDate))
	releaseKey := release
	if orig != "" && release == "" && date != "" && date >= orig {
		release, date = date, ""
	}
	s.Year = dateYear(cmp.Or(date, orig, release))

	seen := map[string]bool{}
	s.Genres = []string{}
	for _, v := range t.all(tagGenre) {
		for _, g := range genreSplit.Split(v, -1) {
			if g = strings.TrimSpace(g); g != "" && !seen[strings.ToLower(g)] {
				seen[strings.ToLower(g)] = true
				s.Genres = append(s.Genres, g)
			}
		}
	}

	// Album PID: "musicbrainz_albumid|albumartistid,album,albumversion,releasedate".
	if mbz := t.first(tagMbzAlbumID); mbz != "" {
		s.AlbumID = newHash(libraryID + `\` + strings.ToLower(mbz))
	} else {
		s.AlbumID = newHash(libraryID + `\` + strings.Join([]string{artistID(s.AlbumArtist),
			strings.ToLower(albumTag), strings.ToLower(version), releaseKey}, `\`))
	}
	return s
}

// searchWords builds the text used for word-prefix search: " word1 word2 ...".
func searchWords(parts ...string) string {
	var b strings.Builder
	for _, w := range searchTokens(strings.Join(parts, " ")) {
		b.WriteByte(' ')
		b.WriteString(w)
	}
	return b.String()
}

func searchTokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

// ---- scanning ----

type ScanStatus struct {
	State      string `json:"state"` // idle, scanning, done, error
	Phase      string `json:"phase"` // finding, reading, saving
	Found      int    `json:"found"`
	Processed  int    `json:"processed"`
	Skipped    int    `json:"skipped"`
	Artists    int    `json:"artists"`
	Albums     int    `json:"albums"`
	Songs      int    `json:"songs"`
	Error      string `json:"error,omitempty"`
	StartedAt  int64  `json:"startedAt,omitempty"`
	FinishedAt int64  `json:"finishedAt,omitempty"`
}

type Scanner struct {
	store  *Store
	mu     sync.Mutex
	status ScanStatus
	cancel context.CancelFunc
	done   chan struct{}
}

func newScanner(st *Store) *Scanner {
	sc := &Scanner{store: st, status: ScanStatus{State: "idle"}}
	sc.status.Artists, sc.status.Albums, sc.status.Songs = st.counts()
	return sc
}

func (sc *Scanner) Status() ScanStatus {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.status
}

func (sc *Scanner) update(f func(*ScanStatus)) {
	sc.mu.Lock()
	f(&sc.status)
	sc.mu.Unlock()
}

// Start begins a scan of root. A scan already running is cancelled first.
func (sc *Scanner) Start(root string) {
	sc.mu.Lock()
	if sc.cancel != nil {
		sc.cancel()
	}
	prev := sc.done
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	sc.cancel, sc.done = cancel, done
	sc.mu.Unlock()
	go func() {
		defer close(done)
		if prev != nil {
			<-prev
		}
		sc.run(ctx, root)
	}()
}

// Wait blocks until the current scan is finished.
func (sc *Scanner) Wait() {
	sc.mu.Lock()
	d := sc.done
	sc.mu.Unlock()
	if d != nil {
		<-d
	}
}

type fileEntry struct {
	abs, rel             string
	size, mtime, created int64
}

func (sc *Scanner) fail(err error) {
	log.Printf("scan failed: %v", err)
	sc.update(func(s *ScanStatus) { s.State, s.Error, s.FinishedAt = "error", err.Error(), time.Now().Unix() })
}

func (sc *Scanner) run(ctx context.Context, root string) {
	start := time.Now()
	log.Printf("scan started: %s", root)
	sc.update(func(s *ScanStatus) {
		*s = ScanStatus{State: "scanning", Phase: "finding", StartedAt: start.Unix(), Artists: s.Artists, Albums: s.Albums, Songs: s.Songs}
	})
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		sc.fail(fmt.Errorf("the music folder %q cannot be opened", root))
		return
	}
	files, err := sc.walk(ctx, root)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		sc.fail(fmt.Errorf("cannot read the music folder: %w", err))
		return
	}
	existing, err := sc.store.allSongs()
	if err != nil {
		sc.fail(err)
		return
	}
	sc.update(func(s *ScanStatus) { s.Phase, s.Artists, s.Albums, s.Songs = "reading", 0, 0, 0 })

	jobs := make(chan fileEntry)
	results := make(chan *Song, 64)
	var wg sync.WaitGroup
	for range min(runtime.NumCPU(), 8) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				results <- readSong(f, existing[f.rel])
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, f := range files {
			select {
			case jobs <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	present := make([]*Song, 0, len(files))
	albumSet, artistSet := map[string]bool{}, map[string]bool{}
	for s := range results {
		if s != nil {
			present = append(present, s)
			albumSet[s.AlbumID] = true
			for _, a := range s.AlbumArtists {
				artistSet[a.ID] = true
			}
		}
		sc.update(func(st *ScanStatus) {
			st.Processed++
			if s == nil {
				st.Skipped++
			}
			st.Songs, st.Albums, st.Artists = len(present), len(albumSet), len(artistSet)
		})
	}
	if ctx.Err() != nil {
		return
	}
	sc.update(func(s *ScanStatus) { s.Phase = "saving" })
	albums, artists := aggregate(present)
	if err := sc.store.saveScan(present, albums, artists); err != nil {
		sc.fail(err)
		return
	}
	ar, al, so := sc.store.counts()
	log.Printf("scan finished in %s: %d songs, %d albums, %d artists", time.Since(start).Round(time.Millisecond), so, al, ar)
	sc.update(func(s *ScanStatus) {
		s.State, s.Phase, s.FinishedAt = "done", "", time.Now().Unix()
		s.Artists, s.Albums, s.Songs = ar, al, so
	})
}

func isDotEntry(name string) bool {
	return name != "." && strings.HasPrefix(name, ".") && !strings.HasPrefix(name, "..")
}

func isIgnoredDir(name string) bool {
	if isDotEntry(name) {
		return true
	}
	for _, d := range ignoredDirs {
		if strings.EqualFold(d, name) {
			return true
		}
	}
	return false
}

// ignoredByNdignore reports whether dir has an empty .ndignore file (meaning: skip everything).
// Gitignore-style patterns inside .ndignore are not supported and are ignored.
func ignoredByNdignore(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, ".ndignore"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			return false
		}
	}
	return true
}

// walk lists the audio files under root. Symlinks are followed, each real folder is visited once.
func (sc *Scanner) walk(ctx context.Context, root string) ([]fileEntry, error) {
	var files []fileEntry
	visited := map[string]bool{}
	var walkDir func(abs, rel string) error
	walkDir = func(abs, rel string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			if visited[real] {
				return nil
			}
			visited[real] = true
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			if rel == "" {
				return err
			}
			log.Printf("skipping folder %s: %v", abs, err)
			return nil
		}
		if ignoredByNdignore(abs) {
			return nil
		}
		for _, e := range entries {
			name := e.Name()
			childAbs, childRel := filepath.Join(abs, name), path.Join(rel, name)
			var info fs.FileInfo
			if e.Type()&fs.ModeSymlink != 0 {
				info, err = os.Stat(childAbs)
			} else {
				info, err = e.Info()
			}
			if err != nil {
				continue
			}
			if info.IsDir() {
				if !isIgnoredDir(name) {
					if err := walkDir(childAbs, childRel); err != nil {
						return err
					}
				}
				continue
			}
			if !info.Mode().IsRegular() || isDotEntry(name) {
				continue
			}
			if _, ok := audioTypes[strings.ToLower(filepath.Ext(name))]; !ok {
				continue
			}
			files = append(files, fileEntry{abs: childAbs, rel: childRel, size: info.Size(),
				mtime: info.ModTime().UnixNano(), created: fileCreated(info)})
			sc.update(func(s *ScanStatus) { s.Found = len(files) })
		}
		return nil
	}
	return files, walkDir(root, "")
}

// readSong reads one file. Unchanged files (same size and mtime) reuse the stored data.
// Files TagLib cannot read are skipped, like Navidrome does. Returns nil when skipped.
func readSong(f fileEntry, prev *Song) *Song {
	if prev != nil && prev.Size == f.size && prev.MTime == f.mtime {
		cp := *prev
		return &cp
	}
	raw, err := taglib.ReadTags(f.abs)
	if err != nil {
		log.Printf("skipping %s: cannot read tags: %v", f.rel, err)
		return nil
	}
	props, err := taglib.ReadProperties(f.abs)
	if err != nil {
		log.Printf("skipping %s: cannot read audio properties: %v", f.rel, err)
		return nil
	}
	t := tagMap{}
	for k, v := range raw {
		lk := strings.ToLower(k)
		t[lk] = append(t[lk], v...)
	}
	s := mapSong(t, f.rel)
	s.ID = newRandomID()
	if prev != nil {
		s.ID = prev.ID
	}
	s.Size, s.MTime, s.Created = f.size, f.mtime, f.created
	s.Duration = float64(props.Length.Milliseconds()) / 1000
	s.BitRate, s.SampleRate, s.Channels = int(props.BitRate), int(props.SampleRate), int(props.Channels)
	s.Suffix = strings.TrimPrefix(strings.ToLower(path.Ext(f.rel)), ".")
	return s
}

// aggregate builds albums and (album) artists from the songs.
func aggregate(songs []*Song) ([]*Album, []*Artist) {
	byID := map[string]*Album{}
	var albums []*Album
	for _, s := range songs {
		a := byID[s.AlbumID]
		if a == nil {
			a = &Album{ID: s.AlbumID, Name: s.Album, Artist: s.AlbumArtist, ArtistID: s.AlbumArtistID,
				Artists: s.AlbumArtists, Genres: []string{}, Created: s.Created, Compilation: s.Compilation}
			byID[s.AlbumID] = a
			albums = append(albums, a)
		}
		a.SongCount++
		a.Duration += s.Duration
		a.Year = max(a.Year, s.Year)
		a.Created = min(a.Created, s.Created)
		for _, g := range s.Genres {
			found := false
			for _, x := range a.Genres {
				found = found || strings.EqualFold(x, g)
			}
			if !found {
				a.Genres = append(a.Genres, g)
			}
		}
	}
	sort.SliceStable(albums, func(i, j int) bool {
		if albums[i].Created != albums[j].Created {
			return albums[i].Created < albums[j].Created
		}
		return albums[i].ID < albums[j].ID
	})
	artistByID := map[string]*Artist{}
	var artists []*Artist
	for _, a := range albums {
		for _, p := range a.Artists {
			ar := artistByID[p.ID]
			if ar == nil {
				ar = &Artist{ID: p.ID, Name: p.Name}
				artistByID[p.ID] = ar
				artists = append(artists, ar)
			}
			ar.AlbumCount++
		}
	}
	return albums, artists
}
