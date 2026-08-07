# Gotchas

Common pitfalls and caveats that agents tend to fall into.

## tmux

- **Put `--` before any user-supplied positional argument.** `send-keys`
  reads a leading dash as a flag, and it fails two different ways —
  `send-keys -l "-abc"` exits 1 with `unknown flag -a`, but `send-keys -l
  "-R"` exits **0 and sends nothing**, because every character happens to be
  a valid flag. The quiet one is the dangerous one: the caller sees success
  and carries on with a payload that never arrived. `SendKeys` and
  `SendKeysLiteral` both pass `--` (see the comment on `SendKeysLiteral` in
  `internal/tmux/tmux.go`), which matters most for `SendPrompt`, since it
  splits long prompts on a byte boundary and a chunk can start with a dash
  even when the prompt does not.

  Of the tmux verbs this package wraps, `send-keys` is the only one that
  takes a user-controlled string as its first positional — everywhere else
  such values sit in a flag's argument slot, where option parsing already
  consumes them. Re-check that if you add a verb.

- **Driving an agent pane with a long burst of keys can leave the outer
  terminal in a state where tmux's prefix stops working.** Symptom: `C-b` and
  any other keybinding does nothing, while the mouse still switches panes.
  The cause is a keyboard-protocol negotiation (kitty keyboard / CSI-u): once
  the real terminal is in that mode it encodes `C-b` as an escape sequence
  rather than byte 0x02, and tmux with `extended-keys off` does not recognise
  it. Mouse events use a separate protocol, hence the asymmetry.

  Recovery is to reset the terminal — closing and reopening the tab is
  reliable; `printf '\033[<u\033[>4;0m'` or `reset` also works. Setting
  `extended-keys on` makes tmux decode what the terminal is now sending,
  which restores the prefix without touching the terminal. Worth knowing
  before assuming tmux or jin has hung.

- **remain-on-exit is set at the pane level** (not globally).
  `TagManagedPane()` applies it only to managed panes.
  Panes added by the user are destroyed immediately.
  (Fixed in commit 980e99f)

- **`Kill` stops the pane's process; it does not destroy the pane.**
  `TerminatePaneProcess` sends SIGHUP to `#{pane_pid}` and the pane stays as a
  dead pane, so `TmuxWindowName` / `TmuxPaneID` still address something real
  after a kill — code that reads either as "this session has live tmux state"
  needs `Status` too. **SIGTERM does not work here**: the `/bin/sh -c` wrapper
  tmux starts execs down the chain, so the pane's pid ends up being the
  *interactive* `$SHELL -ic` that runs the agent, and interactive shells ignore
  SIGTERM (measured: zsh 5.9 stays in `Ss` with `pane_dead=0`). SIGHUP is what
  the pane already receives today when `kill-pane` closes the pty. It goes to
  the pane's own pid, not its process group — the agent sits in a job of its
  own, which a killpg on the pane's group would miss. Only the fallback path
  (process ignored the signal, or its pid could not be read) reaches
  `kill-pane`, and it tears the inner session down with it so nothing is left
  unowned once the two fields are cleared.

- **A dead pane keeps reporting the pid it started with.** `#{pane_pid}` on a
  pane with `pane_dead=1` still answers with the exited process's number
  (verified on tmux 3.6a), which the OS may have reissued to something else
  entirely. Anything that signals a pane must check `IsPaneDead` first —
  `stopAgentPane` does, which is what makes a second kill a no-op instead of a
  shot at a stranger's process. `pane_dead` also only speaks for the pane's
  direct child; a descendant that survives the pty SIGHUP would leave the pane
  dead and the agent running.

- **A kill is no longer visible as a cleared `TmuxWindowName`.**
  `Session.killSeq` is what tells a caller that dropped `m.mu` whether a kill
  landed while it was away — `applyRecovery` compares it to decide whether its
  probe results still describe reality. `Status` cannot substitute: a session
  reloaded from disk is normalized to Stopped before anyone kills anything.

- **tmux session name** is the `tmux.SessionName` constant ("jin"). Do not change it.

- **inner tmux**: jind-ai uses its own tmux socket (`-L jin`).
  It runs as a separate server process from the user's main tmux.

- **base-index issue**: If `base-index=1` is set in the user's `~/.tmux.conf`,
  the `:0.0` target becomes invalid. Use pane IDs (`%N`) instead.

- **Pane options survive `respawn-pane`.** `respawn-pane -k` replaces the
  process and `clear-history -H` wipes the screen, but neither resets
  pane-scoped options. `tmux.PaneLabelOption` feeds `pane-border-format`, so a
  pane emptied without clearing it keeps the old session's name on its border.
  Reset it alongside every respawn — `Model.clearDisplayedSession`
  (`internal/tui/model.go`, the inverse of `recordDisplayedSession`) and
  `reattachTmux` (`cmd/jin/cmd/tui.go`) are the two places that do.

- **A delete finalizes after the daemon has answered.** `handleDelete` accepts
  the request once `PreCheckDelete` passes and does the worktree removal and
  `kill-session` on a background goroutine. Anything still attached to the
  target keeps displaying it for that whole window — and then displays the dead
  frame `tmux attach` leaves behind. The TUI moves the display pane off the
  target at request time (`Model.deleteSession`), not when the record finally
  disappears from `List`.

## Session send

- **`SendPrompt` verifies keystrokes landed before pressing Enter.**
  `tmux send-keys` reports success unconditionally, even when the target TUI
  is still redrawing after startup and drops the incoming keys. To make this
  observable, `Manager.SendPrompt` captures the pane before and after the
  send and checks that the tail of the prompt appeared in the visible buffer
  (`sendVerifyOK` in `internal/session/manager.go`). Attempts repeat with
  backoff until the budget from `sendVerifyBudget` runs out; Enter is only
  pressed after a successful verify.

  **The contract reads in one direction only.** A prompt that was dropped
  outright is reported as an error instead of being silently committed —
  that half holds. The converse does not: when `jin session send` returns
  nil, the prompt **reached the input area**, and that is all it says. It
  does **not** say the turn was submitted. Verify observes the input area;
  the Enter that follows is a separate event whose outcome nothing checks.
  The next entry is the case where the two come apart.

- **A completion overlay eats the Enter, and the send still reports
  success.** Claude Code opens a file-completion overlay while the prompt
  ends in an `@` token, and Enter there accepts a candidate instead of
  submitting. Measured on Claude Code 2.1.224 / tmux 3.5a, three runs per
  row:

  | prompt | overlay | what Enter did | n |
  |---|---|---|---|
  | `list @internal/agent` | opens | consumed; the input area was left holding `list @internal/agentdocs/`, rewritten by the completion and unsent | 3/3 |
  | `list @internal/agent and say ok` | does not open | submitted normally | 3/3 |
  | `/<ambiguous-prefix>` | never drawn, still live | ran a **different** command | 3/3 |
  | `/<exact-command>` | never drawn | ran as sent | 3/3 |
  | `explain the fix/send-deadlock branch` | does not open | submitted normally | 3/3 |

  Verify passes in every one of those rows, because the text really is in the
  input area — so `SendPrompt` returns nil and `jin session send` exits 0.
  What makes this expensive rather than merely wrong is the status: an
  unsubmitted prompt leaves the session on `idle`, so a
  `session wait --until idle,permission` behind it returns immediately and
  the caller reads the **previous** turn's output as this turn's result.
  Nothing in the sequence looks like a failure.

  A bare slash command is the same defect with the evidence removed: sent as
  one `send-keys -l` burst the slash overlay is never drawn into
  `capture-pane` at all, so no capture-based check can see the state it is
  in. An ambiguous prefix still ran a different command than the one sent —
  which one depends on where the nudge left the selection, see the nudge
  entry below.

  **The fix closes the overlay instead of detecting it.**
  `Agent.DismissOverlayKeys(prompt)` lets an adapter declare the keys for
  that; claude returns `["Escape"]` for a prompt whose last whitespace-
  separated token starts with `@`, or whose trimmed whole starts with `/`
  and contains no whitespace, and nil otherwise. The condition is narrow on
  purpose: Escape also interrupts a turn in progress, so it must not go to
  prompts that cannot open an overlay. `SendPrompt` sends the keys after verify
  succeeds and before Enter, then captures once more to confirm the tail is
  still in the input area — a dismiss key that wiped the input would
  otherwise turn one silent failure into another. With Escape in place every
  row above submitted verbatim, 3/3 each, the ambiguous prefix included
  (Claude Code then answers `Unknown command: <prefix>`, which is the correct
  outcome for what was actually sent).

  codex and opencode return nil. Their completion overlays are unmeasured,
  and applying an unmeasured remedy is how the claim this entry replaces got
  written in the first place.

  **The narrowing bounds the interrupt risk, it does not remove it.** The
  prompts that get Escape are exactly the ones an orchestrator sends most —
  a bare `/command`, or a message ending in an `@path`. So if a session is
  reported `idle` while it is in fact mid-turn, one of those sends will
  interrupt real work rather than merely queue behind it. That misreport is
  a known open bug: jin shows the parent as `idle` while a sub-agent runs.
  Sending on a wrong `idle` was already destructive — the clear keys and the
  prompt go into a live input either way — but before this change the turn
  survived, and now it does not. Weigh that before widening the condition.

  The clear step is unaffected: `C-u` was measured to empty Claude Code's
  input even with the overlay open (3/3), so the residue-concat path that
  would otherwise follow a swallowed Enter does not arise.

- **The verify contract covers delivery, not readiness — the first send still
  has to wait for idle.** `SendPrompt` rejects any session whose status is not
  `idle` before it touches the pane, so on a session that just came up verify
  never runs at all. `session new` answers while the session is still
  `creating`: provisioning and `StartBackground` are dispatched to a
  goroutine and the handler returns the reservation immediately. The session
  then sits at `running`, and on the starts measured nothing the agent does
  moves the status off it before the timeout in the next bullet does —
  `Stop` needs a turn to finish, and `idle_prompt` needs the input line to
  sit untouched long enough for Claude Code to notice. Measured on Claude
  Code over three normal starts, `new` to `idle` took 42s every time, and a
  `send` fired straight after `new` failed 3/3 with
  `session is not idle (current status: running)` — `creating` if you beat
  a process spawn to it.

  **So the wait belongs there, and it is `--status idle` specifically.** Not
  the `--until idle,permission` that README recommends for orchestration:
  that pair is for waiting on a turn to end, and `SendPrompt` accepts only
  `idle`. Pass the id `new` returned (`--json`, `.id`) when you let the
  description default, because worktree provisioning and the agent's first
  hook can both rewrite it inside that window; an explicit `-d` sets
  `DescriptionLocked`, which closes both, and that is why the agent docs
  address sessions by description. Leave the 300s default alone:
  a hook arriving late in startup restarts the clock behind it, so trimming
  to just past the measured 42s is not safe.

  **A `session wait` that times out is a diagnosis, not a signal to retry.**
  `--no-start` parks the session on `stopped`, and so do a creation whose
  provisioning or start failed (that one also sets `ErrorMessage`) and an
  agent that spawned and then died (that one does not). None of them ever
  reaches `idle`, so follow the timeout with `session info`: `stopped`
  separates those from a slow start. `running` does not — a slow start and
  a session the daemon recovered after a restart look alike there, and the
  next bullet explains why the recovered one never crosses on its own.

- **What ends the wait for a fresh session is a timeout, not a readiness
  signal.** The `idle` it arrives at comes from the `hookIdleTimeout`
  fallback in `captureOutputTmux`: 30s with no hook, measured against
  `LastOutputTime`. Despite the name, the pane's own output never moves that
  field — the start, a pane respawn, daemon recovery, an incoming hook and
  the fallback itself do — so every hook that lands during startup restarts
  the 30s, and one carrying a status verdict takes the session off `running`
  so the fallback stops applying rather than merely resetting. The two are
  not the same: a reset only delays the crossing, with no ceiling on how far
  it slides, while a verdict cancels it outright — the fallback is guarded
  on the status still being `running`, and only jind-ai's own start, restart
  and recovery paths write that back. A `thinking` verdict still reaches
  `idle` on the turn's `Stop`; `permission` waits on a human, and `stopped`
  on nothing at all. Recovery writes `running` back without re-arming the
  fallback, because the guard also requires `StartedAt`, which is
  runtime-only: a session the daemon recovered after a restart is left
  outside the fallback on purpose and waits on a real hook.

  **The crossing lands on a 10s tick, and that is where the 42s comes
  from.** The check runs on the capture loop's tick, created a few lines
  after the clock is set, so a start with no hooks at all crosses on the 30s
  tick. A normal Claude Code start is not that: its `SessionStart` hook
  lands inside the first tick and moves `LastOutputTime` without moving the
  status, pushing the crossing out to the 40s tick — which is the 42s
  above, read through `session wait`'s own 2s poll.

  **Nothing in that path looks at what is on screen.** A session blocked on
  a dialog (Claude Code's trust prompt, say) is exactly as quiet and is
  marked `idle` too. The send that follows passes the status gate and fails
  verify instead.

- **The note this replaces was wrong when it was written, not merely
  outdated.** It said orchestration callers could drop the wait between
  `session new` and the first `session send`. The idle gate predates
  verify-by-capture, so the wait was already required then; the note mistook
  a stronger transport guarantee for a readiness guarantee. Making `new`
  asynchronous later widened the window rather than opening it.

  The transport guarantee it leaned on was itself weaker than advertised —
  delivery to the input area, never submission (see the completion-overlay
  entry above). So the note was wrong twice over, and the second error
  outlived the first: the correction written at the time replaced the
  readiness claim and left the transport claim standing.

- **The retry budget scales with the prompt, so it is not a fixed 5s.**
  A big prompt costs more per attempt (more chunks, far more clear
  keypresses, more nudges), and a flat timeout would quietly leave large
  sends with one attempt and no retry — or cut the look loop short before the
  tail has been walked into view. `sendVerifyBudget` adds a per-chunk, a
  per-clear-key and a per-look term on top of `sendVerifyTimeoutBase`. A short
  prompt still gets roughly the old 5s and stays responsive.

- **A failed verify retries by looking again, not by re-sending.** Re-sending
  is destructive: the next attempt clears the input area and pushes the whole
  prompt again, throwing away whatever the TUI had rendered. An agent slower
  than one settle delay is then restarted from zero every time and never
  converges. So one attempt nudges and re-checks the pane up to
  `sendVerifyLookCount` times before it gives up and re-sends. Measured on Codex
  with a 16KB prompt: re-sending immediately produced 16 failed attempts
  across the entire budget, while looking without re-sending verified in
  1.7s. The same change took a small prompt to OpenCode from never verifying
  to verifying reliably.

- **The clear phase dominates the cost of a large send.** Every tmux verb is
  a separate process, and the input-area clear issues one press per visual
  row of possible residue — 277 presses for a 16KB prompt. Measured
  per-invocation: ~1.4ms calling the tmux binary directly, ~3ms via
  `exec.Command` from a Go process (what the daemon actually does), and
  ~33ms from a shell when `tmux` on `PATH` is a version-manager shim that
  re-execs the real binary. `send-keys -N` would batch the presses, but it
  was measured to have no effect on Claude Code or OpenCode — only Codex
  honours it — so they cannot be collapsed.

- **`send --wait-running` is the only thing that observes a submission.**
  `SendPrompt` guarantees keystroke reception and stops there, so the
  question `--wait-running` asks — did the session leave idle for
  running/thinking/permission? — is the only signal that separates a prompt
  the agent took from one still sitting in the input area. Keep the flag on
  any send whose result something downstream reads.

  An earlier version of this entry said the opposite: that callers who "only
  care whether the prompt was seen" could drop the flag. That advice inverts
  the risk, because "seen" was never the property in doubt. It rested on
  reading the delivery guarantee as a submission guarantee — the same
  misreading corrected at the top of this section.

  It is a signal, not a proof of failure in reverse: a timeout says nothing
  confirmed the turn started, which covers an agent that was merely slow as
  well as an Enter that went nowhere. That is an "attach and look" outcome,
  which is what `exitcode.Timeout` from `send` is documented to mean.

- **The verify check keys off the prompt's tail, not full text.** TUIs wrap
  long input across visible rows and may add ANSI styling. `promptTail` /
  `normalizeForVerify` reduce both sides to the same form and match only the
  last `sendVerifyTailBytes` bytes. A prompt whose entire tail happens to
  already exist in the pane (rare — e.g. re-sending the same short phrase
  seen elsewhere on screen) will not falsely satisfy verify because the
  check compares occurrence counts before/after, not mere presence.

- **Verify removes whitespace rather than collapsing it, and drops
  box-drawing runes.** `capture-pane` emits a newline at each wrap position
  where the prompt itself has nothing, so collapsing runs to a single space
  still leaves the needle and the pane disagreeing exactly at the seam —
  measured failure rate for a 32-byte tail was ~16% on Claude Code and
  Codex and ~44% on OpenCode, and Japanese text meets the condition
  essentially always. OpenCode additionally draws a vertical bar at the
  start of every wrapped row, hence stripping U+2500–U+257F. Both sides are
  normalized identically, so a prompt that legitimately contains box-drawing
  characters still matches. Do not "strengthen" verify by raising
  `sendVerifyTailBytes`: a longer needle is *more* likely to straddle a
  wrap, not less. Real captured panes covering this live in
  `internal/session/testdata/sendverify/`.

- **Prompts are sent in chunks, not one write.** A single oversized
  `send-keys -l` either exceeds tmux's own argument limit (16341 bytes,
  reported as `command too long`) or gets folded by the agent TUI into a
  `[Pasted Content N chars]` placeholder that hides the tail from
  `capture-pane` — so the send looks dropped even though it landed. Measured
  fold thresholds: Claude Code 801B, Codex 1001B, OpenCode none;
  `sendChunkMaxBytes` is 800 to stay under all of them. `sendChunkDelay`
  separates the writes because with no gap Codex coalesces adjacent chunks
  into one read and folds them anyway.

- **A nudge key is sent before every verify capture, and it is `Down`, not
  `End`.** Codex only repaints on key events — a capture taken without one
  can show stale content indefinitely (still stale after 37s in testing).
  OpenCode needs it for a different reason: it draws only a fixed-size window
  of its input buffer, and that window follows the *cursor*. `End` moves to
  the end of the current visual row, which on a wrapped multi-row input never
  reaches the end of the buffer, so the window never scrolls and the tail is
  never drawn — and undrawn text cannot be captured at all, because it lives
  only inside OpenCode and was never written to the terminal. `Down` advances
  a row at a time, walking the cursor and the window toward the end.

  `Down` is sent for every adapter, including ones that opted out of
  clearing. It was measured safe on Claude Code and Codex in the way that
  matters: five presses against an *empty* input left the pane byte-identical
  — no history recall dropping text into the field, which would commit
  something nobody sent — and twenty presses against a filled input preserved
  every byte. Both still verify a 16KB prompt with it. `C-End`, `NPage` and
  `C-e` moved nothing on OpenCode.

  **That population had no completion state in it, and `Down` is not inert
  there.** An empty input and a plain-text input were the only two conditions
  measured; a live completion list is a third, and in it `Down` moves the
  selection. Measured on Claude Code 2.1.224 with a slash prefix matching two
  commands, three runs each: without the nudge the first entry ran, with the
  nudge the second did — a different command from either the one sent or the
  one the same bytes produce unnudged. "Byte-identical across every case we tried" and "safe
  unconditionally" are not the same statement, and the gap between them is
  exactly the case nobody tried.

  It stays one constant rather than an adapter capability anyway, but for a
  new reason: the dismiss step closes the overlay before Enter, so whatever
  the nudge did to a selection no longer decides what gets submitted
  (prefix + nudge + Escape submitted the prefix verbatim, 3/3). Removing the nudge
  instead would have been the costly repair — `sendVerifyLookCount` scales
  with prompt length on the premise that each look drags OpenCode's viewport
  further toward the tail, and that premise is the nudge.

  That cover is claude-only. codex and opencode opt out of the dismiss keys,
  so for them the nudge is still unconditional over a completion state nobody
  has measured — the same standing this paragraph just took away from the
  claim above it. Measure their overlays before treating it as settled.

- **Input-area clear per attempt suppresses residual-concat corruption.**
  `Manager.SendPrompt` sends the key sequence returned by the adapter's
  `ClearInputKeys()` — currently `["C-u"]` for claude / codex / opencode —
  before each attempt's baseline capture, so any residual text in the TUI's
  input area (previous user typing, or a strict-prefix fragment left over
  when a first attempt was partially delivered and dropped the rest) is
  wiped before the new prompt lands. Without this step, the verify path
  ("did the tail appear one more time than before?") happily passes on a
  concatenated buffer, and Enter commits `<residual><prompt>`. Adapters
  that return nil or an empty slice from `ClearInputKeys()` opt out and
  fall through to the pre-refactor behaviour, inheriting the
  residual-concat risk. A visible side effect on covered adapters: a user
  manually attached to the tmux pane (`tmux attach -t jin`) mid-typing
  will see their input erased when `SendPrompt` is invoked externally.
  This is deliberate — the input area belongs to the transport layer
  during a send.

- **One clear press only clears one visual row, so the sequence repeats.**
  The count comes from the prompt's own length (a retry can only face
  residue from what we sent, which makes the prompt an upper bound), using a
  deliberately narrow assumed width — overshooting is harmless because the
  clear key is a no-op on empty input, while undershooting leaves residue.
  Never substitute `C-c` for this: it was measured to terminate Codex and
  OpenCode outright on a single press against empty input, and Claude Code
  on two presses under a second. An empty input area is the normal state, so
  a defensive `C-c` would kill sessions routinely.

  **`C-l` is the same trap and looks more tempting.** It appears to wipe
  Claude Code's input in one keystroke, but measured against a 270-byte
  input the first press changed nothing and only the second emptied the
  screen — and two presses is reported to run `clear`, discarding the
  conversation context. It fails at the job on one press and is destructive
  on two, against an input area that is usually already empty.

  How many presses the clear actually needs differs per agent *and per input
  size*, so do not tune the count from one measurement: a single `C-u` empties
  Codex and OpenCode at small sizes, while Claude Code removes one visual row
  per press (measured: 72 characters off a 270-character input, 171 off a
  1350-character one, matching its row width). The repeat count is sized for
  the agent that needs the most.

  **"OpenCode clears in one press" only holds for small buffers.** At 3072B it
  took 98–230 presses before the input read as empty, and at 4096B 300 presses
  did not empty it — whereas `sendClearRepeats` would send only 55 at that
  size. Those counts are an upper bound, not a requirement: presses sent while
  the agent is stalled may be coalesced. Do not turn "one press is enough"
  into an adapter-declared constant on this evidence.

- **OpenCode used to cap out around 2KB per send, and the cap was ours, not
  its.** This is the measurement that led to the paste transport below; it
  still describes what happens on the keystroke path, which OpenCode no longer
  takes. Measured on tmux 3.5a against a real pane, driving production
  `SendPrompt` with Enter swallowed, restarting the agent before each size:

  | prompt | result | budget | time OpenCode needs | `C-u` presses to empty |
  |---|---|---|---|---|
  | 256B–2048B | **3/3** | 15.3s | 0.5s → 8.4s | 1 |
  | 3072B | 2/3 | 19.2s | 14.0–19.3s | 98–230 |
  | 4096B | 0/3 | 19.9s | ~24s | 300 was not enough |
  | 8192B | 0/2 | 22.6s | **88s** | — |
  | 8192B, 5.5-minute window | **3/3** | (forced) | 88s, RSS ~1.47GB | 1 |

  Given enough time and memory headroom OpenCode handles 8KB fine — three
  independent runs at 1m28s each, peaking at ~1.47GB RSS from a ~700MB idle
  baseline, and the box still cleared in one press afterwards. It is expensive,
  not broken: one 4096-byte prompt grows RSS by about 500MB, and each nudge
  keypress costs roughly 0.87s of CPU. Claude Code takes 32KB and Codex 16KB on
  the same machine, so the cost is specific to OpenCode.

  **Two of our own limits stop it well before the agent does**, and neither
  scales with the prompt:

  - `sendVerifyBudget` gives 22.6s at 8192B, because `sendVerifyLookCount` is
    capped by `sendVerifyLooksMax = 60`. The cap binds from 3072B upward, so
    4096B and 8192B differ by only 2.7s of budget while the work grows from
    ~24s to 88s.
  - `defaultRequestTimeout` in the daemon client is 60s, so raising the send
    budget alone would just move the failure to the IPC layer.

  Raising the ceiling means raising both, deliberately — an unbounded verify
  budget would let one send block a session for minutes.

  **The 88 seconds is redraw, not transport, and bracketed paste removes it.**
  Delivering the bytes takes about 0.1s by any route; the rest is OpenCode
  rendering 8192 characters as if they had been typed. Measured at 8192B with a
  fresh agent per mode:

  | delivery | outcome | elapsed | RSS |
  |---|---|---|---|
  | chunked `send-keys -l` (what we do) | tail visible | 88s | +770MB |
  | `paste-buffer` without `-p` | tail visible | 96.7s | +860MB |
  | **`paste-buffer -p`** | folds to `[Pasted ~1 lines]` | **0.41s** | **±0MB** |

  An earlier note here said paste-buffer offered no advantage. That comparison
  omitted `-p`: with no paste markers tmux just writes the bytes, the TUI treats
  them as typed input, and the cost is identical to send-keys by construction.

  The catch is that a fold hides the tail, so `sendVerifyOK` can never match.
  Whether a placeholder can stand in for the tail depends on the agent:

  | agent | folds above | placeholder | the number means |
  |---|---|---|---|
  | Claude Code | ~801B | `[Pasted text #N]` | **a counter** (1, 2, 3 …) — useless |
  | Codex | ~1001B | `[Pasted Content N chars]` | **exact byte count** |
  | OpenCode | always, with `-p` | `[Pasted ~N lines]` | **exact line count** |

  OpenCode's count held at 1, 2, 3, 7, 33, 100, 999 and 2500 lines across
  900B–62KB (the `~` is cosmetic), and 62KB pastes instantly — far past anything
  send-keys reaches. A folded paste does submit in full: 8216B of 101 lines
  pasted, folded, and submitted came back as 8217B and 101 lines in OpenCode's
  own message store, the extra byte being a trailing newline it appends.

  **OpenCode now takes the paste path.** An adapter opts in by returning the
  text its TUI will show for the prompt from `Agent.PastePlaceholder`;
  `SendPrompt` then delivers the prompt as one bracketed paste and looks for
  that string instead of the prompt's tail. The adapter owns both the wording
  and the count it embeds, so nothing OpenCode-specific lives in the manager.
  Measured end to end through production `SendPrompt` on the same pane, at the
  normal budget:

  | prompt | before (keystrokes) | after (paste) |
  |---|---|---|
  | 64B | ok | 3/3, 89ms |
  | 8192B | **0/3**, needed 88s | **3/3, 0.11s** |
  | 16384B | out of reach | 3/3, 0.11s |
  | 65536B | out of reach | **3/3, 0.11s** |

  Claude Code and Codex stay on keystrokes: Claude Code's summary numbers
  pastes (`#1`, `#2`) instead of measuring them, and both already handle 32KB
  and 16KB fast enough that trading the tail match for a count buys nothing.

  Three properties of the paste path are worth remembering:

  - A count cannot detect bytes lost *inside* a line. Acceptable here because
    a paste is one atomic write, so the chunk-boundary losses the tail match
    was built to catch cannot occur.
  - OpenCode does not fold every paste (100B stayed text, 512B folded), so the
    tail match remains the fallback. `SendPrompt` accepts whichever shape
    appears rather than depending on where that threshold sits.
  - **The input clear is clamped on this path.** Sizing it from the prompt
    would spend 512 tmux processes wiping a single row before issuing the two
    calls that do the work — measured as the bulk of a 64KB send (1.12s
    against 0.11s clamped). The trade is that the clamped count wipes ~500
    typed characters but not ~5000; anything *pasted* collapses to one row and
    clears in a single press either way. The unclamped count never defended
    against human-typed residue anyway, since it scales with our prompt.

  Two consequences worth knowing:

  - **Failures at these sizes are the agent stalling, not keys being lost.**
    Keys are queued, not dropped: a character typed during a stall showed up
    later as delayed cursor movement. The agent also recovers — RSS fell back
    to 697MB after GC and the box then cleared in one press.
  - **Only the total verify window matters; the nudge cadence does not.** At
    4096B, 200ms × 78 looks (production) gave 0/3, while both 1500ms × 40
    looks and 200ms × 300 looks — same ~60s window — gave 3/3. Note that
    raising `sendVerifyLooksMax` alone does not widen the window, because
    `sendVerifyLookCount` returns `looksBase + len/60` and the cap only ever
    lowers that.

  **Terminal width does not matter, despite an earlier claim that it did.**
  2048B measured 3/3 at both 48 and 152 columns with the same timings
  (6.99/8.41/9.35s vs 6.97/8.42/8.20s). If anything wider is worse: 4096B was
  OOM-killed at 152 columns but survived at 48. The cost tracks rendered
  cells, not wrapped rows. The earlier "0/3 at 48 columns, 3/3 at 152" came
  from measuring on tmux 3.6a, which injects garbage into the input on attach.

  **The prompt itself always arrives.** Measured by deleting a known number of
  characters from the end and reading back how much remained, a 1080-byte
  prompt was present in full, and the same content split at 25, 50, 100 and
  270 bytes arrived complete every time — chunking is not the cause. Even the
  failing 8192B case had all 8192 bytes plus the tail marker sitting in the
  box. Because verify fails rather than passing on partial evidence, Enter is
  never pressed, so nothing truncated is committed. The failure is a loud
  "could not verify", not silent corruption.

  **That is a property of this failure mode, not of `send`.** It holds
  wherever the keys are the thing that goes missing, because verify is
  watching precisely that. The opencode update modal further down is the
  clearest example: it swallows every keystroke, nothing appears, verify
  never passes, and the error names itself. A completion overlay fails the
  other way round — the keys all arrive and only the Enter is taken — so
  verify passes on true evidence, `send` exits 0, and the session stays
  `idle` with an unsent prompt in the box. Same command, opposite shape: one
  is loud because the transport broke, the other silent because the transport
  worked and the commit did not. The dismiss step closes that second shape on
  claude; it does not turn "fails loudly" into a property of `send`. The
  clear-cap entry below is a third silent case, still open.

  The nudge must still not be sent in a burst: at 50ms intervals it never
  revealed the tail, at 200ms or slower it often did, which is why
  `sendVerifyLookDelay` is not tightened.


- **tmux behaviour varies sharply between versions, and 3.5a is the one that
  works — this table is what the README's 3.5+ floor rests on.** Measured
  against real agent panes on one machine:

  | tmux | garbage in the input on attach | rendering | repeated attach |
  |---|---|---|---|
  | 3.3a | — | — | **2nd attach fails: `can't find session`** |
  | **3.5a** | **no** | **fine** | **fine** |
  | 3.6a | **yes** | fine | fine |
  | 3.7a | no | **broken** | fine |

  - **3.3a** — the second attach to a session fails outright. jind-ai attaches
    repeatedly by design, so this is not usable at all; that failure is why the
    documented minimum sits above it.
  - **3.6a** drops a short run of garbage into an agent's input area whenever a
    client attaches — `c/0000cccc/cccc/cccc…`, fragments of `rgb:…`
    colour-query responses. It does not corrupt a send (the run is short and
    the per-attempt input clear removes it), but **numbers measured on 3.6a
    with a client attaching mid-run may have had garbage injected into the
    input**. Treat older measurements accordingly.
  - **3.7a** fixes that and breaks vertical redraw instead: OpenCode's input
    box stops growing as lines are added and only part of it paints, and Claude
    Code's post-submit working view renders wrong.

  The garbage was initially misattributed to OpenCode mishandling its own
  colour query — wrong on both counts, since it is version-dependent and not
  OpenCode's doing. Suspect the tmux version before the agent.

- **Above roughly 30KB the clear is best-effort and consistency is not
  guaranteed.** `sendClearMaxKeys` caps the repeats so a pathological prompt
  cannot spin the pane for minutes. Past the cap the input area may still
  hold residue, and the send proceeds anyway — `sendVerifyOK` only checks
  that the tail appeared one more time than before, so a leading
  `<residual>` is invisible to it and Enter would commit the concatenation.
  There is no error for this case. Callers that need a hard guarantee at
  that size should split the prompt themselves. Related: Claude Code elides
  the middle of very long input as `[...Truncated text #1 +N lines...]`
  above ~20800B and does not restore it for any key sequence, so
  "everything I sent is present" cannot be confirmed from the pane at all —
  only "the tail arrived".

## Session result

- **A nil `Agent.Transcript()` is an error, not an empty result.**
  `handleResult` used to call `transcript.NewReader()` — the Claude Code
  reader — for every session whatever its kind, so a codex or opencode session
  came back `entries: []` with `success: true`. That is the same answer a
  Claude Code session gives when the agent genuinely said nothing, so a caller
  could not tell "this kind cannot be read" from "the child did no work". The
  handler now resolves the adapter (`agent.Lookup(info.AgentKind)`, which the
  daemon already uses elsewhere) and refuses when the adapter has no reader,
  naming the kind in the error. The failure shape it replaces is the one from
  Session send above: an answer that is wrong and looks fine.

  **`AgentSessionID == ""` is the case that stays empty and successful.** A
  session whose agent has not started yet has no log to read, and that is a
  state every session passes through rather than a read failure — the same
  distinction `transcript.Reader` already draws by turning `ErrNoTranscript`
  into `(nil, nil)`. `TranscriptSource` requires that of every implementation,
  so an adapter cannot make "too early" look like an error either.

  This is a breaking change for opencode, which has no reader: `session result`
  on an opencode session exits non-zero where it used to exit 0 with nothing.
  See [CHANGELOG.md](../CHANGELOG.md).

  **`AgentSessionID != ""` does not prove the ID is the agent's own, and on
  codex it usually is not yet.** `ReserveCreation` pre-mints a UUID for every
  kind, and the Codex adapter deliberately ignores it — Codex has no
  `--session-id`, so it picks its own and jin learns it from the SessionStart
  hook write-back. Between spawn and that write-back, `Locator.Find` looks up a
  UUID no rollout carries, misses, and the handler answers empty and
  successful; if the session was launched without hooks (`SpawnCommand` falls
  back to a hook-less `codex` when `Setup` never captured an exec path) the
  window never closes. `AgentSessionStarted` does **not** discriminate — it is
  set at spawn (`manager.go`, "commit the started-once invariant"), not on
  hook arrival, so moving the early return onto it relocates the window rather
  than closing it. Closing it properly means recording whether the ID has been
  re-keyed, which is a persisted-field change; until then the ambiguity is
  stated in the agent-facing `gotchas` doc so an orchestrator does not read
  empty-and-successful as "the child did nothing".

- **The Codex reader groups rollout rows by role, because Codex writes one
  block per row.** A rollout is one `response_item` per block — measured over
  14 real sessions / 501 lines, 252 of those lines are `response_item` — so a
  1:1 mapping to `transcript.Entry` would make `--last 5` mean five *blocks*
  on codex and five *messages* on Claude Code: the same flag with a different
  meaning per kind. Consecutive rows are therefore accumulated into one Entry
  until `Entry.Type` changes, which reproduces the shape Claude Code's own
  transcript has:

  ```
  user prompt                          → Entry{user,      [text]}
  reasoning, assistant message, call   → Entry{assistant, [thinking, text, tool_use]}
  tool call output                     → Entry{user,      [tool_result]}
  ```

  Every one of the 501 lines carries a top-level `timestamp`, in the same
  24-character shape Claude Code emits and non-decreasing across all 14 files,
  so the lexical `since` comparison carries over unchanged. `Entry.Timestamp`
  is the group's **last** line rather than its first, which is what keeps an
  incremental read exact: `since` is exclusive, so every line already folded
  into the entry a caller last saw has to compare as "at or before" the
  timestamp that caller passes back. Stamping the first line would leave the
  group's later lines above the bound and return them again inside a partial
  duplicate.

  **A bare timestamp is not a unique cursor, and `--since` loses an entry when
  it collides.** If the entry following the caller's cursor carries the same
  timestamp as the cursor itself, the exclusive comparison drops it — and no
  later poll brings it back. This is a property of the protocol rather than of
  either reader: measured, adjacent entries collide on 1 of 112 pairs across
  the 14 codex rollouts and on 42 of 51,681 pairs across 242 Claude Code
  transcripts, where the same comparison has always been in use. Grouping makes
  a collision slightly likelier on codex, since a whole group collapses to one
  timestamp. Diverging here — making codex inclusive while Claude Code stays
  exclusive — would trade a rare silent loss for `--since` meaning two
  different things per kind, which is the failure grouping exists to avoid;
  fixing it properly means changing what the cursor is, which is an IPC change.
  `TestReadEntries_SinceDropsAnEntrySharingTheBoundaryTimestamp` pins the
  behaviour so it reads as known rather than as an oversight.

  **Grouping closes at the end of a read, so polling splits it.** An assistant
  group still being written comes back as one entry now and another entry on
  the next `--since` read, where a single full read would have returned one
  entry holding all of it. Nothing is lost or repeated — the blocks stay typed
  and ordered — but the entry count differs between an incremental and a whole
  read, and an assistant entry holding only a `tool_use` can read as "the child
  said nothing". Claude Code has no equivalent, since one line is one entry
  there. Read whole when completeness matters.

  **Known degeneration:** a human turn arriving directly after a tool result
  would land in the same `user` Entry as that result. The measured corpus never
  produces that ordering (an assistant turn always sits between them), and the
  blocks stay individually typed and ordered if it ever does, so it is a
  coarser grouping rather than lost information.

- **Rows that are deliberately not conversation.** `message` rows with
  `role: "developer"` are injected system text (43/43 in the corpus) and never
  reach the caller; nor do `response_item/agent_message` rows, which are
  inter-agent RPC. No `event_msg` row is copied in as an utterance either — the
  `event_msg` stream restates what `response_item` already carries, and reading
  both would double-count the same utterance in 12 of the 14 sessions. Codex
  also injects context under `role: "user"`, so user text is filtered block by
  block with the same `isPseudoUser` test the description enhancer uses — one
  item can hold an injection and the operator's own words side by side.

  **`event_msg/task_complete` is read for one thing: a turn that died.** When
  it carries an error the reader emits a standalone `system` entry holding
  Codex's own `info` and `message`. That is the only record of the failure —
  3 of the 14 sessions end with the agent having said nothing because the turn
  hit a usage limit, and from `response_item` lines alone that is
  indistinguishable from a turn still being worked on. An orchestrator would
  wait out its whole timeout on it.

- **Codex records no `is_error`, so `--errors-only` is structurally
  incomplete.** `custom_tool_call.status` is `"completed"` on all 41 of them,
  failed patches included, and there is no other flag. Two signals are
  recoverable from the output text — a first line starting with `Script
  failed` (1/41) and a JSON output carrying `"timed_out": true` (2
  occurrences) — and they are what sets `Block.IsError`.

  **A shell command exiting non-zero is not among them:** Codex writes
  `Script completed` and the exit code appears nowhere in the file. The whole
  class of "the build broke, the tests failed" is therefore undetectable, which
  makes an empty `--errors-only` on codex uninformative rather than reassuring.
  That limit is stated in the agent-facing `gotchas` doc
  (`internal/agentdocs/docs/gotchas.md`) because an orchestrator is who gets
  misled by it; keep the two in step. Reporting the gap in the response itself
  would change the IPC payload, which is a follow-up rather than part of this
  change.

  `event_msg/patch_apply_end.success` is a third signal and is not used: it
  carries no `call_id`, so there is nothing to attach it to.

- **Two more things a codex result cannot carry.** Token usage lives in
  `event_msg/token_count` rows with no id linking them to a message, so
  attaching it would mean pairing by position — an assumption, not a
  measurement — and `Entry.Usage` is left empty on codex until that pairing is
  verified. Reasoning summaries are empty in 53/53 rows (the content is
  encrypted), so no `thinking` block is emitted for them; 53 blank blocks would
  only crowd out real content under `--last N`.

- **`--tool` cannot discriminate on codex.** All 41 `custom_tool_call` rows in
  the corpus declare the name `exec` — 41 of the 46 tool calls it contains —
  with the actual operation inside the call body; the 5 `function_call` rows
  carry a name of their own, which is passed through the same way. The reader
  passes the declared name through rather than parsing the body:
  reading the harness-generated source to guess at `apply_patch` versus
  `web__run` would couple jind-ai to undocumented internals, and getting it
  wrong would attribute an operation the agent never named.

- **What the 14-session corpus does not cover.** No sample of `codex resume`
  exists, so whether a resumed session appends to its existing rollout or
  starts a new file is unverified — `--since` correctness depends directly on
  that. Neither is there any sample of a permission/approval wait, of
  `history_mode` other than `legacy`, of MCP tools, or of a custom tool other
  than `exec`. A full-corpus count says what the corpus contains, not what
  Codex can emit; treat those paths as unmeasured rather than absent.

## Hook

- **Session identification uses the `JIN_SESSION_ID` environment variable** (most reliable).
  Claude Code's session ID is used as a fallback.
  (Improved in commit a0bd6f7)

- **CWD tracking uses the hook's `cwd` field**.
  tmux's `pane_current_path` is also polled, but the hook takes priority.
  (Added in commit a705a80)

## Claude Code adapter

- **Workspace trust lives in `~/.claude.json`, which is not a settings file.**
  `EnsureTrustState` writes `projects[<workDir>].hasTrustDialogAccepted = true`
  there because that is the only place Claude Code reads it from; the docs
  describe the file as holding "per-project state (allowed tools, trust
  settings)" alongside the OAuth session, the user/local-scope MCP
  configuration and every cache. The settings files are a separate system with
  a validated schema that has no `projects` key at all, so trust written to
  `~/.claude/settings.json`, `~/.claude/settings.local.json` or
  `<project>/.claude/settings.local.json` is silently ignored. `--settings`
  injection cannot carry it either, which is why this is the one place any
  jind-ai adapter writes to user-global config — the documented exception to
  the design principle in [architecture.md](architecture.md), not a precedent
  for the next adapter.

- **Claude Code inherits trust from ancestor directories.** It checks the
  workspace key (the git root, or the start directory outside a repo) and then
  walks up to `/`, so a trusted parent covers everything beneath it.
  `EnsureTrustState` mirrors that walk before deciding to write. Practical
  consequence: **trust the directory worktrees are created under, once, and
  jind-ai stops writing to `~/.claude.json` for worktree sessions** — every
  session below it is already covered. Sessions started without `--worktree`
  run in the current directory, which is somewhere else entirely, so those
  still get an entry apiece until their own tree is trusted.

  The inheritance cuts both ways. The entry jind-ai writes for a session's
  workDir trusts **that whole subtree**, exactly as accepting the dialog there
  would have. jind-ai never writes an entry *above* the workDir, so the grant is
  always the tree the user pointed the session at — but starting a session
  directly in `~` or `/` does trust everything beneath it, and jind-ai does not
  refuse to: the only other thing it could do is leave the session stuck on the
  dialog it exists to prevent.

  The directory to trust is the *parent* of where worktrees land, not an
  individual worktree. By default that is `~/.local/state/jind-ai/worktrees`
  (worktrees go in `.../worktrees/<name>`). `worktree.base_dir` replaces that
  with a placement *template*, expanding `{name}`, `{repo}` and `${ENV}` — so
  what to trust is the deepest directory that is the same for every worktree,
  which is not always the template's parent. `/wt/{name}/src` puts it two levels
  up from the worktree, and a template containing `{repo}` has a different one
  per repository.

  ```
  cd ~/.local/state/jind-ai/worktrees && claude   # accept the trust dialog, then quit
  ```

  That directory must not sit inside a git repository. Claude Code keys trust by
  git root, so accepting the dialog there would trust the entire repository
  rather than the directory you are standing in. The default location is outside
  any repo; a custom `worktree.base_dir` need not be.

- **Old jind-ai versions wrote trust to `~/.claude/settings.local.json` and it
  never took effect.** Installs that ran those versions have a large dead
  `projects` map there — commonly over a thousand entries, nearly all of them
  paths of worktrees that no longer exist. jind-ai no longer writes to that
  file and does not delete or migrate what is already in it: the file belongs
  to the user, and moving the entries would only bloat `~/.claude.json` with
  dead paths. If the file has no other keys it is safe to remove:

  ```
  jq 'keys' ~/.claude/settings.local.json   # ["projects"] → nothing else is in there
  rm ~/.claude/settings.local.json
  ```

- **Every value in `~/.claude.json` survives a jind-ai write; the file's shape
  does not.** The adapter decodes it as `map[string]json.RawMessage` at both
  levels and sets exactly one scalar, because a typed round-trip would drop the
  ~76 top-level keys it has no fields for — including the OAuth session. For the
  same reason the encoder has HTML escaping switched off: `encoding/json`
  rewrites `<`, `>` and `&` inside raw messages too, which would mangle an MCP
  server URL's query string. Be clear about the limit, though: Go sorts map keys
  on marshal and the whole file is re-indented, so a write moves nearly every
  line even though no value changes. Claude Code restores its own ordering on
  its next write.

  One thing does not survive: bytes that are not valid UTF-8. Values are safe,
  but object *keys* decode into Go strings and the decoder substitutes U+FFFD —
  and the keys here are filesystem paths, which on Linux are bytes. Rather than
  silently rename somebody's project entry, such a file is refused like any
  other malformed input.

  The file mode is set to `0600` on every write. That is right for a file
  holding an OAuth session, but note it is applied to files that already exist:
  a `~/.claude.json` deliberately left at `0644` will come back at `0600`, and
  one at `0400` is now rewritten rather than refused, because replacing a file
  by rename needs no write permission on the file itself.

  A file that is unreadable or not valid JSON produces an error instead of a
  replacement, so the failure mode stays "a trust dialog appears" rather than
  "the user is logged out". Claude Code handles the corrupt case itself, and
  better than jind-ai could: it names the file and the parse error on stderr,
  moves the broken file aside into `~/.claude/backups/`, and starts from a fresh
  config. Repairing it here would race that recovery and skip the backup. The
  cost is that the session which hit the corrupt file gets the trust dialog —
  which in a jind-ai pane means it hangs, not that someone answers it. Claude
  Code's recovery leaves a valid file behind, so the next session start writes
  the entry normally; `JIN_DEBUG=1` logs jind-ai's side of it.

- **What the atomic write buys, and what it does not.** `atomicfile.Write`
  renames a complete temp file over the target, so no reader ever sees a
  half-written file. That is all it does. It does **not** prevent lost updates:
  read and write are separate steps, and whoever renames last wins.

  Concurrent calls dropped one of the two entries about 85 times in 100 when
  measured on a harness that calls `EnsureTrustState` directly. Ordinary session
  starts never reached that state — `Manager.StartBackground` holds `m.mu` for
  the whole of `startSessionTmux`, so they queue — but the quick-fail resume
  retry rebuilds its spawn command after releasing `m.mu` on purpose, and that
  one can run alongside a start. Those writes are serialised by a mutex in the
  adapter now, which covers everything inside the daemon process.

  Claude Code writing the file from *its* process cannot be locked out: the CLI
  takes no lock, so there is nothing for jind-ai to take either. That one is
  handled by re-checking instead. The adapter stamps the file's size and
  modification time when it reads, checks them again immediately before the
  rename, and on a change reloads and merges into the new contents rather than
  renaming a stale snapshot over it — up to three attempts, after which it
  writes anyway, because a session stuck on the trust dialog is worse than the
  sliver of a window three attempts leave. That sliver is the rename itself
  rather than the whole read-merge-write, which is where it would otherwise sit;
  measured, the latter runs about 19 ms on a config with 1800 project entries.

  This matters more than the numbers suggest, because jind-ai's normal use is
  starting sessions while other Claude Code processes are running. What would be
  rolled back is whatever the CLI last saved — usually `lastCost` or
  `lastSessionId`, but the same write covers the OAuth session. Note also that
  jind-ai writes at most once per session start and only when nothing in the
  workDir's ancestry is trusted yet, so trusting the base directory removes the
  exposure entirely.

- **The rename replaces a symlink instead of following it.** `os.Rename` swaps
  the link itself, so a `~/.claude.json` symlinked into a dotfiles repository
  would have become a plain file in `$HOME` with the repository copy orphaned at
  its old contents — silently, since nothing fails. `EnsureTrustState` resolves
  the path before writing, so the link survives and the file the user actually
  keeps is the one updated. A link whose target does not exist yet — repository
  not cloned, volume not mounted — is followed as well, via `readlink`, and the
  target is created: `filepath.EvalSymlinks` refuses a path it cannot stat, and
  falling back to the link path would destroy the link in exactly the case the
  resolution exists for.

  Because the rename has to stay on one filesystem, the temp file lands next to
  the resolved target — inside the dotfiles directory for the duration of the
  write, which is why it carries a recognisable `.jin-` prefix rather than a
  random name. It also means the write needs permission on that *directory*,
  not just on the file, which a plain overwrite would not have.

## Codex adapter

> Codex has two trust prompts of its own, both described below. Neither has
> anything to do with the Claude Code workspace trust above: different agent,
> different file, different mechanism. Nothing in this section touches
> `~/.claude.json`.

- **Initial `/hooks` trust approval is required.** The first time `jin
  session new --agent codex` runs in a given install (or after the `jin`
  binary path changes), Codex shows a `Hooks need review — N hooks are new
  or changed` dialog. Select **"Trust all and continue"** to enable status
  tracking. The trust hash is persisted to `~/.codex/config.toml` under
  `[hooks.state]`, so subsequent spawns skip the dialog as long as the
  command path stays the same. `--dangerously-bypass-hook-trust` is not
  used by jind-ai on purpose.

- **30s poll fallback during the trust dialog is harmless for status
  tracking, but not for sends.** Between session spawn and the user's trust
  confirmation, no hook fires. The daemon's `[POLL] no hook received for
  30s, fallback` path takes the status from `running` down to `idle`. Once
  trust lands, subsequent `UserPromptSubmit` / `Stop` hooks drive the status
  correctly. If you see the poll fallback in normal use, the trust dialog is
  usually still open in the pane. A `send` issued in that window is the part
  that is not harmless: it passes the status gate on that borrowed `idle`
  and then fails verify, because the dialog has the keystrokes. See
  "Nothing in that path looks at what is on screen" under Session send.

- **Directory trust ("Do you trust this directory?")** is a separate
  Codex sandbox prompt shown on the first launch in a given cwd; it is
  unrelated to `/hooks` and answered independently.

- **jind-ai forces two `-c` overrides on every Codex spawn** —
  `disable_paste_burst=true` so a large input is not folded into a
  `[Pasted text …]` placeholder that hides the prompt tail from verify, and
  `check_for_update_on_startup=false` so the update prompt does not eat the
  first keystrokes. They are passed per spawn, never written into
  `~/.codex/config.toml`. **Visible trade-off:** a human pasting into a
  jind-ai-managed Codex pane gets raw text instead of the placeholder.
  Rationale and the silent-ignore caveat are on `configArgs` in
  `internal/agent/codex/spawn.go` — read it before removing either key or
  relying on one to justify dropping the chunking.

- **`AgentSessionID` is unknown until SessionStart.** Codex has no
  `--session-id` equivalent (openai/codex#13242). jind-ai spawns fresh
  `codex` on first start, ignores the pre-minted UUID it created for the
  Session record, and lets the `SessionStart` hook's stdin JSON carry the
  real Codex UUID back — the existing re-key path
  (`manager.go:1231-1234`) latches it without any daemon change. On
  resume, `codex resume <UUID>` fast-fails in a few seconds for unknown
  IDs, so the existing 10-second quick-fail auto-recovery covers the
  "session removed by hand" edge case without a defensive pre-glob.

- **`Layer C-transcript` reads the rollout JSONL.** The Codex enhancer
  extracts the first `role: "user"` message that is not a
  `<environment_context>` pseudo-user injection. See
  `internal/agent/codex/rollout.go`.

- **`session result` reads the same rollout files, through a separate
  reader.** `Locator.Find` is shared; the entry mapping is not. What that
  format can and cannot express — and why `--errors-only` and `--tool` are
  weak on codex — is under "Session result" above.

## opencode adapter

- **`OPENCODE_CONFIG_DIR` is additive, not a replacement.** opencode's
  `ConfigPaths.directories()` returns
  `unique([~/.config/opencode, …project .opencode dirs, $OPENCODE_CONFIG_DIR])`,
  so pointing it at jind-ai state does **not** hide the user's own agents,
  commands or plugins. Setting it does suppress opencode's "seed an empty
  `~/.config/opencode/opencode.json`" behaviour, which is a bonus rather
  than a problem. Verified against opencode 1.17.18.

- **opencode treats the config dir as its own.** On start it writes a
  `.gitignore` into `<StateDir>/opencode/` and installs
  `@opencode-ai/plugin` into a `node_modules/` beside it. That is expected
  — it does the same to `~/.config/opencode` — and is exactly why the
  directory must live under jind-ai state rather than anywhere the user
  owns.

- **Bun does not need to be on `PATH`.** The opencode binary bundles its
  own Bun runtime, so the file-based plugin loads and runs even with bun
  entirely absent from `PATH` (verified: all three status events still
  fired). An external bun only matters for npm-specified plugins, which
  jind-ai does not use.

- **`session.status` fires once per step.** A single trivial turn
  publishes ~9 `session.status{type:"busy"}` events. The plugin suppresses
  consecutive duplicates of the same canonical event, so one turn yields
  one `UserPromptSubmit`. Removing that suppression multiplies daemon IPC
  for no gain.

- **Going idle publishes two events.** `SessionStatus.set()` publishes
  `session.status{type:"idle"}` *and* `session.idle`. Only `session.idle`
  is mapped to `Stop`; mapping both would double-report every turn.

- **`AgentSessionID` is unknown until `session.created`.** opencode has no
  flag that assigns a session id at startup (`--session` only continues an
  existing one). jind-ai spawns fresh, and the plugin's `SessionStart`
  carries the real id back through the usual re-key path. The resume
  branch keys off the **`ses_` prefix** rather than `AgentSessionStarted`,
  because `startSessionTmux` sets that flag before the process is even
  spawned — without the prefix test a pre-minted UUID would be passed to
  `--session`. For genuinely stale ids, `opencode --session <unknown>`
  exits 1 with `Session not found` in about a second, well inside the 10 s
  quick-fail auto-recovery window.

- **Every export in `plugin/jin.ts` must be a function.** The file has no
  default export, so opencode falls back to `getLegacyPlugins()`, which
  walks `Object.values(mod)` and throws `Plugin export is not a function`
  on the first export that is neither a function nor an object exposing
  `.server` (`packages/opencode/src/plugin/index.ts`). Adding one
  `export const VERSION = "1"` takes the entire plugin down, and opencode
  swallows a load failure as a warning — so the symptom is status silently
  never updating. The name `server` is conventional, not required: only
  the default-export path (npm-packaged plugins) validates names.

- **Subagents create child sessions, and their events must be dropped.**
  opencode's task tool calls `sessions.create({ parentID: … })`
  (`packages/opencode/src/tool/task.ts`), and the child publishes
  `session.created` / `session.status` / `session.idle` on the same bus as
  the parent. Forwarding them is actively harmful: `HandleHookEvent`
  re-keys `Session.AgentSessionID` on *any* hook whose `session_id`
  differs, so a child's `SessionStart` repoints the jin session at the
  subagent and breaks resume, and a child's idle reports the turn finished
  while the parent is still working. The plugin therefore reports on an
  **allow-list** of root sessions, never on "everything except known
  children" — see the next entry for why the deny-list shape is unsound.
  The allow-list is seeded from `JIN_OPENCODE_ROOT_SESSION` (set by
  `SpawnCommand` only when resuming) and extended by any `session.created`
  without a `parentID`, which covers a fresh spawn and `/new` mid-session.

- **Unknown session ids are resolved by asking opencode, not by guessing.**
  **Three** paths reach a session with no `session.created`: resuming with
  `--session`, switching sessions from the TUI (`/sessions`, and its
  `/resume` / `/continue` aliases, or `<leader>l`), and continuing a
  subagent through the task tool's `task_id`. An id arriving that way can
  be a root or a subagent, and both guesses are wrong in one of those
  cases — assuming "root" hands Manager a subagent id to re-key onto,
  assuming "child" silently freezes status after every session switch — so
  the plugin calls `client.session.get({ path: { id } })` once per unknown
  id and caches the answer. Lookup failures report nothing (fail-closed)
  and are deliberately not cached, so the next event retries rather than
  marking a real root permanently unreportable.

  Because opencode dispatches the hook as `void hook.event?.(...)`, handlers
  overlap; the plugin caches the in-flight promise, not just the result, so
  one turn's ~9 `session.status` events share a single lookup.

- **An opencode modal swallows every keystroke.** Some time after launch
  opencode raises an "Update Available — A new release vX.Y.Z is available"
  dialog that captures all keyboard input. While it is up, neither `tmux
  send-keys` nor `jin session send` can put a character in the prompt box;
  `Escape` dismisses it and input works again immediately. `jin session
  send` behaves correctly here — it retries, never sees the text land, and
  returns an error **without pressing Enter**, so a half-formed prompt is
  never committed. If a send fails with "the TUI may not have been ready to
  receive input", capture the pane before assuming the verify heuristic is
  at fault.

  It behaves correctly because of *how* this dialog fails, not because
  `send` covers dialogs generally: the keystrokes never arrive, which is the
  one thing verify watches. A completion overlay takes the Enter and lets the
  keystrokes through, and that one reports success — see the
  completion-overlay entry under Session send.

- **`session result` is unsupported here, and says so.** The adapter returns
  nil from `Transcript()`: opencode keeps its conversation in a SQLite
  database rather than a JSONL log, so it needs a reader neither shipped one
  provides. The command fails instead of answering empty — see "Session
  result" above for why an error is the better wrong answer.

- **The plugin is a pure observer.** It subscribes via the `event` hook,
  not the `permission.ask` hook. Note that `permission.ask` (a `Hooks`
  interface key, which can rewrite the user's allow/deny decision) and
  `permission.asked` (a bus event type) are different things that both
  exist — the upstream docs list them in a way that invites conflating
  them. Status reporting must never sit on the permission decision path.

## Agent picker (TUI)

- **Picker initial selection is snapshot at create-popup launch, not on
  each keystroke.** The create-popup reads `JIN_UI_AGENT` (transient
  default from `jin ui --agent`) and `config.default_agent` when it
  starts. Editing `config.yaml` while the TUI is already open does not
  change what the picker preselects on the next `n` press until the
  create-popup process re-launches (which it does per `n` press, so a
  new session created after saving config picks up the new default).

- **`jin ui --agent <kind>` writes an outer-tmux env, not a config
  value.** Starting `jin ui` without `--agent` on the same outer-tmux
  server clears the env (`UnsetEnvironment`) so a stale value from a
  previous `--agent codex` invocation does not silently preselect Codex.

- **The picker step disappears when only one adapter is registered.**
  `stepAgent` is skipped based on `len(agent.Kinds()) < 2`. Both create
  → agent and fleet-step Esc-back short-circuit past it so the flow
  matches the pre-picker UX in single-adapter builds.

## Switch-session popup (TUI)

- **`/` opens a tmux popup, it does not filter inline.** Unlike `vi` / `less`
  / most other TUI apps where `/` starts an inline incremental search, jind-ai
  binds `/` at the outer tmux (`jin-mgr`) root key table to launch the
  switch-session popup (`jin session-filter-popup`). Muscle memory from other
  tools ("press `/`, type, see the list narrow in place") will instead pop
  open a full-screen picker — this is intentional (see
  [architecture.md](architecture.md#switch-session-popup)), not a bug.

- **Requires tmux `display-popup` (tmux 3.2+).** The switch-session picker shares
  its launch mechanism with the action palette popup — both call
  `tmux display-popup -E`. On tmux 3.1 or older,
  `display-popup` doesn't exist, so the outer-tmux `bind-key` for `/` fires
  but the popup command errors out instead of opening. jind-ai's documented
  minimum is tmux 3.5+ (see README's Requirements section), which already
  covers this.

## Display pane (TUI)

- **A kill's reswitch is confirmed against the list, never consumed by
  whichever poll answers first.** `Model.pendingKillID`
  (`internal/tui/model.go`, resolved in `updateListMode`'s `sessionsMsg`
  branch) holds the session whose `Kill` was issued and stays armed until a
  `sessionsMsg` actually shows that session as no longer alive
  (`isSessionAlive`), or gone from the list. The session poll runs on its own
  clock, so a `List()` that started before the daemon saw the `Kill` still
  answers "running" — acting on that snapshot re-points the display pane at the
  session being killed, and nothing re-points it afterwards, because the killed
  record stays in the list and `displaysLiveSession()` therefore reports the
  pane as settled. What the user sees is whatever the dead attach left behind:
  `[exited]` when the re-attach beat the inner session's destruction,
  `no sessions` when it lost. A bare bool cannot express "not this snapshot" —
  any future re-point request needs to carry the same kind of evidence.

  The arm is not a leak risk. `Manager.Kill` fails only for a session it does
  not have, and that record is absent from every later list too, which disarms
  it. The one case that outlives its own kill is a `Kill` that fails in
  transport while `List` still succeeds: the arm then waits for that session to
  stop for some other reason, and re-points to whatever the cursor is on at
  that point — late, and not necessarily where the user last put the pane.

## Code Structure

- **Debug logging uses `internal/debug.NewLogger`**.
  Call `var debugLog = debug.NewLogger("filename.log")` to get a logger for any package.

- **config.Manager and config.StateManager are separate** instances. Do not confuse them.

- **Session.WorkDir is dynamically updated** (via hooks and tmux polling).
  Initial value = directory at creation time, but it follows when claude changes directory.

## Testing

- **Test coverage is ~40%**. Test files exist for all packages.
  Uses only the standard library (no testify, etc.). Add tests for new code.
  The `tmux.Runner` interface was introduced for testability.

- **Never build a Unix socket path out of `t.TempDir()`.** `sun_path` is capped
  at ~108 bytes, and `t.TempDir()` names its directory after the test — a long
  subtest name pushes the socket over the limit and `net.Listen` fails with
  `bind: invalid argument`. Go 1.26 truncates the pattern and Go 1.24 does not,
  so this passes on a newer local toolchain and fails on the version in
  `go.mod`. Use `testutil.SocketPath(t, name)`.

- **`make test-e2e` covers two packages**, `./test/e2e/` and `./internal/tui/`.
  The TUI's tmux-backed tests (`model_tmux_e2e_test.go`, build tag `e2e`) drive
  unexported `Model` methods against a real outer tmux, so they cannot live in
  the external `e2e` package. `go test ./...` does not build them, and the plain
  unit CI job has no tmux — the `e2e` job runs `make test-e2e`, so the package
  list only ever has to be edited in the Makefile.

## Concurrency

- **Session creation is protected by `createMu`** (at the daemon.Server level).
  This is a separate lock from `session.Manager.mu`.

- **I/O operations should be performed outside the lock** (to prevent deadlocks).
  Refer to the `List()` pattern: take a snapshot under RLock → release lock → read transcripts

## Popup Sizes

- **Outer-tmux bind-key popups need a `jin ui` restart** to pick up config
  changes. `action` (default `M-p`) and `session_filter` (default `M-f`) are
  bound at outer tmux (`jin-mgr`) root with hardcoded `-w`/`-h` args written
  once at TUI startup (`applyActionPanelBinding` / `applySessionFilterBinding`
  in `cmd/jin/cmd/tui.go`). Changing `popups.action` or `popups.session_filter`
  in config takes effect only after `jin ui` re-runs and re-issues the
  `bind-key` command. Inner popups (opened from inside the BubbleTea update
  loop — `create`, `help`, and the palette-launched
  `session_filter`) read config on each open, so they don't need a restart.

- **Popup sizes are percent-only**. `popups.<name>.width` / `.height` are
  `int` values in the range 1-100 (interpreted as percent of the outer tmux
  client area). tmux itself accepts absolute cell counts (`80`, `40c`) but
  jind-ai does not — the schema is deliberately narrower.

- **Range violations behave asymmetrically** between user config and plugin
  manifests. User config out-of-range (e.g. `width: 150`) logs a warning
  and falls back to the default — a broken config never blocks the TUI.
  Plugin manifest out-of-range (`popup.width: 150` in `jin-plugin.yaml`)
  hard-fails `Validate()` and the plugin lands in `StateBroken` — a plugin
  author is expected to fix the manifest.

- **`keybindings.plugins.<name>.keys` (0.7.x shape) is dropped on 0.8.0**.
  0.8.0 replaced it with `keybindings.plugins.<name>.actions.<id>.keys` to
  match the multi-action manifest schema. At startup, jind-ai logs one
  `plugin keybindings config: %s uses deprecated v1 shape ...` WARN per
  affected plugin and drops that plugin's bindings — the TUI itself still
  starts, so this is not a hard failure, but the shortcuts stay silent
  until the config is rewritten. For a plugin with only a default action,
  `actions.default.keys: [...]` is the shortest translation. The 0.8.0
  release note in [CHANGELOG.md](../CHANGELOG.md) has a full before/after
  example.
