package qrlogin

import (
	"encoding/json"
	"fmt"

	"github.com/rs/zerolog"
)

// Message types on the secure channel (matrix-rust-sdk qrcode/messages.rs).
const (
	msgLoginProtocols        = "m.login.protocols"
	msgLoginProtocol         = "m.login.protocol"
	msgLoginProtocolAccepted = "m.login.protocol_accepted"
	msgLoginSuccess          = "m.login.success"
	msgLoginDeclined         = "m.login.declined"
	msgLoginFailure          = "m.login.failure"
	msgLoginSecrets          = "m.login.secrets"

	protocolDeviceAuthorizationGrant = "device_authorization_grant"
)

// FailureReason is the reason in an m.login.failure message.
type FailureReason string

const (
	FailureAuthorizationExpired      FailureReason = "authorization_expired"
	FailureDeviceAlreadyExists       FailureReason = "device_already_exists"
	FailureDeviceNotFound            FailureReason = "device_not_found"
	FailureUnexpectedMessageReceived FailureReason = "unexpected_message_received"
	FailureUnsupportedProtocol       FailureReason = "unsupported_protocol"
	FailureUserCancelled             FailureReason = "user_cancelled"
)

// LoginFailureError is an m.login.failure message from the other device.
type LoginFailureError struct {
	Reason FailureReason
}

func (e *LoginFailureError) Error() string {
	return fmt.Sprintf("the other device ended the login: %s", e.Reason)
}

// channelMessage is the union of all messages. Only the fields of Type are set.
type channelMessage struct {
	Type string `json:"type"`

	// m.login.protocols
	Protocols  []string `json:"protocols,omitempty"`
	Homeserver string   `json:"homeserver,omitempty"` // MSC4108
	BaseURL    string   `json:"base_url,omitempty"`   // MSC4388

	// m.login.protocol
	Protocol                 string              `json:"protocol,omitempty"`
	DeviceAuthorizationGrant *authorizationGrant `json:"device_authorization_grant,omitempty"` // MSC4108
	DeviceID                 string              `json:"device_id,omitempty"`
	// MSC4388 puts the grant fields next to protocol.
	VerificationURI         string `json:"verification_uri,omitempty"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`

	// m.login.failure
	Reason FailureReason `json:"reason,omitempty"`
}

type authorizationGrant struct {
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
}

func (m *channelMessage) homeserverURL(v Variant) string {
	if v == VariantMSC4388 {
		return m.BaseURL
	}
	return m.Homeserver
}

func loginProtocolMessage(v Variant, deviceID, verificationURI, verificationURIComplete string) *channelMessage {
	msg := &channelMessage{Type: msgLoginProtocol, Protocol: protocolDeviceAuthorizationGrant, DeviceID: deviceID}
	if v == VariantMSC4388 {
		msg.VerificationURI = verificationURI
		msg.VerificationURIComplete = verificationURIComplete
	} else {
		msg.DeviceAuthorizationGrant = &authorizationGrant{
			VerificationURI:         verificationURI,
			VerificationURIComplete: verificationURIComplete,
		}
	}
	return msg
}

func failureMessage(reason FailureReason) *channelMessage {
	return &channelMessage{Type: msgLoginFailure, Reason: reason}
}

// SecretsBundle is the content of m.login.secrets: the private cross-signing
// keys and the key-backup key of the account. All values are unpadded
// base64. The type never prints or logs the keys.
type SecretsBundle struct {
	CrossSigning CrossSigningSecrets `json:"cross_signing"`
	Backup       *BackupSecrets      `json:"backup,omitempty"`
}

// CrossSigningSecrets holds the seeds of the three cross-signing keys.
type CrossSigningSecrets struct {
	MasterKey      string `json:"master_key"`
	UserSigningKey string `json:"user_signing_key"`
	SelfSigningKey string `json:"self_signing_key"`
}

// BackupSecrets is the key of the server-side room key backup.
type BackupSecrets struct {
	Algorithm     string `json:"algorithm"`
	BackupVersion string `json:"backup_version"`
	Key           string `json:"key"`
}

// Clear overwrites the keys in memory as far as Go allows.
func (s *SecretsBundle) Clear() {
	if s == nil {
		return
	}
	s.CrossSigning = CrossSigningSecrets{}
	s.Backup = nil
}

func (s SecretsBundle) String() string {
	return fmt.Sprintf("SecretsBundle{cross_signing: ..., backup: %t}", s.Backup != nil)
}

func (s SecretsBundle) GoString() string { return s.String() }

func (s SecretsBundle) MarshalZerologObject(e *zerolog.Event) {
	e.Bool("has_backup", s.Backup != nil)
}

// parseSecrets reads an m.login.secrets message. It does not use
// channelMessage, so the keys never sit in a general struct.
func parseSecrets(data []byte) (*SecretsBundle, error) {
	var bundle SecretsBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return nil, fmt.Errorf("invalid m.login.secrets: %w", err)
	}
	cs := bundle.CrossSigning
	if cs.MasterKey == "" || cs.SelfSigningKey == "" || cs.UserSigningKey == "" {
		return nil, fmt.Errorf("invalid m.login.secrets: cross-signing keys missing")
	}
	return &bundle, nil
}
