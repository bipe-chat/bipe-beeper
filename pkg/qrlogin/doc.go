// Package qrlogin is the new-device side of the Matrix QR code login
// (MSC4108, with the MSC4388 secure channel when the server advertises it).
//
// The new device shows a QR code. A signed-in device scans it, opens a secure
// channel through a rendezvous session on the homeserver, and approves an
// OAuth 2.0 device authorization grant. After the login, the signed-in device
// sends the E2EE secrets (cross-signing keys and the key-backup key) over the
// channel.
//
// The package follows matrix-rust-sdk
// (crates/matrix-sdk/src/authentication/oauth/qrcode) and vodozemac
// (src/ecies, src/hpke) byte for byte. It does not depend on bridgev2, so any
// Go Matrix client can use it.
//
// The package never logs tokens, keys or channel plaintext.
package qrlogin
