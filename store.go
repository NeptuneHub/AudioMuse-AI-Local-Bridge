package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS songs (
	id TEXT PRIMARY KEY, path TEXT NOT NULL UNIQUE,
	size INTEGER NOT NULL, mtime INTEGER NOT NULL, created INTEGER NOT NULL,
	title TEXT NOT NULL, album TEXT NOT NULL, album_id TEXT NOT NULL,
	artist TEXT NOT NULL, artist_id TEXT NOT NULL, artists TEXT NOT NULL,
	album_artist TEXT NOT NULL, album_artist_id TEXT NOT NULL, album_artists TEXT NOT NULL,
	track INTEGER NOT NULL, disc INTEGER NOT NULL, year INTEGER NOT NULL, genres TEXT NOT NULL,
	duration REAL NOT NULL, bitrate INTEGER NOT NULL, samplerate INTEGER NOT NULL, channels INTEGER NOT NULL,
	suffix TEXT NOT NULL, compilation INTEGER NOT NULL, search TEXT NOT NULL,
	missing INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS songs_album ON songs(album_id);
CREATE TABLE IF NOT EXISTS albums (
	id TEXT PRIMARY KEY, name TEXT NOT NULL, artist TEXT NOT NULL, artist_id TEXT NOT NULL,
	artists TEXT NOT NULL, genres TEXT NOT NULL, year INTEGER NOT NULL, song_count INTEGER NOT NULL,
	duration REAL NOT NULL, created INTEGER NOT NULL, compilation INTEGER NOT NULL, search TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS albums_created ON albums(created);
CREATE TABLE IF NOT EXISTS artists (
	id TEXT PRIMARY KEY, name TEXT NOT NULL, album_count INTEGER NOT NULL, search TEXT NOT NULL
);`

type IDName struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Song is one audio file. Path is relative to the music folder, with "/" separators.
type Song struct {
	ID, Path                      string
	Size, MTime, Created          int64 // MTime in ns, Created in ms
	Title, Album, AlbumID         string
	Artist, ArtistID              string
	Artists                       []IDName
	AlbumArtist, AlbumArtistID    string
	AlbumArtists                  []IDName
	Track, Disc, Year             int
	Genres                        []string
	Duration                      float64
	BitRate, SampleRate, Channels int
	Suffix                        string
	Compilation                   bool
}

type Album struct {
	ID, Name, Artist, ArtistID string
	Artists                    []IDName
	Genres                     []string
	Year, SongCount            int
	Duration                   float64
	Created                    int64
	Compilation                bool
}

type Artist struct {
	ID, Name   string
	AlbumCount int
}

type Settings struct {
	MusicDir, Username, Password string
}

func (s Settings) ready() bool { return s.Username != "" && s.Password != "" }

type Store struct {
	db  *sql.DB
	key []byte
}

func openStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	key, err := loadKey(filepath.Join(dir, "bridge.key"))
	if err != nil {
		return nil, err
	}
	dsn := filepath.Join(dir, "bridge.db") + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, key: key}, nil
}

func (st *Store) Close() error { return st.db.Close() }

// loadKey returns the random key used to encrypt the password, creating it on first run.
func loadKey(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) == 32 {
		return b, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, os.WriteFile(path, key, 0o600)
}

func (st *Store) encrypt(plain string) (string, error) {
	block, err := aes.NewCipher(st.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (st *Store) decrypt(enc string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(st.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", errors.New("bad ciphertext")
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	return string(plain), err
}

func (st *Store) setting(key string) string {
	var v string
	_ = st.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v
}

func (st *Store) loadSettings() Settings {
	s := Settings{MusicDir: st.setting("music_dir"), Username: st.setting("username")}
	if enc := st.setting("password"); enc != "" {
		// If the key file was lost the password cannot be read: the user simply runs setup again.
		s.Password, _ = st.decrypt(enc)
	}
	return s
}

func (st *Store) saveSettings(s Settings) error {
	enc, err := st.encrypt(s.Password)
	if err != nil {
		return err
	}
	tx, err := st.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range map[string]string{"music_dir": s.MusicDir, "username": s.Username, "password": enc} {
		if _, err := tx.Exec(`INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const songCols = `id, path, size, mtime, created, title, album, album_id, artist, artist_id, artists,
	album_artist, album_artist_id, album_artists, track, disc, year, genres, duration, bitrate,
	samplerate, channels, suffix, compilation`

const albumCols = `id, name, artist, artist_id, artists, genres, year, song_count, duration, created, compilation`

type rowScanner interface{ Scan(...any) error }

func scanSong(r rowScanner) (*Song, error) {
	s := &Song{}
	var artists, albumArtists, genres string
	var comp int
	err := r.Scan(&s.ID, &s.Path, &s.Size, &s.MTime, &s.Created, &s.Title, &s.Album, &s.AlbumID,
		&s.Artist, &s.ArtistID, &artists, &s.AlbumArtist, &s.AlbumArtistID, &albumArtists,
		&s.Track, &s.Disc, &s.Year, &genres, &s.Duration, &s.BitRate, &s.SampleRate, &s.Channels,
		&s.Suffix, &comp)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(artists), &s.Artists)
	_ = json.Unmarshal([]byte(albumArtists), &s.AlbumArtists)
	_ = json.Unmarshal([]byte(genres), &s.Genres)
	s.Compilation = comp != 0
	return s, nil
}

func scanAlbum(r rowScanner) (*Album, error) {
	a := &Album{}
	var artists, genres string
	var comp int
	err := r.Scan(&a.ID, &a.Name, &a.Artist, &a.ArtistID, &artists, &genres, &a.Year, &a.SongCount,
		&a.Duration, &a.Created, &comp)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(artists), &a.Artists)
	_ = json.Unmarshal([]byte(genres), &a.Genres)
	a.Compilation = comp != 0
	return a, nil
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// allSongs returns every known song, including missing ones, keyed by relative path.
func (st *Store) allSongs() (map[string]*Song, error) {
	rows, err := st.db.Query(`SELECT ` + songCols + ` FROM songs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*Song{}
	for rows.Next() {
		s, err := scanSong(rows)
		if err != nil {
			return nil, err
		}
		out[s.Path] = s
	}
	return out, rows.Err()
}

// saveScan replaces the library in one transaction: readers keep seeing the old data until commit.
// Songs not in present are only marked missing, so their ids come back if the files return.
func (st *Store) saveScan(present []*Song, albums []*Album, artists []*Artist) error {
	tx, err := st.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE songs SET missing = 1`); err != nil {
		return err
	}
	ins, err := tx.Prepare(`INSERT INTO songs(` + songCols + `, search, missing)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0)
		ON CONFLICT(path) DO UPDATE SET size=excluded.size, mtime=excluded.mtime, created=excluded.created,
		title=excluded.title, album=excluded.album, album_id=excluded.album_id, artist=excluded.artist,
		artist_id=excluded.artist_id, artists=excluded.artists, album_artist=excluded.album_artist,
		album_artist_id=excluded.album_artist_id, album_artists=excluded.album_artists, track=excluded.track,
		disc=excluded.disc, year=excluded.year, genres=excluded.genres, duration=excluded.duration,
		bitrate=excluded.bitrate, samplerate=excluded.samplerate, channels=excluded.channels,
		suffix=excluded.suffix, compilation=excluded.compilation, search=excluded.search, missing=0`)
	if err != nil {
		return err
	}
	defer ins.Close()
	for _, s := range present {
		_, err := ins.Exec(s.ID, s.Path, s.Size, s.MTime, s.Created, s.Title, s.Album, s.AlbumID,
			s.Artist, s.ArtistID, jsonText(s.Artists), s.AlbumArtist, s.AlbumArtistID, jsonText(s.AlbumArtists),
			s.Track, s.Disc, s.Year, jsonText(s.Genres), s.Duration, s.BitRate, s.SampleRate, s.Channels,
			s.Suffix, boolInt(s.Compilation), searchWords(s.Title, s.Album, s.Artist, s.AlbumArtist))
		if err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM albums`); err != nil {
		return err
	}
	for _, a := range albums {
		_, err := tx.Exec(`INSERT INTO albums(`+albumCols+`, search) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			a.ID, a.Name, a.Artist, a.ArtistID, jsonText(a.Artists), jsonText(a.Genres), a.Year, a.SongCount,
			a.Duration, a.Created, boolInt(a.Compilation), searchWords(a.Name, a.Artist))
		if err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM artists`); err != nil {
		return err
	}
	for _, ar := range artists {
		if _, err := tx.Exec(`INSERT INTO artists(id, name, album_count, search) VALUES(?,?,?,?)`,
			ar.ID, ar.Name, ar.AlbumCount, searchWords(ar.Name)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func noRows[T any](v T, err error) (T, error) {
	if errors.Is(err, sql.ErrNoRows) {
		var zero T
		return zero, nil
	}
	return v, err
}

// song returns a present song by id, or nil.
func (st *Store) song(id string) (*Song, error) {
	return noRows(scanSong(st.db.QueryRow(`SELECT `+songCols+` FROM songs WHERE id = ? AND missing = 0`, id)))
}

// songByPath returns a present song by relative path, or nil.
func (st *Store) songByPath(rel string) (*Song, error) {
	return noRows(scanSong(st.db.QueryRow(`SELECT `+songCols+` FROM songs WHERE path = ? AND missing = 0`, rel)))
}

func (st *Store) songs(query string, args ...any) ([]*Song, error) {
	rows, err := st.db.Query(`SELECT `+songCols+` FROM songs `+query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Song
	for rows.Next() {
		s, err := scanSong(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (st *Store) albums(query string, args ...any) ([]*Album, error) {
	rows, err := st.db.Query(`SELECT `+albumCols+` FROM albums `+query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Album
	for rows.Next() {
		a, err := scanAlbum(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (st *Store) songsByAlbum(albumID string) ([]*Song, error) {
	return st.songs(`WHERE album_id = ? AND missing = 0 ORDER BY disc, track, title, path`, albumID)
}

func (st *Store) album(id string) (*Album, error) {
	return noRows(scanAlbum(st.db.QueryRow(`SELECT `+albumCols+` FROM albums WHERE id = ?`, id)))
}

func (st *Store) albumsNewest(offset, size int) ([]*Album, error) {
	return st.albums(`ORDER BY created DESC, id LIMIT ? OFFSET ?`, size, offset)
}

// searchFilter builds a word-prefix filter: every token must start a word of the search column.
func searchFilter(tokens []string) (string, []any) {
	var sb strings.Builder
	var args []any
	for _, t := range tokens {
		sb.WriteString(` AND search LIKE ? ESCAPE '\'`)
		t = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(t)
		args = append(args, "% "+t+"%")
	}
	return sb.String(), args
}

func (st *Store) searchSongs(tokens []string, offset, limit int) ([]*Song, error) {
	where, args := searchFilter(tokens)
	return st.songs(`WHERE missing = 0`+where+` ORDER BY rowid LIMIT ? OFFSET ?`, append(args, limit, offset)...)
}

func (st *Store) searchAlbums(tokens []string, offset, limit int) ([]*Album, error) {
	where, args := searchFilter(tokens)
	return st.albums(`WHERE 1 = 1`+where+` ORDER BY rowid LIMIT ? OFFSET ?`, append(args, limit, offset)...)
}

func (st *Store) searchArtists(tokens []string, offset, limit int) ([]*Artist, error) {
	where, args := searchFilter(tokens)
	rows, err := st.db.Query(`SELECT id, name, album_count FROM artists WHERE 1 = 1`+where+
		` ORDER BY rowid LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Artist
	for rows.Next() {
		a := &Artist{}
		if err := rows.Scan(&a.ID, &a.Name, &a.AlbumCount); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (st *Store) counts() (artists, albums, songs int) {
	_ = st.db.QueryRow(`SELECT (SELECT count(*) FROM artists), (SELECT count(*) FROM albums),
		(SELECT count(*) FROM songs WHERE missing = 0)`).Scan(&artists, &albums, &songs)
	return
}
