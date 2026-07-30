# hrd

`hrd` is a terminal picker for local Herdr sessions and Sprites. It gives the
terminal to the selected destination and refreshes after you detach with
`ctrl+b q`. Run it from a terminal outside Herdr: `hrd` rejects nested launches
so the outer session cannot steal the inner session's keybindings.

## Requirements

- Go 1.25.8 or newer
- `herdr` in `PATH`
- Optional Sprite support: `SPRITE_TOKEN`
- SSH transport only: `ssh-agent` and `ssh-add`

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

Selecting a Sprite wakes only that Sprite and opens its Herdr sessions. The
opening inventory never wakes every Sprite. Session rows show Herdr's own
running or stopped state; the endpoint row separately shows the latest Sprite
API lifecycle observation. Inventory refreshes silently every ten seconds and
after actions that may change Sprite lifecycle. Press `r` to refresh it
explicitly and show progress.

When Herdr is absent, `hrd` asks before it downloads
<https://herdr.dev/install.sh> to a validated temporary path, runs that file
with `/bin/sh`, removes it, and checks that `herdr --version` runs. Opening a
Sprite never runs the installer without that confirmation.
Herdr publishes no checksum or signature manifest for this installer. HTTPS,
its redirects, the downloaded script, and the asset URLs chosen by that script
form the trust boundary. The installer is the launcher's sole shell use; no user
text enters its command or arguments.

After selecting a session, choose one of two transports:

- `Native Sprite terminal` is the default. It runs
  `/home/sprite/.local/bin/herdr --session NAME` inside the Sprite with the
  Sprites terminal API and sets `/bin/bash` as the shell for new panes. Herdr
  and its config live in the Sprite; no SSH key or service is needed.
- `SSH` keeps local Herdr and local integrations in use. It remains available
  for sandbox providers without a native exec PTY.

The SSH transport checks `SSH_AUTH_SOCK`, then 1Password's standard agent
socket. It uses a sole key automatically; when an agent exposes several, the picker asks
which one to use and remembers that choice for the current run. Only the
selected public key is installed in the Sprite.

When no usable key exists, `hrd` starts a private foreground agent and hands the
terminal to plain `ssh-add`. With no arguments, `ssh-add` loads OpenSSH's default
identities and owns any passphrase prompt. `hrd` never parses private-key bytes
or puts a key path in argv.

The SSH transport installs OpenSSH when needed, writes the selected public key
to `/home/sprite/.ssh/hrd_authorized_keys`, and creates or updates the fixed
`hrd-sshd` service. It opens an ephemeral loopback-only SSH bridge. Tokens stay
in process memory. The private agent and its socket are removed when `hrd`
exits.

The picker shows the authenticated SSH host-key fingerprint before SSH
handoff. Compare it with OpenSSH's prompt. Herdr may ask to accept the host key,
install its matching remote binary, or restart a mismatched server. If a reused
loopback port has an old host key, remove only the exact entry OpenSSH names:

```sh
ssh-keygen -R '[127.0.0.1]:PORT'
```

`hrd` never deletes a Sprite. Setup failures and unknown creates retain any
resource; clean up disposable Sprites outside `hrd`.

```text
enter   open an endpoint or attach a session
n       create Sprite from the endpoint screen
s       stop a running Sprite session
R       restart a running Sprite session
r       refresh the current screen
/       filter
esc     return to endpoints
?       help
q       quit when the filter is not editing
ctrl+c  quit
```

The launcher uses only fixed Herdr commands. It never interpolates shell source
or accepts user-supplied commands.
