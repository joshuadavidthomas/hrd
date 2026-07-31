# hrd

`hrd` manages your herd of sandboxes and the [Herdr](https://herdr.dev) sessions running on them. It gathers local and remote sessions into one terminal picker, hands the terminal to the session you choose, then returns to the herd when you detach.

Sprites is the first supported sandbox provider. Other providers can follow once a second integration gives the shared boundary a concrete shape.

## Install

### Release binaries

[GitHub releases](https://github.com/joshuadavidthomas/hrd/releases) provide archives and checksums for Linux and macOS on amd64 and arm64. Download the archive for your system, move `hrd` onto your `PATH`, then check it:

```sh
hrd --version
```

### Go install

With Go 1.26.5 or newer:

```sh
go install github.com/joshuadavidthomas/hrd/cmd/hrd@latest
```

## Requirements

- Linux or macOS
- [`herdr`](https://herdr.dev) on your local `PATH`
- Sprite support: a [Sprites](https://sprites.dev) account and `SPRITE_TOKEN`
- SSH transport only: OpenSSH's `ssh-agent` and `ssh-add`

Run `hrd` from a terminal outside Herdr. Nested launches are rejected so an outer Herdr session cannot steal the inner session's keybindings.

## Quick start

Run the picker:

```sh
hrd
```

Choose a local Herdr session and press `enter`. The default session runs `herdr`; a named session runs `herdr --session NAME`. Detach with `ctrl+b q` to return to `hrd`.

## Sprites

Create a Sprites API token, export it before starting `hrd`, then run the picker:

```sh
export SPRITE_TOKEN="..."
hrd
```

The endpoint screen lists your Sprites beside the local machine. Press `n` to create a Sprite, or open an existing one to inspect its Herdr sessions. Creation, waking, setup, and session processes can incur provider charges. `hrd` never deletes a Sprite.

Selecting a Sprite wakes that Sprite when needed. Endpoint inventory refreshes every ten seconds while the endpoint screen is open and after actions that may change Sprite state. Warm and running Sprites may also be probed for session inventory. Press `r` for an explicit refresh.

### Herdr setup and config

When a Sprite lacks Herdr, `hrd` asks before downloading <https://herdr.dev/install.sh> to a validated temporary path and running it with `/bin/sh`. Herdr does not publish a checksum or signature manifest for this installer. HTTPS, its redirects, the downloaded script, and the assets selected by that script form the trust boundary.

After setup, `hrd` copies Herdr's local `config.toml` into the Sprite. It reads the operating system's user config directory: usually `$XDG_CONFIG_HOME/herdr/config.toml` or `~/.config/herdr/config.toml` on Linux, and `~/Library/Application Support/herdr/config.toml` on macOS. Opening an existing Sprite also syncs the file once successfully; failed syncs retry during refresh while the listed sessions remain usable. The remote replacement is atomic, with mode `0600`. If the local file does not exist, the remote config stays unchanged.

### Transports

After choosing a Sprite session, choose how to attach:

- `Native Sprite terminal` runs Herdr inside the Sprite through its terminal API. It needs no SSH key or service and uses `/bin/bash` for new panes.
- `SSH` runs Herdr locally and connects it to the Sprite. Use this when you need local Herdr integrations or when the native terminal path is unavailable.

For SSH, `hrd` checks `SSH_AUTH_SOCK` and then 1Password's standard agent socket. It uses a sole key automatically. When an agent exposes several keys, `hrd` asks which public key to install and remembers the choice for that run.

If no usable key exists, `hrd` starts a private foreground agent and gives the terminal to `ssh-add`. The SSH setup installs OpenSSH in the Sprite when needed, writes the selected public key, and runs a fixed SSH service. A local loopback-only bridge connects Herdr to that service. `SPRITE_TOKEN` stays in process memory. The private agent and its socket are removed when `hrd` exits.

## Keys

```text
enter   open an endpoint or attach a session
n       create a Sprite from the endpoint screen
s       stop a running Sprite session
R       restart a running Sprite session
r       refresh the current screen
/       filter
esc     return to endpoints
?       help
q       quit when the filter is not editing
ctrl+c  quit
```

## Development

Install the tools declared in `mise.toml`, then run the CI checks and build local release archives:

```sh
mise install
just check
```

The default test suite does not create, wake, stop, or delete Sprites.

See [`docs/releasing.md`](docs/releasing.md) for the release process.

## License

`hrd` is licensed under the MIT license. See [`LICENSE`](LICENSE).
