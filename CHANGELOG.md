# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Every pull request with a user-visible change adds an entry under
**[Unreleased]**; a release moves those entries under its version.

## [Unreleased]

## [0.1.4] - 2026-11-19

### Added

- Publish `plugin-metadata.json` as a GoReleaser release asset so `conduitd` can read plugin compatibility metadata directly from this repository's releases.

## [0.1.3] - 2026-11-19

### Added

- Implemented `SyncContacts` RPC returning 1:1 person contacts with `@lid` canonical JID resolution, deterministic canonical name preference, and bounded-concurrency avatar lookup, with unit tests.

### Fixed

- Avoid negative-caching empty avatars in `resolveAvatarURL` when the lookup fails or runs with a canceled context.

## [0.1.2] - 2026-11-19

### Added

- Outgoing delivery (`message.delivered`) and read (`message.read`) receipt events from recipient receipts.
- `channel.disconnected` event emission on WhatsApp `Disconnected` and `LoggedOut` socket events.
- Updated `github.com/cestila/ConduitOmniChat/sdk` to `v0.2.0`.

## [0.1.0] - 2026-11-13

First public release. Requires a `conduitd` that speaks plugin protocol
version 1 (SDK `v0.1.0`).

### Added

- WhatsApp multi-device login by QR code, built on whatsmeow.
- Text messages, media and voice notes, replies, reactions, polls, edit,
  delete, pin and read receipts.
- Plugin manifest (`GetManifest`) so the desktop app renders the account form
  and QR instructions.

[Unreleased]: https://github.com/cestila/conduit-plugin-whatsapp/compare/v0.1.4...HEAD
[0.1.4]: https://github.com/cestila/conduit-plugin-whatsapp/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/cestila/conduit-plugin-whatsapp/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/cestila/conduit-plugin-whatsapp/compare/v0.1.0...v0.1.2
[0.1.0]: https://github.com/cestila/conduit-plugin-whatsapp/releases/tag/v0.1.0
