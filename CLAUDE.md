# claude-android-emulator

A Claude Code mod (function-hooks plugin) that runs an Android emulator in the desktop app's
browser pane: `/emulator` launches a Go bridge (`bridge/`) that serves the live view.
Read `README.md` for usage and `docs/plan.md` for the architecture and roadmap before changing code.

## Owner requirement: nothing manual

The user never runs a command or touches a terminal: `/emulator` starts the bridge, Claude opens
the live view, and a Start button in it boots the emulator. User-facing text (the README's Install
and Use, the page) describes only that flow; commands belong under the README's Develop.

## The mod API

- The API is early access and moves between Claude Code releases. The authority is the
  declaration file the engine lays in `.claude-plugin/types/claude-code/index.d.ts` (git-ignored,
  regenerated on every load) — grep it for the event or element at hand, never guess props.
- The `/plugin-authoring` skill's `reference.md` is the long-form map; load the skill before
  non-trivial work on hooks or rendering.
- The hooks module has no Node and no DOM: everything goes through `$` (`$.process`, `$.fs`,
  `$.http`, `$.clock`, `$.state`). Binary data never flows through `$.process` stdout (it is text).

## Rules learned the hard way

- When the module unloads, a `$.process.spawn` child dies together with every process descending
  from it, its own session notwithstanding. Anything that must outlive the session (the emulator)
  is started through a launcher that exits at once, so it is reparented away.
- `$.prompt.submit` is refused inside a `command.run` hook (it would wait on the turn the hook
  holds); submit from a `$.clock.after(0, …)` timer the hook starts.
- A hook's own time is capped (`HookBudget`); waiting on a plain promise counts, a `$` call does not.
- Never write frames, builds or other churn into the plugin folder: the session hot-reloads on every
  change there. They go under `$TMPDIR/claude-android-emulator/`.
- A plugin tool's result reaches the API unconverted: an image is a Messages API block
  (`{ type: 'image', source: { type: 'base64', media_type, data } }`), never MCP's `{ data, mimeType }`.
- A `tool.call` matcher names the tool as a literal, or the validator cannot tell which tool it serves;
  the tool's input type is merged into `McpToolInputs` in `hooks/tool.ts` so the matcher type-checks.
- To see what a headless session sends the model, point `ANTHROPIC_BASE_URL` at a local fake Messages
  API that scripts the tool calls and logs the request bodies; real headless turns need a login.
- The validator follows `$` only into functions declared in the hooks module itself: every function
  taking `$` lives in `hooks/register.ts`; other files hold pure helpers.

## Checks before any commit

```bash
claude plugin validate .
```

```bash
claude plugin test .
```

and `tsc -p .` once the engine has laid the types. Every behaviour change gets a test in `tests/`;
a test that draws runs over both the `terminal` and `desktop` surfaces.

For the bridge:

```bash
go -C bridge vet ./...
```

```bash
GOOS=linux go -C bridge vet ./...
```

```bash
GOOS=windows go -C bridge vet ./...
```

```bash
go -C bridge test -race ./...
```

```bash
gofmt -l bridge
```

`bridge/proto/emulator_controller.proto` is a pinned SDK copy: after replacing it, run
`go -C bridge generate ./...` and commit the regenerated `bridge/internal/emulatorpb/`. The bridge
serves this machine only: keep the loopback listener, the `Host` / `Origin` guard on every route,
and the page's refusal to be framed (another site could steer clicks onto the emulator). Its stdout
carries only the `{"liveView": …}` line the mod reads; logs go to stderr. Emulators it boots keep
`-grpc-use-token` (their gRPC port listens on every interface), start through the launcher so they
leave the bridge's process tree, run detached in a session of their own, and log to a file under
`$TMPDIR/claude-android-emulator/`, never a pipe. Screenshots and recordings default to the user's
Desktop, so every test sets `CapturesDir` to a temporary folder. `adb shell` joins its arguments
into one line the device's shell parses, so any value from a request is validated before it reaches
adb (package names, language tags), and a free-form one (a URL, an activity) goes through
`adb.Quote`.
