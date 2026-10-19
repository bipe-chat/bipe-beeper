package qrlogin

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const (
	loginInitiateMessage = "MATRIX_QR_CODE_LOGIN_INITIATE"
	loginOKMessage       = "MATRIX_QR_CODE_LOGIN_OK"
)

var (
	// ErrInvalidCheckCode is returned when the typed number does not match
	// the check code of the channel.
	ErrInvalidCheckCode = errors.New("the check code does not match")
	// ErrUnexpectedMessage is returned when the other device sends a message
	// that the protocol does not allow at this point.
	ErrUnexpectedMessage = errors.New("unexpected secure channel message")
)

// cryptoChannel is an established ECIES or HPKE channel.
type cryptoChannel interface {
	sealMessage(plaintext, aad []byte) string
	openMessage(message string, aad []byte) ([]byte, error)
	checkDigits() uint8
}

// pendingChannel is the new-device (G) side before the other device writes
// the first message.
type pendingChannel struct {
	variant Variant
	rv      rendezvous
	qr      *QRCodeData
	ecies   *eciesKeyPair
	hpke    *hpkeRecipientKey
}

// newPendingChannel creates the rendezvous session on the homeserver and the
// ephemeral key for the QR code.
func newPendingChannel(ctx context.Context, cli *http.Client, homeserver string, variant Variant) (*pendingChannel, error) {
	p := &pendingChannel{variant: variant}
	switch variant {
	case VariantMSC4108:
		rv, err := createMSC4108Rendezvous(ctx, cli, homeserver)
		if err != nil {
			return nil, err
		}
		key, err := newECIESKeyPair()
		if err != nil {
			return nil, err
		}
		p.rv, p.ecies = rv, key
		p.qr = &QRCodeData{Variant: variant, Intent: IntentLogin, PublicKey: key.publicKey(), RendezvousURL: rv.url}
	case VariantMSC4388:
		rv, err := createMSC4388Rendezvous(ctx, cli, homeserver)
		if err != nil {
			return nil, err
		}
		key, err := newHPKERecipientKey()
		if err != nil {
			return nil, err
		}
		p.rv, p.hpke = rv, key
		p.qr = &QRCodeData{Variant: variant, Intent: IntentLogin, PublicKey: key.public, RendezvousID: rv.id, BaseURL: rv.baseURL}
	default:
		return nil, fmt.Errorf("unknown variant %d", variant)
	}
	return p, nil
}

// connect waits for the other device to scan the QR code, checks the
// initiate message and answers with the OK message. The channel is not
// trusted until confirm passes.
func (p *pendingChannel) connect(ctx context.Context) (*secureChannel, error) {
	aad := p.rv.aad()
	initial, err := p.rv.receive(ctx)
	if err != nil {
		return nil, err
	}
	sc := &secureChannel{variant: p.variant, rv: p.rv}
	switch p.variant {
	case VariantMSC4108:
		ch, plaintext, err := p.ecies.establishInbound(initial)
		if err != nil {
			return nil, err
		} else if string(plaintext) != loginInitiateMessage {
			return nil, fmt.Errorf("%w: expected the initiate message", ErrUnexpectedMessage)
		}
		sc.crypto = ch
		if err = sc.sendRaw(ctx, loginOKMessage); err != nil {
			return nil, err
		}
	case VariantMSC4388:
		pending, plaintext, err := p.hpke.establish(initial, aad)
		if err != nil {
			return nil, err
		} else if string(plaintext) != loginInitiateMessage {
			return nil, fmt.Errorf("%w: expected the initiate message", ErrUnexpectedMessage)
		}
		ch, response, err := pending.respond([]byte(loginOKMessage), p.rv.aad())
		if err != nil {
			return nil, err
		}
		sc.crypto = ch
		if err = p.rv.send(ctx, response); err != nil {
			return nil, err
		}
	}
	return sc, nil
}

// secureChannel is an established channel. Until confirmed is set, the other
// end is not authenticated.
type secureChannel struct {
	variant   Variant
	rv        rendezvous
	crypto    cryptoChannel
	confirmed bool
}

// confirm compares the number that the user typed with the check code.
func (s *secureChannel) confirm(typed string) error {
	typed = strings.TrimSpace(typed)
	n, err := strconv.Atoi(typed)
	if err != nil || n < 0 || n > 99 || len(typed) > 2 {
		return ErrInvalidCheckCode
	}
	want := []byte{s.crypto.checkDigits()}
	if subtle.ConstantTimeCompare([]byte{byte(n)}, want) != 1 {
		return ErrInvalidCheckCode
	}
	s.confirmed = true
	return nil
}

func (s *secureChannel) sendRaw(ctx context.Context, plaintext string) error {
	return s.rv.send(ctx, s.crypto.sealMessage([]byte(plaintext), s.rv.aad()))
}

func (s *secureChannel) receiveRaw(ctx context.Context) ([]byte, error) {
	aad := s.rv.aad()
	msg, err := s.rv.receive(ctx)
	if err != nil {
		return nil, err
	}
	return s.crypto.openMessage(msg, aad)
}

func (s *secureChannel) send(ctx context.Context, msg any) error {
	if !s.confirmed {
		return errors.New("secure channel is not confirmed")
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return s.sendRaw(ctx, string(data))
}

// receive returns the message type and the raw JSON. The caller parses the
// JSON, so secrets do not pass through channelMessage.
func (s *secureChannel) receive(ctx context.Context) (string, []byte, error) {
	if !s.confirmed {
		return "", nil, errors.New("secure channel is not confirmed")
	}
	data, err := s.receiveRaw(ctx)
	if err != nil {
		return "", nil, err
	}
	var head struct {
		Type string `json:"type"`
	}
	if err = json.Unmarshal(data, &head); err != nil {
		return "", nil, fmt.Errorf("%w: invalid JSON", ErrUnexpectedMessage)
	}
	return head.Type, data, nil
}

// receiveMessage receives one message that is not m.login.secrets.
func (s *secureChannel) receiveMessage(ctx context.Context) (*channelMessage, error) {
	msgType, data, err := s.receive(ctx)
	if err != nil {
		return nil, err
	} else if msgType == msgLoginSecrets {
		clear(data)
		return nil, fmt.Errorf("%w: secrets before the login", ErrUnexpectedMessage)
	}
	var msg channelMessage
	if err = json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("%w: invalid %s", ErrUnexpectedMessage, msgType)
	}
	if msg.Type == msgLoginFailure {
		return nil, &LoginFailureError{Reason: msg.Reason}
	}
	return &msg, nil
}

// sendFailure tells the other device why the login stops. It ignores errors,
// because the login fails anyway.
func (s *secureChannel) sendFailure(ctx context.Context, reason FailureReason) {
	if s.confirmed {
		_ = s.send(ctx, failureMessage(reason))
	}
}
