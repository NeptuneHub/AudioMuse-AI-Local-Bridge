# AudioMuse-AI-Local-Bridge

## What it is

AudioMuse-AI Local Bridge is a small program that lets AudioMuse-AI use the music files on your computer. It reads the tags of the songs in your music folder and shares them with AudioMuse-AI using the same API as a Navidrome server, so you do not need a real music server. Artists and albums come from the song tags, the same way Navidrome does it. Playlists made by AudioMuse-AI are saved as `.m3u8` files in your music folder, so other music players can open them too.

## Install and use

1. Download the zip for your system from the Releases page and unzip it.
2. Double-click the program. Your browser opens at `http://localhost:4520`. Keep the program window open.
3. Choose your music folder, a username and a password, then click "Save and scan". The page shows the progress and how many artists, albums and songs were found.
4. In AudioMuse-AI, add a music server of type Navidrome with the address shown on the page and the same username and password. Use `http://127.0.0.1:4520` if AudioMuse-AI runs on the same computer, or `http://your-ip:4520` if it runs elsewhere

The first time, macOS can block the program because it is not signed. Open System Settings, then Privacy & Security, and click "Open Anyway". On Windows, click "More info" and then "Run anyway".

## For developers

You need Go 1.26 or newer. No C compiler is needed, because SQLite and TagLib are pure Go.

```
go test ./...
go build
```

To build for another system, set `GOOS` and `GOARCH`, for example `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build`. The program accepts `-port` (default 4520), `-data` (folder for the settings) and `-no-browser`. When you push a tag like `v1.0.0`, GitHub Actions builds the zips for macOS, Windows and Linux and attaches them to a release.
