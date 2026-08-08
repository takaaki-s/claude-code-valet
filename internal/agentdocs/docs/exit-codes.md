---
title: Exit codes and JSON errors
description: What each exit code means and how to branch on it, plus the shape of an error under --json.
---

# Exit codes and JSON errors

Every `jin` command reports failure through an exit code. Branch on the code
rather than on the message text — messages get reworded, codes do not.

| Code | Name | Meaning | What to do |
|------|------|---------|------------|
| 0 | Success | — | — |
| 1 | GeneralError | Anything without a code of its own — including a `session result` whose conversation could not be read at all (every shipped kind has a reader; on `opencode` the read runs `opencode`, which has to be on the daemon's PATH) | Read the message. For `result`, what an *empty* answer means still depends on the kind — see the `gotchas` doc |
| 2 | SessionNotFound | No session matched the selector | `jin session list --json` and re-check the selector |
| 3 | DaemonNotRunning | Reserved — **not currently emitted**, see below | — |
| 4 | Timeout | `session wait`, `send --wait-running`, or `respond` hit its timeout | From `wait`: the child is still working — wait again or escalate. From `send`: nothing confirms the child took the prompt — attach and look at the input box before deciding whether to resend. From `respond`: the prompt is still on screen — attach and look before answering again |
| 5 | WorktreeDirty | A git worktree has uncommitted changes | Commit, stash, or drop the changes |
| 6 | AmbiguousSelector | The selector matched more than one session | Narrow it — see the `selectors` doc |

## A daemon that is down exits 1, not 3

Code 3 is defined but no command returns it today: a failure to reach the
daemon surfaces as a general error. So there is no code to branch on. Check up
front instead:

```bash
jin daemon status >/dev/null 2>&1 || jin daemon start
```

Do not match on the message text to detect it — that is exactly what the rule
above warns against, and the wording is not a contract. Checking first costs
one command and cannot rot.

## Branching

```bash
jin session wait work --until idle,permission --timeout 300
case $? in
  0) ;;                                  # settled
  4) echo "still working" ;;             # not a failure — decide whether to keep waiting
  2) echo "session is gone" ;;
  *) echo "check the message" ;;
esac
```

Code 4 from `wait` deserves attention: it means the child did not settle in
time, not that anything broke. Waiting again is often right. Killing the
session on a timeout throws away work in progress.

Code 4 from `send --wait-running` means the opposite: the child did *not*
start on the prompt within the window. The text was verified in its input
buffer and Enter was sent, but nothing confirms the child took it.

"Verified in the input buffer" is a narrower claim than it sounds. It says
the text was there before Enter — not that Enter submitted it, and not that
what was there is still what you sent, since an agent's completion popup can
rewrite the text and then eat the Enter. So attach and look at the input box
first. Both blind reflexes are wrong from here: resending duplicates the turn
if the child merely started late, and repeats the same swallow if a popup
took the Enter.

The most frequent cause is neither: while a child runs a sub-agent, jin
reports the parent as `idle`, so `--wait-running` times out on a prompt that
was submitted and is being worked on right now. `jin pane capture <selector>`
settles it — an empty input box with the agent busy means the send was fine
and the flag was wrong, not the send.

Code 4 from `respond` means the prompt was still on screen after the answer
went out. That is a narrower uncertainty than send's: exit 0 from `respond`
means the prompt is gone, so a 4 is specifically "the keys were delivered and
the dialog did not move". Answering again risks answering twice — attach and
look first.

`respond` reports its other refusals as code 1, and they are worth telling
apart from a failure: nothing to answer, a form of several questions (which
jin will not half-fill), and an agent kind it does not drive all land there.
Read the message; each says what to do instead.

## Errors under --json

With `--json`, a failure prints one object to **stdout** (not stderr) and exits
with the same code:

```json
{"success":false,"error":"session not found: nosuch","exit_code":2}
```

So a JSON-consuming caller can read the outcome from stdout alone and does not
need to capture stderr separately.

Note the asymmetry: this shape applies to errors. Successful commands print
their own payload, whose shape depends on the command.

## Usage errors versus runtime failures

An unknown flag or a wrong argument count prints the usage block — you got the
command line wrong. A failure from inside a command that was invoked correctly
(`no session matches`, `daemon not running`) prints one line and no usage
block, so piping output to `tail` stays useful.
