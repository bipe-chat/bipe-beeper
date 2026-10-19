# bipe-beeper

Your Bipe chats in [Beeper](https://www.beeper.com), through a bridge that you run on your own computer.

Status: early. The bridge starts and registers with Beeper. Sign-in and chats are not built yet (bipe-chat/bipe#240).

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

Then open a DM with `@sh-bipebot:beeper.local` in Beeper and send `login`.

## Build

```bash
./build.sh
```

Always build with `-tags goolm`. The green gate is `go build -tags goolm ./... && go vet -tags goolm ./...`.

## License

AGPL-3.0. See [LICENSE](LICENSE).
