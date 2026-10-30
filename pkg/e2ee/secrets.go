// Package e2ee imports the E2EE secrets that the phone sends after a QR
// login (cross-signing keys and the key-backup key) into a mautrix-go
// OlmMachine. The bridge connector and bipe-mcp use it. Nothing here logs a
// key.
package e2ee

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/id"

	"github.com/bipe-chat/bipe-beeper/pkg/qrlogin"
)

// DecodeSecret decodes a base64 secret with or without padding.
func DecodeSecret(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	return base64.RawStdEncoding.DecodeString(s)
}

// ImportSecrets checks the cross-signing keys against the public keys on
// the server, imports them, signs this device, and stores the keys in the
// crypto store. It returns the backup key, or nil when the phone sent none.
func ImportSecrets(ctx context.Context, cli *mautrix.Client, mach *crypto.OlmMachine, secrets *qrlogin.SecretsBundle) (*backup.MegolmBackupKey, error) {
	var seeds crypto.CrossSigningSeeds
	var err error
	if seeds.MasterKey, err = DecodeSecret(secrets.CrossSigning.MasterKey); err != nil {
		return nil, errors.New("invalid master key")
	}
	if seeds.SelfSigningKey, err = DecodeSecret(secrets.CrossSigning.SelfSigningKey); err != nil {
		return nil, errors.New("invalid self-signing key")
	}
	if seeds.UserSigningKey, err = DecodeSecret(secrets.CrossSigning.UserSigningKey); err != nil {
		return nil, errors.New("invalid user-signing key")
	}
	defer func() {
		clear(seeds.MasterKey)
		clear(seeds.SelfSigningKey)
		clear(seeds.UserSigningKey)
	}()
	if err = checkCrossSigningKeys(ctx, cli, seeds); err != nil {
		return nil, err
	}
	if err = mach.ImportCrossSigningKeys(seeds); err != nil {
		return nil, errors.New("failed to import the cross-signing keys")
	}
	for name, seed := range map[id.Secret][]byte{
		id.SecretXSMaster:      seeds.MasterKey,
		id.SecretXSSelfSigning: seeds.SelfSigningKey,
		id.SecretXSUserSigning: seeds.UserSigningKey,
	} {
		if err = mach.CryptoStore.PutSecret(ctx, name, base64.RawStdEncoding.EncodeToString(seed)); err != nil {
			return nil, fmt.Errorf("failed to store the cross-signing keys: %w", err)
		}
	}
	if err = mach.SignOwnDevice(ctx, mach.OwnIdentity()); err != nil {
		return nil, fmt.Errorf("failed to sign the device: %w", err)
	}

	if secrets.Backup == nil {
		return nil, nil
	} else if secrets.Backup.Algorithm != string(id.KeyBackupAlgorithmMegolmBackupV1) {
		return nil, fmt.Errorf("unsupported key backup algorithm %s", secrets.Backup.Algorithm)
	}
	raw, err := DecodeSecret(secrets.Backup.Key)
	if err != nil {
		return nil, errors.New("invalid backup key")
	}
	defer clear(raw)
	key, err := backup.MegolmBackupKeyFromBytes(raw)
	if err != nil {
		return nil, errors.New("invalid backup key")
	}
	if err = mach.CryptoStore.PutSecret(ctx, id.SecretMegolmBackupV1, base64.RawStdEncoding.EncodeToString(raw)); err != nil {
		return nil, fmt.Errorf("failed to store the backup key: %w", err)
	}
	return key, nil
}

// checkCrossSigningKeys refuses keys whose public master key is not the one
// on the server. The phone is authenticated, but a wrong key must not sign
// this device.
func checkCrossSigningKeys(ctx context.Context, cli *mautrix.Client, seeds crypto.CrossSigningSeeds) error {
	keys, err := seeds.Decode()
	if err != nil {
		return errors.New("invalid cross-signing keys")
	}
	resp, err := cli.QueryKeys(ctx, &mautrix.ReqQueryKeys{DeviceKeys: mautrix.DeviceKeysRequest{cli.UserID: {}}})
	if err != nil {
		return fmt.Errorf("failed to query the cross-signing keys: %w", err)
	}
	master, ok := resp.MasterKeys[cli.UserID]
	if !ok {
		return errors.New("the account has no cross-signing master key")
	}
	want := keys.MasterKey.PublicKey()
	for _, key := range master.Keys {
		if key == want {
			return nil
		}
	}
	return errors.New("the master key from the phone does not match the server")
}

// LoadStoredSecrets reads the keys that an earlier login stored. It returns
// the backup key, or nil when none is stored.
func LoadStoredSecrets(ctx context.Context, mach *crypto.OlmMachine) (*backup.MegolmBackupKey, error) {
	var seeds crypto.CrossSigningSeeds
	for name, dst := range map[id.Secret]*[]byte{
		id.SecretXSMaster:      (*[]byte)(&seeds.MasterKey),
		id.SecretXSSelfSigning: (*[]byte)(&seeds.SelfSigningKey),
		id.SecretXSUserSigning: (*[]byte)(&seeds.UserSigningKey),
	} {
		value, err := mach.CryptoStore.GetSecret(ctx, name)
		if err != nil {
			return nil, err
		} else if value == "" {
			return nil, nil
		}
		if *dst, err = DecodeSecret(value); err != nil {
			return nil, errors.New("invalid stored cross-signing key")
		}
	}
	if err := mach.ImportCrossSigningKeys(seeds); err != nil {
		return nil, errors.New("invalid stored cross-signing keys")
	}
	value, err := mach.CryptoStore.GetSecret(ctx, id.SecretMegolmBackupV1)
	if err != nil || value == "" {
		return nil, err
	}
	raw, err := DecodeSecret(value)
	if err != nil {
		return nil, errors.New("invalid stored backup key")
	}
	defer clear(raw)
	key, err := backup.MegolmBackupKeyFromBytes(raw)
	if err != nil {
		return nil, errors.New("invalid stored backup key")
	}
	return key, nil
}
