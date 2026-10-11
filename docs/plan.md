# Plan: an Android screen inside Claude Code

## Goal

An Android emulator or USB device, live and interactive, inside Claude Code — in the terminal
and in the desktop app's Code tab — plus tools that let Claude see and drive it while it builds
an app. The desktop app's built-in iOS Simulator panel is the reference experience.

## Where it stands

`v0.1.0` is a walking skeleton: `/android` opens a pane that lists adb devices and shows one
screenshot of the selected device (adb `screencap` → PNG file → `Image` on the terminal,
downscaled JPEG inside an `Svg` on remote surfaces). Validated, type-checked, tested on the
`terminal` and `desktop` surfaces. See the spike log at the end for what was checked against a
real emulator.

## Findings

### What the mod API gives us

| Need | Terminal | Desktop / VS Code / mobile |
| --- | --- | --- |
| Pixels | `Image`: PNG/RGBA bytes, or a **file or POSIX shared-memory name another local process wrote**, read by the terminal itself (kitty graphics protocol: Ghostty, kitty) — the pixels never cross `$` | No `Image`, no `Raster`. Only `Svg`, source capped at 131 072 characters → a JPEG data URI of roughly 95 KB |
| Frame rate | `$.ui.blit({ requestId, key, source })` swaps a keyed `Image` without a redraw, folded to the surface's frame rate | Each frame is a state write → re-render → tree sent to the client: a few fps at best |
| Pointer and keys | `Client` surface module: `onPointer` with sub-cell `fine` coordinates (documented for "a module showing another program's pixels"), `onKey`, `post` → `ui.message` | `Client` exists on desktop too, so the same overlay can catch taps |
| Talking to a helper | `$.process.spawn` (text stdout, child dies with the module), `$.process.run`, `$.http.fetch` with `socketPath` (HTTP over a Unix socket), `$.fs` | same |
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
- A third-party report claims the desktop app has a gated "Android Emulator" toggle. If Anthropic
  ships native Android support in the desktop app, the terminal pane and Claude tools still stand.
- The kitty graphics protocol limits terminal pixels to Ghostty and kitty; elsewhere the pane shows
  `alt` text (a `Raster` low-res fallback is possible).

## Target architecture

```
emulator ──gRPC streamScreenshot──┐
USB device ──scrcpy H.264─────────┤
                                  ▼
                       android-bridge (native helper, one per session)
                         • writes each frame to a fresh POSIX shm object
                         • prints JSON lines: {"frame":"/ab-42","w":540,"h":1200}
                         • serves control on a Unix socket: /touch /key /text /config
                                  │ stdout                    ▲ $.http.fetch(socketPath)
                                  ▼                           │
                       hooks module (this mod)  ──────────────┘
                         • $.process.spawn keeps the bridge alive while the pane is open
                         • $.ui.blit({ key: 'screen', source: { shm } }) per frame
                         • ui.message from the Client overlay → touch / key
                         • desktop: small JPEG frames → Svg (few fps) or browser-pane link
                         • $.tool.register: screenshot, tap, swipe, text, button, launch, open_url
```

Frames never land in the plugin folder (the session hot-reloads on changes there).

## Roadmap

One PR per item, each reviewed before the next starts.

1. **Spikes** (findings go into the spike log, no product code):
   1. The desktop Code tab draws a JPEG data URI inside `Svg` (run `/android` with a device up).
   2. Ghostty draws the `Image`; a `Client` laid over it (`position: absolute`) still gets pointer
      events.
   3. gRPC `streamScreenshot` from a throwaway client: fps at pane size, row order, token mode.
   4. Shared-memory frames through `$.ui.blit` at 30–60 fps in Ghostty.
2. **Bridge v0** (`bridge/`): emulator discovery, gRPC client, frames → shm, JSON-lines protocol,
   control socket; tested against a fake gRPC server.
3. **Live terminal pane**: spawn the bridge, blit frames, fps in the status line, stop streaming when
   the pane closes, fall back to `screencap` when the bridge is missing.
4. **Input**: `Client` overlay → tap / drag / scroll → `sendTouch`; keys → `sendKey`; a row of
   Back / Home / Recents / Rotate / Power / Volume buttons.
5. **Desktop surface**: throttled JPEG-in-`Svg` with a `Client` overlay for taps; evaluate a
   bridge-served localhost page for the built-in browser pane as the real-time option.
6. **Claude tools**: an `android` tool mirroring the iOS Simulator's actions; `screenshot` saves a
   PNG Claude reads back (whether a plugin tool result can carry an image block is unverified).
7. **Emulator management**: list AVDs, boot headless with `-grpc`, stop; an AVD picker in the pane.
8. **Physical devices**: pinned scrcpy server, H.264 → VideoToolbox → shm, scrcpy control socket.
9. **Release**: CI (validate, plugin tests, `tsc`, bridge tests and builds), packaged bridge
   binaries, README, public repository.

## Open decisions (owner)

- **Bridge language.** Recommended: **Go** — first-class gRPC, one static binary per platform,
  easy cross-compilation; FFmpeg is spawned for device decoding at first. Alternatives: Swift
  (native VideoToolbox, macOS-only), Rust. Go is not installed on this machine yet.
- **Bridge distribution.** Recommended for the first public release: CI-built binaries shipped
  inside the plugin (a `/plugin install` copies the repository, so it works with no extra step);
  revisit with release downloads or a Homebrew tap if the size hurts.
- **When to make the repository public.**

## Spike log

| Spike | Result |
| --- | --- |
| Emulator discovery | `emulator -avd <name> -no-window -grpc <port>` boots headless and writes `pid_<pid>.ini` with `grpc.port` and no `grpc.token` (no auth unless asked for) |
| Skeleton capture pipeline | Works end to end against a headless emulator outside the UI (devices → `screencap` → PNG → JPEG). `screencap` delivers a couple of frames per second at best, PNG or raw: a live view needs the bridge |
| Desktop frame size | A 720 px JPEG of the phone screen takes about a quarter of the `Svg` source cap: room for a sharper desktop frame |
| Desktop `Svg` drawing, Ghostty `Image`, `Client` over `Image`, gRPC stream rate | Not yet run (roadmap 1.1–1.4) |
