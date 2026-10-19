package qrlogin

import (
	"bytes"
	"encoding/base64"
	"testing"
)

// Test vectors from matrix-rust-sdk
// crates/matrix-sdk-crypto/src/types/qr_login/msc_4108.rs and msc_4388.rs.
// The rust-sdk copied the byte vectors from the MSCs.

var msc4108Login = []byte{
	0x4D, 0x41, 0x54, 0x52, 0x49, 0x58, 0x02, 0x03, 0xd8, 0x86, 0x68, 0x6a, 0xb2, 0x19, 0x7b,
	0x78, 0x0e, 0x30, 0x0a, 0x9d, 0x4a, 0x21, 0x47, 0x48, 0x07, 0x00, 0xd7, 0x92, 0x9f, 0x39,
	0xab, 0x31, 0xb9, 0xe5, 0x14, 0x37, 0x02, 0x48, 0xed, 0x6b, 0x00, 0x47, 0x68, 0x74, 0x74,
	0x70, 0x73, 0x3a, 0x2f, 0x2f, 0x72, 0x65, 0x6e, 0x64, 0x65, 0x7a, 0x76, 0x6f, 0x75, 0x73,
	0x2e, 0x6c, 0x61, 0x62, 0x2e, 0x65, 0x6c, 0x65, 0x6d, 0x65, 0x6e, 0x74, 0x2e, 0x64, 0x65,
	0x76, 0x2f, 0x65, 0x38, 0x64, 0x61, 0x36, 0x33, 0x35, 0x35, 0x2d, 0x35, 0x35, 0x30, 0x62,
	0x2d, 0x34, 0x61, 0x33, 0x32, 0x2d, 0x61, 0x31, 0x39, 0x33, 0x2d, 0x31, 0x36, 0x31, 0x39,
	0x64, 0x39, 0x38, 0x33, 0x30, 0x36, 0x36, 0x38,
}

var msc4108Reciprocate = append(append([]byte{}, msc4108Login[:7]...), append([]byte{0x04}, append(append([]byte{}, msc4108Login[8:]...),
	0x00, 0x0A, 0x6d, 0x61, 0x74, 0x72, 0x69, 0x78, 0x2e, 0x6f, 0x72, 0x67)...)...)

const msc4108Base64 = "TUFUUklYAgS0yzZ1QVpQ1jlnoxWX3d5jrWRFfELxjS2gN7pz9y+3PABaaHR0" +
	"cHM6Ly9zeW5hcHNlLW9pZGMubGFiLmVsZW1lbnQuZGV2L19zeW5hcHNlL2Ns" +
	"aWVudC9yZW5kZXp2b3VzLzAxSFg5SzAwUTFINktQRDQ3RUc0RzFUM1hHACVo" +
	"dHRwczovL3N5bmFwc2Utb2lkYy5sYWIuZWxlbWVudC5kZXYv"

var msc4388Login = []byte{
	0x49, 0x4F, 0x5F, 0x45, 0x4C, 0x45, 0x4D, 0x45, 0x4E, 0x54, 0x5F, 0x4D, 0x53, 0x43, 0x34,
	0x33, 0x38, 0x38, 0x03, 0x00, 0xd8, 0x86, 0x68, 0x6a, 0xb2, 0x19, 0x7b, 0x78, 0x0e, 0x30,
	0x0a, 0x9d, 0x4a, 0x21, 0x47, 0x48, 0x07, 0x00, 0xd7, 0x92, 0x9f, 0x39, 0xab, 0x31, 0xb9,
	0xe5, 0x14, 0x37, 0x02, 0x48, 0xed, 0x6b, 0x24, 0x65, 0x38, 0x64, 0x61, 0x36, 0x33, 0x35,
	0x35, 0x2D, 0x35, 0x35, 0x30, 0x62, 0x2D, 0x34, 0x61, 0x33, 0x32, 0x2D, 0x61, 0x31, 0x39,
	0x33, 0x2D, 0x31, 0x36, 0x31, 0x39, 0x64, 0x39, 0x38, 0x33, 0x30, 0x36, 0x36, 0x38, 0x20,
	0x68, 0x74, 0x74, 0x70, 0x73, 0x3A, 0x2F, 0x2F, 0x6D, 0x61, 0x74, 0x72, 0x69, 0x78, 0x2D,
	0x63, 0x6C, 0x69, 0x65, 0x6E, 0x74, 0x2E, 0x6D, 0x61, 0x74, 0x72, 0x69, 0x78, 0x2E, 0x6F,
	0x72, 0x67,
}

const msc4388Base64 = "SU9fRUxFTUVOVF9NU0M0Mzg4AwG0yzZ1QVpQ1jlnoxWX3d5jrWRFfELxjS2gN" +
	"7pz9y+3PBowMUhYOUswMFExSDZLUEQ0N0VHNEcxVDNYRyRodHRwczovL3N5bm" +
	"Fwc2Utb2lkYy5sYWIuZWxlbWVudC5kZXY"

const vectorKey = "2IZoarIZe3gOMAqdSiFHSAcA15KfOasxueUUNwJI7Ws"

func mustKey(t *testing.T, s string) [32]byte {
	t.Helper()
	b, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil || len(b) != 32 {
		t.Fatalf("bad key %q", s)
	}
	return [32]byte(b)
}

func TestQRCodeMSC4108(t *testing.T) {
	q, err := ParseQRCode(msc4108Login)
	if err != nil {
		t.Fatal(err)
	}
	if q.Variant != VariantMSC4108 || q.Intent != IntentLogin {
		t.Fatalf("variant %v intent %v", q.Variant, q.Intent)
	}
	if q.PublicKey != mustKey(t, vectorKey) {
		t.Fatal("public key differs")
	}
	if q.RendezvousURL != "https://rendezvous.lab.element.dev/e8da6355-550b-4a32-a193-1619d9830668" {
		t.Fatalf("rendezvous URL %q", q.RendezvousURL)
	}
	out, err := q.Bytes()
	if err != nil || !bytes.Equal(out, msc4108Login) {
		t.Fatal("re-encoding differs")
	}

	q, err = ParseQRCode(msc4108Reciprocate)
	if err != nil {
		t.Fatal(err)
	}
	if q.Intent != IntentReciprocate || q.ServerName != "matrix.org" {
		t.Fatalf("intent %v server %q", q.Intent, q.ServerName)
	}
	if out, _ = q.Bytes(); !bytes.Equal(out, msc4108Reciprocate) {
		t.Fatal("re-encoding differs")
	}

	q, err = ParseQRCodeBase64(msc4108Base64)
	if err != nil {
		t.Fatal(err)
	}
	if q.PublicKey != mustKey(t, "tMs2dUFaUNY5Z6MVl93eY61kRXxC8Y0toDe6c/cvtzw") ||
		q.RendezvousURL != "https://synapse-oidc.lab.element.dev/_synapse/client/rendezvous/01HX9K00Q1H6KPD47EG4G1T3XG" ||
		q.ServerName != "https://synapse-oidc.lab.element.dev/" {
		t.Fatalf("base64 vector parsed wrong: %+v", q)
	}
	if b64, _ := q.Base64(); b64 != msc4108Base64 {
		t.Fatal("base64 re-encoding differs")
	}
}

func TestQRCodeMSC4388(t *testing.T) {
	q, err := ParseQRCode(msc4388Login)
	if err != nil {
		t.Fatal(err)
	}
	if q.Variant != VariantMSC4388 || q.Intent != IntentLogin || q.PublicKey != mustKey(t, vectorKey) ||
		q.RendezvousID != "e8da6355-550b-4a32-a193-1619d9830668" || q.BaseURL != "https://matrix-client.matrix.org" {
		t.Fatalf("parsed wrong: %+v", q)
	}
	if out, _ := q.Bytes(); !bytes.Equal(out, msc4388Login) {
		t.Fatal("re-encoding differs")
	}

	reciprocate := append([]byte{}, msc4388Login...)
	reciprocate[19] = 0x01
	if q, err = ParseQRCode(reciprocate); err != nil || q.Intent != IntentReciprocate {
		t.Fatalf("reciprocate: %v %+v", err, q)
	}

	q, err = ParseQRCodeBase64(msc4388Base64)
	if err != nil {
		t.Fatal(err)
	}
	if q.Intent != IntentReciprocate || q.RendezvousID != "01HX9K00Q1H6KPD47EG4G1T3XG" || q.BaseURL != "https://synapse-oidc.lab.element.dev" {
		t.Fatalf("base64 vector parsed wrong: %+v", q)
	}
	if b64, _ := q.Base64(); b64 != msc4388Base64 {
		t.Fatal("base64 re-encoding differs")
	}
}

func TestQRCodeInvalid(t *testing.T) {
	for name, data := range map[string][]byte{
		"empty":       nil,
		"truncated":   msc4108Login[:40],
		"bad version": append(append([]byte("MATRIX"), 0x01), msc4108Login[7:]...),
		"bad intent":  append(append([]byte("MATRIX"), 0x02, 0x09), msc4108Login[8:]...),
		"bad prefix":  []byte("NOT_A_LOGIN_QR_CODE_AT_ALL_REALLY"),
		"4388 cut":    msc4388Login[:60],
	} {
		if _, err := ParseQRCode(data); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	long := &QRCodeData{Variant: VariantMSC4388, RendezvousID: string(make([]byte, 256))}
	if _, err := long.Bytes(); err == nil {
		t.Error("too long rendezvous ID: no error")
	}
}

func TestCheckCodeDigits(t *testing.T) {
	// From vodozemac src/hpke/check_code.rs.
	for _, c := range []struct {
		code            CheckCode
		leading, noLead uint8
	}{
		{CheckCode{0, 0}, 0, 10},
		{CheckCode{9, 9}, 99, 19},
		{CheckCode{8, 9}, 89, 99},
		{CheckCode{0xff, 0xff}, 55, 45},
	} {
		if got := c.code.digitAllowLeadingZero(); got != c.leading {
			t.Errorf("%v leading zero: %d, want %d", c.code, got, c.leading)
		}
		if got := c.code.digitNoLeadingZero(); got != c.noLead {
			t.Errorf("%v no leading zero: %d, want %d", c.code, got, c.noLead)
		}
	}
}
