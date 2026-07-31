# hrd

`hrd` manages your herd of sandboxes and the [Herdr](https://herdr.dev) sessions running on them. It gathers local and remote sessions into one terminal picker, hands the terminal to the session you choose, then returns to the herd when you detach.

`hrd` currently supports local Herdr sessions and [Sprites](https://sprites.dev) as its first sandbox provider.

## Requirements

- Linux or macOS
- [`herdr`](https://herdr.dev) on your local `PATH`
- For Sprites: a Sprites account and `SPRITE_TOKEN`
- For the SSH transport: OpenSSH's `ssh-agent` and `ssh-add`

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

## Quick start

Run `hrd` outside a Herdr session. `hrd` rejects nested runs because both programs need the same terminal keybindings.

Run the picker:

```sh
hrd
```

Choose a local Herdr session and press `enter`. The default session runs `herdr`; a named session runs `herdr --session NAME`. Detach with `ctrl+b q` to return to `hrd`.

## Configuration

`hrd` reads `$XDG_CONFIG_HOME/hrd/config.toml` when `XDG_CONFIG_HOME` is set. Otherwise, it reads `~/.config/hrd/config.toml` on Linux or `~/Library/Application Support/hrd/config.toml` on macOS. A missing file uses the defaults. Unknown settings and invalid values stop startup with an error.

The `herdr_config_sync` setting controls when `hrd` copies local Herdr config to a sandbox:

```toml
herdr_config_sync = "install"
```

| Value | Behavior |
|---|---|
| `never` | Never copy local Herdr config |
| `install` | Copy after `hrd` installs Herdr; this is the default |
| `open` | Copy after installation and whenever you open a sandbox |

## Use hrd with Sprites

Create a Sprites API token, export it before starting `hrd`, then run the picker:

```sh
export SPRITE_TOKEN="..."
hrd
```

The endpoint screen lists your Sprites beside the local machine. Press `n` to create a Sprite, or open an existing one to inspect its Herdr sessions. Creation, waking, setup, and session processes can incur provider charges. `hrd` never deletes a Sprite.

Selecting a Sprite wakes that Sprite when needed. Endpoint inventory refreshes every ten seconds while the endpoint screen is open and after actions that may change Sprite state. Warm and running Sprites may also be probed for session inventory. Press `r` for an explicit refresh.

### What hrd changes on a Sprite

When a Sprite lacks Herdr, `hrd` asks before downloading <https://herdr.dev/install.sh> to a validated temporary path and running it with `/bin/sh`. Herdr does not publish a checksum or signature manifest for this installer. HTTPS, its redirects, the downloaded script, and the assets selected by that script form the trust boundary.

With the default `herdr_config_sync = "install"` policy, `hrd` copies local Herdr config after it installs Herdr and leaves existing Sprite config unchanged. The `open` policy also syncs when you open an existing Sprite. Failed syncs retry during refresh while the listed sessions remain usable.

The source is `$XDG_CONFIG_HOME/herdr/config.toml` when `XDG_CONFIG_HOME` is set, `~/.config/herdr/config.toml` on Linux, or `~/Library/Application Support/herdr/config.toml` on macOS. Settings that name local executables, such as `terminal.default_shell`, must also name an executable available in the Sprite. The remote replacement is atomic, with mode `0600`. If the local file does not exist, the remote config stays unchanged.

### Choose a transport

After choosing a Sprite session, choose how to attach:

- `Native Sprite terminal` runs Herdr inside the Sprite through its terminal API. It needs no SSH key or service. `hrd` sets `SHELL=/bin/bash`; Herdr's `terminal.default_shell` setting takes precedence.
- `SSH` runs Herdr locally and connects it to the Sprite. Use this when you need local Herdr integrations or when the native terminal path is unavailable.

For SSH, `hrd` first checks the OpenSSH-compatible agent named by `SSH_AUTH_SOCK`. It also recognizes 1Password's standard agent socket on Linux and macOS. It uses a sole key automatically. When an agent exposes several keys, `hrd` asks which public key to install and remembers the choice for that run.

If an agent can accept keys but has none, `hrd` gives the terminal to `ssh-add`. Otherwise, it starts a private agent before running `ssh-add`. The SSH setup installs OpenSSH in the Sprite when needed, writes the selected public key, and runs a fixed SSH service. A local loopback-only bridge connects Herdr to that service. `SPRITE_TOKEN` stays in process memory. Any private agent and its socket are removed when `hrd` exits.

## Key bindings

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
