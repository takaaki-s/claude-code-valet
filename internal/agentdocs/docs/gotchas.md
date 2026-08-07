---
title: Traps an orchestrating agent will hit
description: The failures that look like bugs but are not — per-agent limits on result collection, the creating-to-idle race, a send that exits 0 without starting a turn, and why a pane capture is not evidence.
---

# Traps an orchestrating agent will hit

## `session output` and `session result` only work for Claude Code

Both read Claude Code's transcript files. For a `codex` or `opencode` session
they return **empty, not an error** — the same thing an idle Claude Code
session with no output yet returns. So a child that did substantial work looks
identical to one that did nothing.

Check the session's kind before you trust an empty result:

```bash
jin session info work --json | jq -r '.agent_kind'
```

What still works for every kind: `new`, `send`, `wait`, `kill`, `delete`,
`list`, `info`, and status (`idle` / `thinking` / `permission`). Only the two
transcript-reading commands are Claude-Code-only.

To check a non-Claude child's work, go to the artifacts instead:

```bash
git -C <workdir> diff              # what actually changed
cd <workdir> && <test command>     # whether it holds up
jin pane capture work              # last resort — see below
```

## Send after `new` needs a wait

`jin session new` returns as soon as the session is reserved, with status
`creating`; provisioning and start continue in the background. `jin session
send` refuses any session that is not `idle`.

So this is a race, and it fails with `session is not idle (current status:
creating)`:

```bash
jin session new --workdir . -d work --json
jin session send work "..." --wait-running   # may fail
```

Wait first:

```bash
jin session new --workdir . -d work --json
jin session wait work --status idle
jin session send work "..." --wait-running
```

A fresh session commonly needs tens of seconds to reach `idle`. Leave the
wait's 300s default alone rather than trimming it to what you measured: a
hook arriving late in startup restarts the clock behind it.

Later sends are covered by the `wait --until idle,permission` you run after
each turn, but only when it comes back `idle` — `send` refuses everything
else. When it comes back `permission`, do not send at all: see below.

## `permission` is a terminal state, and it needs a human

A session waiting on a permission prompt has stopped. It will not move until
someone answers. Waiting only for `idle` burns the entire timeout on it:

```bash
jin session wait work --until idle,permission   # right
jin session wait work --status idle             # hangs on a blocked session
```

When `wait` returns and the status is `permission`, do not send another
prompt — it will be rejected (not idle). Report it to the human, or attach.

## A pane capture is not evidence

`jin pane capture` returns **only what is currently visible** in the pane —
not the scrollback, not what scrolled past while the agent worked. A long
tool output that ran off the top is simply absent, and the capture looks like
a complete picture of a session that did far more than it shows.

Never conclude "the child did nothing" or "the child finished" from a capture.
Use `session result` (Claude Code), or check the artifacts directly.

## A child's report is not a result

An agent that says "fixed and tested" may have edited nothing. For code
changes, look at `git diff` and run the tests yourself. `session result
--tool Bash --json` and `--errors-only` exist for exactly this check on
Claude Code sessions.

## Everything except `jin docs` needs the daemon

Start it before anything else, rather than detecting the failure afterwards —
a daemon that is down exits 1, not 3, so there is no code to branch on:

```bash
jin daemon status >/dev/null 2>&1 || jin daemon start
```

`jin docs list` and `jin docs show` work without it — the docs are compiled
into the binary.

## Large prompts are slower, not broken

`send` verifies the text actually reached the agent's input box before
pressing Enter, so a large prompt takes measurably longer to return. That is
the verification working, not a hang — the retry budget scales with prompt
size.

If `send` fails while verifying, the prompt did **not** land — re-send rather
than assuming partial delivery. If it fails after that (a `--wait-running`
timeout, or the session stopping), the text was verified in the input box
and Enter was sent: attach and look before you resend.

One failure tells you neither, and it says so. An error carrying
`its outcome is unknown` is the client giving up on a daemon that may still
be working. Do not re-send on it — attach and look.

## A `send` that exits 0 is not a turn that started

`send` verifies the text reached the input box. It does not check what the
Enter after it did. An agent TUI can take that Enter for itself — a
completion popup is the usual culprit — and leave the prompt unsent while
`send` reports success.

The session then stays `idle`, so the `wait --until idle,permission` behind
it returns at once and you read the previous turn's output as this one's.
Pass `--wait-running` on every send whose result you act on; exit 4 from it
means nothing confirmed the turn started.

When a send did exit 0 but the child looks like it never worked, check for
this before concluding anything about the child:

```bash
jin session info work --json | jq -r '.status'   # still idle right after a send?
jin pane capture work                            # is your prompt still in the box?
```

This is the one question a capture answers well: the input box is on screen
by definition, which is not true of the work you would otherwise be judging
from it (see "A pane capture is not evidence").

Your prompt sitting in the input box — possibly rewritten, e.g. a trailing
`@internal/agent` turned into `@internal/agentdocs/` — is the signature.
`jin` closes the popup before pressing Enter on Claude Code sessions, so you
should not see this there; if you do, attach and press Enter yourself rather
than re-sending the same text, which reproduces it. The prompts that trigger
it end in an `@` path token or consist of a bare `/command` — putting a word
after the `@` token avoids it entirely.

## Whitespace-only prompts are rejected

A prompt consisting only of whitespace (or only box-drawing characters) is
refused, because the verification step has nothing to search the pane for.
Send real text.

## Don't reuse a description across concurrent sessions

Selectors resolve by description substring. Two live sessions described
`test` make every `test` selector ambiguous (exit code 6) until one is
deleted. Name them distinctly up front — see the `selectors` doc.
