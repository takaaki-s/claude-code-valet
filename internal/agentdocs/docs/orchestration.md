---
title: Drive a child session end to end
description: The full delegation loop — create a session, send a prompt, wait for it to settle, collect what it actually did, clean up. Read this first.
---

# Drive a child session end to end

You are the orchestrator. A child session is another agent working in its own
tmux pane; you start it, give it work, wait, and check the result. You stay
responsible for what it produces.

## Before anything else

Every command below except `jin docs` needs the daemon:

```bash
jin daemon status >/dev/null 2>&1 || jin daemon start
```

Do this up front rather than reacting to a failure: a daemon that is down
exits 1 like any other error, so there is no distinct code to branch on.

## The loop

```bash
# 1. Create. Always give it a description — it becomes the selector you use
#    for every later command.
jin session new --workdir ~/repos/myapp -d fix-login --json

# 2. Wait for it to come up. `new` returns immediately with status
#    `creating`; `send` refuses anything that is not `idle`.
jin session wait fix-login --status idle

# 3. Send work. `prompt` is an alias for `send`. Always pass
#    `--wait-running`: without it a prompt that reached the input box but
#    was never submitted still exits 0.
jin session send fix-login "run go test ./... and fix what fails" --wait-running

# 4. Wait for it to settle. Include `permission` — a session blocked on a
#    permission prompt is stopped, not working, and waiting only for `idle`
#    will burn the whole timeout on it. If this returns almost instantly,
#    the turn never started — see below.
jin session wait fix-login --until idle,permission --timeout 600 --json

# 5. Collect.
jin session output fix-login --last 1 --json     # the final assistant text
jin session result fix-login --json              # what it actually did

# 6. Clean up when done.
jin session kill fix-login
jin session delete fix-login
```

## Why step 2 is not optional

`jin session new` returns as soon as the session is reserved — status
`creating`, with provisioning and start still running in the background. And
`jin session send` refuses any session that is not `idle`.

Skipping the wait is therefore a race you sometimes lose, with
`session is not idle (current status: creating)`.

Later sends are covered by step 4's wait, but only when it comes back `idle`:
`--until idle,permission` returns on either, and `send` refuses everything but
`idle`. Check the status you got before sending again.

Once the session is idle, `send` handles the rest itself: it verifies the
prompt actually reached the input box, retrying while the TUI settles,
before pressing Enter. A successful `send` means the text landed **in the
input box**. It does not mean the child started a turn.

## Why step 3 needs `--wait-running`

Those two can come apart — an agent TUI can take the Enter for something of
its own (a completion popup, a dialog) and leave the prompt sitting unsent.
`send` still exits 0, because the text did land.

The damage is in step 4, not step 3. An unsubmitted prompt leaves the session
on `idle`, so `wait --until idle,permission` returns **immediately** and you
go on to read the previous turn's output as this turn's result. Nothing in
the sequence looks like a failure.

`--wait-running` closes that: it polls until the child leaves idle for
running/thinking/permission and exits 4 if it never does. Exit 4 here means
"nothing confirmed the turn started" — attach and look at the input box
rather than resending blind. See the `exit-codes` doc.

## Inspecting what the child did

`session output` gives you the assistant's text. That is often not enough —
a child that reports "fixed it" may have edited nothing.

`session result` returns structured transcript entries (`text`, `thinking`,
`tool_use`, `tool_result`) so you can check the actions rather than the claim:

```bash
# Only Bash calls
jin session result fix-login --tool Bash --json

# Only failed tool calls
jin session result fix-login --errors-only --json
```

**`session output` and `session result` only work for Claude Code sessions.**
For `codex` and `opencode` they return empty rather than an error. See the
`gotchas` doc.

### Incremental collection

`--since` is strictly exclusive: pass the last entry's timestamp to get only
what came after it, with no duplicates.

```bash
T1=$(jin session result work --json | jq -r '.entries[-1].timestamp')
# only when the previous wait came back idle, not permission
jin session send work "now also run go vet" --wait-running
jin session wait work --until idle,permission
jin session result work --since "$T1" --json
```

## Statuses

`creating`, `running`, `thinking`, `idle`, `permission`, `stopped`, `deleting`.

Terminal for a turn: `idle` (finished) and `permission` (blocked, needs a
human). `stopped` means the process is gone.

## Accepting the work

Do not forward a child's report as your own conclusion. For code changes,
check `git diff` and run the tests yourself. If the work is short of the bar,
send a correction to the *same* session — it still has the context:

```bash
# only when the previous wait came back idle, not permission
jin session send fix-login "the test still fails on line 42; fix that too" --wait-running
jin session wait fix-login --until idle,permission
```

Escalate to the human when a session sits in `permission`, or when two rounds
of correction have not moved it.

## Running several at once

Sessions are independent. Create them all, then wait on each in turn:

```bash
for d in api web worker; do
  jin session new --workdir ~/repos/$d -d "task-$d" --json
done
for d in api web worker; do
  jin session wait "task-$d" --status idle
  jin session send "task-$d" "..." --wait-running
done
for d in api web worker; do
  jin session wait "task-$d" --until idle,permission --timeout 900
done
```

Creating them all first lets the sessions provision in parallel; the second
loop then only waits out whatever is left.

Give each a distinct `--description`: that is what makes them addressable.
Use `--fleet <name>` to group related sessions.

## Cleaning up

```bash
jin session list --json           # what exists
jin cleanup stopped --dry-run     # preview
jin cleanup stopped               # remove stopped sessions
```

Leaving sessions behind costs a tmux pane each and clutters every later
`session list`.
