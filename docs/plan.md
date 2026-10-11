# Plan: an Android screen inside Claude Code

## Goal

An Android emulator or USB device, live and interactive, inside the Claude desktop app's Code
tab, plus tools that let Claude see and drive it while it builds an app. The desktop app's
built-in iOS Simulator panel is the reference experience.

## Where it stands

`/emulator` runs the Go bridge in `bridge/` (built from source until the release slice ships
binaries) and asks Claude to open its live view in the app's built-in browser pane. The page lists
the SDK's virtual devices, boots one from a Start button and shows it live and touchable; a toolbar
rotates it, saves screenshots and screen recordings, opens App info and presses Power, and a settings
panel switches the dark theme, font size, display size and language; the emulator outlives the
session. The in-pane live view, the terminal code and the prototype helper
are gone. See the spike log at the end.

## Findings

### What the mod API gives us

| Need | Terminal | Desktop / VS Code / mobile |
| --- | --- | --- |
| Pixels | `Image`: PNG/RGBA bytes, or a **file or POSIX shared-memory name another local process wrote**, read by the terminal itself (kitty graphics protocol: Ghostty, kitty) — the pixels never cross `$` | No `Image`, no `Raster`. Only `Svg`, source capped at 131 072 characters → a JPEG data URI of roughly 95 KB |
| Frame rate | `$.ui.blit({ requestId, key, source })` swaps a keyed `Image` without a redraw, folded to the surface's frame rate | Each frame is a state write → re-render → tree sent to the client: a few fps at best |
| Pointer and keys | `Client` surface module: `onPointer` with sub-cell `fine` coordinates (documented for "a module showing another program's pixels"), `onKey`, `post` → `ui.message` | `Client` runs in a sandboxed frame: whole cells only (no `fine`), pointer capture from press to release. Its region is as tall as what the module draws, capped by the `Client`'s own height, which never stretches it |
| Talking to a helper | `$.process.spawn` (text stdout; the child and every process descending from it die with the module), `$.process.run`, `$.http.fetch` with `socketPath` (HTTP over a Unix socket), `$.fs` | same |
| Claude-facing tools | `$.tool.register` → `mcp__android-emulator__<name>`, served by a `tool.call` hook | same |

Consequences:

- **A native helper is required.** `$.process` stdout is text, so video cannot flow through the
  hooks module; the emulator's gRPC is HTTP/2, which `$.http` cannot speak. The helper decodes
  or receives frames and writes them where the terminal reads them; the mod only routes small
  messages.
- **The desktop app is the weak surface.** Mods cannot add a native video panel like the iOS
  Simulator's. Options there: low-fps JPEG-in-`Svg` inside the pane, or a helper-served localhost
  page (real-time video and input) opened in the app's built-in browser pane — Claude can open it
  with its browser tool, the mod itself has no API for that.
- Pick surface elements by narrowing `e.surface`; `'Image' in elements` is true on desktop too.

### Frame sources

**Emulator: gRPC `EmulatorController`** (the path Android Studio's embedded emulator uses).

- Enabled by `-grpc <port>`; plaintext HTTP/2 on localhost, no auth unless `-grpc-use-token`
  (then `authorization: Bearer <grpc.token>`) or `-grpc-use-jwt`. Never pass
  `-idle-grpc-timeout`: it kills the emulator when gRPC goes idle.
- Discovery: `pid_*.ini` under `~/Library/Caches/TemporaryItems/avd/running/` (macOS),
  `$XDG_RUNTIME_DIR/avd/running` (Linux) with `grpc.port`, `grpc.token`, `port.serial`.
- `streamScreenshot(ImageFormat) → stream Image`: RGB888 / RGBA8888 / PNG, scaled server-side to a
  requested size, pushed on change (an idle screen sends nothing). Full-size RGB888 frames exceed
  gRPC's default 4 MiB message limit, so request the pane's pixel size or raise the limit.
- Input on the same channel: `sendTouch` (multi-touch, `pressure = 0` releases), `sendMouse`,
  `sendKey` (`GoBack`, `GoHome`, `AppSwitch`, key codes), plus `getStatus`, `streamNotification`.
- The proto ships with the SDK (`emulator/lib/emulator_controller.proto`); the helper pins a copy.
- `ImageTransport { channel: MMAP }` makes the emulator write pixels straight into a client-owned
  shm region or file — but it crashes or writes nothing on Apple Silicon before emulator 37.2.12.
  A later optimisation, not the baseline.
- Unverified: real fps/latency; the proto's note that rows are "bottom up".

**Physical device: scrcpy server.** Push a pinned `scrcpy-server` jar (the protocol is internal and
versioned), read H.264 from its video socket (`raw_stream=true` gives bare H.264), decode with
VideoToolbox (FFmpeg `-hwaccel videotoolbox` → `rawvideo`), inject input through its binary control
socket or `adb shell input`. Android Studio does the same with its own on-device agent.

**Fallback everywhere: `adb exec-out screencap`** (raw mode skips PNG encoding) — a few fps, no
helper needed. This is what the skeleton uses.

### Prior art

- **mobile-mcp's `/mobile-mirror` mod** (mobile-next) already mirrors a device in a kitty/Ghostty
  pane: a `screencap` loop into rotating PNG files, `$.ui.blit` with `file` sources, a `Client`
  overlay for taps, `adb shell input` for input. It runs at screencap speed. Its register module is
  the best reference for the pane/pointer plumbing; our difference is a live gRPC stream, emulator
  lifecycle, a desktop story, and Android-specific Claude tools.
- Android Studio "Running Devices": emulator gRPC for emulators, a screen-sharing agent for devices.
- The desktop app's iOS Simulator panel: live stream, clicks, a frame-rate/resolution menu, and a
  session tool with `attach`, `launch`, `screenshot`, `tap`, `swipe`, `touch_path`, `text`,
  `button`, `open_url` — the tool surface to mirror.

### Risks

- The mod API is early access and changes between Claude Code releases.
- The desktop app already ships a built-in Android Emulator panel and Claude tool, switched off by a
  server-side rollout flag (its settings row stays hidden while the flag is off). Once it reaches an
  account, it outclasses this mod's desktop pane; the Claude tools would be the remaining value.
- The built-in `/mobile` command claims `/android` as an alias, so the mod's command is `/emulator`.
- The kitty graphics protocol limits terminal pixels to Ghostty and kitty; elsewhere the pane shows
  `alt` text (a `Raster` low-res fallback is possible).

## Target architecture

```
emulator ──gRPC streamScreenshot / sendTouch / sendKey──┐
                                                        ▼
                       android-bridge (Go, one per session, localhost only)
                         • finds the running emulator and its gRPC port
                         • serves the live view: a page with a canvas, frames over a
                           WebSocket, touches and keys back the same way
                         • a small HTTP API for the mod and Claude's tools
                                  ▲ $.process.spawn          ▲ $.http.fetch
                                  │                          │
                       hooks module (this mod)  ─────────────┘
                         • /emulator: starts the bridge, answers with the live view's URL, asks
                           Claude to open it in the built-in browser pane ($.prompt.submit)
                         • $.tool.register: screenshot, tap, swipe, text, key, launch, open_url

built-in browser pane ──http://127.0.0.1:<port>──▶ the live view
```

A mod cannot open the browser pane itself: Claude opens it with its browser tool, which the
command's prompt asks for. Frames never land in the plugin folder (the session hot-reloads on
changes there).

## Roadmap

One PR per item, each reviewed before the next starts.

1. **Desktop view prototypes**: the spikes in the spike log; settles the browser pane.
2. **Bridge v0** (`bridge/`, Go): emulator discovery, gRPC client from the pinned proto, the live
   view page (device only, Back / Home / Recents, keyboard), WebSocket frames and input, a status
   endpoint, a guard against other sites (loopback only, own `Host` and `Origin`, no framing);
   tested against a fake gRPC server.
3. **Start the emulator from the live view**: the bridge finds the SDK and lists the AVDs; the page
   shows them with a Start button, boots one with `-qt-hide-window -gpu host -grpc <free port>`,
   shows boot progress, then the live screen.
4. **Mod as launcher**: `/emulator` starts the bridge (a prebuilt binary for the user's platform)
   and stops it with the session, and the prompt that has Claude open the live view in the browser
   pane. Drops the in-pane live view, the terminal code and `prototypes/live-helper`.
5. **Live view toolbar**: rotate, screenshot, screen recording, power, App info, shut down.
6. **Device settings**: dark theme, font size, display size, language, reset.
7. **Claude tools**: an `android` tool mirroring the iOS Simulator's actions (screenshot, tap,
   swipe, text, key, launch, open_url, logs) over the bridge's HTTP API; whether a plugin tool
   result can carry an image block is unverified.
8. **Release**: CI (validate, plugin tests, `tsc`, bridge tests and cross-compiled binaries),
   README, public repository.

Later: physical devices (pinned scrcpy server, H.264 decoded in the bridge, scrcpy control socket).

## Decisions

- **Desktop only.** The owner dropped the terminal: the mod targets the desktop app's Code tab.
- **The live view lives in the built-in browser pane.** The owner compared both and chose it: the
  mod pane redraws the whole pane per frame and stays at a few frames per second even with touch
  working; the browser pane streams to a canvas at the emulator's rate.
- **Bridge language: Go** — first-class gRPC, one static binary per platform, easy
  cross-compilation; FFmpeg is spawned for device decoding at first.
- **Nothing manual.** The owner: the emulator starts from one button in the live view and the user
  never runs a command. `/emulator` starts the bridge, Claude opens the live view, the page boots
  the emulator. User-facing docs describe only that flow.
- **Emulators the bridge boots ask for a gRPC token** (`-grpc-use-token`): the emulator's gRPC
  port listens on every interface, not only loopback; the token lands in the discovery file, where
  the bridge reads it.
- **The bridge reads the panel size from the hardware config** (`getStatus`, `hw.lcd.width` and
  `hw.lcd.height`), not a screenshot: it is the portrait panel whatever the rotation, it is known
  before the display is up, so the page can show the boot, and it costs no PNG.
- **Bridge distribution: prebuilt binaries inside the plugin.** CI cross-compiles
  `cmd/android-bridge` with `CGO_ENABLED=0` for macOS, Linux and Windows on arm64 and amd64; the
  mod runs the one for the user's platform, so users never need Go. The binaries stay out of
  `main`'s history (the release slice picks the branch or tag the marketplace entry points to);
  `go run` is for development only.

- **No status pane.** The live view in the browser pane is the only UI: `/emulator`'s output names
  the live view's URL or what went wrong, and a toast says when the bridge stops.
- **The emulator outlives the session**, as Android Studio's and the iOS Simulator's do: it runs
  until Shut down, and the next `/emulator` shows it again. The bridge boots it through a launcher
  that exits at once, so the engine's kill of the bridge's process tree spares it.

## Open decisions (owner)

- **When to make the repository public.**

## Spike log

| Spike | Result |
| --- | --- |
| Emulator discovery | `emulator -avd <name> -no-window -grpc <port>` boots headless and writes `pid_<pid>.ini` with `grpc.port` and no `grpc.token` (no auth unless asked for) |
| Skeleton capture pipeline | Works end to end against a headless emulator outside the UI (devices → `screencap` → PNG → JPEG). `screencap` delivers a couple of frames per second at best, PNG or raw: a live view needs the bridge |
| Desktop frame size | A 720 px JPEG of the phone screen takes about a quarter of the `Svg` source cap: room for a sharper desktop frame |
| gRPC stream and input (prototype helper) | `streamScreenshot` RGB888 at reduced size works with no token; rows arrive top-down, despite the proto's "bottom up" note. `sendTouch` taps and swipes and `sendKey` `GoHome`/`GoBack`/`AppSwitch` all act on the emulator |
| Option A: browser-pane live view | Works in the desktop app's built-in browser pane: live frames, drag to swipe, nav keys. First rejected by the owner (it reads as a browser), then chosen once the mod pane proved stuck at a few frames per second |
| Emulator frame rate | `-no-window` selects software rendering (`swangle` / `lavapipe`) and caps the gRPC stream far below the display rate. `-qt-hide-window -gpu host` (how Android Studio launches it) renders on the host GPU and roughly triples the stream rate under continuous swipes, at either stream size. Always launch that way |
| MJPEG in Chromium | A plain `<img>` plays the helper's MJPEG stream in the app's Chromium; each part must end with the boundary or the frame never shows |
| Option B: live mod pane | Owner reports 0–2 fps on the desktop: every frame is a state write, a re-render and a new `Svg` image. The emulator and the helper's JPEG encoding are not the bottleneck. Next: the stream test, to find an embedding the desktop plays natively |
| Desktop touch overlay | Never received a press: a `Client` drew `height="100%"` content inside an auto-height region, so the region collapsed to one row whatever `height` the `Client` was given. The desktop's frame runtime shows why: the region is a flex item that shrinks to the `Client`'s height but never grows to it |
| Desktop touch surface | A `Client` sized `100%` × `100%` inside an absolute `Box` pinned to the picture (`top`/`left`/`right`/`bottom` 0), drawing content taller than any picture, so its region shrinks to exactly the picture; presses, drags and releases stream to the emulator as touches. On the desktop app touch works across the whole picture, but frames still lag: the owner chose the browser pane |
| Ghostty `Image`, `Client` over `Image` | Not run: the terminal is out of scope |
| Start from the live view | `emulator -avd Pixel_7 -qt-hide-window -gpu host -grpc <free port> -grpc-use-token`, detached in its own session: the discovery file carries `avd.id` and `grpc.token`, the bridge connects within seconds, and `getStatus().booted` turns true about 15 s after Start (snapshot boot). A SIGTERM to the bridge alone leaves the emulator running; killing the bridge's whole process tree takes the emulator with it, which matters for how the mod stops the bridge. A killed emulator leaves its `pid_<pid>.ini` behind, so "running" also checks that the pid is alive. With the panel size from `getStatus`, the bridge connects about a second after Start and the page shows "Booting" for the rest. `setVmState(SHUTDOWN)` powers it off and removes its file |
| Display off | `Power` on API 37 streams full-size black frames; the empty 0×0 image the proto promises for an inactive display was not seen. The bridge keeps the last stream size and the page blacks out the screen if one arrives |
| Engine kill of a mod's child | When the module unloads, the engine sends SIGTERM to a `$.process.spawn` child and kills every process descending from it, including one in a session of its own (`setsid`); a process whose parent already exited, reparented to launchd, survives. Measured with a throwaway mod under `claude -p --plugin-dir` |
| Prompt from a command | `$.prompt.submit` inside a `command.run` hook is refused (it would wait on the turn the hook holds); from a `$.clock.after(0, …)` timer the hook starts, it queues a model turn after the command. End to end in a headless session: `/emulator` built and ran the bridge, the prompt started a turn, Start booted Pixel_7 under launchd (ppid 1), ending the session stopped the bridge and left the emulator running, and the next session's `/emulator` connected to it at once |
| Toolbar on the real emulator | `setPhysicalModel(ROTATION)` with z + 90° turns Pixel_7 to Android's `ROTATION_90` (landscape, auto-rotate on); its frames then report `LANDSCAPE` and arrive upright, 1200×540 out of a 1200×1200 box. Touches stay in the panel's natural coordinates: a tap at a Settings row's centre, mapped from the landscape picture onto the portrait panel, opened that row. `getScreenshot(PNG)` answers 2400×1080 in landscape. With auto-rotate off the same sensor turn still turns the picture, the app staying portrait on its side. `screenrecord` interrupted with SIGINT leaves a playable MP4; `echo $$; exec screenrecord` prints the pid to interrupt, so a stop leaves any other recording on the device running; `Power` sleeps and wakes the device; `dumpsys activity activities` names the app in front and `cmd package resolve-activity` the launcher |
| Device settings on the real emulator | `cmd uimode night yes`, `settings put system font_scale`, `wm density` and `cmd locale set-device-locale` each take effect at once, and every failure exits non-zero; `cmd uimode night` names the mode (`auto` and the schedules included), and `dumpsys uimode`'s `mComputedNightMode` whether the dark theme is on now. `cmd locale list-device-locales` lists every locale ICU knows, numbering-system variants (`-u-nu-latn`) included, and an unknown `cmd` subcommand prints to stdout and exits 255. Display size's own + and − buttons in Settings step through exactly the densities DisplayDensityUtils' arithmetic gives: 356, 420, 460, 500, 540 for a 420 dpi, 1080 px wide panel |
