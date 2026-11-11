package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type apiClient struct {
	t   *testing.T
	srv *httptest.Server
}

func newAPI(t *testing.T) (*apiClient, *App, string) {
	a, music := testApp(t)
	writeWAV(t, filepath.Join(music, "Artist/Album/01 - One.wav"), tags("TITLE", "One", "ARTIST", "Artist", "ALBUM", "Album", "TRACKNUMBER", "1", "GENRE", "Rock"))
	writeWAV(t, filepath.Join(music, "Artist/Album/02 - Two.wav"), tags("TITLE", "Two", "ARTIST", "Artist", "ALBUM", "Album", "TRACKNUMBER", "2"))
	writeWAV(t, filepath.Join(music, "Other/Best/Hello World.wav"), tags("TITLE", "Hello World", "ARTIST", "Other", "ALBUM", "Best"))
	writeWAV(t, filepath.Join(music, "loose.wav"), nil)
	scan(t, a)
	srv := httptest.NewServer(a.handler())
	t.Cleanup(srv.Close)
	return &apiClient{t, srv}, a, music
}

// raw calls an endpoint with exactly the given params and returns the subsonic-response object.
func (c *apiClient) raw(endpoint string, params url.Values) map[string]any {
	c.t.Helper()
	resp, err := http.Get(c.srv.URL + "/rest/" + endpoint + ".view?" + params.Encode())
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		c.t.Fatalf("%s: HTTP %d %s", endpoint, resp.StatusCode, body)
	}
	assertNoNull(c.t, endpoint, body)
	var out map[string]map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		c.t.Fatalf("%s: %v %s", endpoint, err, body)
	}
	return out["subsonic-response"]
}

// call sends the same auth params AudioMuse-AI sends (u, p=enc:hex, v, c, f=json).
func (c *apiClient) call(endpoint string, kv ...string) map[string]any {
	c.t.Helper()
	p := url.Values{"u": {"admin"}, "p": {"enc:" + hex.EncodeToString([]byte("secret12"))}, "v": {"1.16.1"}, "c": {"AudioMuse-AI"}, "f": {"json"}}
	for i := 0; i+1 < len(kv); i += 2 {
		p.Add(kv[i], kv[i+1])
	}
	return c.raw(endpoint, p)
}

func (c *apiClient) ok(endpoint string, kv ...string) map[string]any {
	c.t.Helper()
	r := c.call(endpoint, kv...)
	if r["status"] != "ok" {
		c.t.Fatalf("%s failed: %v", endpoint, r["error"])
	}
	return r
}

func errCode(r map[string]any) int {
	if e, ok := r["error"].(map[string]any); ok {
		return int(e["code"].(float64))
	}
	return -1
}

func assertNoNull(t *testing.T, where string, body []byte) {
	t.Helper()
	var v any
	_ = json.Unmarshal(body, &v)
	var walk func(any, string)
	walk = func(x any, p string) {
		switch x := x.(type) {
		case nil:
			t.Errorf("%s: null value at %s", where, p)
		case map[string]any:
			for k, v := range x {
				walk(v, p+"."+k)
			}
		case []any:
			for i, v := range x {
				walk(v, fmt.Sprintf("%s[%d]", p, i))
			}
		}
	}
	walk(v, "")
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func TestAuth(t *testing.T) {
	c, _, _ := newAPI(t)
	base := func() url.Values { return url.Values{"u": {"admin"}, "v": {"1.16.1"}, "c": {"test"}, "f": {"json"}} }

	p := base()
	p.Set("p", "secret12")
	if r := c.raw("getMusicFolders", p); r["status"] != "ok" {
		t.Errorf("plain password: %v", r["error"])
	}
	p = base()
	sum := md5.Sum([]byte("secret12" + "salt1"))
	p.Set("t", hex.EncodeToString(sum[:]))
	p.Set("s", "salt1")
	if r := c.raw("getMusicFolders", p); r["status"] != "ok" {
		t.Errorf("token auth: %v", r["error"])
	}
	if r := c.call("getMusicFolders"); r["status"] != "ok" {
		t.Errorf("enc: password: %v", r["error"])
	}
	p = base()
	p.Del("u")
	p.Set("p", "secret12")
	if code := errCode(c.raw("getMusicFolders", p)); code != 10 {
		t.Errorf("missing u: code %d", code)
	}
	if code := errCode(c.raw("getMusicFolders", url.Values{"apiKey": {"k"}, "v": {"1"}, "c": {"x"}})); code != 44 {
		t.Errorf("apiKey: code %d", code)
	}
	if code := errCode(c.raw("getMusicFolders", url.Values{"apiKey": {"k"}, "u": {"admin"}})); code != 43 {
		t.Errorf("apiKey + u: code %d", code)
	}
	for i := 0; i < 5; i++ {
		p = base()
		p.Set("p", "wrong")
		if code := errCode(c.raw("getMusicFolders", p)); code != 40 {
			t.Errorf("wrong password: code %d", code)
		}
	}
	if code := errCode(c.call("getMusicFolders")); code != 40 {
		t.Errorf("after 5 failures the client must be blocked, got code %d", code)
	}
}

func TestBrowsing(t *testing.T) {
	c, _, music := newAPI(t)
	r := c.ok("getMusicFolders")
	if f := list(obj(r["musicFolders"])["musicFolder"]); len(f) != 1 || obj(f[0])["name"] != "Music Library" {
		t.Errorf("music folders: %v", r["musicFolders"])
	}
	if r["openSubsonic"] != true || r["version"] != "1.16.1" {
		t.Errorf("envelope: %v", r)
	}

	albums := list(obj(c.ok("getAlbumList2", "type", "newest", "size", "500", "offset", "0")["albumList2"])["album"])
	if len(albums) != 3 {
		t.Fatalf("newest albums: %d", len(albums))
	}
	if al := obj(c.ok("getAlbumList2", "type", "frequent", "size", "10")["albumList2"]); len(al) != 0 {
		t.Errorf("frequent must be an empty albumList2, got %v", al)
	}
	if code := errCode(c.call("getAlbumList2", "type", "random")); code != 0 {
		t.Errorf("unsupported type: code %d", code)
	}
	if code := errCode(c.call("getAlbumList2", "type", "newest", "musicFolderId", "2")); code != 70 {
		t.Errorf("unknown folder: code %d", code)
	}

	var albumID string
	for _, a := range albums {
		if obj(a)["name"] == "Album" {
			albumID = obj(a)["id"].(string)
		}
	}
	album := obj(c.ok("getAlbum", "id", albumID)["album"])
	songs := list(album["song"])
	if len(songs) != 2 || obj(songs[0])["title"] != "One" || album["songCount"].(float64) != 2 {
		t.Fatalf("album: %v", album)
	}
	for _, k := range []string{"id", "name", "songCount", "duration", "created", "artists", "genres", "displayArtist"} {
		if _, ok := album[k]; !ok {
			t.Errorf("album field %q missing", k)
		}
	}
	song := obj(c.ok("getSong", "id", obj(songs[0])["id"].(string))["song"])
	for _, k := range []string{"id", "isDir", "title", "path", "suffix", "albumId", "artistId", "mediaType", "genres", "artists", "albumArtists", "displayArtist", "displayAlbumArtist", "samplingRate", "channelCount"} {
		if _, ok := song[k]; !ok {
			t.Errorf("song field %q missing", k)
		}
	}
	for _, k := range []string{"playCount", "played", "userRating", "starred", "averageRating", "bookmarkPosition"} {
		if _, ok := song[k]; ok {
			t.Errorf("song field %q must be absent", k)
		}
	}
	if song["path"] != filepath.Join(music, "Artist/Album/01 - One.wav") || song["genre"] != "Rock" || song["duration"].(float64) != 1 {
		t.Errorf("song: %v", song)
	}
	if code := errCode(c.call("getSong", "id", "nope")); code != 70 {
		t.Errorf("unknown song: code %d", code)
	}
	if code := errCode(c.call("getAlbum")); code != 10 {
		t.Errorf("missing id: code %d", code)
	}
}

func TestSearch3(t *testing.T) {
	c, _, _ := newAPI(t)
	seen := map[string]bool{}
	for offset := 0; ; offset += 2 {
		res := obj(c.ok("search3", "query", "", "songCount", "2", "songOffset", fmt.Sprint(offset))["searchResult3"])
		songs := list(res["song"])
		for _, s := range songs {
			seen[obj(s)["id"].(string)] = true
		}
		if len(songs) < 2 {
			break
		}
	}
	if len(seen) != 4 {
		t.Errorf("empty query must page through all 4 songs, got %d", len(seen))
	}
	res := obj(c.ok("search3", "query", `""`, "songCount", "100", "artistCount", "0", "albumCount", "0")["searchResult3"])
	if len(list(res["song"])) != 4 || res["artist"] != nil || res["album"] != nil {
		t.Errorf(`query "" with counts 0: %v`, res)
	}
	res = obj(c.ok("search3", "query", "hel wor", "albumCount", "10", "songCount", "10", "artistCount", "0")["searchResult3"])
	if s := list(res["song"]); len(s) != 1 || obj(s[0])["title"] != "Hello World" {
		t.Errorf("word prefix search: %v", res)
	}
	res = obj(c.ok("search3", "query", "best", "albumCount", "10", "songCount", "0", "artistCount", "0")["searchResult3"])
	if al := list(res["album"]); len(al) != 1 || obj(al[0])["name"] != "Best" {
		t.Errorf("album search: %v", res)
	}
}

func TestStream(t *testing.T) {
	c, a, music := newAPI(t)
	s, _ := a.store.songByPath("loose.wav")
	q := url.Values{"u": {"admin"}, "p": {"secret12"}, "v": {"1.16.1"}, "c": {"t"}, "id": {s.ID}}
	resp, err := http.Get(c.srv.URL + "/rest/stream?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	want, _ := os.ReadFile(filepath.Join(music, "loose.wav"))
	if string(got) != string(want) || resp.Header.Get("Content-Type") != "audio/x-wav" {
		t.Fatalf("stream: %d bytes, type %q", len(got), resp.Header.Get("Content-Type"))
	}
	req, _ := http.NewRequest("GET", c.srv.URL+"/rest/stream.view?"+q.Encode(), nil)
	req.Header.Set("Range", "bytes=0-9")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || len(part) != 10 {
		t.Errorf("range: HTTP %d, %d bytes", resp.StatusCode, len(part))
	}
}

func TestPlaylists(t *testing.T) {
	c, a, music := newAPI(t)
	all := list(obj(c.ok("search3", "query", "", "songCount", "100", "artistCount", "0", "albumCount", "0")["searchResult3"])["song"])
	var ids []string
	for _, s := range all {
		ids = append(ids, obj(s)["id"].(string))
	}

	// Create, like AudioMuse-AI: name + songId list, then fill with updatePlaylist in batches.
	r := c.ok("createPlaylist", "name", "a/b:c_automatic", "songId", ids[0], "songId", "unknown-id")
	pl := obj(r["playlist"])
	id := pl["id"].(string)
	if pl["songCount"].(float64) != 1 || len(list(pl["entry"])) != 1 {
		t.Fatalf("created playlist: %v", pl)
	}
	file := filepath.Join(music, "a_b_c_automatic.m3u8")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("playlist file not created: %v", err)
	}
	if !strings.HasPrefix(string(data), "#EXTM3U\n#PLAYLIST:a/b:c_automatic\n#EXTINF:1,") || !strings.Contains(string(data), "\n"+all[0].(map[string]any)["path"].(string)[len(music)+1:]+"\n") {
		t.Fatalf("file content:\n%s", data)
	}
	var add []string
	for i := 0; i < 45; i++ {
		add = append(add, "songIdToAdd", ids[i%len(ids)])
	}
	c.ok("updatePlaylist", append([]string{"playlistId", id, "public", "true"}, add...)...)
	if n := obj(c.ok("getPlaylist", "id", id)["playlist"])["songCount"].(float64); n != 46 {
		t.Fatalf("after adding 45 songs: %v", n)
	}

	// The name AudioMuse-AI sent comes back exactly.
	pls := list(obj(c.ok("getPlaylists")["playlists"])["playlist"])
	if len(pls) != 1 || obj(pls[0])["name"] != "a/b:c_automatic" || obj(pls[0])["owner"] != "admin" {
		t.Fatalf("getPlaylists: %v", pls)
	}

	// Clear by indexes, highest first, like AudioMuse-AI; then add new songs.
	var rm []string
	for i := 45; i >= 0; i-- {
		rm = append(rm, "songIndexToRemove", fmt.Sprint(i))
	}
	c.ok("updatePlaylist", append([]string{"playlistId", id, "songIdToAdd", ids[1]}, rm...)...)
	entries := list(obj(c.ok("getPlaylist", "id", id)["playlist"])["entry"])
	if len(entries) != 1 || obj(entries[0])["id"] != ids[1] {
		t.Fatalf("remove then add: %v", entries)
	}

	// Lines that match no song stay in the file and are not shown.
	data, _ = os.ReadFile(file)
	_ = os.WriteFile(file, append(data, []byte("#EXTINF:10,Gone\nnot/here.mp3\n")...), 0o644)
	c.ok("updatePlaylist", "playlistId", id, "songIdToAdd", ids[2])
	data, _ = os.ReadFile(file)
	if !strings.Contains(string(data), "not/here.mp3") {
		t.Error("unknown line was removed from the file")
	}
	if pl := obj(c.ok("getPlaylist", "id", id)["playlist"]); pl["songCount"].(float64) != 2 {
		t.Errorf("unknown line must be hidden: %v", pl)
	}

	// A playlist made by hand is listed, with its file name as name.
	_ = os.WriteFile(filepath.Join(music, "Mine.m3u8"), []byte("Artist/Album/02 - Two.wav\r\n"), 0o644)
	pls = list(obj(c.ok("getPlaylists")["playlists"])["playlist"])
	if len(pls) != 2 || obj(pls[1])["name"] != "Mine" || obj(pls[1])["songCount"].(float64) != 1 {
		t.Errorf("hand-made playlist: %v", pls)
	}

	// Rename moves the file. Delete removes it.
	c.ok("updatePlaylist", "playlistId", id, "name", "Renamed")
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("old file still exists after rename")
	}
	newID := playlistID("Renamed.m3u8")
	if pl := obj(c.ok("getPlaylist", "id", newID)["playlist"]); pl["name"] != "Renamed" {
		t.Errorf("renamed: %v", pl)
	}
	c.ok("deletePlaylist", "id", newID)
	if _, err := os.Stat(filepath.Join(music, "Renamed.m3u8")); !os.IsNotExist(err) {
		t.Error("file still exists after delete")
	}
	if code := errCode(c.call("getPlaylist", "id", newID)); code != 70 {
		t.Errorf("deleted playlist: code %d", code)
	}

	// The scanner never treats playlist files as songs.
	if s := scan(t, a); s.Songs != 4 {
		t.Errorf("songs after playlists: %d", s.Songs)
	}
}

func TestLyrics(t *testing.T) {
	c, a, music := newAPI(t)
	s, _ := a.store.songByPath("loose.wav")
	if ll := obj(c.ok("getLyricsBySongId", "id", s.ID)["lyricsList"]); len(ll) != 0 {
		t.Errorf("no lyrics must give an empty lyricsList, got %v", ll)
	}
	_ = os.WriteFile(filepath.Join(music, "loose.lrc"), []byte("[ar:Me]\n[00:12.50]Second\n[00:01.00]First\n"), 0o644)
	l := obj(list(obj(c.ok("getLyricsBySongId", "id", s.ID)["lyricsList"])["structuredLyrics"])[0])
	lines := list(l["line"])
	if l["synced"] != true || l["displayArtist"] != "Me" || len(lines) != 2 || obj(lines[0])["value"] != "First" || obj(lines[1])["start"].(float64) != 12500 {
		t.Errorf("lrc: %v", l)
	}
	one, _ := a.store.songByPath("Artist/Album/02 - Two.wav")
	_ = os.WriteFile(filepath.Join(music, "Artist/Album/02 - Two.txt"), []byte("line one\n\nline two\n"), 0o644)
	l = obj(list(obj(c.ok("getLyricsBySongId", "id", one.ID)["lyricsList"])["structuredLyrics"])[0])
	if l["synced"] != false || len(list(l["line"])) != 2 {
		t.Errorf("txt: %v", l)
	}
}
