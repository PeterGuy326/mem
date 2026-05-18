# Shell Stream Plumbing Cheat Sheet

In POSIX shells, every process inherits three file descriptors:
0 for input, 1 for normal output, 2 for diagnostics. The `>`
operator points fd 1 at a file. To capture diagnostics alongside
normal output use `2>&1` after the primary redirect, or the
shorter `&>file` form in Bash. Order matters: `cmd >log 2>&1`
sends both streams to `log`, but `cmd 2>&1 >log` only redirects
stdout and leaves stderr on the terminal.

Use `< file` to feed input, and a here-document `<<EOF` for
multi-line literals. Pipelines connect fd 1 of one process to
fd 0 of the next.
