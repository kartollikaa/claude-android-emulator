# claude-android-emulator

A Claude Code mod (function-hooks plugin) that draws an Android emulator/device screen in a pane.
Read `README.md` for usage and `docs/plan.md` for the architecture and roadmap before changing code.

## The mod API

- The API is early access and moves between Claude Code releases. The authority is the
  declaration file the engine lays in `.claude-plugin/types/claude-code/index.d.ts` (git-ignored,
  regenerated on every load) — grep it for the event or element at hand, never guess props.
- The `/plugin-authoring` skill's `reference.md` is the long-form map; load the skill before
  non-trivial work on hooks or rendering.
- The hooks module has no Node and no DOM: everything goes through `$` (`$.process`, `$.fs`,
  `$.http`, `$.clock`, `$.state`). Binary data never flows through `$.process` stdout (it is text).

## Rules learned the hard way

- Pick surface-specific elements by narrowing `e.surface`, never with `'Image' in elements`:
  the resolved table answers `in` for elements the surface cannot draw.
- Terminal pixels go through `Image` (`file`/`shm` sources read by the terminal itself); remote
  surfaces (desktop, vscode, mobile) have no `Image`, only `Svg` (131072-character source cap).
- Never write frames or other churn into the plugin folder: the session hot-reloads on every
  change there. Frames go under `$TMPDIR/claude-android-emulator/`.
- Render hooks never write state; writes happen in press handlers or other events via `update`.
- The validator follows `$` only into functions declared in the hooks module itself, and reads
  state atoms only when they are declared there: every function taking `$` and every `atom(...)`
  lives in `hooks/register.tsx`; other files hold pure helpers.

## Checks before any commit

```bash
claude plugin validate .
```

```bash
claude plugin test .
```

and `tsc -p .` once the engine has laid the types. Every behaviour change gets a test in `tests/`
run over both `terminal` and `desktop` surfaces.
