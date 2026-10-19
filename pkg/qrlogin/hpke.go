package qrlogin

import (
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hpke"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// The HPKE channel of MSC4388, as vodozemac src/hpke implements it:
// DHKEM(X25519, HKDF-SHA256), HKDF-SHA256, ChaCha20-Poly1305, base mode,
// info "MATRIX_QR_CODE_LOGIN". HPKE is one-way, so the recipient derives a
// response context for the other direction (the RFC 9458 method).
//
// As in ECIES, device G shows the QR code and is the HPKE recipient.

var (
	hpkeKEM  = hpke.DHKEM(ecdh.X25519())
	hpkeKDF  = hpke.HKDFSHA256()
	hpkeAEAD = hpke.ChaCha20Poly1305()
)

const hpkeResponseNonceSize = chacha20poly1305.KeySize

// hpkeRecipientKey is the key pair of device G.
type hpkeRecipientKey struct {
	private hpke.PrivateKey
	public  [32]byte
}

func newHPKERecipientKey() (*hpkeRecipientKey, error) {
	priv, err := hpkeKEM.GenerateKey()
	if err != nil {
		return nil, err
	}
	pub := priv.PublicKey().Bytes()
	return &hpkeRecipientKey{private: priv, public: [32]byte(pub)}, nil
}

// establish opens the initial message from S. It returns the plaintext and
// a channel that still needs the initial response.
func (k *hpkeRecipientKey) establish(initialMessage string, aad []byte) (*hpkeRecipientPending, []byte, error) {
	raw, err := decodeBase64(initialMessage)
	if err != nil || len(raw) < 32 {
		return nil, nil, fmt.Errorf("%w: invalid HPKE initial message", ErrDecryption)
	}
	enc, ciphertext := [32]byte(raw[:32]), raw[32:]
	recipient, err := hpke.NewRecipient(enc[:], k.private, hpkeKDF, hpkeAEAD, []byte(qrLoginInfoPrefix))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrDecryption, err)
	}
	plaintext, err := recipient.Open(aad, ciphertext)
	if err != nil {
		return nil, nil, ErrDecryption
	}
	return &hpkeRecipientPending{recipient: recipient, ourKey: k.public, theirKey: enc}, plaintext, nil
}

type hpkeRecipientPending struct {
	recipient *hpke.Recipient
	ourKey    [32]byte
	theirKey  [32]byte

	// randRead is the source of the response nonce. Tests replace it.
	randRead func([]byte) (int, error)
}

// respond makes the bidirectional channel and the initial response message
// that carries plaintext to S.
func (p *hpkeRecipientPending) respond(plaintext, aad []byte) (*establishedHPKE, string, error) {
	nonce := make([]byte, hpkeResponseNonceSize)
	read := p.randRead
	if read == nil {
		read = rand.Read
	}
	if _, err := read(nonce); err != nil {
		return nil, "", err
	}
	resp, err := newResponseContext(p.recipient, p.theirKey, nonce)
	if err != nil {
		return nil, "", err
	}
	ciphertext := resp.seal(plaintext, aad)
	code, err := hpkeCheckCode(p.recipient, p.ourKey, p.theirKey)
	if err != nil {
		return nil, "", err
	}
	ch := &establishedHPKE{
		open: p.recipient.Open,
		seal: resp.seal,
		code: code,
	}
	return ch, encodeBase64(append(nonce, ciphertext...)), nil
}

// hpkeSender is device S. Only the tests use it.
type hpkeSender struct {
	sender   *hpke.Sender
	ourKey   [32]byte // the encapsulated key
	theirKey [32]byte
}

func newHPKESender(theirKey [32]byte, plaintext, aad []byte) (*hpkeSender, string, error) {
	pub, err := hpkeKEM.NewPublicKey(theirKey[:])
	if err != nil {
		return nil, "", err
	}
	enc, sender, err := hpke.NewSender(pub, hpkeKDF, hpkeAEAD, []byte(qrLoginInfoPrefix))
	if err != nil {
		return nil, "", err
	}
	ciphertext, err := sender.Seal(aad, plaintext)
	if err != nil {
		return nil, "", err
	}
	s := &hpkeSender{sender: sender, ourKey: [32]byte(enc), theirKey: theirKey}
	return s, encodeBase64(append(enc, ciphertext...)), nil
}

func (s *hpkeSender) finish(initialResponse string, aad []byte) (*establishedHPKE, []byte, error) {
	raw, err := decodeBase64(initialResponse)
	if err != nil || len(raw) < hpkeResponseNonceSize {
		return nil, nil, fmt.Errorf("%w: invalid HPKE initial response", ErrDecryption)
	}
	resp, err := newResponseContext(s.sender, s.ourKey, raw[:hpkeResponseNonceSize])
	if err != nil {
		return nil, nil, err
	}
	plaintext, err := resp.open(aad, raw[hpkeResponseNonceSize:])
	if err != nil {
		return nil, nil, err
	}
	code, err := hpkeCheckCode(s.sender, s.theirKey, s.ourKey)
	if err != nil {
		return nil, nil, err
	}
	ch := &establishedHPKE{
		open: resp.open,
		seal: func(pt, aad []byte) []byte {
			ct, _ := s.sender.Seal(aad, pt)
			return ct
		},
		code: code,
	}
	return ch, plaintext, nil
}

type hpkeExporter interface {
	Export(exporterContext string, length int) ([]byte, error)
}

// hpkeCheckCode exports two bytes with the info
// prefix || "_CHECKCODE" || G public key || S public key.
func hpkeCheckCode(ctx hpkeExporter, gKey, sKey [32]byte) (CheckCode, error) {
	info := qrLoginInfoPrefix + "_CHECKCODE" + string(gKey[:]) + string(sKey[:])
	out, err := ctx.Export(info, 2)
	if err != nil {
		return CheckCode{}, err
	}
	return CheckCode(out), nil
}

// responseContext is a bare HPKE AEAD context (RFC 9180 section 5.2) with a
// key and base nonce from the response secret.
type responseContext struct {
	aead      cipher.AEAD
	baseNonce []byte
	seq       uint64
}

// newResponseContext follows vodozemac CreateResponseContext: export 32 bytes
// with info prefix + "_RESPONSE", then HKDF-SHA256 with the salt
// encapsulated key || response nonce, expanded to "key" and "nonce".
func newResponseContext(ctx hpkeExporter, encapsulatedKey [32]byte, responseNonce []byte) (*responseContext, error) {
	secret, err := ctx.Export(qrLoginInfoPrefix+"_RESPONSE", chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	defer clear(secret)
	salt := append(append([]byte{}, encapsulatedKey[:]...), responseNonce...)
	key, err := hkdf.Key(sha256.New, secret, salt, "key", chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	baseNonce, err := hkdf.Key(sha256.New, secret, salt, "nonce", chacha20poly1305.NonceSize)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	return &responseContext{aead: aead, baseNonce: baseNonce}, nil
}

func (r *responseContext) nonce() []byte {
	nonce := make([]byte, len(r.baseNonce))
	binary.BigEndian.PutUint64(nonce[len(nonce)-8:], r.seq)
	for i := range nonce {
		nonce[i] ^= r.baseNonce[i]
	}
	return nonce
}

func (r *responseContext) seal(plaintext, aad []byte) []byte {
	ct := r.aead.Seal(nil, r.nonce(), plaintext, aad)
	r.seq++
	return ct
}

// open advances the sequence only on success, as RFC 9180 says.
func (r *responseContext) open(aad, ciphertext []byte) ([]byte, error) {
	pt, err := r.aead.Open(nil, r.nonce(), ciphertext, aad)
	if err != nil {
		return nil, ErrDecryption
	}
	r.seq++
	return pt, nil
}

type establishedHPKE struct {
	open func(aad, ciphertext []byte) ([]byte, error)
	seal func(plaintext, aad []byte) []byte
	code CheckCode
}

func (c *establishedHPKE) sealMessage(plaintext, aad []byte) string {
	return encodeBase64(c.seal(plaintext, aad))
}

func (c *establishedHPKE) openMessage(message string, aad []byte) ([]byte, error) {
	ciphertext, err := decodeBase64(message)
	if err != nil || len(ciphertext) == 0 {
		return nil, fmt.Errorf("%w: invalid message", ErrDecryption)
	}
	pt, err := c.open(aad, ciphertext)
	if err != nil {
		return nil, ErrDecryption
	}
	return pt, nil
}

func (c *establishedHPKE) checkDigits() uint8 {
	return c.code.digitNoLeadingZero()
}
