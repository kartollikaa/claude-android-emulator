# Android Emulator for Claude Code

A Claude Code mod that shows an Android emulator or a USB device right inside Claude Code: in the
terminal and in the desktop app's Code tab. It is the Android counterpart of the desktop app's
built-in iOS Simulator panel.

> **Status: walking skeleton.** `/android` opens a pane that lists attached devices and shows a
> screenshot of the selected one on demand. Live streaming, input forwarding and model-facing
> tools come next: see [docs/plan.md](docs/plan.md).

## Requirements

- macOS (the skeleton uses `sips`; Linux support is on the plan)
- Android SDK platform-tools (`adb`), found through `ANDROID_HOME`, `ANDROID_SDK_ROOT`,
  `~/Library/Android/sdk`, Homebrew, or `PATH`
- A running emulator (`emulator -avd <name>`) or a device with USB debugging enabled
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

- `/android` opens the **Android** pane and takes a screenshot of the first ready device.
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

## Prototypes: live desktop views

`prototypes/live-helper` is a throwaway Node helper that reads the emulator's gRPC frame stream
and forwards touches and keys back. It drives the live views being compared. It needs an emulator
started with gRPC on and the host GPU (`-no-window` falls back to software rendering, which caps
the frame rate):

```bash
emulator -avd Pixel_7 -qt-hide-window -gpu host -grpc 8554
```

```bash
npm --prefix prototypes/live-helper install
```

```bash
node prototypes/live-helper/server.mjs
```

- **Option A, browser pane:** open `http://127.0.0.1:7357` in the desktop app's built-in browser
  pane. Live canvas: click to tap, drag to swipe, type to send keys, Back / Home / Recents.
- **Option B, mod pane:** `/android`, then **Go live**. On the desktop the pane polls the helper for
  JPEG frames drawn as an `Svg`; in the terminal it draws the helper's raw frame files as an `Image`
  swapped with `$.ui.blit`. Both have a `Client` touch overlay that sends taps and swipes back.
- **Stream test (desktop):** **Test stream** draws the helper's MJPEG stream through each embedding
  the desktop might load natively (`Svg` image, interactive `Svg`, `Markdown` image); the helper
  logs which ones the desktop requests (`mjpeg stream opened: v=...`).

## Layout

| Path | What it is |
| --- | --- |
| `.claude-plugin/plugin.json` | The mod's manifest |
| `.claude-plugin/marketplace.json` | Makes this repository a one-plugin marketplace |
| `hooks/register.tsx` | The hooks module: the `/android` command, the pane, live mode |
| `hooks/touch-overlay.tsx` | The `Client` surface module that turns pointer drags into gestures |
| `hooks/adb.ts`, `hooks/live.ts` | Pure helpers: adb discovery, parsing, sizing, live frames |
| `prototypes/live-helper/` | Throwaway Node helper for the desktop view prototypes |
| `types/index.d.ts` | The `$.state` contract |
| `tests/` | `claude plugin test` suites |
| `docs/plan.md` | Findings, target architecture, roadmap, spike log |
