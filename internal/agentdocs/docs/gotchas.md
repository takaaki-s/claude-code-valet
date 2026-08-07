---
title: Traps an orchestrating agent will hit
description: The failures that look like bugs but are not — per-agent limits on result collection, the creating-to-idle race, a send that exits 0 without starting a turn, and why a pane capture is not evidence.
---

# Traps an orchestrating agent will hit

## What `session result` answers depends on the agent kind

| kind | `jin session result` |
|---|---|
| `claude` | everything — text, thinking, tool calls, tool results, errors |
| `codex` | the conversation and the tool calls, with the limits below |
| `opencode` | **an error** — jin cannot read that agent's conversation log |

Check the kind before you decide what an answer means:

```bash
jin session info work --json | jq -r '.agent_kind'
```

The `opencode` failure is deliberate. That case used to answer zero entries
and exit 0, which reads exactly like a child that ran and said nothing — a
wrong answer delivered quietly. It now exits non-zero and names the kind.
It exits 1, the same code as any other general failure, so there is no code
that means "unreadable kind" — decide from `agent_kind` up front rather than
from the message text.

**An empty result that exits 0 is still ambiguous, and on codex it can be
wrong for longer than you expect.** It normally means the agent has not
started, so no conversation exists yet. But a codex session carries an ID jin
minted before launch, and Codex picks its own instead; until Codex's first
hook reports the real one back, jin looks up a session that does not exist and
answers empty and successful. If hooks are not wired for that session, it
never stops doing so. Do not read empty-and-successful as "the child did
nothing" on a codex session — check `git diff` and the tests, or
`jin pane capture` if you need to see that it is alive at all.

`session output` is a different command and unchanged: it reads Claude Code's
transcript only, and on a `codex` or `opencode` session it reports that no
transcript was found. For a codex child use `session result` instead.

What works for every kind: `new`, `send`, `wait`, `kill`, `delete`, `list`,
`info`, and status (`idle` / `thinking` / `permission`).

For a child jin cannot read, go to the artifacts:

```bash
git -C <workdir> diff              # what actually changed
cd <workdir> && <test command>     # whether it holds up
jin pane capture work              # last resort — see below
```

## `session info`'s last-message fields are a preview, not a result

`jin session info --json` carries `last_user_message` and
`last_assistant_message`. They are what the session list and the TUI row show,
and they now come from the session's own adapter — so a `codex` child fills
them in where it used to leave them blank forever.

Do not collect a child's work from them:

- **Empty means nothing in particular.** These decorate a row that has to
  render either way, so every failure is silent — no reader for that kind, an
  unreadable log, or an agent that genuinely has not spoken all produce the
  same two empty strings, and the command still exits 0. `session result` is
  the call that tells those apart.
- **They are truncated to 500 bytes**, the user preview from the start and the
  assistant preview from the end.
- **They are one message each**, not a turn: tool calls, tool results, and
  thinking are not in them.
- **Injected text is excluded on purpose** — environment blocks, the body of an
  invoked skill, a subagent's own turn. `last_user_message` is what the
  operator typed, which is usually what you want and is not the raw last user
  entry.

```bash
jin session info work --json | jq -r '.last_assistant_message'   # a glance
jin session result work --last 5                                  # the answer
```

## On Codex, `--errors-only` misses the failures you care about

**An empty `--errors-only` on a `codex` session is not evidence that nothing
failed.** Codex's log carries no error flag: a tool call that failed is
recorded exactly like one that succeeded — measured over 14 real sessions,
every one of the 41 calls that carry a status say `completed`, failed patches
included. jin can raise only two signals, both read out of the output text:

- the output's first line starts with `Script failed` (1/41 in that corpus)
- the output's JSON carries `"timed_out": true` (2 occurrences)

**A command that merely exits non-zero is neither.** Codex records
`Script completed` and the exit code appears nowhere in the file, so a broken
build, a failing test run, a rejected lint — the failures you delegate work in
order to find — cannot be detected at all.

So on codex, use `--errors-only` to find *some* failures quickly, never to
conclude there were none. Read the tool results, and run the check yourself:

```bash
jin session result work --json \
  | jq -r '.entries[].blocks[]? | select(.kind=="tool_result") | .output' | tail -50
cd <workdir> && <test command>
```

One codex failure *is* recorded, and it is worth checking for: a turn that died
outright (a usage limit, say) comes back as an entry of type `system` carrying
the reason. 3 of the 14 measured sessions end that way with the agent having
said nothing at all, which otherwise looks exactly like a child still working.

**`--errors-only` does not return it.** That filter keeps entries holding a
failed tool result, and a dead turn is not one — so the single failure Codex
does record is the one thing the failure filter drops. Run this alongside
`--errors-only` every time, not instead of it:

```bash
jin session result work --json | jq -r '.entries[] | select(.type=="system")'
```

## On Codex, `--tool` cannot tell one tool from another

Codex declares one tool name for nearly everything it runs: 41 of the 46 calls
in the measured corpus are named `exec`, and what actually happened — editing a
file, searching the web, running a command — lives inside the call body. jin
reports the name Codex declared, so `--tool exec` matches almost every call and
almost nothing else matches at all. Filter the blocks yourself instead.

## A Codex result carries no usage and no thinking

Token usage is recorded separately from the messages in Codex's log with
nothing tying the two together, so `usage` is always absent on codex entries.
Reasoning summaries are empty (53/53 in the corpus — the real content is
encrypted), so jin emits no `thinking` blocks rather than padding the result
with blank ones. Neither is missing data you can recover by asking again.

## Two Codex paths nobody has measured

- **`--since` after a `codex resume`.** Whether a resumed session appends to
  the same log file or starts a new one was not measured. Incremental
  collection depends on that, so after a resume prefer a full read over
  trusting `--since` to be complete.
- **Waiting on an approval.** No sample of how Codex records a pending
  permission prompt exists, so do not expect a result to show that the child
  is blocked on one. The session's status is what tells you that — wait with
  `--until idle,permission`.

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
Use `session result` (within the per-kind limits above), or check the
artifacts directly.

## A child's report is not a result

An agent that says "fixed and tested" may have edited nothing. For code
changes, look at `git diff` and run the tests yourself. `session result
--tool Bash --json` and `--errors-only` exist for exactly this check on
Claude Code sessions; on codex both filters are weak, so read the entries and
run the tests rather than filtering.

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
