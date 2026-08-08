# Changelog

goreleaser assembles per-release notes from Conventional Commits history and
attaches them to the corresponding [GitHub Release](https://github.com/takaaki-s/jind-ai/releases).
This file is the curated overview — highlights per release, not a per-commit
log.

## Unreleased

### Features

- **`jin session result` works on Codex sessions.** It reads the agent's own
  rollout log and returns the same `text` / `tool_use` / `tool_result` entries
  a Claude Code session returns, so an orchestrator can inspect what a codex
  child actually did instead of falling back to `git diff`. Two filters are
  weaker there than on Claude Code and the difference matters when you act on
  them: `--errors-only` cannot see a command that merely exited non-zero
  (Codex records no exit code), and `--tool` barely discriminates (Codex
  declares the same tool name for nearly everything it runs). `usage` and
  `thinking` are not available on codex entries. See
  [docs/gotchas.md](docs/gotchas.md#session-result), or `jin docs show gotchas`
  for the orchestration-facing version.

- **`jin session result` works on opencode sessions too.** opencode keeps its
  conversation in a SQLite database, so jind-ai asks opencode for it: the
  command runs `opencode export --pure <session id>` and parses the result.
  Nothing is recorded, no copy is kept, and no SQLite dependency or
  database-schema coupling is taken on. Where the codex reader is weak this one
  is not: `thinking` carries the real reasoning text, entries carry `usage`
  (the sum over the messages opencode splits one turn across — its
  reasoning-token count has no field in jind-ai's shape and is dropped), and
  `--tool` matches real per-tool names. Until now the command answered zero
  entries and exit 0 on an opencode session, which reads exactly like a child
  that ran and said nothing.

  The limits matter more than the capabilities. `opencode` has to be on the
  **daemon's** PATH — sessions start through your login shell, which resolves a
  version manager's shims the daemon may not see — and a read that cannot
  happen is an error, not an empty result; the one case that stays empty and
  exits 0 is a session whose agent has not reported its own id yet, the same
  window codex has. Each read costs 1.45–1.77s whatever the session size (the
  time is opencode's start-up), so `--since` is no cheaper than a full read and
  fewer, larger polls are the way to use it. `--errors-only` is exact for
  `bash` and partial elsewhere: it catches whatever opencode flagged as an
  error, plus a shell command that exited non-zero (opencode records those as
  `completed`, so the flag comes from the exit status recorded alongside it) —
  and no other tool records an exit status at all (0 of 161 calls). Timestamps
  are opencode's own except where its clock disagrees with the order of the
  conversation — parallel tool calls, 13 of 620 blocks measured — where the
  previous value is carried forward, so two entries can share a stamp and
  `--since` can skip one, exactly as on the other two kinds. Revert and undo
  are not tracked, so content an opencode child undid can still appear in the
  result. See
  [docs/gotchas.md](docs/gotchas.md#session-result), or `jin docs show gotchas`
  for the orchestration-facing version.
- **Session rows and `jin session info` show a codex session's last messages.**
  The two message previews — the TUI's second line, the `session list` rows,
  and `last_user_message` / `last_assistant_message` in
  `jin session info --json` — were read with the Claude Code transcript reader
  whatever agent the session ran, so on codex and opencode they were blank
  permanently. They now go through the session's own adapter, like
  `jin session result` already did. Claude Code sessions show exactly what they
  showed before. opencode rows stay blank on purpose: reading an opencode
  conversation means running `opencode export`, which is far too expensive for
  a path the TUI refreshes on a timer. These stay a preview rather than a result: an unreadable
  transcript leaves them empty and the command still succeeds, so an empty
  preview is not evidence a child said nothing — use `jin session result` for
  that. `jin session output` is unchanged and still reads Claude Code only.

### Behaviour change

- **Workspace trust for Claude Code sessions is now written to
  `~/.claude.json`**, the file Claude Code actually reads it from, instead of
  `~/.claude/settings.local.json`, where it was silently ignored. A session
  started in a fresh worktree no longer stops at the trust dialog.
- The old file is neither migrated nor deleted — jind-ai just stops writing
  to it. Installs that ran earlier versions have a large dead `projects` map
  there, which is safe to remove when the file holds nothing else; see
  [docs/gotchas.md](docs/gotchas.md#claude-code-adapter) for the steps.
- Trusting the directory your worktrees are created under, once, stops
  jind-ai writing to `~/.claude.json` for every session below it — Claude
  Code inherits trust from ancestor directories. Sessions started without
  `--worktree` run elsewhere and are not covered by that.

## 0.9.0

### Features

- **`jin plugin update <name>` now moves an unpinned install to the
  plugin's latest release** instead of re-cloning the locked commit
  (which made `update` a silent no-op on registry installs). Registry
  names resolve to the registry's declared `latest_version`; raw git-URL
  installs pick the highest semver tag from `git ls-remote --tags`,
  falling back to the locked ref when the remote advertises no valid
  tags.
- **Install-time pin captured in the lock**: a bare `install <name>` or
  `install <url>` marks the entry as unpinned (`update` will follow
  latest), while `install <name> -v <ver>` and `install <url>@<ref>`
  mark it pinned (`update` refuses to move it and points at reinstall
  as the way to bump). Old lock files without the field default to
  unpinned, matching the install-time behaviour of a bare install.

### Behaviour change (not a schema break)

- On the first `jin plugin update <name>` after upgrading, unpinned
  installs may move to a newer release — the pre-0.9.0 `update` was
  effectively a no-op that always re-cloned the locked SHA, so this is
  the first time the command actually delivers updates. Users who want
  the old "lock every install to the ref I first saw" behaviour can
  reinstall with a `-v` / `@ref` pin.

## 0.8.1

### Bug Fixes

- **`jin plugin ls-remote` and every other registry-consulting `plugin`
  path work again.** 0.8.0 tightened the plugin manifest's
  `schema_version` from 1 to 2 (`CurrentSchemaVersion = 2`), but the
  registry-document parser was sharing that constant, so every shipped
  0.8.0 binary rejected the live `registry.json` (`schema_version: 1`)
  with `schema_version 1 not supported (this build understands 2)`.
  Registry doc and plugin manifest now have independent version
  constants (`CurrentRegistrySchemaVersion` / `CurrentSchemaVersion`)
  and a regression test parses a literal
  `{"schema_version": 1, "plugins": []}` payload so a future manifest
  bump can never break registry parsing the same way.

## 0.8.0

### Features

- **A plugin can now declare multiple actions.** Manifests support an
  `actions:` list where each entry carries its own `id` / `entrypoint` /
  `on` / `popup` / `label`. The first entry is the implicit default action
  (no explicit flag). Palette rows, keybindings, and `jin plugin run` all
  operate at the action level. Existing v1 manifests (`schema_version: 1`
  with top-level `entrypoint` / `on`) are normalised at parse time into a
  single-action shape, so plugin authors need to do nothing to keep
  working.
- **`jin plugin run <name> [action]`** — an optional second positional
  argument selects the action. Omitted invocations run the default action
  (`actions[0]`) and keep the exact pre-existing output
  (`Started plugin <name> (global)`) so scripts that grep the CLI's
  success message stay working. Shell completion is two-stage: plugin
  names first, then that plugin's action IDs.
- **`JIN_ACTION_ID`** env var — every plugin run receives the ID of the
  action that fired it, so a shared entrypoint script can dispatch on
  `$JIN_ACTION_ID` instead of parsing argv.
- **`jin plugin install` / `update` consent screens are per-action.** v2
  manifests now render every action's `id` / `entrypoint` / `on`. This
  also fixes a v1-era rendering path that read the (now-forbidden)
  top-level `on:` field and left the `Events:` line empty for v2 plugins.
- **`jin plugin validate --run-build` checks every action's entrypoint**
  exists after build, not just the default action's — so multi-binary
  v2 plugins get the full sanity check.
- **`actions[].listener: true`** — marks an action as an event-only
  endpoint. It still fires on matching `on:` events, but is hidden from
  every user-facing surface (palette, help popup, shell completion).
  Lets a plugin split its listener from its user-invoked action without
  the listener cluttering the palette as an entry that does nothing when
  clicked. `jin plugin run <plugin> <action>` still accepts a listener
  ID directly for debugging. A listener with `on: []` is a validation
  error (`R22 RuleListenerRequiresOn`) — a listener with no events has
  no runtime purpose.

### Breaking changes

- **`keybindings.plugins.<name>.keys`** → **`keybindings.plugins.<name>.actions.<id>.keys`**.
  The old shape is detected at load time, a single WARN is logged per
  plugin, and that plugin's binding is dropped (TUI startup is never
  blocked). Rewrite by hand; for a plugin with only a default action,
  `actions.default.keys: [...]` is the shortest translation.

  ```yaml
  # Before (0.7.x — ignored with a WARN under 0.8.0)
  keybindings:
    plugins:
      notifier:
        keys: ["M-n"]

  # After (0.8.0)
  keybindings:
    plugins:
      notifier:
        actions:
          default:
            keys: ["M-n"]
          send-dm:
            keys: ["M-d"]
  ```

- **`plugin.EventDispatcher.RunAction`** signature is now
  `RunAction(name, actionID, ev, depth, actx)`. An empty `actionID` selects
  the default action; an unknown ID returns a synchronous error listing
  the available actions.
- **`daemon.PluginRunRequest`** gains an `Action string` field (empty =
  default action). IPC wire-compat is preserved: pre-0.8.0 clients simply
  omit the field and land on the default action, matching their previous
  behaviour.
- **`plugin.PopupSizeResolver`** signature is now
  `(pluginName, actionID string, m *manifest.PopupConfig) → (w, h string)`.
  The daemon's built-in resolver ignores `actionID` for now — per-action
  popup size in user config is out of scope for this release — but the
  argument is plumbed so a later config schema can widen without another
  breaking signature change.

## 0.7.2

### Bug Fixes

- **`jin plugin install <name>` / `jin plugin update` now clone from
  `github.com`.** Registry entries record `repo` as bare `owner/name`
  (the crawler's GitHub `FullName`), but the resolver was passing that
  string to `git clone` unchanged and hitting an unresolvable host.
  Bare entries are now prefixed with `https://github.com/` before
  clone; entries that already carry a URL scheme (mirrors, `file://`
  fixtures) pass through untouched.

## 0.7.1

### Features

- **`install.source.build` is now optional.** Plugins that ship a directly
  executable entrypoint (shell scripts, prebuilt binaries checked into the
  repo) can omit the `build` block entirely and point `install.source.entrypoint`
  at the script or binary. Only `install.source.entrypoint` remains required
  under `install.source`. Existing manifests that declare a `build` block
  continue to validate unchanged.

## 0.7.0

### Features

- **Plugin registry** — new `jin plugin ls-remote`, `jin plugin install <name>`
  (registry-resolved with SHA pin and a consent screen), and `jin plugin
  validate` commands. See [docs/plugin-registry.md](docs/plugin-registry.md)
  for the discover/install/publish flow and full flag reference.
- **Unified plugin manifest (`jind-ai-plugin.yaml`)** — the runtime dispatcher
  and the registry crawler now read the same file with the same schema. The
  old `jin-plugin.yaml` / `api_version` shape has been removed.
- **`pkg/plugin/manifest`** — the manifest package is now exported so the
  registry crawler and any third-party tool validate manifests bit-for-bit
  identically to jin itself.

### Breaking changes

`0.7.0` is a pre-1.0 minor bump and carries breaking changes to the plugin
system. See [docs/plugin-registry.md#pre-10-break-policy](docs/plugin-registry.md#pre-10-break-policy)
for the policy in full.

- The plugin manifest file is now `jind-ai-plugin.yaml` (was
  `jin-plugin.yaml`); the `api_version` field is gone and `schema_version: 1`
  takes its place. Existing plugins must migrate the file name, add
  `schema_version` / `name` / `version` / `description` / `jin:`, and move
  `run` / `build` under `install.source.{entrypoint,build[]}`.
- The built-in desktop notifier has been removed from the daemon. Install
  [`jind-ai-notifier`](https://github.com/takaaki-s/jind-ai-notifier) — the
  same notifier repackaged as a plugin — to restore the behaviour.
