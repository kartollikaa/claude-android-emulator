# Plan: an Android screen inside Claude Code

## Goal

An Android emulator or a connected device (USB or Wi-Fi), live and interactive, inside the Claude desktop app's Code
tab, plus tools that let Claude see and drive it while it builds an app. The desktop app's
built-in iOS Simulator panel is the reference experience.

## Where it stands

`/emulator` runs the Go bridge in `bridge/` (built from source until the release slice ships
binaries) and asks Claude to open its live view in the app's built-in browser pane. The page lists
the SDK's virtual devices, boots one from a Start button and shows it live and touchable; a toolbar
rotates it, saves screenshots and screen recordings, opens App info and presses Power, and a settings
panel switches the dark theme, font size, display size and language; Claude's `emulator` tool
sees and drives the same device; the emulator outlives the session. With no emulator running, a phone
adb reaches over USB or Wi-Fi shows the same way, streamed by the embedded scrcpy server and decoded in
the page. The in-pane live view, the terminal code and the prototype helper are gone. See the spike
log at the end.

## Findings

### What the mod API gives us

| Need | Terminal | Desktop / VS Code / mobile |
| --- | --- | --- |
| Pixels | `Image`: PNG/RGBA bytes, or a **file or POSIX shared-memory name another local process wrote**, read by the terminal itself (kitty graphics protocol: Ghostty, kitty) — the pixels never cross `$` | No `Image`, no `Raster`. Only `Svg`, source capped at 131 072 characters → a JPEG data URI of roughly 95 KB |
| Frame rate | `$.ui.blit({ requestId, key, source })` swaps a keyed `Image` without a redraw, folded to the surface's frame rate | Each frame is a state write → re-render → tree sent to the client: a few fps at best |
| Pointer and keys | `Client` surface module: `onPointer` with sub-cell `fine` coordinates (documented for "a module showing another program's pixels"), `onKey`, `post` → `ui.message` | `Client` runs in a sandboxed frame: whole cells only (no `fine`), pointer capture from press to release. Its region is as tall as what the module draws, capped by the `Client`'s own height, which never stretches it |
| Talking to a helper | `$.process.spawn` (text stdout; the child and every process descending from it die with the module), `$.process.run`, `$.http.fetch` with `socketPath` (HTTP over a Unix socket), `$.fs` | same |
| Claude-facing tools | `$.tool.register` → `mcp__android-emulator__<name>`, served by a `tool.call` hook; a result in Messages API blocks reaches the model as is, images included | same |

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
versioned: the server refuses a client of another version), run it with `app_process` over `adb
shell`, and reach its sockets through `adb forward` (`tunnel_forward=true`). The video socket
carries 12 bytes of codec metadata (codec id, width, height) once, then packets behind a 12-byte
header (config flag, key-frame flag, PTS, size): Annex B H.264, SPS and PPS in the config packet.
The control socket takes binary messages: key codes, text, touches (each carrying the video size it
was made for: a touch made for another size is dropped), `RESET_VIDEO` for a fresh config and key
frame. The size is sent once: after a rotation the encoder restarts and only the new SPS carries the
new size. Android Studio does the same with its own on-device agent.

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
                         • $.tool.register: one `emulator` tool, an action per call (start,
                           screenshot, tap, swipe, text, button, open_url, launch, logs,
                           settings, rotate), each a call to the bridge's HTTP API

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
7. **Claude tools**: an `emulator` tool mirroring the iOS Simulator's actions (start, screenshot,
   tap, swipe, text, button, launch, open_url, logs) plus the settings and rotation, over the
   bridge's HTTP API.
8. **Device seam**: the bridge drives a device interface instead of `*emulator.Conn`, the emulator
   its only implementation; no behaviour change.
9. **Connected devices**: a phone adb reaches over USB or Wi-Fi shows in the live view and answers
   Claude's tool (see Connected devices below).
10. **Release**: CI (validate, plugin tests, `tsc`, bridge tests and cross-compiled binaries),
   README, public repository.

Later: one emulator or device per session, each bridge claiming its own.

### Connected devices

- **Any transport adb has.** A phone plugged in over USB, one on wireless debugging (Android 11+,
  paired once; adb reconnects it through mDNS) and one reached with `adb connect` all show the same
  way: scrcpy runs over whatever transport adb uses. A phone listed twice (USB and Wi-Fi, the same
  `ro.serialno`) counts once, over USB. Pairing a new phone over Wi-Fi is not covered yet.
- **Which device shows.** A running emulator first, as today; with none, the first adb device in the
  `device` state that is not a discovered emulator. While a phone shows, the bridge keeps looking for
  emulators and switches to one as it answers. The page's Virtual devices button opens the AVD list
  over the phone; Starting one switches the view to it, and shutting it down falls back to the phone.
  A phone in the `unauthorized` state shows on the page with a hint to allow USB debugging on it. A
  phone whose stream fails before its first frame is tried again at doubling intervals, capped,
  until it streams or leaves adb's list; the page and Claude's tool say why it does not show.
  `/status` names the `kind` (`emulator` or `device`), a phone's `model` and whether it is on `wifi`.
- **Video.** The bridge runs the pinned scrcpy server (video H.264, no audio, control on, clipboard
  sync off, long side from `-stream-width`) and forwards its packets to the page untouched; the page
  decodes them with WebCodecs and draws each frame on the canvas, the newest config packet riding
  along with every key frame (an Annex B key frame must carry SPS and PPS). Each viewer follows the
  packets in order: one that joins late gets the newest config packet and the packets since the
  newest key frame. A backlog past its cap drops its oldest packets and can no longer start a viewer,
  and a viewer too far behind starts over; either then asks for `RESET_VIDEO` once and starts from
  the key frame it brings. Viewers that keep up never wait for a reset. The bridge reads the current
  video size from each SPS and the display's rotation from `dumpsys window` when the size changes.
- **Bandwidth.** Over USB the server keeps its default bit rate; over the network (a `host:port` or
  mDNS serial) the bridge asks for half of it, plenty for a phone screen at the stream's size.
- **Input.** Touches, keys and text go to the scrcpy control socket, touches in the current video's
  pixels and keys as Android key codes. Text goes through the phone's key map, so only what it has
  (ASCII) arrives. Rotation sets `user_rotation` a quarter on from the locked rotation (the display's,
  if auto-rotate was on) with auto-rotate off, since scrcpy's own rotate message only toggles; an app
  locked to portrait stays so, as the emulator's does when its sensor turns.
- **Everything else** runs over adb against the phone's serial, as for an emulator: screenshots
  (`screencap -p`, scaled and JPEG-encoded in the bridge for Claude), recordings, App info, links,
  launching and installing apps, logs and the device settings. A Wi-Fi serial (`host:port`, an mDNS
  service name) is made safe before it names a capture file. The panel size is `wm size`'s override
  when one is set, since the display then draws at it. Shut down is an emulator's only: the page
  hides it and the route answers 409 for a phone; Power sleeps and wakes the phone. Claude's tool
  drives a phone as it does an emulator, and its `start` only says a phone is shown.

## Decisions

- **Desktop only.** The owner dropped the terminal: the mod targets the desktop app's Code tab.
- **The live view lives in the built-in browser pane.** The owner compared both and chose it: the
  mod pane redraws the whole pane per frame and stays at a few frames per second even with touch
  working; the browser pane streams to a canvas at the emulator's rate.
- **Bridge language: Go** — first-class gRPC, one static binary per platform, easy
  cross-compilation.
- **A phone's video is decoded in the page.** The browser pane's Chromium decodes H.264 through
  WebCodecs with hardware acceleration, so the bridge forwards scrcpy's packets as they are and needs
  no FFmpeg.
- **The scrcpy server is pinned and embedded** in the bridge binary (`go:embed`), its release
  digest checked by a test and its Apache-2.0 licence beside it; the bridge pushes it to
  `/data/local/tmp` under a name carrying its version.
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

- **One tool for Claude, an action per call**, as the iOS Simulator's `control` tool is: one
  schema in the prompt rather than a dozen, and the description says how the actions fit together
  (screenshot first, coordinates in its pixels). It is listed up front (`isDeferred: false`) so
  Claude reaches for it without a search.
- **Claude's coordinates are the screenshot's pixels.** The screenshot is the upright screen scaled
  so its long side fits the model's image budget, JPEG rather than PNG (a fraction of the size);
  the bridge maps those pixels onto the panel whatever the rotation, so Claude never converts.
- **Tool results are Messages API blocks** (`{ type: 'image', source: { type: 'base64', … } }`
  beside a text block): the engine passes a plugin tool's result through unconverted, so an
  MCP-style `{ type: 'image', data, mimeType }` would reach the API malformed.

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
| Image results from a plugin tool | A plugin's tool is served through a loopback MCP server; a `tool.call` result that is a string arrives as text, and an array of Messages API blocks arrives as a tool_result holding those blocks, a base64 image among them. An MCP-style image block passes through unconverted. Measured by pointing a headless session (`ANTHROPIC_BASE_URL`) at a local fake Messages API that scripts the tool calls and logs each request body |
| Claude's tool on the real emulator | The same fake API scripted start, screenshot, Home, a swipe, settings, logs and an off-screen tap against Pixel_7: each answered as expected and the JPEG reached the request as an image block. It caught a race: a bridge just started reported no emulator before it had looked, so start tried to boot a second one; `/status` now waits for that first look |
| WebCodecs in the browser pane | The pane's Chromium is a secure context on `http://127.0.0.1`, has `VideoDecoder`, and supports H.264 Baseline, Main and High (`avc1.42E01E`, `avc1.4D401F`, `avc1.640028`), hardware acceleration preferred |
| scrcpy server 3.3.3 against Pixel_7 | Pushed, started with `tunnel_forward=true audio=false control=true max_size=1080 send_device_meta=false`, reached through `adb forward tcp:… localabstract:scrcpy_<scid>`: the dummy byte, then codec metadata `h264` 488×1080, a 33-byte config packet (SPS, PPS) and a key frame. Home by key code, a tap carrying the video size opened a Settings row, `RESET_VIDEO` gave a fresh config packet and key frame. `ROTATE_DEVICE` set `user_rotation` but turned nothing in a portrait-locked activity |
| Pixel 9a (Android 17) over USB through the bridge | With no emulator, the bridge attached to the phone within a second of starting: `/status` named it `device`, `Pixel 9a`, booted, panel 1080×2424 from `wm size`, video 536×1208 (H.264 High 3.2, a 31-byte config packet) at `-stream-width 540`. A new viewer got the state, then the config packet and a key frame. Home by key code and `/launch` of Settings acted on the phone. `/screen` took 0.6–0.7 s on a still screen, almost all of it `screencap -p` on the phone, and up to 2.5 s right after an action. The launcher stays portrait whatever `user_rotation` says, so rotation counts on from `user_rotation`, not the display's. When an emulator shut down, the bridge fell back to the phone about a second later. A bridge stopped by SIGTERM first left its `adb forward` behind (it exited before the stream's cleanup); it now waits for it. Not yet measured: packet and byte rates while scrolling, a turn in an app that rotates, Wi-Fi |
| `$.http.fetch` on a slow answer | A throwaway mod fetched from a server that answers after 30 s: the fetch was aborted at 30 s with `no complete answer within 30000ms`. The bridge's boot wait answers well inside that, and a cold boot from the tool (snapshot boot) finished inside a single wait |
