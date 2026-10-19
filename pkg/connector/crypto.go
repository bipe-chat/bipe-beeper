package connector

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/crypto/verificationhelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
	"github.com/bipe-chat/bipe-beeper/pkg/qrlogin"
)

// E2EE setup of the bridge device (bipe#423). After a QR login the phone
// sends the cross-signing keys and the key-backup key. The bridge signs its
// own device, restores the room keys from the backup and uploads its own new
// room keys to the backup. Without the secrets, the device stays unverified
// and the user can verify it with SAS emoji from the phone.
//
// The keys stay in the crypto store (the SQLite file of the login, values
// encrypted with the pickle key). Nothing here logs a key.

const backupRestoreTimeout = 5 * time.Minute

// afterQRLogin gives the E2EE secrets to the client of the new login. The
// client imports them in Connect, after the crypto store is open.
func (c *Connector) afterQRLogin(ctx context.Context, login *bridgev2.UserLogin, secrets *qrlogin.SecretsBundle) {
	if client, ok := login.Client.(*Client); ok {
		client.pendingSecrets = secrets
	} else {
		secrets.Clear()
	}
}

// setupE2EE runs in Connect after the crypto helper is ready and before the
// sync starts.
func (c *Client) setupE2EE(ctx context.Context) {
	log := c.login.Log.With().Str("action", "e2ee setup").Logger()
	ctx = log.WithContext(ctx)
	mach := c.crypto.Machine()

	secrets := c.pendingSecrets
	c.pendingSecrets = nil
	restore := false
	if secrets != nil {
		err := c.importSecrets(ctx, mach, secrets)
		secrets.Clear()
		if err != nil {
			log.Warn().Str("reason", err.Error()).Msg("Failed to import the E2EE secrets from the phone")
		} else {
			restore = true
		}
	} else if err := c.loadStoredSecrets(ctx, mach); err != nil {
		log.Warn().Err(err).Msg("Failed to load the stored E2EE secrets")
	}

	if c.backupKey != nil {
		version, err := mach.GetAndVerifyLatestKeyBackupVersion(ctx, c.backupKey)
		switch {
		case err != nil:
			log.Warn().Err(err).Msg("Failed to check the key backup")
		case version == nil:
			log.Info().Msg("The account has no key backup")
		default:
			c.backupVersion = version.Version
			if err = mach.SetKeyBackupVersion(ctx, version.Version); err != nil {
				log.Warn().Err(err).Msg("Failed to save the key backup version")
			}
			if restore {
				restoreCtx, cancel := context.WithTimeout(ctx, backupRestoreTimeout)
				err = mach.GetAndStoreKeyBackup(restoreCtx, version.Version, c.backupKey)
				cancel()
				if err != nil {
					log.Warn().Err(err).Msg("Failed to restore room keys from the key backup")
				}
			}
			mach.SessionReceived = c.backupRoomKey
		}
	}

	c.setupVerification(ctx, mach)
	if mach.CrossSigningKeys == nil && !mach.IsDeviceTrusted(ctx, mach.OwnIdentity()) {
		c.sendNotice(ctx, bipe.DeviceNotVerified)
	}
}

func decodeSecret(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	return base64.RawStdEncoding.DecodeString(s)
}

// importSecrets checks the cross-signing keys against the public keys on
// the server, imports them, signs this device, and keeps the backup key.
func (c *Client) importSecrets(ctx context.Context, mach *crypto.OlmMachine, secrets *qrlogin.SecretsBundle) error {
	var seeds crypto.CrossSigningSeeds
	var err error
	if seeds.MasterKey, err = decodeSecret(secrets.CrossSigning.MasterKey); err != nil {
		return errors.New("invalid master key")
	}
	if seeds.SelfSigningKey, err = decodeSecret(secrets.CrossSigning.SelfSigningKey); err != nil {
		return errors.New("invalid self-signing key")
	}
	if seeds.UserSigningKey, err = decodeSecret(secrets.CrossSigning.UserSigningKey); err != nil {
		return errors.New("invalid user-signing key")
	}
	defer func() {
		clear(seeds.MasterKey)
		clear(seeds.SelfSigningKey)
		clear(seeds.UserSigningKey)
	}()
	if err = c.checkCrossSigningKeys(ctx, seeds); err != nil {
		return err
	}
	if err = mach.ImportCrossSigningKeys(seeds); err != nil {
		return errors.New("failed to import the cross-signing keys")
	}
	for name, seed := range map[id.Secret][]byte{
		id.SecretXSMaster:      seeds.MasterKey,
		id.SecretXSSelfSigning: seeds.SelfSigningKey,
		id.SecretXSUserSigning: seeds.UserSigningKey,
	} {
		if err = mach.CryptoStore.PutSecret(ctx, name, base64.RawStdEncoding.EncodeToString(seed)); err != nil {
			return fmt.Errorf("failed to store the cross-signing keys: %w", err)
		}
	}
	if err = mach.SignOwnDevice(ctx, mach.OwnIdentity()); err != nil {
		return fmt.Errorf("failed to sign the bridge device: %w", err)
	}

	if secrets.Backup == nil {
		return nil
	} else if secrets.Backup.Algorithm != string(id.KeyBackupAlgorithmMegolmBackupV1) {
		return fmt.Errorf("unsupported key backup algorithm %s", secrets.Backup.Algorithm)
	}
	raw, err := decodeSecret(secrets.Backup.Key)
	if err != nil {
		return errors.New("invalid backup key")
	}
	defer clear(raw)
	key, err := backup.MegolmBackupKeyFromBytes(raw)
	if err != nil {
		return errors.New("invalid backup key")
	}
	if err = mach.CryptoStore.PutSecret(ctx, id.SecretMegolmBackupV1, base64.RawStdEncoding.EncodeToString(raw)); err != nil {
		return fmt.Errorf("failed to store the backup key: %w", err)
	}
	c.backupKey = key
	return nil
}

// checkCrossSigningKeys refuses keys whose public master key is not the one
// on the server. The phone is authenticated, but a wrong key must not sign
// this device.
func (c *Client) checkCrossSigningKeys(ctx context.Context, seeds crypto.CrossSigningSeeds) error {
	keys, err := seeds.Decode()
	if err != nil {
		return errors.New("invalid cross-signing keys")
	}
	resp, err := c.cli.QueryKeys(ctx, &mautrix.ReqQueryKeys{DeviceKeys: mautrix.DeviceKeysRequest{c.userID: {}}})
	if err != nil {
		return fmt.Errorf("failed to query the cross-signing keys: %w", err)
	}
	master, ok := resp.MasterKeys[c.userID]
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

// loadStoredSecrets reads the keys that an earlier login stored.
func (c *Client) loadStoredSecrets(ctx context.Context, mach *crypto.OlmMachine) error {
	var seeds crypto.CrossSigningSeeds
	for name, dst := range map[id.Secret]*[]byte{
		id.SecretXSMaster:      (*[]byte)(&seeds.MasterKey),
		id.SecretXSSelfSigning: (*[]byte)(&seeds.SelfSigningKey),
		id.SecretXSUserSigning: (*[]byte)(&seeds.UserSigningKey),
	} {
		value, err := mach.CryptoStore.GetSecret(ctx, name)
		if err != nil {
			return err
		} else if value == "" {
			return nil
		}
		if *dst, err = decodeSecret(value); err != nil {
			return errors.New("invalid stored cross-signing key")
		}
	}
	if err := mach.ImportCrossSigningKeys(seeds); err != nil {
		return errors.New("invalid stored cross-signing keys")
	}
	value, err := mach.CryptoStore.GetSecret(ctx, id.SecretMegolmBackupV1)
	if err != nil || value == "" {
		return err
	}
	raw, err := decodeSecret(value)
	if err != nil {
		return errors.New("invalid stored backup key")
	}
	defer clear(raw)
	if c.backupKey, err = backup.MegolmBackupKeyFromBytes(raw); err != nil {
		return errors.New("invalid stored backup key")
	}
	return nil
}

// backupRoomKey is the OlmMachine.SessionReceived hook. It uploads each room
// key that is not in the backup yet, among them the keys of the sessions
// that this device creates when Beeper sends a message.
func (c *Client) backupRoomKey(ctx context.Context, roomID id.RoomID, sessionID id.SessionID, _ uint32) {
	go func() {
		ctx := context.WithoutCancel(ctx)
		if err := c.uploadRoomKey(ctx, roomID, sessionID); err != nil {
			c.login.Log.Warn().Err(err).Stringer("room_id", roomID).Stringer("session_id", sessionID).Msg("Failed to back up a room key")
		}
	}()
}

func (c *Client) uploadRoomKey(ctx context.Context, roomID id.RoomID, sessionID id.SessionID) error {
	c.backupLock.Lock()
	defer c.backupLock.Unlock()
	helper := c.crypto
	if helper == nil || c.backupKey == nil || c.backupVersion == "" {
		return nil
	}
	mach := helper.Machine()
	igs, err := mach.CryptoStore.GetGroupSession(ctx, roomID, sessionID)
	if err != nil {
		return err
	} else if igs == nil || igs.KeySource == id.KeySourceBackup || igs.KeyBackupVersion == c.backupVersion {
		return nil
	}
	firstIndex := igs.Internal.FirstKnownIndex()
	exported, err := igs.Internal.Export(firstIndex)
	if err != nil {
		return fmt.Errorf("failed to export the session: %w", err)
	}
	encrypted, err := backup.EncryptSessionData(c.backupKey, backup.MegolmSessionData{
		Algorithm:          id.AlgorithmMegolmV1,
		ForwardingKeyChain: igs.ForwardingChains,
		SenderClaimedKeys:  backup.SenderClaimedKeys{Ed25519: igs.SigningKey},
		SenderKey:          igs.SenderKey,
		SessionKey:         string(exported),
		SharedHistory:      igs.SharedHistory,
	})
	clear(exported)
	if err != nil {
		return fmt.Errorf("failed to encrypt the session: %w", err)
	}
	data, err := json.Marshal(encrypted)
	if err != nil {
		return err
	}
	_, err = c.cli.PutKeysInBackupForRoomAndSession(ctx, c.backupVersion, roomID, sessionID, &mautrix.ReqKeyBackupData{
		FirstMessageIndex: int(firstIndex),
		ForwardedCount:    len(igs.ForwardingChains),
		IsVerified:        igs.SenderKey == mach.OwnIdentity().IdentityKey,
		SessionData:       data,
	})
	if err != nil {
		return err
	}
	igs.KeyBackupVersion = c.backupVersion
	return mach.CryptoStore.PutGroupSession(ctx, igs)
}

// sendNotice posts a notice to the user's management room.
func (c *Client) sendNotice(ctx context.Context, text string) {
	roomID, err := c.login.User.GetManagementRoom(ctx)
	if err != nil {
		c.login.Log.Err(err).Msg("Failed to get the management room")
		return
	}
	_, err = c.main.br.Bot.SendMessage(ctx, roomID, event.EventMessage, &event.Content{
		Parsed: &event.MessageEventContent{MsgType: event.MsgNotice, Body: text},
	}, nil)
	if err != nil {
		c.login.Log.Err(err).Msg("Failed to send a notice")
	}
}

// sasVerification accepts SAS verification from the user's own devices, for
// a bridge device without the secrets. The user compares the emojis and
// answers with the confirm or reject command.
type sasVerification struct {
	client *Client
	helper *verificationhelper.VerificationHelper

	mu      sync.Mutex
	pending id.VerificationTransactionID // shown, waiting for the user
}

func (c *Client) setupVerification(ctx context.Context, mach *crypto.OlmMachine) {
	v := &sasVerification{client: c}
	v.helper = verificationhelper.NewVerificationHelper(c.cli, mach, nil, v, false, false, true)
	if err := v.helper.Init(ctx); err != nil {
		c.login.Log.Err(err).Msg("Failed to start the verification helper")
		return
	}
	c.verification = v
}

func (v *sasVerification) VerificationRequested(ctx context.Context, txnID id.VerificationTransactionID, from id.UserID, fromDevice id.DeviceID) {
	if from != v.client.userID || fromDevice == v.client.cli.DeviceID {
		_ = v.helper.CancelVerification(ctx, txnID, event.VerificationCancelCodeUser, "only the user's own devices can verify this device")
		return
	}
	if err := v.helper.AcceptVerification(ctx, txnID); err != nil {
		v.client.login.Log.Err(err).Msg("Failed to accept the verification request")
	}
}

func (v *sasVerification) VerificationReady(ctx context.Context, txnID id.VerificationTransactionID, otherDeviceID id.DeviceID, supportsSAS, supportsScanQRCode bool, qrCode *verificationhelper.QRCode) {
	// The phone starts SAS.
}

func (v *sasVerification) ShowSAS(ctx context.Context, txnID id.VerificationTransactionID, emojis []rune, emojiDescriptions []string, decimals []int) {
	v.mu.Lock()
	v.pending = txnID
	v.mu.Unlock()
	var shown string
	if len(emojis) > 0 {
		parts := make([]string, len(emojis))
		for i, e := range emojis {
			parts[i] = string(e)
		}
		shown = strings.Join(parts, "  ")
	} else {
		parts := make([]string, len(decimals))
		for i, d := range decimals {
			parts[i] = fmt.Sprint(d)
		}
		shown = strings.Join(parts, " ")
	}
	prefix := v.client.main.br.Config.CommandPrefix
	v.client.sendNotice(ctx, fmt.Sprintf(bipe.VerifyCompare, shown, prefix+" "+cmdVerifyConfirm, prefix+" "+cmdVerifyReject))
}

func (v *sasVerification) VerificationCancelled(ctx context.Context, txnID id.VerificationTransactionID, code event.VerificationCancelCode, reason string) {
	v.clearPending(txnID)
	v.client.sendNotice(ctx, bipe.VerifyCancelled)
}

func (v *sasVerification) VerificationDone(ctx context.Context, txnID id.VerificationTransactionID, method event.VerificationMethod) {
	v.clearPending(txnID)
	v.client.sendNotice(ctx, bipe.VerifyDone)
}

func (v *sasVerification) clearPending(txnID id.VerificationTransactionID) {
	v.mu.Lock()
	if v.pending == txnID {
		v.pending = ""
	}
	v.mu.Unlock()
}

// answer confirms or rejects the emojis that the bridge showed last.
func (v *sasVerification) answer(ctx context.Context, confirm bool) bool {
	v.mu.Lock()
	txnID := v.pending
	v.pending = ""
	v.mu.Unlock()
	if txnID == "" {
		return false
	}
	var err error
	if confirm {
		err = v.helper.ConfirmSAS(ctx, txnID)
	} else {
		err = v.helper.CancelVerification(ctx, txnID, event.VerificationCancelCodeSASMismatch, "the user said the emojis differ")
	}
	if err != nil {
		v.client.login.Log.Err(err).Msg("Failed to answer the verification")
	}
	return true
}
