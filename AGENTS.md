# bipe-beeper: agent notes

A bridgev2 bridge (mautrix-go) that puts the chats of one Bipe account in Beeper. Each user runs their own bridge with `bbctl` ([ADR 0128](https://github.com/bipe-chat/bipe/blob/main/docs/adr/0128-beeper-bridge-per-user.md)). Tracker: bipe-chat/bipe, tracking issue bipe-chat/bipe#240.

- **Green gate:** `go build -tags goolm ./... && go vet -tags goolm ./...`. CI runs the same.
- Always build with `-tags goolm` (pure-Go Olm). Without it, mautrix-go needs libolm through cgo.
- SQLite (`mattn/go-sqlite3`) needs cgo, so release binaries build on a runner for each OS, not by cross-compiling.
- `pkg/connector/` bridges any Matrix homeserver. Bipe-only code (names, defaults, Bipe event fallbacks) goes in `pkg/bipe/`. The core never imports Bipe rules beyond `pkg/bipe`.
- mautrix-go is pinned in `go.mod`. Read its release notes before an upgrade: bridgev2 interfaces change between minor versions.
- Never log access tokens, refresh tokens or keys.
- Write in ASD-STE100 style. User-facing strings are pt-BR and go through the humanizer skill.
