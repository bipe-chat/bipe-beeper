package qrlogin

import (
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

// The ECIES channel of MSC4108, as vodozemac src/ecies implements it: X25519,
// HKDF-SHA512 with no salt, ChaCha20-Poly1305 with a counter nonce.
//
// Device G shows the QR code with its public key. Device S scans it and
// starts the channel. In a login, the new device is G.

const qrLoginInfoPrefix = "MATRIX_QR_CODE_LOGIN"

// ErrDecryption is returned when a channel message does not decrypt.
var ErrDecryption = errors.New("failed to decrypt secure channel message")

type eciesRole int

const (
	eciesInitiator eciesRole = iota // device S
	eciesRecipient                  // device G
)

// eciesKeyPair is the ephemeral Curve25519 key pair of one channel.
type eciesKeyPair struct {
	private *ecdh.PrivateKey
}

func newECIESKeyPair() (*eciesKeyPair, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &eciesKeyPair{private: priv}, nil
}

func (k *eciesKeyPair) publicKey() [32]byte {
	return [32]byte(k.private.PublicKey().Bytes())
}

// establishInbound is the G side: decrypt the initial message from S.
func (k *eciesKeyPair) establishInbound(initialMessage string) (*establishedECIES, []byte, error) {
	theirKey, ciphertext, err := decodeECIESInitialMessage(initialMessage)
	if err != nil {
		return nil, nil, err
	}
	ch, err := newEstablishedECIES(k.private, theirKey, eciesRecipient)
	if err != nil {
		return nil, nil, err
	}
	plaintext, err := ch.decryptRaw(ciphertext)
	if err != nil {
		return nil, nil, err
	}
	return ch, plaintext, nil
}

// establishOutbound is the S side: start a channel to the key in the QR code.
func (k *eciesKeyPair) establishOutbound(theirKey [32]byte, plaintext []byte) (*establishedECIES, string, error) {
	ch, err := newEstablishedECIES(k.private, theirKey, eciesInitiator)
	if err != nil {
		return nil, "", err
	}
	ciphertext := ch.encryptRaw(plaintext)
	pub := k.publicKey()
	return ch, encodeBase64(ciphertext) + "|" + encodeBase64(pub[:]), nil
}

func decodeECIESInitialMessage(msg string) (key [32]byte, ciphertext []byte, err error) {
	ctPart, keyPart, ok := strings.Cut(msg, "|")
	if !ok {
		return key, nil, fmt.Errorf("%w: initial message has no separator", ErrDecryption)
	}
	rawKey, err := decodeBase64(keyPart)
	if err != nil || len(rawKey) != 32 {
		return key, nil, fmt.Errorf("%w: invalid public key in initial message", ErrDecryption)
	}
	ciphertext, err = decodeBase64(ctPart)
	if err != nil {
		return key, nil, fmt.Errorf("%w: invalid ciphertext in initial message", ErrDecryption)
	}
	return [32]byte(rawKey), ciphertext, nil
}

type establishedECIES struct {
	encrypt, decrypt cipher.AEAD
	encNonce         uint64
	decNonce         uint64
	checkCode        CheckCode
}

func newEstablishedECIES(ours *ecdh.PrivateKey, theirKey [32]byte, role eciesRole) (*establishedECIES, error) {
	theirPub, err := ecdh.X25519().NewPublicKey(theirKey[:])
	if err != nil {
		return nil, err
	}
	// crypto/ecdh rejects an all-zero shared secret, which is the
	// "non-contributory" check of x25519-dalek.
	shared, err := ours.ECDH(theirPub)
	if err != nil {
		return nil, fmt.Errorf("non-contributory key: %w", err)
	}
	defer clear(shared)

	ourKey := [32]byte(ours.PublicKey().Bytes())
	gKey, sKey := theirKey, ourKey
	if role == eciesRecipient {
		gKey, sKey = ourKey, theirKey
	}
	suffix := "|" + encodeBase64(gKey[:]) + "|" + encodeBase64(sKey[:])

	encName, decName := "_ENCKEY_S", "_ENCKEY_G"
	if role == eciesRecipient {
		encName, decName = "_ENCKEY_G", "_ENCKEY_S"
	}
	encKey, err := hkdf.Key(sha512.New, shared, nil, qrLoginInfoPrefix+encName+suffix, chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	defer clear(encKey)
	decKey, err := hkdf.Key(sha512.New, shared, nil, qrLoginInfoPrefix+decName+suffix, chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	defer clear(decKey)
	code, err := hkdf.Key(sha512.New, shared, nil, qrLoginInfoPrefix+"_CHECKCODE"+suffix, 2)
	if err != nil {
		return nil, err
	}

	ch := &establishedECIES{checkCode: CheckCode(code)}
	if ch.encrypt, err = chacha20poly1305.New(encKey); err != nil {
		return nil, err
	}
	if ch.decrypt, err = chacha20poly1305.New(decKey); err != nil {
		return nil, err
	}
	return ch, nil
}

// eciesNonce is the vodozemac nonce: the first 12 bytes of a little-endian
// 128-bit counter that starts at zero.
func eciesNonce(counter uint64) []byte {
	nonce := make([]byte, chacha20poly1305.NonceSize)
	binary.LittleEndian.PutUint64(nonce, counter)
	return nonce
}

func (c *establishedECIES) encryptRaw(plaintext []byte) []byte {
	nonce := eciesNonce(c.encNonce)
	c.encNonce++
	return c.encrypt.Seal(nil, nonce, plaintext, nil)
}

// decryptRaw advances the nonce also when decryption fails, as vodozemac does.
func (c *establishedECIES) decryptRaw(ciphertext []byte) ([]byte, error) {
	nonce := eciesNonce(c.decNonce)
	c.decNonce++
	plaintext, err := c.decrypt.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrDecryption
	}
	return plaintext, nil
}

func (c *establishedECIES) sealMessage(plaintext, _ []byte) string {
	return encodeBase64(c.encryptRaw(plaintext))
}

func (c *establishedECIES) openMessage(message string, _ []byte) ([]byte, error) {
	ciphertext, err := decodeBase64(message)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid base64", ErrDecryption)
	}
	return c.decryptRaw(ciphertext)
}

func (c *establishedECIES) checkDigits() uint8 {
	return c.checkCode.digitAllowLeadingZero()
}
