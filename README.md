# bipe-beeper

Your Bipe chats in [Beeper](https://www.beeper.com), through a bridge that you run on your own computer.

Status: early. The bridge syncs the chats of a Bipe account into Beeper (bipe-chat/bipe#424). Sign-in with the phone is not built yet (bipe-chat/bipe#422). Until then, developers sign in with an access token (see "Development sign-in").

## How it works

- You run the bridge with Beeper Bridge Manager (`bbctl`) on a machine you control.
- The bridge signs in to Bipe as a new device. You approve it on your phone in Bipe > Configurações > Aparelhos conectados > Conectar um computador.
- Beeper servers carry your messages encrypted (end-to-bridge encryption). The plaintext is only on your machine. Beeper sees room names, members and timing.

Decision record: [ADR 0128](https://github.com/bipe-chat/bipe/blob/main/docs/adr/0128-beeper-bridge-per-user.md).

## Run it

You need Go 1.27, a C compiler (for SQLite) and a Beeper account.

```bash
brew install beeper/tap/bbctl
```

```bash
bbctl login
```

```bash
bbctl config --type bridgev2 sh-bipe -o config.yaml
```

Add the `network` section to `config.yaml`:

```yaml
network:
    homeserver_url: https://matrix.bipe.net.br
```

Build and start the bridge:

```bash
./build.sh && ./bipe-beeper -c config.yaml
```

To get the last 50 messages of each chat on the first sync, set these values in the `backfill` section of `config.yaml`:

```yaml
backfill:
    enabled: true
    max_initial_messages: 50
```

Then open a DM with `@sh-bipebot:beeper.local` in Beeper and send `login`.

## Development sign-in

Until the phone sign-in exists, a developer can sign in with an access token. Set `dev_token_login: true` in the `network` section, then send `login token` to the bridge bot and paste the token.

- The token must belong to a new device of the account. The bridge uploads its own encryption keys for that device. A token copied from another app's device does not work.
- The new device gets keys only for messages sent after it signs in. Older encrypted messages show as "Não deu para ler esta mensagem aqui. Abra o Bipe para ver." The phone sign-in (bipe-chat/bipe#422) brings the key backup.
- Do not use this on real accounts. The default is `false`.

## Events

Both sides are Matrix, so most events map one to one. The bridge keeps the remote event ID as the message ID.

"Both ways" means Bipe to Beeper and Beeper to Bipe. "One way" names the direction. This table is what the code does. The hand test between Beeper and a Bipe phone has not run yet.

| Event | Matrix type | Status |
|---|---|---|
| Text, notice, emote | `m.room.message` (`m.text`, `m.notice`, `m.emote`) | Both ways |
| Formatting | `formatted_body` (HTML) | Both ways |
| Mentions | `m.mentions`, `matrix.to` user links | Both ways. User IDs change to ghosts and back. |
| Replies | `m.in_reply_to` | Both ways |
| Threads | `m.thread` | Both ways |
| Edits | `m.replace` | Both ways |
| Deletes | `m.room.redaction` | Both ways |
| Reactions | `m.reaction` | Both ways. Removing a reaction works both ways. |
| Images, video, audio, files | `m.image`, `m.video`, `m.audio`, `m.file` | Both ways. The bridge downloads, decrypts, encrypts and uploads again. |
| Voice notes | `m.audio` with `org.matrix.msc3245.voice` | Both ways |
| Stickers | `m.sticker` | Both ways |
| Location | `m.location` | Both ways |
| Read receipts | `m.receipt` | Both ways |
| Typing | `m.typing` | Both ways |
| Room name | `m.room.name` | Both ways |
| Room topic | `m.room.topic` | Both ways |
| Room avatar | `m.room.avatar` | Both ways |
| Members join and leave | `m.room.member` | One way, Bipe to Beeper |
| User name and avatar | profile, `m.room.member` | One way, Bipe to Beeper (ghosts) |
| Poll start | `org.matrix.msc3381.poll.start` | Both ways |
| Poll votes | `org.matrix.msc3381.poll.response` | Both ways |
| Poll end | `org.matrix.msc3381.poll.end` | One way, Bipe to Beeper. bridgev2 does not send a poll end from Beeper to the bridge. |
| Own messages from another Bipe device | any | One way, Bipe to Beeper. They show as sent by you (double puppeting through bbctl). |
| Invites and message requests | `m.room.member` invite | One way, Bipe to Beeper, as a message request. Accept or delete it in Beeper. The bridge never joins for you. |
| Leaving a room on Bipe | `m.room.member` leave | One way, Bipe to Beeper. The chat goes away in Beeper. |
| History | `/messages` | One way, Bipe to Beeper. The last 50 messages on the first sync, more on scroll. |
| Messages without a key on the bridge device | `m.room.encrypted` | One way, as a notice |
| Bipe-only and unknown events | any other type or msgtype | One way, as "Mensagem do Bipe. Abra o app para ver." (bipe-chat/bipe#425 adds a fallback for each type) |
| Power levels, join rules | state | Not bridged |
| Calls | `m.call.*`, MatrixRTC | Not bridged |
| Verification | `m.key.verification.*` | Not bridged |
| Stable polls | `m.poll.*` | Not bridged, shown as the unknown event text |
| Delete a joined chat in Beeper | `com.beeper.delete_chat` | Beeper only. The bridge does not leave the Bipe room. |

## Build

```bash
./build.sh
```

Always build with `-tags goolm`. The green gate is `go build -tags goolm ./... && go vet -tags goolm ./...`.

## License

AGPL-3.0. See [LICENSE](LICENSE).
