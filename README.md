# hrd

`hrd` is a terminal picker for local Herdr sessions and Sprites. It gives the
terminal to the selected destination and refreshes after you detach with
`ctrl+b q`.

## Requirements

- Go 1.25.8 or newer
- `herdr` in `PATH`
- Optional Sprite support: `SPRITE_TOKEN` and an ssh-agent with a loaded key

## Install

```sh
go install github.com/joshuadavidthomas/hrd/cmd/hrd@latest
```

## Local sessions

Run `hrd`, choose a session, and press `enter`. The default session runs
`herdr`; a named session runs `herdr --session NAME`. Stopped sessions stay
selectable because Herdr restores them during attachment.

## Sprites

Set `SPRITE_TOKEN` before starting `hrd`. Press `n` to create a Sprite with a
lowercase name. Creation, waking, and provisioning can incur charges. `hrd`
sends one create request and never retries it. An unknown outcome blocks another
creation for that name until a complete inventory contains it or you reconcile
it outside `hrd`.

Before a remote attach, `hrd` reads current public keys from your ssh-agent,
installs OpenSSH when needed, writes them to
`/home/sprite/.ssh/hrd_authorized_keys`, and creates or updates the fixed
`hrd-sshd` service. It opens an ephemeral loopback-only SSH bridge. Tokens stay
in process memory and private keys never enter `hrd`.

The picker shows the authenticated SSH host-key fingerprint before terminal
handoff. Compare it with OpenSSH's prompt. Herdr may ask to accept the host key,
install its matching remote binary, or restart a mismatched server. If a reused
loopback port has an old host key, remove only the exact entry OpenSSH names:

```sh
ssh-keygen -R '[127.0.0.1]:PORT'
```

`hrd` never deletes a Sprite. Setup failures and unknown creates retain any
resource; clean up disposable Sprites outside `hrd`.

```text
enter   attach
n       create Sprite when enabled
r       refresh
/       filter
?       help
q       quit when the filter is not editing
ctrl+c  quit
```

The launcher invokes `herdr` directly with fixed arguments and no shell.
