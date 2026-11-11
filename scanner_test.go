package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"go.senan.xyz/taglib"
)

// writeWAV makes a 1 second silent WAV file and tags it.
func writeWAV(t *testing.T, path string, tags map[string][]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const n = 8000
	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("RIFF")
	_ = binary.Write(&b, le, uint32(36+n*2))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(8000), uint32(16000), uint16(2), uint16(16)} {
		_ = binary.Write(&b, le, v)
	}
	b.WriteString("data")
	_ = binary.Write(&b, le, uint32(n*2))
	b.Write(make([]byte, n*2))
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(tags) > 0 {
		if err := taglib.WriteTags(path, tags, 0); err != nil {
			t.Fatal(err)
		}
	}
}

func tags(kv ...string) map[string][]string {
	m := map[string][]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = append(m[kv[i]], kv[i+1])
	}
	return m
}

// testApp creates an app with a temp data folder and a temp music folder, already set up.
func testApp(t *testing.T) (*App, string) {
	t.Helper()
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	music := t.TempDir()
	a := newApp(st, defaultPort)
	if err := a.save(Settings{MusicDir: music, Username: "admin", Password: "secret12"}); err != nil {
		t.Fatal(err)
	}
	return a, music
}

func scan(t *testing.T, a *App) ScanStatus {
	t.Helper()
	a.scanner.Start(a.cfg().MusicDir)
	a.scanner.Wait()
	s := a.scanner.Status()
	if s.State != "done" {
		t.Fatalf("scan state %q: %s", s.State, s.Error)
	}
	return s
}

func TestMapSongFallbacks(t *testing.T) {
	s := mapSong(tagMap{}, "Pink Floyd/The Wall/CD1/01 - In the Flesh.flac")
	if s.Title != "01 - In the Flesh" {
		t.Errorf("title from file name, got %q", s.Title)
	}
	if s.Album != unknownAlbum || s.Artist != unknownArtist || s.AlbumArtist != unknownArtist {
		t.Errorf("fallbacks: album %q artist %q albumArtist %q", s.Album, s.Artist, s.AlbumArtist)
	}
	if len(s.Genres) != 0 || s.Genres == nil {
		t.Errorf("genres must be an empty list, got %#v", s.Genres)
	}
}

func TestMapSongArtists(t *testing.T) {
	s := mapSong(tagMap{"artist": {"Alpha feat. Beta"}, "album": {"X"}}, "a.mp3")
	if s.Artist != "Alpha feat. Beta" || len(s.Artists) != 2 || s.Artists[0].Name != "Alpha" || s.Artists[1].Name != "Beta" {
		t.Errorf("artist split: %q %+v", s.Artist, s.Artists)
	}
	if s.AlbumArtist != "Alpha" || s.ArtistID != artistID("Alpha") {
		t.Errorf("album artist falls back to first track artist, got %q", s.AlbumArtist)
	}
	c := mapSong(tagMap{"artist": {"Someone"}, "compilation": {"1"}}, "b.mp3")
	if c.AlbumArtist != variousArtists {
		t.Errorf("compilation album artist, got %q", c.AlbumArtist)
	}
	m := mapSong(tagMap{"artist": {"A / B"}, "artists": {"A", "B"}}, "c.mp3")
	if len(m.Artists) != 2 || m.Artist != "A / B" {
		t.Errorf("multi-valued artists tag: %q %+v", m.Artist, m.Artists)
	}
}

func TestMapSongAlbumGrouping(t *testing.T) {
	cd1 := mapSong(tagMap{"album": {"The Wall"}, "albumartist": {"Pink Floyd"}, "discnumber": {"1"}}, "PF/The Wall/CD1/a.flac")
	cd2 := mapSong(tagMap{"album": {"The Wall"}, "albumartist": {"Pink Floyd"}, "discnumber": {"2"}}, "PF/The Wall/CD2/b.flac")
	other := mapSong(tagMap{"album": {"Animals"}, "albumartist": {"Pink Floyd"}}, "PF/Animals/c.flac")
	if cd1.AlbumID != cd2.AlbumID {
		t.Error("discs with the same tags must be one album")
	}
	if cd1.AlbumID == other.AlbumID {
		t.Error("different albums must have different ids")
	}
	m1 := mapSong(tagMap{"album": {"A"}, "musicbrainz_albumid": {"abc"}}, "x.flac")
	m2 := mapSong(tagMap{"album": {"B"}, "musicbrainz_albumid": {"abc"}}, "y.flac")
	if m1.AlbumID != m2.AlbumID {
		t.Error("same MusicBrainz album id must be one album")
	}
	if len(cd1.AlbumID) != 22 {
		t.Errorf("ids are 22 base62 chars, got %q", cd1.AlbumID)
	}
}

func TestMapSongNumbers(t *testing.T) {
	s := mapSong(tagMap{"tracknumber": {"3/12"}, "discnumber": {"2/2"}, "date": {"2011-05-02"}, "originaldate": {"1973"},
		"genre": {"Rock; Pop", "rock"}}, "a.flac")
	if s.Track != 3 || s.Disc != 2 {
		t.Errorf("track %d disc %d", s.Track, s.Disc)
	}
	if s.Year != 1973 {
		t.Errorf("year from original date (Navidrome rule), got %d", s.Year)
	}
	if len(s.Genres) != 2 || s.Genres[0] != "Rock" || s.Genres[1] != "Pop" {
		t.Errorf("genres %v", s.Genres)
	}
	if y := mapSong(tagMap{"date": {"2011"}}, "b.flac").Year; y != 2011 {
		t.Errorf("year %d", y)
	}
}

func TestScanAndRescan(t *testing.T) {
	a, music := testApp(t)
	writeWAV(t, filepath.Join(music, "Artist/Album/01 - One.wav"), tags("TITLE", "One", "ARTIST", "Artist", "ALBUM", "Album", "TRACKNUMBER", "1"))
	writeWAV(t, filepath.Join(music, "Artist/Album/02 - Two.wav"), tags("TITLE", "Two", "ARTIST", "Artist", "ALBUM", "Album", "TRACKNUMBER", "2"))
	writeWAV(t, filepath.Join(music, "loose.wav"), nil)
	writeWAV(t, filepath.Join(music, ".hidden/h.wav"), nil)
	writeWAV(t, filepath.Join(music, "@eaDir/e.wav"), nil)
	writeWAV(t, filepath.Join(music, "Ignored/i.wav"), nil)
	_ = os.WriteFile(filepath.Join(music, "Ignored/.ndignore"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(music, "notes.txt"), []byte("x"), 0o644)

	s := scan(t, a)
	if s.Songs != 3 || s.Albums != 2 || s.Artists != 2 {
		t.Fatalf("counts: %+v", s)
	}
	loose, _ := a.store.songByPath("loose.wav")
	if loose == nil || loose.Title != "loose" || loose.Album != unknownAlbum || loose.Duration != 1 {
		t.Fatalf("untagged song: %+v", loose)
	}
	one, _ := a.store.songByPath("Artist/Album/01 - One.wav")
	if one.ID != newHash("Artist/Album/01 - One.wav") {
		t.Fatalf("song id must be the hash of the relative path, got %q", one.ID)
	}

	// A rescan keeps ids. A deleted file is hidden; when it comes back it gets its old id.
	scan(t, a)
	again, _ := a.store.songByPath("Artist/Album/01 - One.wav")
	if again.ID != one.ID {
		t.Fatal("rescan changed the song id")
	}
	data, _ := os.ReadFile(filepath.Join(music, "Artist/Album/01 - One.wav"))
	_ = os.Remove(filepath.Join(music, "Artist/Album/01 - One.wav"))
	if s := scan(t, a); s.Songs != 2 {
		t.Fatalf("missing file still counted: %+v", s)
	}
	if gone, _ := a.store.song(one.ID); gone != nil {
		t.Fatal("missing song must be hidden")
	}
	_ = os.WriteFile(filepath.Join(music, "Artist/Album/01 - One.wav"), data, 0o644)
	scan(t, a)
	if back, _ := a.store.songByPath("Artist/Album/01 - One.wav"); back == nil || back.ID != one.ID {
		t.Fatal("returning file must keep its old id")
	}

	// Databases of older versions have random ids: the next scan replaces them.
	if _, err := a.store.db.Exec(`UPDATE songs SET id = 'old-random-id' WHERE path = ?`, "Artist/Album/01 - One.wav"); err != nil {
		t.Fatal(err)
	}
	scan(t, a)
	if s, _ := a.store.songByPath("Artist/Album/01 - One.wav"); s == nil || s.ID != one.ID {
		t.Fatalf("old random id not replaced: %+v", s)
	}
}

func TestScanMissingFolderChangesNothing(t *testing.T) {
	a, music := testApp(t)
	writeWAV(t, filepath.Join(music, "a.wav"), nil)
	scan(t, a)
	a.scanner.Start(filepath.Join(music, "does-not-exist"))
	a.scanner.Wait()
	if st := a.scanner.Status(); st.State != "error" {
		t.Fatalf("expected error, got %+v", st)
	}
	if _, _, n := a.store.counts(); n != 1 {
		t.Fatalf("library changed after a failed scan: %d songs", n)
	}
}

func TestCheckScansOnlyWhenFilesChange(t *testing.T) {
	a, music := testApp(t)
	writeWAV(t, filepath.Join(music, "a.wav"), tags("TITLE", "A"))
	scan(t, a)

	// Nothing changed: the check stops after listing the files and the status stays the same.
	a.scanner.update(func(s *ScanStatus) { s.FinishedAt = 1 }) // a real scan would change it
	a.scanner.Check(music)
	a.scanner.Wait()
	if s := a.scanner.Status(); s.State != "done" || s.FinishedAt != 1 || s.Songs != 1 {
		t.Fatalf("check without changes: %+v", s)
	}

	// A new file: the check scans and finds it.
	writeWAV(t, filepath.Join(music, "b.wav"), tags("TITLE", "B"))
	a.scanner.Check(music)
	a.scanner.Wait()
	if s := a.scanner.Status(); s.State != "done" || s.Songs != 2 {
		t.Fatalf("check after adding a file: %+v", s)
	}

	// A missing folder shows the error once, and the check scans again when it is back.
	moved := music + "-moved"
	if err := os.Rename(music, moved); err != nil {
		t.Fatal(err)
	}
	a.scanner.Check(music)
	a.scanner.Wait()
	if s := a.scanner.Status(); s.State != "error" {
		t.Fatalf("check with the folder missing: %+v", s)
	}
	if err := os.Rename(moved, music); err != nil {
		t.Fatal(err)
	}
	a.scanner.Check(music)
	a.scanner.Wait()
	if s := a.scanner.Status(); s.State != "done" || s.Songs != 2 {
		t.Fatalf("check after the folder is back: %+v", s)
	}
}
