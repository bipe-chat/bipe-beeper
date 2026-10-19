package bipe

import "maunium.net/go/mautrix/oauth"

// DeviceDisplayName is the name of the bridge device in Linked devices on
// the phone.
const DeviceDisplayName = "Beeper"

// OAuthClient is the client metadata that the bridge registers with MAS. MAS
// shows the client name on its consent page.
func OAuthClient() oauth.ClientMetadata {
	return oauth.ClientMetadata{
		ClientName: "Bipe no Beeper",
		ClientURI:  "https://github.com/bipe-chat/bipe-beeper",
	}
}

// User-facing strings of the phone QR login. They went through the humanizer
// skill.
const (
	LoginFlowName        = "Entrar com o celular"
	LoginFlowDescription = "Escaneie um código com o app do Bipe no celular."

	LoginScanQR          = "No celular, abra Bipe > Configurações > Aparelhos conectados > Conectar um computador e escaneie este código."
	LoginTypeCheckCode   = "Digite o número de 2 dígitos que aparece no celular."
	LoginCheckCodeField  = "Número de 2 dígitos"
	LoginCheckCodeHelp   = "O número que o celular mostra depois de ler o código."
	LoginTypeUserCode    = "No celular, digite este código na página que abriu e confirme."
	LoginQRExpired       = "O código QR expirou. Comece de novo."
	LoginCheckCodeWrong  = "O número não bate com o do celular. Comece de novo."
	LoginDeclined        = "O celular recusou a entrada."
	LoginApprovalExpired = "O tempo para aprovar acabou. Comece de novo."
	LoginNotSupported    = "Este servidor não aceita entrar por código QR."
	SessionEnded         = "A sessão do Bipe acabou. Entre de novo."
)

// User-facing strings of the device verification (bipe#423). They went
// through the humanizer skill. VerifyCompare takes the emojis and the two
// commands.
const (
	DeviceNotVerified = "Este aparelho ainda não foi verificado. Abra o Bipe no celular para aprovar."
	VerifyCompare     = "Compare estes emojis com os do celular:\n\n%s\n\nSe forem iguais, envie `%s`. Se forem diferentes, envie `%s`."
	VerifyDone        = "Pronto. Este aparelho está verificado."
	VerifyCancelled   = "A verificação não terminou. Tente de novo pelo celular."
	VerifyNothingOpen = "Não há verificação aberta."
)
