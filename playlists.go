package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

// Playlists are "<name>.m3u8" files in the Playlists folder of the music folder, created when the
// first playlist is saved. The files are the only storage. Song paths are relative to that folder
// ("../Artist/Album/song.flac"), as the M3U format expects, so other players can open them too.

const playlistFolder = "Playlists"

func playlistDir(root string) string { return filepath.Join(root, playlistFolder) }

type plEntry struct {
	pre  []string // comment lines before the path, e.g. #EXTINF
	path string   // the path line exactly as written in the file
	song *Song    // the scanned song, nil when the line matches no song
}

type playlist struct {
	file             string // file name inside the Playlists folder
	name             string
	entries          []*plEntry
	tail             []string // comment lines after the last entry
	created, changed int64    // ms
}

// visible returns the entries that match a scanned song: the only ones the API shows.
func (p *playlist) visible() []*plEntry {
	var out []*plEntry
	for _, e := range p.entries {
		if e.song != nil {
			out = append(out, e)
		}
	}
	return out
}

func playlistID(file string) string { return newHash("playlist", strings.ToLower(file)) }

// oneLine keeps names and titles on one line, so they cannot add lines to the file.
func oneLine(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }

// playlistFile turns a playlist name into a safe file name.
func playlistFile(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		if r < 32 || r == 127 || strings.ContainsRune(`/\:*?"<>|`, r) {
			r = '_'
		}
		b.WriteRune(r)
	}
	s := b.String()
	if strings.HasPrefix(s, ".") {
		s = "_" + s[1:]
	}
	for len(s) > 200 {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	s = strings.TrimRight(s, " .")
	if s == "" {
		s = "Playlist"
	}
	switch strings.ToUpper(s) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		s += "_"
	}
	return s + ".m3u8"
}

// entryRel converts a path line of a playlist in folder dir to a path relative to the music folder
// root, or "" if it is outside. Relative lines are relative to dir.
func entryRel(root, dir, line string) string {
	p := line
	if strings.HasPrefix(strings.ToLower(p), "file://") {
		u, err := url.Parse(p)
		if err != nil {
			return ""
		}
		p = u.Path
		if runtime.GOOS == "windows" && len(p) > 2 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
		p = filepath.FromSlash(p)
	} else if strings.Contains(p, "://") {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(p, `\`, "/")))
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

func playlistFiles(root string) ([]string, error) {
	dir := playlistDir(root)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // no playlist saved yet
	} else if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if isDotEntry(n) || !strings.EqualFold(filepath.Ext(n), ".m3u8") {
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, n)); err == nil && info.Mode().IsRegular() {
			out = append(out, n)
		}
	}
	return out, nil
}

func (a *App) loadPlaylist(root, file string) (*playlist, error) {
	dir := playlistDir(root)
	full := filepath.Join(dir, file)
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	p := &playlist{file: file, created: fileCreated(info), changed: info.ModTime().UnixMilli()}
	text := strings.ReplaceAll(strings.TrimPrefix(string(data), "\ufeff"), "\r\n", "\n")
	var pending []string
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case l == "" || strings.EqualFold(l, "#EXTM3U"):
		case strings.HasPrefix(l, "#PLAYLIST:"):
			p.name = strings.TrimSpace(strings.TrimPrefix(l, "#PLAYLIST:"))
		case strings.HasPrefix(l, "#"):
			pending = append(pending, l)
		default:
			e := &plEntry{pre: pending, path: l}
			if rel := entryRel(root, dir, l); rel != "" {
				e.song, _ = a.store.songByPath(rel)
			}
			p.entries = append(p.entries, e)
			pending = nil
		}
	}
	p.tail = pending
	if p.name == "" {
		p.name = strings.TrimSuffix(file, filepath.Ext(file))
	}
	return p, nil
}

// findPlaylist returns the playlist with this id, or nil.
func (a *App) findPlaylist(root, id string) (*playlist, error) {
	files, err := playlistFiles(root)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if playlistID(f) == id {
			return a.loadPlaylist(root, f)
		}
	}
	return nil, nil
}

// writePlaylist writes to a temporary file and renames it, so a crash never leaves half a file.
// It creates the Playlists folder when it is missing.
func writePlaylist(root string, p *playlist) error {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#PLAYLIST:" + oneLine(p.name) + "\n")
	for _, e := range p.entries {
		for _, l := range e.pre {
			b.WriteString(l + "\n")
		}
		b.WriteString(e.path + "\n")
	}
	for _, l := range p.tail {
		b.WriteString(l + "\n")
	}
	dir := playlistDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".playlist-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmp.Name(), 0o644)
	return os.Rename(tmp.Name(), filepath.Join(dir, p.file))
}

func songEntry(s *Song) *plEntry {
	return &plEntry{
		pre:  []string{fmt.Sprintf("#EXTINF:%d,%s - %s", int(s.Duration), oneLine(s.Artist), oneLine(s.Title))},
		path: "../" + s.Path, song: s,
	}
}

// songEntries returns entries for the known ids. Unknown ids are dropped, like Navidrome.
func (a *App) songEntries(ids []string) []*plEntry {
	var out []*plEntry
	for _, id := range ids {
		if s, _ := a.store.song(id); s != nil {
			out = append(out, songEntry(s))
		}
	}
	return out
}

func fileErr(err error) *apiError {
	return &apiError{0, "Cannot write the playlist file in the Playlists folder: " + err.Error()}
}

// createPlaylist makes a new playlist file (replacing a file with the same name), or with
// playlistId replaces the songs of an existing one.
func (a *App) createPlaylist(root, id, name string, songIDs []string) (*playlist, *apiError) {
	a.plMu.Lock()
	defer a.plMu.Unlock()
	var p *playlist
	if id != "" {
		var err error
		if p, err = a.findPlaylist(root, id); err != nil {
			return nil, fileErr(err)
		} else if p == nil {
			return nil, errNotFound("Playlist")
		}
		p.entries, p.tail = nil, nil
	} else {
		p = &playlist{file: playlistFile(name), name: name}
	}
	p.entries = a.songEntries(songIDs)
	if err := writePlaylist(root, p); err != nil {
		return nil, fileErr(err)
	}
	p, err := a.loadPlaylist(root, p.file)
	if err != nil {
		return nil, fileErr(err)
	}
	return p, nil
}

// updatePlaylist removes the given visible indexes (positions before the change), then appends
// the new songs, like Navidrome. Lines that match no song stay in the file untouched.
func (a *App) updatePlaylist(root, id string, name *string, add []string, remove []int) *apiError {
	a.plMu.Lock()
	defer a.plMu.Unlock()
	p, err := a.findPlaylist(root, id)
	if err != nil {
		return fileErr(err)
	} else if p == nil {
		return errNotFound("Playlist")
	}
	vis := p.visible()
	drop := map[*plEntry]bool{}
	for _, i := range remove {
		if i >= 0 && i < len(vis) {
			drop[vis[i]] = true
		}
	}
	kept := p.entries[:0]
	for _, e := range p.entries {
		if !drop[e] {
			kept = append(kept, e)
		}
	}
	p.entries = append(kept, a.songEntries(add)...)

	dir := playlistDir(root)
	oldFile := p.file
	if name != nil && strings.TrimSpace(*name) != "" && *name != p.name {
		newFile := playlistFile(*name)
		switch {
		case newFile == oldFile:
		case strings.EqualFold(newFile, oldFile): // only the letter case changes
			if err := os.Rename(filepath.Join(dir, oldFile), filepath.Join(dir, newFile)); err != nil {
				return fileErr(err)
			}
			oldFile = newFile
		default:
			if _, err := os.Stat(filepath.Join(dir, newFile)); err == nil {
				return &apiError{0, fmt.Sprintf("A playlist file named %q already exists", newFile)}
			}
		}
		p.name, p.file = *name, newFile
	}
	if err := writePlaylist(root, p); err != nil {
		return fileErr(err)
	}
	if oldFile != p.file {
		if err := os.Remove(filepath.Join(dir, oldFile)); err != nil {
			return fileErr(err)
		}
	}
	return nil
}

func (a *App) deletePlaylist(root, id string) *apiError {
	a.plMu.Lock()
	defer a.plMu.Unlock()
	p, err := a.findPlaylist(root, id)
	if err != nil {
		return fileErr(err)
	} else if p == nil {
		return errNotFound("Playlist")
	}
	if err := os.Remove(filepath.Join(playlistDir(root), p.file)); err != nil {
		return fileErr(err)
	}
	return nil
}
