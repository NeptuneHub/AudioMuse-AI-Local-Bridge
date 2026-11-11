AudioMuse-AI Local Bridge for macOS

The app works on Apple Silicon and Intel Macs with macOS 12 or newer.


1. Move "AudioMuse-AI Local Bridge" to the Applications folder.


2. Approve the app the first time.

The app is not signed with an Apple developer account, so macOS blocks it
the first time you open it. Choose one of these two ways.

With the Terminal: open the Terminal app, paste this line and press Enter.

    xattr -dr com.apple.quarantine "/Applications/AudioMuse-AI Local Bridge.app"

Then double-click the app.

Without the Terminal: double-click the app and close the warning. Open
System Settings, then Privacy & Security. Scroll down and click
"Open Anyway" next to "AudioMuse-AI Local Bridge", enter your password,
then open the app again.

You only need to do this once.


3. Use it.

The AudioMuse-AI logo appears in the menu bar and your browser opens at
http://localhost:4520. Choose your music folder, a username and a password.
Click the logo to open the page again, see the log, stop or start the
server, or quit.


Where the files are

Your library, settings and log are in this folder:

    ~/Library/Application Support/AudioMuse-AI-Local-Bridge

To open it, in Finder choose Go, then Go to Folder, and paste the path.
