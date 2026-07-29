# hrd

`hrd` is a terminal picker for Herdr sessions. It lists the sessions reported by
`herdr session list --json`, gives the terminal to the selected session, then
refreshes the picker after you detach.

## Requirements

- Go 1.25 or newer to build from source
- `herdr` installed in `PATH`

## Install

```sh
go install github.com/joshuadavidthomas/hrd/cmd/hrd@latest
```

## Use

Run `hrd`, choose a session, and press `enter`. The default session runs
`herdr`; a named session runs `herdr --session NAME`. Stopped sessions remain
selectable because Herdr restores them during attachment.

Detach from Herdr with `ctrl+b q`. The picker returns after the child exits and
loads the session list again.

```text
enter   attach
r       refresh
/       filter
?       help
q       quit when the filter is not editing
ctrl+c  quit
```

The launcher invokes `herdr` directly and passes arguments without a shell.
