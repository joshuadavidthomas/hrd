# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Added a terminal picker for local and sandboxed Herdr sessions.
- Added Sprites discovery, creation, waking, stopping, restarting, and session attachment.
- Added native Sprite terminal and managed SSH transports.
- Added confirmed Herdr installation and explicit `never`, `install`, and `open` config-sync policies.
- Added Linux and macOS release archives for amd64 and arm64 with checksums and build-provenance attestations.
- Added Homebrew distribution through `joshuadavidthomas/homebrew-homebrew`.

### Fixed

- Kept Sprite sessions usable when Herdr config synchronization fails and retried failed eligible syncs.
- Suppressed Herdr's detach output when returning to the picker.

### Security

- Replaced remote Herdr config atomically with user-only file permissions.
- Kept release publication behind tests, draft creation, and provenance attestation.

[unreleased]: https://github.com/joshuadavidthomas/hrd/compare/v0.1.0...HEAD
