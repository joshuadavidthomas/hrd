# hrd

`hrd` finds [Herdr](https://herdr.dev) sessions on your machine and in [Sprites](https://sprites.dev), attaches the selected session to your terminal, and reopens the picker when you detach.

## Requirements

- Linux or macOS
- A Sprites account and `SPRITE_TOKEN` to use Sprites
- [`herdr`](https://herdr.dev) on your local `PATH` for local sessions and the SSH transport
- For SSH, a key from `SSH_AUTH_SOCK` or 1Password; `ssh-add` if the agent has no key; `ssh-agent` if `hrd` must start one

## Install

### Install script

```sh
curl -fsSL https://raw.githubusercontent.com/joshuadavidthomas/hrd/main/install.sh | sh
```

The script installs `hrd` in `~/.local/bin`. Set `HRD_INSTALL_DIR` to choose another directory or `HRD_VERSION` to install a specific release:

```sh
curl -fsSL https://raw.githubusercontent.com/joshuadavidthomas/hrd/main/install.sh \
  | HRD_INSTALL_DIR="$HOME/bin" HRD_VERSION=v0.1.0 sh
```

The script checks the archive checksum. If an authenticated [`gh`](https://cli.github.com/) command is available, it also verifies the archive's build provenance. Set `HRD_REQUIRE_ATTESTATION=1` to stop when provenance verification is unavailable.

### Homebrew

```sh
brew install --cask joshuadavidthomas/homebrew/hrd
```

### Release binaries

[GitHub releases](https://github.com/joshuadavidthomas/hrd/releases) contain archives and checksums for Linux and macOS on amd64 and arm64. Download and extract the archive for your system, move the extracted `hrd` binary to a directory on your `PATH`, then check the install:

```sh
hrd --version
```

### Go install

With Go 1.26.5 or newer:

```sh
go install github.com/joshuadavidthomas/hrd/cmd/hrd@latest
```

## Quick start

Start `hrd` outside a Herdr session because both programs use the same terminal keys. A nested launch exits with an error.

```sh
hrd
```

Select a local session and press `enter`. The default session runs `herdr`; a named session runs `herdr --session NAME`. Press `ctrl+b q` to detach and return to `hrd`.

## Connect to a Sprite

Create a Sprites API token and export it before starting `hrd`:

```sh
export SPRITE_TOKEN="..."
hrd
```

The home screen lists Sprites below your local sessions. Select a Sprite and press `enter`, select a session and press `enter`, then choose `Native` or `SSH` and press `enter` again. Press `n` on the home screen to create a Sprite. Opening a Sprite wakes it.

Creating or waking a Sprite and running commands can incur Sprites charges. `hrd` never deletes a Sprite.

`hrd` refreshes the home screen every ten seconds while it is visible. It also preloads Herdr sessions from warm and running Sprites. Press `r` to refresh the current list.

## Configuration reference

`hrd` reads its config from:

| Condition | Path |
|---|---|
| `XDG_CONFIG_HOME` is an absolute path | `$XDG_CONFIG_HOME/hrd/config.toml` |
| Linux | `~/.config/hrd/config.toml` |
| macOS | `~/Library/Application Support/hrd/config.toml` |

A missing file uses the defaults. Unknown settings and invalid values stop startup with an error.

The `herdr_config_sync` setting controls when `hrd` copies local Herdr config to a Sprite:

```toml
herdr_config_sync = "install"
```

| Value | Behavior |
|---|---|
| `never` | Never copy local Herdr config |
| `install` | Copy after `hrd` installs Herdr; this is the default |
| `open` | Copy after installation and whenever you open a Sprite |

## Changes made to a Sprite

### Herdr installation

When a Sprite lacks Herdr, `hrd` asks for approval. It then downloads <https://herdr.dev/install.sh> over HTTPS to a validated temporary path and runs it with `/bin/sh`.

Herdr publishes no checksum or signature for this installer. Installation trusts the HTTPS connection, its redirects, the script, and any assets the script downloads.

### Herdr config

With the default `herdr_config_sync = "install"` policy, `hrd` copies your local Herdr config after it installs Herdr. It leaves the remote config unchanged when Herdr was already installed. The `open` policy also copies config when you open an existing Sprite. A failed copy retries during refresh without hiding the Sprite's sessions.

`hrd` reads the local Herdr config from:

| Condition | Path |
|---|---|
| `XDG_CONFIG_HOME` is an absolute path | `$XDG_CONFIG_HOME/herdr/config.toml` |
| Linux | `~/.config/herdr/config.toml` |
| macOS | `~/Library/Application Support/herdr/config.toml` |

`hrd` copies the file without changing its contents. A setting that names a local executable, such as `terminal.default_shell`, must name an executable that also exists in the Sprite. `hrd` replaces the remote file atomically and sets its mode to `0600`. A missing local file leaves the remote file unchanged.

## Attachment transports

After you select a Sprite session, `hrd` asks how to attach.

- `Native` starts Herdr inside the Sprite through the Sprites terminal API. It needs no SSH key or service. `hrd` sets `SHELL=/bin/bash`; Herdr's `terminal.default_shell` setting takes precedence.
- `SSH` runs Herdr locally and connects it to the Sprite. Use it when Herdr must run on your machine.

### SSH keys

`hrd` first checks the OpenSSH-compatible agent named by `SSH_AUTH_SOCK`, then the standard 1Password agent socket on Linux and macOS. It selects the only available key. If the agent holds several keys, `hrd` asks you to choose one and remembers that choice until it exits.

For an empty agent, `hrd` gives the terminal to `ssh-add`. If no usable agent exists, `hrd` starts a private agent before running `ssh-add`.

SSH setup installs OpenSSH when needed, authorizes the selected key for `sprite`, disables password and keyboard-interactive login, and blocks root and other users. A bridge bound to `127.0.0.1` connects local Herdr to the service. `hrd` removes `SPRITE_TOKEN` from child environments and stops any private agent it started when it exits.

## Key bindings

```text
enter   open a Sprite or attach a session
n       create a Sprite from the home screen
s       stop a running Sprite session
R       restart a running Sprite session
r       refresh the current screen
/       filter
esc     go back or cancel
?       help
q       quit when no text field is active
ctrl+c  quit
```

## Contributing

Install the pinned tools and Git hooks, then run the same checks as CI:

```sh
mise install
pre-commit install --install-hooks
just check
```

[`prek`](https://prek.j178.dev/) reads the same hook configuration:

```sh
prek install --prepare-hooks
```

Run `just coverage` for race-enabled test coverage. `just test` does not create, wake, stop, or delete Sprites. The release check builds an unpublished snapshot under the ignored `dist/` directory.

See [`CHANGELOG.md`](CHANGELOG.md) for notable changes and [`docs/releasing.md`](docs/releasing.md) for the release process.

## License

`hrd` uses the MIT License. See [`LICENSE`](LICENSE).
