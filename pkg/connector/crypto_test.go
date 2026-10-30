package connector

import (
	"testing"

	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/id"

	"github.com/bipe-chat/bipe-beeper/pkg/e2ee"
)

// The secrets bundle from the matrix-rust-sdk test serialize_secrets_bundle.
func TestSecretsDecode(t *testing.T) {
	var seeds crypto.CrossSigningSeeds
	var err error
	if seeds.MasterKey, err = e2ee.DecodeSecret("rTtSv67XGS6k/rg6/yTG/m573cyFTPFRqluFhQY+hSw"); err != nil {
		t.Fatal(err)
	}
	if seeds.SelfSigningKey, err = e2ee.DecodeSecret("4jbPt7jh5D2iyM4U+3IDa+WthgJB87IQN1ATdkau+xk"); err != nil {
		t.Fatal(err)
	}
	if seeds.UserSigningKey, err = e2ee.DecodeSecret("YkFKtkjcsTxF6UAzIIG/l6Nog/G2RigCRfWj3cjNWeM="); err != nil {
		t.Fatal("padded base64 must decode too:", err)
	}
	keys, err := seeds.Decode()
	if err != nil {
		t.Fatal(err)
	}
	pub := keys.PublicKeys()
	if pub.MasterKey == "" || pub.MasterKey == pub.SelfSigningKey {
		t.Fatal("bad public keys")
	}

	raw, err := e2ee.DecodeSecret("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err != nil || len(raw) != 32 {
		t.Fatalf("backup key: %v", err)
	}
	if _, err = backup.MegolmBackupKeyFromBytes(raw); err != nil {
		t.Fatal(err)
	}
}

// The upload path encrypts MegolmSessionData; a client with the backup key
// must decrypt it.
func TestBackupSessionRoundTrip(t *testing.T) {
	key, err := backup.NewMegolmBackupKey()
	if err != nil {
		t.Fatal(err)
	}
	data := backup.MegolmSessionData{
		Algorithm:         id.AlgorithmMegolmV1,
		SenderClaimedKeys: backup.SenderClaimedKeys{Ed25519: "ed"},
		SenderKey:         "curve",
		SessionKey:        "session-key",
	}
	enc, err := backup.EncryptSessionData(key, data)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := enc.Decrypt(key)
	if err != nil {
		t.Fatal(err)
	}
	if dec.SessionKey != "session-key" || dec.Algorithm != id.AlgorithmMegolmV1 {
		t.Fatalf("decrypted %+v", dec)
	}
}
