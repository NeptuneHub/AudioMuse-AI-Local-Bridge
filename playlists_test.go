package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPlaylistFile(t *testing.T) {
	cases := map[string]string{
		"Rock Mix_automatic": "Rock Mix_automatic.m3u8",
		"a/b:c*?":            "a_b_c__.m3u8",
		".hidden":            "_hidden.m3u8",
		"trailing. ":         "trailing.m3u8",
		"":                   "Playlist.m3u8",
		"con":                "con_.m3u8",
		"line\nbreak":        "line_break.m3u8",
	}
	for in, want := range cases {
		if got := playlistFile(in); got != want {
			t.Errorf("playlistFile(%q) = %q, want %q", in, got, want)
		}
	}
	if got := playlistFile(strings.Repeat("é", 300)); len(got) > 205 {
		t.Errorf("long name not capped: %d bytes", len(got))
	}
}

func TestEntryRel(t *testing.T) {
	root := t.TempDir()
	dir := playlistDir(root)
	cases := map[string]string{
		"../Artist/Album/a.flac":                       "Artist/Album/a.flac",
		`..\Artist\Album\a.flac`:                       "Artist/Album/a.flac",
		"mix.mp3":                                      "Playlists/mix.mp3",
		"./sub/x.mp3":                                  "Playlists/sub/x.mp3",
		filepath.Join(root, "Artist", "b.mp3"):         "Artist/b.mp3",
		"file://" + filepath.ToSlash(root) + "/c.mp3":  "c.mp3",
		"../../outside.mp3":                            "",
		"..":                                           "",
		"http://radio.example/stream":                  "",
		filepath.Join(filepath.Dir(root), "other.mp3"): "",
	}
	for in, want := range cases {
		if got := entryRel(root, dir, in); got != want {
			t.Errorf("entryRel(%q) = %q, want %q", in, got, want)
		}
	}
}
