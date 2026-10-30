# bipe-mcp: your Bipe chats in your own AI assistant

Status (30 October 2026): first version, bipe-chat/bipe#439. The unit tests pass. The test against a real Bipe homeserver and phone has not run yet.

`bipe-mcp` is an MCP server that runs on your computer. An MCP client (Claude Desktop, Cursor, Claude Code or another) starts it and talks to it over stdin and stdout. `bipe-mcp` opens no network port. Bipe does not run it for you.

`bipe-mcp` signs in to your Bipe account as a new device, like the Beeper bridge. It decrypts and encrypts messages on your computer.

Decisions: [ADR 0130](https://github.com/bipe-chat/bipe/blob/main/docs/adr/0130-mcp-local-by-default.md) (local by default), [ADR 0210](https://github.com/bipe-chat/bipe/blob/main/docs/adr/0210-ai-open-to-any-assistant-drafts-not-sends.md) (drafts by default, sends behind a switch), [ADR 0128](https://github.com/bipe-chat/bipe/blob/main/docs/adr/0128-beeper-bridge-per-user.md) (sign-in with the phone). Tool list: bipe-shared `registries/mcp-tools.json`. Rules: bipe-shared `docs/mcp.md` and `docs/drafts-room.md`.

## What you need

- A Mac or a Linux computer. Windows builds, but it has no lock against two copies (see "Limits").
- Go 1.27 and a C compiler. SQLite needs the compiler. On a Mac, the Xcode Command Line Tools give it.
- The Bipe app on your phone, with Configurações > Aparelhos conectados > Conectar um computador.

## 1. Install

```bash
git clone https://github.com/bipe-chat/bipe-beeper.git && cd bipe-beeper
```

```bash
go build -tags goolm -o bipe-mcp ./cmd/bipe-mcp && sudo mv bipe-mcp /usr/local/bin/
```

## 2. Sign in

Run this in a terminal. Make the terminal large enough for the QR code.

```bash
bipe-mcp login
```

1. On the phone, open Bipe > Configurações > Aparelhos conectados > Conectar um computador and scan the code.
2. Type the 2-digit number that the phone shows.
3. On the phone, type the code that the terminal shows, and confirm.

Do the three steps in less than one minute. The server keeps the QR session for 60 seconds.

The phone then sends the encryption keys. `bipe-mcp` verifies itself and downloads the room keys from the key backup, so it can read older messages. If the phone sends no keys, `bipe-mcp` reads only messages that arrive after the sign-in.

`bipe-mcp` keeps its files in your user config dir, in the folder `bipe-mcp` (macOS: `~/Library/Application Support/bipe-mcp`, Linux: `~/.config/bipe-mcp`). The folder has mode 0700:

- `session.json` (mode 0600): the tokens of the device and the key of the crypto store.
- `crypto.db`: the encryption keys of the device and the room keys.
- `lock`: stops a second `bipe-mcp` with the same account.

Use `--config DIR` (or `BIPE_MCP_CONFIG`) for another folder, and `--homeserver URL` for another homeserver.

The phone shows the device as "bipe-mcp" in Aparelhos conectados. To sign out, run `bipe-mcp logout`, or remove the device on the phone.

## 3. Choose the scopes

A scope gives the MCP client a set of tools. The client sees only the tools of the scopes that you give. A call to another tool fails.

| Scope | Tools | What the client gets |
|---|---|---|
| `chats:read` | `list_chats`, `set_full_content` | Chat names, unread counts, last activity. No text. |
| `content-own` | `read_my_messages`, `draft_message` | The text of your own messages. Drafts. |
| `content-full` | `read_chat` | Every message of a chat, after you turn on full reading for that chat. |
| `content-send` | `send_message` | Sends as you, while the switch on the phone is on. |

The default is `chats:read,content-own`. Give the scopes with `--scopes` or `BIPE_MCP_SCOPES`:

```bash
bipe-mcp --scopes chats:read,content-own,content-full
```

When a content scope is on, `bipe-mcp` writes one line to stderr at start: the text of your chats goes to the model of your MCP client, which can be outside Brazil.

## The tools

- `list_chats`: your chats, with names, unread counts and the time of the last event. No message text. The drafts room is not in the list.
- `read_my_messages`: the messages that you sent in one chat. The server filters by sender, and `bipe-mcp` filters again. It never returns a message of another member.
- `set_full_content`: turns full reading of one chat on or off. On writes the room state event `chat.bipe.assistant` with your user ID as state key and `{"full_content": true}`. Every member's Bipe app then shows "Um assistente de @você lê esta conversa". Off writes `{}`, and the notice goes away. If the server refuses the write, full reading stays off.
- `read_chat`: every message of one chat. It works only while your `chat.bipe.assistant` event in that chat says `full_content: true`. `bipe-mcp` reads the event from the server before every call. If it cannot read the event, it refuses.
- `draft_message`: puts a draft in your drafts room. The Bipe app shows it in the target chat. Nothing is sent until you tap send. See "Drafts".
- `send_message`: sends a message as you. See "The send switch".

`read_my_messages` and `read_chat` return at most 200 messages (default 50), oldest first. A message that `bipe-mcp` has no key for has `"undecryptable": true` and no text.

## Drafts

`draft_message` follows bipe-shared `docs/drafts-room.md`:

1. It reads the global account data `chat.bipe.drafts_room`. When it names a room that you are in, `bipe-mcp` uses that room.
2. Otherwise it creates the room: encrypted, invite only, no invites, history visible from join, name "Rascunhos", creation type `chat.bipe.drafts`. Then it writes `chat.bipe.drafts_room`. If it cannot read the account data, it stops: it never makes a second room.
3. It sends an encrypted `m.room.message` with `msgtype` `m.text`, the text in `body`, and `chat.bipe.draft` `{room_id, reply_to?, thread_root?, client}`. `client` is the name that the MCP client gives in `initialize` (`clientInfo.name`), trimmed, at most 64 characters. A client without a name shows as "Assistente".

The target chat must be a chat that you are in. The text must not be blank and has at most 8000 characters.

## The send switch

`send_message` has two locks (ADR 0210):

1. The scope `content-send`, which you give with `--scopes`.
2. The switch "Assistente pode enviar" in Bipe > Configurações > Privacidade on the phone. It is off by default. The phone writes the global account data `chat.bipe.assistant_send`.

`bipe-mcp` reads the switch from the server before every send. It sends only when the account data is exactly `{"enabled": true}`. Anything else, or a read error, is off. When the switch is off, the tool answers in Portuguese: turn on "Assistente pode enviar", or use `draft_message`. If you turn the switch off on the phone, the next send stops.

Every sent message carries `"chat.bipe.assistant": {"client": "<MCP client name>"}`. Every member's Bipe app shows "Enviada pelo assistente de <nome>" under it.

## What leaves your computer

- To the Bipe homeserver: the same as any Bipe device. Messages and drafts go encrypted. The server sees that you have a drafts room, the `chat.bipe.assistant` state event, and the account data.
- To your MCP client: what the tools return. With a content scope, that is message text. The client sends it to its model. That model can run outside Brazil. Bipe does not control it.
- `bipe-mcp` sends nothing to Bipe or anyone else about how you use it. It writes logs only to stderr, and the logs hold no tokens, keys or message text.

## Connect an MCP client

The client starts `bipe-mcp` itself. Sign in first (step 2): a `bipe-mcp` that the client starts cannot show the QR code.

Claude Desktop (`claude_desktop_config.json`), Cursor (`.cursor/mcp.json`) and other clients that use the same format:

```json
{
  "mcpServers": {
    "bipe": {
      "command": "/usr/local/bin/bipe-mcp",
      "args": ["--scopes", "chats:read,content-own"]
    }
  }
}
```

Claude Code:

```bash
claude mcp add bipe -- /usr/local/bin/bipe-mcp --scopes chats:read,content-own
```

Only one client at a time can use one account folder. For a second client at the same time, sign in again with another `--config` folder. That makes a second device.

## Limits

- The rules live in honest clients. A modified client can leave out the assistant marker or ignore full reading (ADR 0130, ADR 0210).
- `bipe-mcp` is an unattested device (ADR 0043).
- Notes are not in `read_my_messages` yet.
- `set_full_content` needs the right to send the state event `chat.bipe.assistant` in that chat. Where the power levels do not allow it, the write fails and full reading stays off.
- On Windows there is no lock against a second `bipe-mcp` on the same folder. Two copies refresh the same token, and the server ends the session.
- New room keys of `bipe-mcp` go to your other devices by key sharing. `bipe-mcp` does not upload them to the key backup yet.

## Sources

- bipe-chat/bipe#439, the issue.
- bipe-shared `registries/mcp-tools.json`, `registries/drafts.json`, `docs/mcp.md`, `docs/drafts-room.md`, `test-vectors/assistant-send.json`, `test-vectors/drafts.json`.
- MCP specification 2025-06-18, https://modelcontextprotocol.io/specification/2025-06-18.
- MCP Go SDK, https://github.com/modelcontextprotocol/go-sdk (v1.8.0).
- MSC4108, https://github.com/matrix-org/matrix-spec-proposals/pull/4108.
