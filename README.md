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

## Layout

| Path | What it is |
| --- | --- |
| `.claude-plugin/plugin.json` | The mod's manifest |
| `.claude-plugin/marketplace.json` | Makes this repository a one-plugin marketplace |
| `hooks/register.tsx` | The hooks module: the `/android` command and the pane |
| `hooks/adb.ts` | Pure helpers: adb discovery, parsing, sizing |
| `types/index.d.ts` | The `$.state` contract |
| `tests/` | `claude plugin test` suites |
| `docs/plan.md` | Research notes and the roadmap |
