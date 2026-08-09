This session is managed by jin (jind-ai), which runs agent sessions in tmux
panes. A parent agent may send you prompts and collect your output through it.

If you need to use jin yourself — to delegate work to another session, or
because a `jin` command failed — its documentation is in the binary:

    jin docs list             # available topics, with a summary of each
    jin docs show <name>      # read one

Both work without the daemon. Prefer them over guessing flags from memory:
they always match the installed version.

`$JIN_BIN` is the absolute path of the jin that started this session. Prefer
`"${JIN_BIN:-jin}"` over a bare `jin`: the one on PATH may be an older install.
