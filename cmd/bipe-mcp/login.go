package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/skip2/go-qrcode"
	"maunium.net/go/mautrix"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
	"github.com/bipe-chat/bipe-beeper/pkg/e2ee"
	"github.com/bipe-chat/bipe-beeper/pkg/qrlogin"
)

const (
	// maxQRRefreshes limits how often the QR code is renewed. Synapse keeps
	// a rendezvous session for 60 seconds.
	maxQRRefreshes = 5
	// secretsTimeout is how long bipe-mcp waits for m.login.secrets.
	secretsTimeout = 45 * time.Second
	// backupRestoreTimeout limits the room key download after sign-in.
	backupRestoreTimeout = 5 * time.Minute
)

// runLogin signs bipe-mcp in as a new device with the MSC4108 QR login, the
// same flow as the bridge (pkg/qrlogin). The terminal shows the QR code on
// stderr; the user types the check code on stdin. An earlier session of
// this config dir is revoked and its crypto store is removed.
func runLogin(ctx context.Context, dir, homeserver string, in io.Reader, out io.Writer, log zerolog.Logger) error {
	if old, err := loadSession(dir); err == nil && old != nil {
		k := newTokenKeeper(dir, old, log)
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err = k.revoke(rctx); err != nil {
			log.Warn().Str("reason", errorKind(err)).Msg("Failed to revoke the old session")
		}
		cancel()
	}
	for _, p := range []string{sessionPath(dir), cryptoPath(dir), cryptoPath(dir) + "-wal", cryptoPath(dir) + "-shm"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	login, err := qrlogin.Start(ctx, qrlogin.Config{HomeserverURL: homeserver, Client: bipe.MCPOAuthClient()})
	if errors.Is(err, qrlogin.ErrNotSupported) {
		return errors.New(bipe.LoginNotSupported)
	} else if err != nil {
		return fmt.Errorf("failed to start the QR login: %w", err)
	}
	defer login.Close(context.WithoutCancel(ctx))

	for refreshes := 0; ; refreshes++ {
		data, err := login.QRCode()
		if err != nil {
			return err
		}
		qr, err := qrcode.New(string(data), qrcode.Low)
		if err != nil {
			return fmt.Errorf("failed to render the QR code: %w", err)
		}
		fmt.Fprintf(out, "\n%s\n%s\n", qr.ToSmallString(false), bipe.LoginScanQR)
		err = login.WaitForScan(ctx)
		if errors.Is(err, qrlogin.ErrRendezvousExpired) {
			if refreshes >= maxQRRefreshes {
				return errors.New(bipe.LoginQRExpired)
			}
			if err = login.RefreshQRCode(ctx); err != nil {
				return err
			}
			continue
		} else if err != nil {
			return fmt.Errorf("failed to open the secure channel: %w", err)
		}
		break
	}

	fmt.Fprintf(out, "%s ", bipe.LoginTypeCheckCode)
	code, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && code == "" {
		login.Cancel(ctx)
		return errors.New("no check code")
	}
	if err = login.ConfirmCheckCode(ctx, strings.TrimSpace(code)); errors.Is(err, qrlogin.ErrInvalidCheckCode) {
		return errors.New(bipe.LoginCheckCodeWrong)
	} else if err != nil {
		return err
	}
	auth, err := login.RequestAuthorization(ctx)
	if err != nil {
		return fmt.Errorf("failed to request the authorization: %w", err)
	}
	fmt.Fprintf(out, "%s\n\n    %s\n\n", bipe.LoginTypeUserCode, auth.UserCode)

	res, err := login.WaitForApproval(ctx)
	switch {
	case errors.Is(err, qrlogin.ErrDeclined):
		return errors.New(bipe.LoginDeclined)
	case errors.Is(err, qrlogin.ErrAuthorizationExpired):
		return errors.New(bipe.LoginApprovalExpired)
	case err != nil:
		return fmt.Errorf("failed to get the tokens: %w", err)
	}
	sctx, cancel := context.WithTimeout(ctx, secretsTimeout)
	secrets, err := login.ReceiveSecrets(sctx)
	cancel()
	if err != nil {
		log.Warn().Str("reason", errorKind(err)).Msg("No E2EE secrets from the phone")
		secrets = nil
	}
	if secrets != nil {
		defer secrets.Clear()
	}

	sess := &storedSession{
		HomeserverURL:      res.HomeserverURL,
		UserID:             string(res.UserID),
		DeviceID:           string(res.DeviceID),
		PickleKey:          newPickleKey(),
		OAuthClientID:      res.Session.ClientID,
		TokenEndpoint:      res.Session.TokenEndpoint,
		RevocationEndpoint: res.Session.RevocationEndpoint,
	}
	sess.setTokens(res.Session.Tokens)
	if err = saveSession(dir, sess); err != nil {
		return fmt.Errorf("failed to save the session: %w", err)
	}

	tokens := newTokenKeeper(dir, sess, log)
	m, err := openMatrix(ctx, dir, sess, tokens, log)
	if err != nil {
		return err
	}
	defer m.close()
	if err = m.cli.SetDeviceInfo(ctx, res.DeviceID, &mautrix.ReqDeviceInfo{DisplayName: bipe.MCPDeviceDisplayName}); err != nil {
		log.Warn().Str("reason", errorKind(err)).Msg("Failed to set the device name")
	}
	if secrets == nil {
		fmt.Fprintln(out, bipe.MCPNoSecrets)
	} else {
		importAndRestore(ctx, m, secrets, log)
	}
	fmt.Fprintln(out, bipe.MCPSignedIn)
	return nil
}

// importAndRestore signs the device with the cross-signing keys from the
// phone and downloads the room keys from the key backup.
func importAndRestore(ctx context.Context, m *matrixClient, secrets *qrlogin.SecretsBundle, log zerolog.Logger) {
	mach := m.helper.Machine()
	key, err := e2ee.ImportSecrets(ctx, m.cli, mach, secrets)
	if err != nil {
		log.Warn().Str("reason", err.Error()).Msg("Failed to import the E2EE secrets from the phone")
		return
	}
	if key == nil {
		return
	}
	version, err := mach.GetAndVerifyLatestKeyBackupVersion(ctx, key)
	if err != nil || version == nil {
		log.Warn().Err(err).Msg("No usable key backup")
		return
	}
	rctx, cancel := context.WithTimeout(ctx, backupRestoreTimeout)
	defer cancel()
	if err = mach.GetAndStoreKeyBackup(rctx, version.Version, key); err != nil {
		log.Warn().Err(err).Msg("Failed to restore room keys from the key backup")
	}
}

func newPickleKey() string {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return base64.RawStdEncoding.EncodeToString(key)
}
