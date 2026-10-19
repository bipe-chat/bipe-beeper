# bipe-beeper: agent notes

A bridgev2 bridge (mautrix-go) that puts the chats of one Bipe account in Beeper. Each user runs their own bridge with `bbctl` ([ADR 0128](https://github.com/bipe-chat/bipe/blob/main/docs/adr/0128-beeper-bridge-per-user.md)). Tracker: bipe-chat/bipe, tracking issue bipe-chat/bipe#240.

- **Green gate:** `go build -tags goolm ./... && go vet -tags goolm ./... && go test -tags goolm ./...`. CI runs the same.
- Always build with `-tags goolm` (pure-Go Olm). Without it, mautrix-go needs libolm through cgo.
- SQLite (`mattn/go-sqlite3`) needs cgo, so release binaries build on a runner for each OS, not by cross-compiling.
- `pkg/connector/` bridges any Matrix homeserver. Bipe-only code (names, defaults, Bipe event fallbacks) goes in `pkg/bipe/`. The core never imports Bipe rules beyond `pkg/bipe`.
- mautrix-go is pinned in `go.mod`. Read its release notes before an upgrade: bridgev2 interfaces change between minor versions.
- Never log access tokens, refresh tokens or keys.
- Write in ASD-STE100 style. User-facing strings are pt-BR and go through the humanizer skill.
- `pkg/qrlogin/` is the new-device side of the MSC4108 QR login (MSC4388 when the server advertises `io.element.msc4388`). It follows matrix-rust-sdk and vodozemac byte for byte. `testdata/ecies_vectors.json` comes from vodozemac (`testdata/gen_ecies.cjs`); keep that test green after any change to the channel code.
- Do not use the mautrix-go `OAuth*` token calls (`OAuthPollDeviceCode`, `OAuthRefreshToken`, `OAuthSetTokens` with a refresh token). mautrix-go v0.31.0 logs form request bodies at debug level, and those bodies hold the device code and the refresh token. Use `qrlogin.Session` and the connector's `oauthSession`.
- One `oauthSession` per login owns the OAuth tokens. MAS rotates the refresh token on each refresh, so a second refresher ends the session.
- Synapse keeps a rendezvous session for 60 seconds after creation, and a write does not extend it. The whole phone flow after the QR code (scan, check code, approval, secrets) must fit in that time.
