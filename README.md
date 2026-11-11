<a href="https://liberapay.com/NeptuneHub/donate"><img alt="Donate using Liberapay" src="https://liberapay.com/assets/widgets/donate.svg"></a>

# AudioMuse-AI-Local-Bridge

<p align="center">
  <img src="https://github.com/NeptuneHub/audiomuse-ai-plugin/blob/master/audiomuseai.png?raw=true" alt="AudioMuse-AI Logo" width="480">
</p>

## What it is

AudioMuse-AI Local Bridge is a small program that lets AudioMuse-AI use the music files on your computer. It reads the tags of the songs in your music folder and shares them with AudioMuse-AI using the same API as a Navidrome server, so you do not need a real music server. Artists and albums come from the song tags, the same way Navidrome does it. Playlists made by AudioMuse-AI are saved as `.m3u8` files in a `Playlists` folder inside your music folder, so other music players can open them too.

**The full list of AudioMuse-AI related repository are:** 
  > * [AudioMuse-AI](https://github.com/NeptuneHub/AudioMuse-AI): the core application, it run Flask and Worker containers to actually run all the feature;
  > * [AudioMuse-AI Helm Chart](https://github.com/NeptuneHub/AudioMuse-AI-helm): helm chart for easy installation on Kubernetes;
  > * [AudioMuse-AI Plugin for Navidrome](https://github.com/NeptuneHub/AudioMuse-AI-NV-plugin): Navidrome Plugin;
  > * [AudioMuse-AI Plugin for Jellyfin](https://github.com/NeptuneHub/audiomuse-ai-plugin): Jellyfin Plugin;
  > * [AudioMuse-AI MusicServer](https://github.com/NeptuneHub/AudioMuse-AI-MusicServer): Open Subosnic like Music Sever with integrated sonic functionality;
  > * [AudioMuse-AI-Local-Bridge](https://github.com/NeptuneHub/AudioMuse-AI-Local-Bridge): Let AudioMuse-AI analyze the music directly on your computer;
  > * [AudioMuse-AI Music Assistant Provider](https://github.com/music-assistant/server/releases/tag/2.11.0b6): Use AudioMuse-AI as sonic analysis provider for Music Assistant v2.11+
  > * [AudioMuse-AI-Jellyfin-SDK](https://github.com/NeptuneHub/AudioMuse-AI-MusicServer): Typed client for the AudioMuse AI endpoints exposed by its Jellyfin plugin, built on @jellyfin/sdk;
  > * [lyrion-audiomuseai-plugin](https://github.com/JameZUK/lyrion-audiomuseai-plugin): Unofficial Lyrion Plugin by [JameZUK](https://github.com/JameZUK);
  > * [ha-audiomuse](https://github.com/colfin22/ha-audiomuse): Unofficial Home Assistant Plugin by [Colfin22](https://github.com/colfin22).

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

The first time, macOS blocks the app because it is not signed. Run `xattr -dr com.apple.quarantine "/Applications/AudioMuse-AI Local Bridge.app"` in the Terminal, or open the app, close the warning, then click "Open Anyway" in System Settings, Privacy & Security. The file `README-macOS.txt` in the zip explains both ways. On Windows, click "More info" and then "Run anyway".

## For developers

You need Go 1.26 or newer. SQLite and TagLib are pure Go, so the Windows and Linux builds need no C compiler. The macOS menu bar icon needs cgo (the Xcode command line tools); a macOS build without cgo runs in the terminal only.

```
go test ./...
go build
```

To build for another system, set `GOOS` and `GOARCH`, for example `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build`. The program accepts `-port` (default 4520), `-data` (the data folder), `-scan-every` (default `5m`, `0` turns it off), `-no-browser` and `-no-tray` (run in the terminal). To publish a release, run the Release workflow in the Actions tab on GitHub. It needs no input: it takes the last version and adds one (v1, v2, v3 and so on), then builds the zips for macOS, Windows and Linux and attaches them to a new release.
