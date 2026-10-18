#!/bin/sh
# goolm: pure-Go Olm, so the build needs no libolm. SQLite still needs cgo.
MAUTRIX_VERSION=$(grep 'maunium.net/go/mautrix ' go.mod | awk '{ print $2 }')
GO_LDFLAGS="-s -w -X main.Tag=$(git describe --exact-match --tags 2>/dev/null) -X main.Commit=$(git rev-parse HEAD) -X 'main.BuildTime=$(date -Iseconds)' -X 'maunium.net/go/mautrix.GoModVersion=$MAUTRIX_VERSION'"
go build -tags goolm -ldflags="$GO_LDFLAGS" -o bipe-beeper ./cmd/bipe-beeper "$@"
