# Plugin registry

jind-ai's plugin registry lets you discover and install community plugins by
name (`jin plugin install jind-ai-notifier`) rather than by full git URL, with
each install pinned to a specific commit SHA the registry vouched for at
publish time.

The registry itself is a JSON document served from GitHub Pages
(`https://takaaki-s.github.io/jind-ai-plugin-registry/registry.json`), produced
by a crawler that scans GitHub for public repos tagged with the
`jind-ai-plugin` topic. No accounts, no submissions — publish by adding the
topic to your repo.

## Manifest (`jind-ai-plugin.yaml`)

The same file is read by jind-ai at runtime (dispatcher, install) and by the
registry crawler at publish time. One file, one source of truth.

```yaml
schema_version: 1
name: my-notifier
version: 0.1.0
description: Desktop notifications for jin sessions
license: MIT
homepage: https://github.com/foo/my-notifier
jin: ">=0.7.0"
install:
  source:
    build:
      - go build -o bin/notifier ./cmd/notifier
    entrypoint: ./bin/notifier
on: ["status_changed:idle", "status_changed:permission"]
timeout: 30s
```

See the ["Manifest" section in the plugin guide](plugins.md#manifest-jind-ai-pluginyaml)
for the full field reference; every field there is honoured identically by the
registry crawler. Two publish-specific notes:

- `name` uniqueness across the registry is resolved by the crawler — see
  [Name conflict policy](#name-conflict-policy).
- Declaring a `jin` range you have not tested is a footgun: consumers
  evaluate it against their own `jin --version`.

## Discovering plugins

```bash
jin plugin ls-remote                          # NAME / LATEST / UPDATED / REPO
jin plugin ls-remote --sort updated           # newest updates first
jin plugin ls-remote --search notif           # substring match on name/description
jin plugin ls-remote --refresh                # bypass the 24h local cache
jin plugin ls-remote --json                   # machine-readable
jin plugin ls-remote --registry https://…     # point at an alternate registry
```

The registry document is cached at `$XDG_STATE_HOME/jind-ai/registry/`
(default `~/.local/state/jind-ai/registry/`) for 24 hours. On expiry the next
call re-fetches with `If-None-Match` / `If-Modified-Since` headers, so an
unchanged registry is a cheap `304`. If the fetch fails for any reason
(network down, registry serving an error), a stale cache is used with a
warning printed to stderr so you can still install; use `--refresh` to force a
network round-trip.

The `NAME` column is what you pass to `jin plugin install` — that path
resolves through the registry and pins to the commit SHA the crawler
recorded. The `REPO` column is shown as `github.com/<owner>/<name>` so it can
also be pasted straight into `jin plugin install` as a fallback (git clone
URL path), though that bypasses the registry's pinning and consent metadata.
The `--json` output keeps `repo` as the crawler's bare `owner/name` so
existing consumers of the JSON schema are unaffected.

## Installing a plugin

```bash
jin plugin install jind-ai-notifier              # registry name → latest_version
jin plugin install jind-ai-notifier -v 0.2.0     # pin a specific version
jin plugin install jind-ai-notifier --refresh    # bypass registry cache
jin plugin install jind-ai-notifier --force      # override an unsatisfied jin compat range
jin plugin install jind-ai-notifier --yes        # skip the consent prompt
```

`jin plugin install` accepts three source shapes:

1. **Registry name** (`jind-ai-notifier`) — resolves through the registry and
   always pins to the commit SHA the registry recorded at crawl time. That
   SHA is written into `plugins.lock.yaml`, so a later `install`/`update` of
   the same version never lands on a different commit than the one you
   approved.
2. **Git URL** (`github.com/owner/repo[@ref]`) — clones directly, bypasses
   the registry. The `REPO` column from `ls-remote` is a valid value here, so
   copy-paste works; use the registry name form (1) when you want the pinned
   SHA rather than whatever the branch resolves to right now.
3. **`--link <path>`** — symlinks a local plugin directory (development).
   Trusted outright; no build runs.

### Consent screen

Before doing anything, install shows a single-screen summary:

```
  Plugin:  jind-ai-notifier @ 0.1.0
  Source:  https://github.com/takaaki-s/jind-ai-notifier@a1b2c3d4e5f6
  Kind:    (unverified community plugin)
  Compat:  jin >=0.7.0  (you have 0.7.0)  ✓

  Installation will:
    1. clone https://github.com/takaaki-s/jind-ai-notifier at a1b2c3d4e5f6 into
       /home/you/.local/share/jind-ai/plugins/jind-ai-notifier/
    2. run: go build -o bin/notifier ./cmd/notifier

Install? [y/N]:
```

The community-plugin marker always shows because the registry performs no
verification; the compat verdict (✓ / ✗) is evaluated against the running
binary.

Skip the prompt with `--yes`. When compat fails, install aborts unless
`--force` is passed — the ✗ verdict is shown either way before that decision.

If clone or build fails, install rolls back atomically: nothing is left in
the plugins directory, and the lock file is untouched. Build output is kept
at `~/.local/state/jind-ai/plugin-logs/<name>-build.log`.

## Publishing a plugin

The registry is topic-based: any public GitHub repo with the `jind-ai-plugin`
topic and a valid `jind-ai-plugin.yaml` at its root is picked up by the next
crawler run (default cadence: every 6 hours). No form to fill in, no PR to
open.

Checklist:

1. Publish the code to a public GitHub repo.
2. Add the `jind-ai-plugin` topic (Repo → About → gear icon → Topics).
3. Place `jind-ai-plugin.yaml` at the repo root.
4. Run `jin plugin validate .` locally — this is exactly what the crawler
   runs, so a green local check is a strong predictor of registry inclusion.
5. Cut a semver tag (`v0.1.0` etc.) — the crawler prefers the latest GitHub
   Release, and falls back to the default branch HEAD if none exists.

For a working starter, use the [`jind-ai-plugin-template-notifier`](https://github.com/takaaki-s/jind-ai-plugin-template-notifier)
template repository ("Use this template" — do not fork). It ships with a
manifest that already passes `jin plugin validate` and a CI snippet that runs
the same validation on every PR.

### Name conflict policy

Two repos can declare the same `name`. The crawler resolves this
deterministically:

- The **previous crawl's owner** of the name keeps it as long as its repo
  still exists and still declares the topic. A new repo claiming the same
  name is rejected.
- Among new claimants in the same crawl run, the repo with the earliest
  `created_at` wins (GitHub's `repos.created_at`).
- If the current owner disappears (repo deleted, topic removed, manifest
  becomes invalid), the name enters a **30-day grace period** during which no
  other repo can take it. The name reappears as an orphaned entry with
  `orphaned_since` set.
- After 30 days of orphaning, the name is released; the next crawl assigns it
  to the earliest-created new claimant.

This is enforced by the crawler alone — jind-ai clients trust whatever the
registry says.

## `jin plugin validate`

`jin plugin validate` is the entry point that runs the exact checks the
crawler runs. Use it locally before publishing, and in your plugin repo's CI:

```bash
jin plugin validate                       # defaults to .
jin plugin validate ./some/plugin/dir
jin plugin validate --run-build           # also run rules #13/#14 (see below)
jin plugin validate --github-actions      # emit ::error / ::warning annotations
jin plugin validate --fail-on-warning     # treat WARN as non-zero exit
```

Exit codes: `0` = pass (or WARN only), `1` = ERROR (or WARN with
`--fail-on-warning`). Under `--github-actions`, ERROR lines print as
`::error file=jind-ai-plugin.yaml,line=N::…` and a markdown summary is
appended to `$GITHUB_STEP_SUMMARY` when set.

See the template repo's `.github/workflows/ci.yaml` for a working snippet
(no separate GitHub Action needed — the workflow downloads a pinned jin
release and invokes `jin plugin validate` directly).

### `--run-build`

Rules #13/#14 are opt-in because they execute your `install.source.build`
commands and then check that every action's entrypoint exists. They run
through the same code path `jin plugin install` uses, so the conditions the
build gets are the install's, not your session's:

- **The environment is filtered the way an install filters it** — see
  [the build environment](plugins.md#language-specific-guidance) for the
  allowlist. A build needing anything outside it fails here, which is the
  point.
- **The budget is the default `plugins.build_timeout` (300s), covering the
  whole sequence** rather than each command. Neither your own configured
  value nor your manifest's `timeout:` is consulted; the README section above
  says why.
- The build runs **in the plugin directory** and its artifacts stay there,
  so run it on a tree you don't mind having built.

What this cannot check is the *contents* of what does get through. `PATH` and
`HOME` are forwarded from whoever runs the command, so a compiler installed
only on your machine still builds fine here and is still missing on your
users'. Name what your plugin needs in its own README.

## Pre-1.0 break policy

jin is currently pre-1.0. It follows the pre-1.0 semver convention:

- **Minor bumps (`0.X.0`) may include breaking changes** — including
  changes to the plugin API, manifest field semantics, environment variables
  passed to plugins, and the shape of the registry JSON document.
- **Patch bumps (`0.X.Y`) are bug fixes only.**
- **`1.0` is reserved for the moment the plugin API is committed to
  stability.** After 1.0, breaking changes will require a major bump; before
  1.0, they will not.

Concretely for plugin authors, this means:

- Declare `jin: ">=0.7.0"` (or a tighter range like `"^0.7"`) so jind-ai can
  refuse to load your plugin on incompatible versions. Install-time checks
  are fail-closed; dispatch-time checks are fail-open (logged once, marked
  `incompatible` in `jin plugin list`).
- Expect to re-test your plugin on every jin minor bump during pre-1.0. When
  a bump lands, the CHANGELOG's Features section will call out any changes
  that affect plugins.
- Follow the [compatibility contract](plugins.md#compatibility) in the
  README: treat unknown env vars / JSON fields / CLI flags as ignorable —
  that's what makes minor bumps survivable, and it also documents the
  `schema_version` window jind-ai supports.
