# Android Emulator for Claude Code

A Claude Code mod that shows an Android emulator or a USB device right inside Claude Code: in the
terminal and in the desktop app's Code tab. It is the Android counterpart of the desktop app's
built-in iOS Simulator panel.

> **Status: in development.** The live, touchable screen runs in the desktop app's built-in browser
> pane through the bridge, and its Start button boots a virtual device. Next: `/emulator` starts the
> bridge and opens the live view, so nothing is run by hand; then model-facing tools. See
> [docs/plan.md](docs/plan.md).

## Requirements

- macOS (the skeleton uses `sips`; Linux support is on the plan)
- Android SDK platform-tools (`adb`), found through `ANDROID_HOME`, `ANDROID_SDK_ROOT`,
  `~/Library/Android/sdk`, Homebrew, or `PATH`
- A virtual device (AVD) made in Android Studio's Device Manager, which the live view starts, or a
  device with USB debugging enabled
- For pixels in the terminal: a terminal with the kitty graphics protocol (Ghostty, kitty).
  Other terminals show the pane's text and a placeholder for the screen.

## Install

The repository is private for now, so installing needs read access to it. In a terminal session:

```
/plugin install android-emulator --marketplace kartollikaa/claude-android-emulator
```

Answer `y` to add the marketplace, then pick a scope. A mod installed at the user scope also loads
in the desktop app's local sessions.

## Use

- `/emulator` opens the **Android** pane and takes a screenshot of the first ready device.
- Press a device's button to switch to it, **Refresh** to re-scan devices and take a new screenshot.

## Develop

Run Claude Code with the mod loaded from this folder:

```bash
claude --plugin-dir ~/Projects/claude-android-emulator --debug
```

Check it the way the engine will read it, then run the tests:

```bash
claude plugin validate ~/Projects/claude-android-emulator
```

```bash
claude plugin test ~/Projects/claude-android-emulator
```

Once a session has loaded the mod from this folder, the engine writes the API types to
`.claude-plugin/types/` (git-ignored), and `tsc -p .` type-checks the mod.

### The bridge by hand

`bridge/` is the Android bridge: a small Go program that lists the SDK's virtual devices, boots one,
streams its screen to a page on `http://127.0.0.1:7357`, and sends clicks, drags and keys back.
Until the mod starts it, run it by hand to work on it (needs Go):

```bash
go -C bridge run ./cmd/android-bridge
```

Then ask Claude to open `http://127.0.0.1:7357` in the browser pane. With no emulator running, the
page lists the AVDs; **Start** boots one detached, with gRPC behind a token and the host GPU
(`-qt-hide-window -gpu host -grpc <free port> -grpc-use-token`), shows its progress, then the
screen. Its output goes to `$TMPDIR/claude-android-emulator/emulator-<avd>.log`. An emulator that
is already running with gRPC is picked up as it is, and the bridge reconnects when one restarts.

Click to tap, drag to swipe, click the screen and type to send keys (Esc is Back), and use the
Back / Home / Recents buttons. `-port`, `-stream-width`, `-sdk` (the Android SDK folder; otherwise
`ANDROID_HOME`, `ANDROID_SDK_ROOT`, then the platform's default), `-discovery-dir` (where emulators
write `pid_<pid>.ini`) and `-v` (debug logs) are the bridge's flags.

The page serves only this machine: the bridge listens on 127.0.0.1 and refuses requests whose
`Host` or `Origin` is not its own, so no other web page can read the screen or drive the emulator.

### Prototypes: live desktop views

`prototypes/live-helper` is the throwaway Node helper the live views were compared with; the bridge
replaces its live page, and the mod's in-pane live mode still reads its frames until the mod becomes
a launcher. It takes the same port as the bridge, so run one at a time:

```bash
npm --prefix prototypes/live-helper install
```

```bash
node prototypes/live-helper/server.mjs
```

- **Option A, browser pane:** open `http://127.0.0.1:7357` in the desktop app's built-in browser
  pane. Live canvas: click to tap, drag to swipe, type to send keys, Back / Home / Recents.
- **Option B, mod pane:** `/emulator`, then **Go live**. On the desktop the pane polls the helper
  for JPEG frames drawn as an `Svg`, with a `Client` touch surface pinned over the picture that
  streams presses, drags and releases to the emulator as they happen. In the terminal it draws the
  helper's raw frame files as an `Image` swapped with `$.ui.blit`, under a `Client` overlay that
  sends each drag as one gesture.
- **Stream test (desktop):** **Test stream** draws the helper's MJPEG stream through each embedding
  the desktop might load natively (`Svg` image, interactive `Svg`, `Markdown` image); the helper
  logs which ones the desktop requests (`mjpeg stream opened: v=...`).

## Layout

| Path | What it is |
| --- | --- |
| `.claude-plugin/plugin.json` | The mod's manifest |
| `.claude-plugin/marketplace.json` | Makes this repository a one-plugin marketplace |
| `hooks/register.tsx` | The hooks module: the `/emulator` command, the pane, live mode |
| `hooks/touch-surface.tsx` | Desktop `Client` surface module: streams pointer presses, drags and releases |
| `hooks/touch-overlay.tsx` | Terminal `Client` surface module: turns pointer drags into gestures |
| `hooks/adb.ts`, `hooks/live.ts` | Pure helpers: adb discovery, parsing, sizing, live frames |
| `bridge/` | The Go bridge: emulator discovery, gRPC client, the live view page and its socket |
| `bridge/proto/` | Pinned copy of the Android SDK's `emulator_controller.proto`; `go -C bridge generate ./...` regenerates `bridge/internal/emulatorpb/` |
| `prototypes/live-helper/` | Throwaway Node helper for the desktop view prototypes |
| `types/index.d.ts` | The `$.state` contract |
| `tests/` | `claude plugin test` suites |
| `docs/plan.md` | Findings, target architecture, roadmap, spike log |
