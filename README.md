# AudioMuse-AI-Local-Bridge

## What it is

AudioMuse-AI Local Bridge is a small program that lets AudioMuse-AI use the music files on your computer. It reads the tags of the songs in your music folder and shares them with AudioMuse-AI using the same API as a Navidrome server, so you do not need a real music server. Artists and albums come from the song tags, the same way Navidrome does it. Playlists made by AudioMuse-AI are saved as `.m3u8` files in a `Playlists` folder inside your music folder, so other music players can open them too.

## Install and use

1. Download the zip for your system from the Releases page and unzip it. On macOS, move "AudioMuse-AI Local Bridge" to the Applications folder.
2. Open the program. The AudioMuse-AI logo appears in the menu bar (macOS) or in the system tray (Windows, Linux), and your browser opens at `http://localhost:4520`.
3. Choose your music folder, a username and a password, then click "Save and scan". The page shows the progress and how many artists, albums and songs were found. Later, new or changed songs are found automatically every 5 minutes.
4. In AudioMuse-AI, add a music server of type Navidrome with the address shown on the page and the same username and password. Use `http://127.0.0.1:4520` if AudioMuse-AI runs on the same computer, or `http://your-ip:4520` if it runs elsewhere.

Click the logo to open its menu. From there you can open the page in the browser, open the log (it has one line for each API call), stop or start the server, and quit. On Linux the logo needs a desktop with a system tray; without a desktop the program runs in the terminal.

| System | Program | Database, key and log |
|---|---|---|
| macOS | `AudioMuse-AI Local Bridge.app` in Applications | `~/Library/Application Support/AudioMuse-AI-Local-Bridge` |
| Windows | `AudioMuse-AI-Local-Bridge.exe` in the folder where you unzipped it | `%AppData%\AudioMuse-AI-Local-Bridge` |
| Linux | `AudioMuse-AI-Local-Bridge` in the folder where you unzipped it | `~/.config/AudioMuse-AI-Local-Bridge` |

The data folder has `bridge.db` (your library and settings), `bridge.key` (the key that protects your password) and `bridge.log`. To start again from zero, quit the program and delete this folder.

The first time, macOS can block the program because it is not signed. Open System Settings, then Privacy & Security, and click "Open Anyway". On Windows, click "More info" and then "Run anyway".

## For developers

You need Go 1.26 or newer. SQLite and TagLib are pure Go, so the Windows and Linux builds need no C compiler. The macOS menu bar icon needs cgo (the Xcode command line tools); a macOS build without cgo runs in the terminal only.

```
go test ./...
go build
```

To build for another system, set `GOOS` and `GOARCH`, for example `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build`. The program accepts `-port` (default 4520), `-data` (the data folder), `-scan-every` (default `5m`, `0` turns it off), `-no-browser` and `-no-tray` (run in the terminal). To publish a release, run the Release workflow in the Actions tab on GitHub. It needs no input: it takes the last version and adds one (v1, v2, v3 and so on), then builds the zips for macOS, Windows and Linux and attaches them to a new release.
