# Releasing hrd

GitHub Actions builds and publishes `hrd` with GoReleaser.

## Release contents

Each release contains:

- `hrd_linux_amd64.tar.gz`
- `hrd_linux_arm64.tar.gz`
- `hrd_darwin_amd64.tar.gz`
- `hrd_darwin_arm64.tar.gz`
- `checksums.txt`

The archives also contain `README.md` and `LICENSE`. GitHub stores a signed build-provenance attestation for each file listed in `checksums.txt`.

## Repository setup

Create a protected GitHub environment named `release`. The workflow uses only the repository `GITHUB_TOKEN`; it needs no added secrets.

## Cut a release

1. Update `main` and confirm that CI passes.
2. Run the local release gate:

```sh
just check
```

3. Create the tag on GitHub at the exact `main` commit. Replace the version below:

```sh
commit=$(jj log -r main --no-graph -T 'commit_id')
gh api --method POST repos/joshuadavidthomas/hrd/git/refs \
  -f ref=refs/tags/v0.1.0 \
  -f sha="$commit"
```

4. Watch the release workflow:

```sh
gh run watch --repo joshuadavidthomas/hrd
```

The workflow tests Linux and macOS, creates a draft GitHub release, attests its archives, then publishes it.

## Verify a release

```sh
gh release view v0.1.0 --repo joshuadavidthomas/hrd
gh release download v0.1.0 --repo joshuadavidthomas/hrd
gh attestation verify hrd_linux_amd64.tar.gz --repo joshuadavidthomas/hrd
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

The attestation job uses artifacts retained for seven days. If only attestation failed during that window, rerunning failed jobs does not rebuild the release.
