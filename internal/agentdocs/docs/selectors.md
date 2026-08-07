---
title: Address a session with a selector
description: How jin resolves the <selector> argument to one session, and how to fix an ambiguous-selector error.
---

# Address a session with a selector

Most `jin session` subcommands take a `<selector>`, not an ID or a `--name`
flag. A selector is resolved in four stages, and the first stage that produces
exactly one match wins.

## Resolution order

1. **ID exact match** — the full session UUID
2. **ID prefix** — 4 characters or more, must match exactly one session
3. **Description exact match** — must match exactly one session
4. **Description substring**, case-insensitive — must match exactly one session

Selectors shorter than 4 characters skip stage 2 entirely: a 2-character
string is far more likely to be a description fragment than the front of a
UUID, and treating it as both would make short names unusable.

## Give every session a description

```bash
jin session new --workdir ~/repos/myapp -d fix-login
```

Now `fix-login` addresses it, and so does `login`. Without `-d`, the
description defaults to the working directory's basename — which is fine until
you have three sessions in sibling checkouts and every selector is ambiguous.

## When it is ambiguous

Matching more than one session at a stage is an error (exit code 6), not a
silent pick of the first. The message lists the candidates:

```
$ jin session kill test
ambiguous selector "test": 3 sessions match
  a1b2c3d4  test-api
  e5f6a7b8  test-web
  ...
```

Recover by narrowing to something unique — a longer substring, or the ID
prefix from the listing:

```bash
jin session kill test-api
jin session kill a1b2c3d4
```

Up to 10 candidates are listed. If you get that many, `jin session list --json`
and filter it yourself rather than guessing.

## When nothing matches

Exit code 2, `session not found`. Usually one of:

- the session was deleted (`jin session list --json` to confirm)
- a typo in the description
- you passed an ID prefix shorter than 4 characters

## Shorthands

`session` is also `sess`; `list` is also `ls`; `delete` is also `rm`; `send` is
also `prompt`. So `jin sess ls` and `jin session list` are the same command.
