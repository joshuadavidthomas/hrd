# Releasing hrd

GitHub Actions builds and publishes `hrd` with GoReleaser.

## Release contents

Each release contains:

- `hrd_linux_amd64.tar.gz`
- `hrd_linux_arm64.tar.gz`
- `hrd_darwin_amd64.tar.gz`
- `hrd_darwin_arm64.tar.gz`
- `checksums.txt`

The archives also contain `README.md` and `LICENSE`. GitHub stores a signed build-provenance attestation for each file listed in `checksums.txt`. The release workflow also publishes `hrd` to the [`joshuadavidthomas/homebrew-homebrew`](https://github.com/joshuadavidthomas/homebrew-homebrew) tap.

## Repository setup

Create a protected GitHub environment named `release`. Add a `HOMEBREW_TAP_GITHUB_TOKEN` environment secret. Use a fine-grained token limited to `joshuadavidthomas/homebrew-homebrew` with `Contents: Read and write`. The repository `GITHUB_TOKEN` publishes the GitHub release and provenance attestations.

## Cut a release

1. Move the pending entries in `CHANGELOG.md` from `Unreleased` into a dated heading such as `## [0.1.0] - 2026-07-31`. Update the comparison links at the bottom of the file.
2. Commit and push the changelog, then confirm that `main` points to that exact commit and all CI checks pass.
3. Run the local release gate from that commit:

```sh
just check
```

4. Create the tag on GitHub at the exact `main` commit. Replace the version below:

```sh
commit=$(jj log -r main --no-graph -T 'commit_id')
gh api --method POST repos/joshuadavidthomas/hrd/git/refs \
  -f ref=refs/tags/v0.1.0 \
  -f sha="$commit"
```

5. Watch the release workflow:

```sh
gh run watch --repo joshuadavidthomas/hrd
```

The workflow tests Linux and macOS, creates a draft GitHub release, attests its archives, publishes the release, then updates the Homebrew cask.

## Verify a release

```sh
gh release view v0.1.0 --repo joshuadavidthomas/hrd
gh release download v0.1.0 --repo joshuadavidthomas/hrd
gh attestation verify hrd_linux_amd64.tar.gz --repo joshuadavidthomas/hrd
brew update
brew install --cask joshuadavidthomas/homebrew/hrd
hrd --version
```

The tag is injected into `internal/version.Version` through GoReleaser's linker flags. Local builds report `dev`:

```sh
hrd --version
```

## Recovery

Inspect a failed run before retrying it:

```sh
gh run list --workflow release.yml --repo joshuadavidthomas/hrd
gh run rerun RUN_ID --failed --repo joshuadavidthomas/hrd
```

If the workflow failed before publishing assets, fix the cause and issue a new patch version. Never move a tag that reached GitHub; users may already have downloaded its files.

The attestation job uses artifacts retained for seven days. If attestation or Homebrew publication failed during that window, rerunning failed jobs does not rebuild the release. Homebrew publication creates the cask on its first run and replaces it on later releases.
