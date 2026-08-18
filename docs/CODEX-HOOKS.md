# Codex hook command execution

`rgt init --agent codex` writes a portable command hook into
`.codex/config.toml`.

On Unix, current Codex runs a command hook through `$SHELL -lc`, falling back
to `/bin/sh -lc`. Its source constructs that command in
[`command_runner.rs` at commit 711a5f8](https://github.com/openai/codex/blob/711a5f8b3a6eb40134146ae9ec22fdcdda5e3170/codex-rs/hooks/src/engine/command_runner.rs#L372-L411).
The [official hooks documentation](https://developers.openai.com/codex/hooks)
also documents `command` hooks and the platform-specific `commandWindows`
override.

Therefore the Unix command is deliberately a shell expression:

```sh
[ -x '/absolute/path/to/rgt' ] && exec '/absolute/path/to/rgt' codex-hook || exec rgt codex-hook
```

The absolute path keeps capture independent of the agent host's `PATH` on the
machine that installed it. If a teammate commits and clones the config, the
first command fails on their machine and the second invokes their own
PATH-installed `rgt`. The `exec` prevents a successful hook from running
twice.

On Windows Codex selects `commandWindows`, which uses the equivalent `cmd.exe`
form because `exec` is a POSIX-shell builtin:

```cmd
"C:\\path\\to\\rgt.exe" codex-hook || rgt codex-hook
```

If neither the embedded binary nor `rgt` on `PATH` exists, `rgt doctor` fails
and names the missing path plus the remediation: install `rgt` and put it on
`PATH`, or run `rgt init --agent codex` on that machine.
