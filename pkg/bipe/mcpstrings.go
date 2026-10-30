package bipe

import "maunium.net/go/mautrix/oauth"

// MCPDeviceDisplayName is the name of the bipe-mcp device in Linked devices
// on the phone.
const MCPDeviceDisplayName = "bipe-mcp"

// MCPOAuthClient is the client metadata that bipe-mcp registers with MAS.
func MCPOAuthClient() oauth.ClientMetadata {
	return oauth.ClientMetadata{
		ClientName: "Bipe para assistentes (bipe-mcp)",
		ClientURI:  "https://github.com/bipe-chat/bipe-beeper",
	}
}

// DraftsRoomName is the name of the drafts room (bipe-shared
// docs/drafts-room.md). Other Matrix apps show it.
const DraftsRoomName = "Rascunhos"

// User-facing strings of bipe-mcp (bipe-chat/bipe#439). The MCP client shows
// tool errors to the user and to its model. Not yet run through the
// humanizer skill.
const (
	MCPContentWarning    = "bipe-mcp: o texto das suas conversas vai para o modelo do seu cliente MCP, que pode ficar fora do Brasil."
	MCPNotSignedIn       = "bipe-mcp ainda não entrou na sua conta. Num terminal, rode `bipe-mcp login` e escaneie o código com o celular."
	MCPAlreadyRunning    = "Outro bipe-mcp já está rodando com esta conta. Feche o outro antes."
	MCPSignedIn          = "Pronto. O bipe-mcp entrou na sua conta. Agora conecte o seu cliente MCP."
	MCPNoSecrets         = "O celular não mandou as chaves. O bipe-mcp só vai ler as mensagens novas. Para ler as antigas, entre de novo."
	MCPSessionEnded      = "A sessão do bipe-mcp acabou. Num terminal, rode `bipe-mcp login` de novo."
	MCPNotReady          = "O bipe-mcp ainda está sincronizando com o Bipe. Tente de novo em alguns segundos."
	MCPScopeMissing      = "Esta ferramenta precisa da permissão %s. Rode o bipe-mcp com --scopes incluindo %s."
	MCPBadArguments      = "Os parâmetros da ferramenta estão errados: %s"
	MCPBadRoomID         = "room_id precisa ser o ID de uma conversa (começa com !)."
	MCPBadEventID        = "reply_to e thread_root precisam ser IDs de mensagem (começam com $)."
	MCPNotJoined         = "Você não está nesta conversa."
	MCPDraftsRoomTarget  = "A conversa de rascunhos não recebe rascunhos nem mensagens."
	MCPBodyBlank         = "A mensagem está vazia."
	MCPBodyTooLong       = "A mensagem passa de %d caracteres."
	MCPFullContentOff    = "A leitura completa desta conversa está desligada. Use set_full_content para ligar. Todo mundo da conversa vai ver um aviso."
	MCPFullContentOnErr  = "Não deu para ligar a leitura completa. Ela continua desligada."
	MCPFullContentOffErr = "Não deu para desligar a leitura completa. Tente de novo."
	// bipe-chat/bipe#879: rooms made before the fix keep power levels where only admins set room state.
	MCPFullContentForbidden = "Esta conversa não deixa você ligar a leitura completa: ela foi criada antes dessa opção existir. Peça para quem administra a conversa permitir o aviso de assistente. A leitura completa continua desligada."
	MCPSendSwitchOff        = "O assistente não pode enviar mensagens. Para permitir, ligue \"Assistente pode enviar\" em Bipe > Configurações > Privacidade. Ou use draft_message: o rascunho aparece no celular e você mesmo envia."
	MCPSendFailed           = "Não deu para enviar a mensagem."
	MCPDraftFailed          = "Não deu para salvar o rascunho."
	MCPReadFailed           = "Não deu para ler as mensagens."
	MCPListFailed           = "Não deu para listar as conversas."
)
