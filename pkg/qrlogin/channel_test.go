package qrlogin

import (
	"bytes"
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

type eciesVectors struct {
	GPrivateKey    string `json:"g_private_key"`
	GPublicKey     string `json:"g_public_key"`
	SPublicKey     string `json:"s_public_key"`
	InitialMessage string `json:"initial_message"`
	CheckCode      uint8  `json:"check_code"`
	CheckCodeBytes string `json:"check_code_bytes"`
	SToG           []struct {
		Plaintext  string `json:"plaintext"`
		Ciphertext string `json:"ciphertext"`
	} `json:"s_to_g"`
	GToS []struct {
		Plaintext  string `json:"plaintext"`
		Ciphertext string `json:"ciphertext"`
	} `json:"g_to_s"`
}

// TestECIESVodozemacVectors replays a channel that vodozemac (device S, via
// matrix-sdk-crypto-wasm) opened to a fixed key. vodozemac decrypted every
// G-to-S ciphertext in the file, so equal ciphertexts here prove that this
// code interoperates. See testdata/gen_ecies.cjs.
func TestECIESVodozemacVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/ecies_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v eciesVectors
	if err = json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	gPriv := mustKey(t, v.GPrivateKey)
	priv, err := ecdh.X25519().NewPrivateKey(gPriv[:])
	if err != nil {
		t.Fatal(err)
	}
	g := &eciesKeyPair{private: priv}
	if pub := g.publicKey(); pub != mustKey(t, v.GPublicKey) {
		t.Fatal("public key differs")
	}

	ch, plaintext, err := g.establishInbound(v.InitialMessage)
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != loginInitiateMessage {
		t.Fatalf("initial plaintext %q", plaintext)
	}
	if ch.checkDigits() != v.CheckCode {
		t.Fatalf("check code %d, vodozemac %d", ch.checkDigits(), v.CheckCode)
	}
	if encodeBase64(ch.checkCode[:]) != v.CheckCodeBytes {
		t.Fatal("check code bytes differ")
	}
	for i, m := range v.GToS {
		if got := ch.sealMessage([]byte(m.Plaintext), nil); got != m.Ciphertext {
			t.Fatalf("G message %d: ciphertext differs from the one vodozemac accepted", i)
		}
	}
	for i, m := range v.SToG {
		got, err := ch.openMessage(m.Ciphertext, nil)
		if err != nil {
			t.Fatalf("S message %d: %v", i, err)
		}
		if string(got) != m.Plaintext {
			t.Fatalf("S message %d: plaintext differs", i)
		}
	}

	// The last S message is the secrets bundle from the rust-sdk test.
	bundle, err := parseSecrets([]byte(v.SToG[len(v.SToG)-1].Plaintext))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Backup == nil || bundle.Backup.BackupVersion != "2" || bundle.CrossSigning.MasterKey != "rTtSv67XGS6k/rg6/yTG/m573cyFTPFRqluFhQY+hSw" {
		t.Fatal("secrets bundle parsed wrong")
	}
	for _, s := range []string{fmt.Sprintf("%v %+v %#v", bundle, *bundle, bundle), fmt.Sprint(*bundle)} {
		if strings.Contains(s, bundle.CrossSigning.MasterKey) {
			t.Fatal("formatting prints a key")
		}
	}
}

func TestECIESInitialMessageEncoding(t *testing.T) {
	// From vodozemac src/ecies/messages.rs.
	const msg = "3On7QFJyLQMAErua9K/yIOcJALvuMYax1AW0iWgf64AwtSMZXwAA012Q|9yA/CX8pJKF02Prd75ZyBQHg3fGTVVGDNl86q1z17Us"
	key, ct, err := decodeECIESInitialMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	if encodeBase64(key[:]) != "9yA/CX8pJKF02Prd75ZyBQHg3fGTVVGDNl86q1z17Us" || encodeBase64(ct)+"|"+encodeBase64(key[:]) != msg {
		t.Fatal("re-encoding differs")
	}
	if _, _, err = decodeECIESInitialMessage("no-separator"); err == nil {
		t.Fatal("no error for a message without separator")
	}
}

func TestECIESRoundTrip(t *testing.T) {
	g, _ := newECIESKeyPair()
	s, _ := newECIESKeyPair()
	sCh, initial, err := s.establishOutbound(g.publicKey(), []byte(loginInitiateMessage))
	if err != nil {
		t.Fatal(err)
	}
	gCh, pt, err := g.establishInbound(initial)
	if err != nil || string(pt) != loginInitiateMessage {
		t.Fatal(err)
	}
	if gCh.checkCode != sCh.checkCode {
		t.Fatal("check codes differ")
	}
	for i := 0; i < 3; i++ {
		msg := gCh.sealMessage([]byte("hello"), nil)
		if got, err := sCh.openMessage(msg, nil); err != nil || string(got) != "hello" {
			t.Fatalf("round %d: %v", i, err)
		}
	}

	// A third party that swaps the key in the initial message breaks the
	// decryption.
	m, _ := newECIESKeyPair()
	_, ct, _ := decodeECIESInitialMessage(initial)
	mPub := m.publicKey()
	if _, _, err = g.establishInbound(encodeBase64(ct) + "|" + encodeBase64(mPub[:])); !errors.Is(err, ErrDecryption) {
		t.Fatalf("swapped key: %v", err)
	}
	// An all-zero key is not contributory.
	if _, _, err = g.establishInbound(encodeBase64(ct) + "|" + encodeBase64(make([]byte, 32))); err == nil {
		t.Fatal("zero key: no error")
	}
}

func TestHPKERoundTrip(t *testing.T) {
	aad1 := []byte("aad-1")
	aad2 := []byte("aad-2")
	g, err := newHPKERecipientKey()
	if err != nil {
		t.Fatal(err)
	}
	s, initial, err := newHPKESender(g.public, []byte(loginInitiateMessage), aad1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = g.establish(initial, aad2); !errors.Is(err, ErrDecryption) {
		t.Fatal("wrong AAD: no decryption error")
	}
	pending, pt, err := g.establish(initial, aad1)
	if err != nil || string(pt) != loginInitiateMessage {
		t.Fatal(err)
	}
	gCh, response, err := pending.respond([]byte(loginOKMessage), aad2)
	if err != nil {
		t.Fatal(err)
	}
	sCh, pt, err := s.finish(response, aad2)
	if err != nil || string(pt) != loginOKMessage {
		t.Fatalf("finish: %v %q", err, pt)
	}
	if gCh.code != sCh.code {
		t.Fatal("check codes differ")
	}
	if d := gCh.checkDigits(); d < 10 || d > 99 {
		t.Fatalf("check digits %d", d)
	}
	for i := 0; i < 3; i++ {
		msg := sCh.sealMessage([]byte("to G"), aad1)
		if got, err := gCh.openMessage(msg, aad1); err != nil || string(got) != "to G" {
			t.Fatalf("S to G %d: %v", i, err)
		}
		msg = gCh.sealMessage([]byte("to S"), aad2)
		if got, err := sCh.openMessage(msg, aad2); err != nil || string(got) != "to S" {
			t.Fatalf("G to S %d: %v", i, err)
		}
	}
	// A failed open does not advance the sequence.
	msg := gCh.sealMessage([]byte("x"), aad1)
	if _, err = sCh.openMessage(msg, aad2); err == nil {
		t.Fatal("wrong AAD: no error")
	}
	if got, err := sCh.openMessage(msg, aad1); err != nil || string(got) != "x" {
		t.Fatalf("retry after failure: %v", err)
	}
}

func TestHPKEResponseNonce(t *testing.T) {
	// The initial response is response nonce (32 bytes) || ciphertext, and
	// the response context nonce is base nonce XOR sequence (RFC 9180).
	g, _ := newHPKERecipientKey()
	_, initial, _ := newHPKESender(g.public, []byte(loginInitiateMessage), nil)
	pending, _, err := g.establish(initial, nil)
	if err != nil {
		t.Fatal(err)
	}
	pending.randRead = func(b []byte) (int, error) {
		for i := range b {
			b[i] = byte(i)
		}
		return len(b), nil
	}
	_, response, err := pending.respond([]byte(loginOKMessage), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := decodeBase64(response)
	for i := 0; i < hpkeResponseNonceSize; i++ {
		if raw[i] != byte(i) {
			t.Fatal("response does not start with the response nonce")
		}
	}
	if len(raw) != hpkeResponseNonceSize+len(loginOKMessage)+16 {
		t.Fatalf("response length %d", len(raw))
	}
	r := &responseContext{baseNonce: bytes.Repeat([]byte{0xff}, 12), seq: 1}
	if n := r.nonce(); n[11] != 0xfe || n[0] != 0xff {
		t.Fatalf("nonce %x", n)
	}
}
