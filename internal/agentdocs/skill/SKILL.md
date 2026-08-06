---
name: jind-ai
description: Delegate work to other agent sessions with jin (jind-ai) — create a session, send it a prompt, wait, collect the result. Use when asked to run work in a separate session, orchestrate several agents in parallel, or when a jin command fails and you need its documentation.
---

# jin (jind-ai)

`jin` runs agent sessions in tmux panes. As the orchestrator you create a
session, give it work, wait for it, check what it produced, and clean up.
You stay responsible for the result — a child's report is not a result.

## Read the docs first

The reference material ships inside the binary and always matches the
installed version:

```bash
jin docs list             # what is available, with a summary of each
jin docs show <name>      # read one
```

Start with `jin docs show orchestration`. Read `gotchas` before concluding
that something is broken — several failure modes look like bugs and are not.

Both commands work without the daemon. Everything else needs it
(`jin daemon status || jin daemon start`).

Do not guess flags from memory; `jin docs list` is cheap and current.
