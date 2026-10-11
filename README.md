# Android Emulator for Claude Code

A Claude Code mod that runs an Android emulator inside the Claude desktop app's Code tab: live and
touchable, in the app's built-in browser pane. It is the Android counterpart of the desktop app's
built-in iOS Simulator panel.

> **Status: in development.** `/emulator` opens the live view, its Start button boots a virtual
> device, and a toolbar rotates it, takes screenshots and recordings, and opens App info. Next:
> device settings, tools Claude can drive the emulator with, and a release with prebuilt bridges.
> See [docs/plan.md](docs/plan.md).

## Requirements

- The Claude desktop app (the live view opens in its browser pane), on macOS
- The Android SDK with the emulator, found through `ANDROID_HOME`, `ANDROID_SDK_ROOT` or the
  platform's default folder (`~/Library/Android/sdk` on macOS)
- A virtual device (AVD) made in Android Studio's Device Manager
- Until the first release ships prebuilt bridges: Go, at the version `bridge/go.mod` names, which
  `/emulator` uses to build the bridge on first use

## Install

The repository is private for now, so installing needs read access to it. In a terminal session:

```
/plugin install android-emulator --marketplace kartollikaa/claude-android-emulator
```

Answer `y` to add the marketplace, then pick a scope. A mod installed at the user scope also loads
in the desktop app's local sessions.

## Use

1. Type `/emulator`. Claude opens the live view in the browser pane.
2. Press **Start** on a virtual device. The page shows the boot, then the screen.
3. Click to tap, drag to swipe, click the screen and type to send keys (Esc is Back). Back, Home and
   Recents sit under the screen; the power button there shuts the emulator down after a second press.

The toolbar above the screen:

- **Rotate left** and **Rotate right** turn the device a quarter; the screen follows in the pane.
- **Screenshot** saves a full-resolution PNG to the Desktop.
- **Record** records the screen and shows the time; press it again to stop and save the MP4 to the
  Desktop. The device stops a long recording by itself, and that one is saved too.
- **App info** opens the system App info screen of the app in front.
- **Power** turns the screen off and on, as the side button does.

The emulator keeps running when the session ends, as Android Studio's does; the next `/emulator`
shows it again.

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

### The bridge

`bridge/` is the Android bridge: a Go program that lists the SDK's virtual devices, boots one,
streams its screen to the live view page on `127.0.0.1`, and sends clicks, drags and keys back.

`/emulator` runs `bin/android-bridge-<os>-<arch>` (`.exe` on Windows) when the plugin has one for
the platform. Releases ship those binaries, cross-compiled by CI and kept out of `main`'s history.
In a checkout without them, `/emulator` builds `bridge/` with Go into
`$TMPDIR/claude-android-emulator/android-bridge-dev`. The mod starts it with `-port 0`, and the
bridge answers with one line on stdout, `{"liveView":"http://127.0.0.1:<port>"}`; its logs go to
stderr. It stops with the session and exits by itself if the process that started it is gone.

To work on the bridge alone, run it by hand and open `http://127.0.0.1:7357` in the browser pane:

```bash
go -C bridge run ./cmd/android-bridge
```

**Start** boots the emulator with gRPC behind a token and the host GPU
(`-qt-hide-window -gpu host -grpc <free port> -grpc-use-token`). A short-lived launcher, a second
run of the bridge's own binary, starts it and exits, so the emulator is not in the bridge's process
tree: Claude Code kills a mod's child together with everything descending from it. The emulator
logs to `$TMPDIR/claude-android-emulator/emulator-<avd>.log`; one that never comes up is stopped
and reported. An emulator already running with gRPC is picked up as it is, and the bridge
reconnects when one restarts.

The bridge's flags:

- `-port`: the live view's port (0 picks a free one).
- `-stream-width`: the streamed frames' short side, their width in portrait.
- `-sdk`: the Android SDK folder; otherwise `ANDROID_HOME`, `ANDROID_SDK_ROOT`, then the platform's
  default.
- `-discovery-dir`: where emulators write `pid_<pid>.ini`.
- `-captures-dir`: where screenshots and recordings are saved; otherwise the Desktop.
- `-v`: debug logs.

The toolbar's routes, each a `POST` with a JSON body: `/rotate` (`{"direction": "left"}` or
`"right"`), `/screenshot` and `/recording/stop` (both answer `{"path": …}`), `/recording/start` and
`/app-settings`. Rotation goes through the emulator's rotation sensor and screenshots through gRPC;
recording (`screenrecord`) and App info use adb from the SDK's `platform-tools`, or from `PATH`.
Frames are drawn upright, so the page sends touches in the upright picture and the bridge turns them
back onto the panel. `/status` carries the `rotation`, `recordingSince` while recording, the
newest saved file as `saved`, and `recordingError` when a recording could not be saved.

The page serves only this machine: the bridge listens on 127.0.0.1 and refuses requests whose
`Host` or `Origin` is not its own, so no other web page can read the screen or drive the emulator.

## Layout

| Path | What it is |
| --- | --- |
| `.claude-plugin/plugin.json` | The mod's manifest |
| `.claude-plugin/marketplace.json` | Makes this repository a one-plugin marketplace |
| `hooks/register.ts` | The hooks module: `/emulator` finds or builds the bridge, runs it and asks Claude to open the live view |
| `hooks/bridge.ts` | Pure helpers: the platform's binary, the bridge's announcement, the prompt |
| `bridge/` | The Go bridge: SDK and AVDs, the emulator launcher, discovery, gRPC client, the live view page and its socket |
| `bridge/proto/` | Pinned copy of the Android SDK's `emulator_controller.proto`; `go -C bridge generate ./...` regenerates `bridge/internal/emulatorpb/` |
| `tests/` | `claude plugin test` suites |
| `docs/plan.md` | Findings, target architecture, roadmap, decisions, spike log |
