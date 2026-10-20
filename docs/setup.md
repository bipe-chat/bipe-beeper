# Set up the Bipe bridge for Beeper

This guide puts the chats of your Bipe account in Beeper. You run the bridge on your own computer. Bipe does not run it for you, and Bipe staff cannot see it.

Read "What leaves Brazil" before you start. It explains what Beeper can see when the bridge runs.

## What you need

- A Beeper account.
- A computer that stays on: a Mac or a Linux machine. The bridge only works while this computer is on and online.
- The Bipe app on your phone, version 26.10.2 build 9 or later (iPhone or Android). Older builds do not have Configurações > Aparelhos conectados > Conectar um computador, and you need that screen to sign in.
- One of these, to build the bridge:
  - Go 1.27 and a C compiler. On a Mac, Xcode or the Xcode Command Line Tools give you the compiler. SQLite needs it.
  - Docker.

The bridge has no release binaries yet. You build it from this repository.

## 1. Install bbctl

`bbctl` is Beeper Bridge Manager. It registers the bridge with your Beeper account and writes its config.

The simplest path is the release binary from https://github.com/beeper/bridge-manager/releases. The files are `bbctl-macos-arm64` (Mac with Apple silicon), `bbctl-macos-amd64` (Intel Mac), `bbctl-linux-amd64` and `bbctl-linux-arm64`. On a Mac with Apple silicon:

```bash
curl -L -o bbctl https://github.com/beeper/bridge-manager/releases/latest/download/bbctl-macos-arm64
```

```bash
chmod +x bbctl && sudo mv bbctl /usr/local/bin/bbctl
```

If you have Go, this also works:

```bash
go install github.com/beeper/bridge-manager/cmd/bbctl@latest
```

Go puts the binary in `~/go/bin`. That folder is often not on your `PATH`. Add it, or call `~/go/bin/bbctl`.

On macOS 27, `brew install beeper/tap/bbctl` (bbctl 0.14.0) fails with "Your Command Line Tools (CLT) does not support macOS 27". Use one of the two paths above.

## 2. Log in to Beeper

```bash
bbctl login
```

If Beeper Desktop is signed in on this computer, `bbctl` offers to use that login. Then you do not need an email code.

## 3. Get the code and write the config

```bash
git clone https://github.com/bipe-chat/bipe-beeper.git
```

```bash
cd bipe-beeper
```

```bash
bbctl config --type bridgev2 -o config.yaml sh-bipe
```

The flags go before the bridge name. With `sh-bipe` first, `bbctl` stops with "Too many arguments specified".

Open `config.yaml` and set the `network` section:

```yaml
network:
    homeserver_url: https://matrix.bipe.net.br
```

The generated config already turns on history (`backfill.enabled: true`, `max_initial_messages: 50`). You do not need to change it.

## 4. Build and start the bridge

With Go:

```bash
./build.sh
```

```bash
./bipe-beeper -c config.yaml
```

With Docker, build the image once:

```bash
docker build -t bipe-beeper .
```

Then start it with the folder that holds `config.yaml` as `/data`:

```bash
docker run -d --name bipe-beeper --restart unless-stopped -v "$PWD":/data bipe-beeper
```

The bridge keeps its database and encryption keys in the file named in `database.uri` in `config.yaml`, in the same folder. Keep that folder. If you lose it, you have to sign in again.

## 5. Sign in with your phone

1. In Beeper, open a chat with `@sh-bipebot:beeper.local` and send `login`.
2. The bridge shows a QR code. On the phone, open Bipe > Configurações > Aparelhos conectados > Conectar um computador and scan it.
3. The phone shows a number with 2 digits. Type it in the chat with the bridge.
4. If the phone opens a page with a code, type the code the bridge shows and confirm.
5. The phone approves the new device. It shows as "Beeper" in Aparelhos conectados.

Each step after the scan must finish in about one minute. If the time runs out, the bridge says so. Send `login` again.

If the bridge says the device is not verified, open Bipe on the phone and verify the "Beeper" device. The bridge shows emojis. If they match the phone, send the confirm command that the bridge names. If they do not match, send the reject command.

Your chats then show in Beeper. The last 50 messages of each chat come in on the first sync, and older messages load when you scroll up.

## What leaves Brazil

Bipe keeps your data on servers in Brazil. With the bridge on, a copy of your chats also goes through Beeper. ADR 0128 records this decision: https://github.com/bipe-chat/bipe/blob/main/docs/adr/0128-beeper-bridge-per-user.md

- The bridge on your computer decrypts your Bipe messages and encrypts them again for Beeper. Beeper servers store and carry the message content only as ciphertext.
- Beeper can read room state in clear: chat names, who is in each chat, and when messages are sent.
- The readable text of your messages exists only on your computer, in the bridge, and in your Beeper apps.
- "Your data stays in Brazil" does not cover an account that you bridge to Beeper.

The people you talk to do not see that you use Beeper. Their messages reach Beeper through your bridge.

## Turn it off

1. On the phone, open Bipe > Configurações > Aparelhos conectados and disconnect "Beeper". This signs the bridge out of your Bipe account. The bridge cannot read new messages after this.
2. Stop the bridge: press Ctrl+C, or `docker rm -f bipe-beeper`, or stop the service (see below).
3. Delete the bridge from Beeper:

```bash
bbctl delete sh-bipe
```

This removes the bridge registration and the bridged chats from your Beeper account. Your chats stay in Bipe.

4. Delete the bridge folder on your computer. It holds the database and the encryption keys of the bridge device.

## Run it as a service

A service starts the bridge again after a reboot. The examples assume the repository is in `~/bipe-beeper` and you built it with `./build.sh`. Change the paths to match your computer.

### macOS (launchd)

Save this as `~/Library/LaunchAgents/chat.bipe.beeper.plist`. Replace `YOU` with your user name.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>chat.bipe.beeper</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/YOU/bipe-beeper/bipe-beeper</string>
        <string>-c</string>
        <string>config.yaml</string>
    </array>
    <key>WorkingDirectory</key>
    <string>/Users/YOU/bipe-beeper</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/Users/YOU/bipe-beeper/bridge.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/YOU/bipe-beeper/bridge.log</string>
</dict>
</plist>
```

```bash
launchctl load ~/Library/LaunchAgents/chat.bipe.beeper.plist
```

To stop it:

```bash
launchctl unload ~/Library/LaunchAgents/chat.bipe.beeper.plist
```

### Linux (systemd)

Save this as `~/.config/systemd/user/bipe-beeper.service`:

```ini
[Unit]
Description=Bipe bridge for Beeper
After=network-online.target

[Service]
WorkingDirectory=%h/bipe-beeper
ExecStart=%h/bipe-beeper/bipe-beeper -c config.yaml
Restart=on-failure

[Install]
WantedBy=default.target
```

```bash
systemctl --user enable --now bipe-beeper
```

For the service to start before you log in, enable lingering for your user once:

```bash
loginctl enable-linger "$USER"
```

With Docker, `--restart unless-stopped` in step 4 already restarts the container after a reboot, as long as Docker starts at boot.

## Update

1. Stop the bridge.
2. Get the new code and build again:

```bash
git pull
```

```bash
./build.sh
```

With Docker, run `docker build -t bipe-beeper .` again, then remove and start the container as in step 4.

3. Start the bridge.

Keep `config.yaml` and the database file. The session and the encryption keys are in the database, so you do not sign in again after an update. Do not run `bbctl config` again unless the release notes say so: it writes a new config with new registration tokens.

## Troubleshooting

**The bridge says "A sessão do Bipe acabou. Entre de novo."** The phone disconnected the "Beeper" device, or the session expired. Send `login` to the bridge and scan a new code.

**Messages show "Não deu para ler esta mensagem aqui. Abra o Bipe para ver."** The bridge device has no key for that message. Check that the device is verified (step 5). Messages from before the sign-in come from the key backup, so verify the device and wait for the next sync.

**The phone does not accept the code.**
- Update Bipe on the phone. Version 26.10.2 build 9 is the minimum.
- Scan within a minute of `login`. Send `login` again to get a new code.
- If the 2-digit number is wrong, the bridge stops. Start again and type the number exactly as the phone shows it.
- If the bridge says "Este servidor não aceita entrar por código QR.", check `homeserver_url` in `config.yaml`.

**Nothing shows in Beeper.** Check that the bridge process runs and that its log has no errors. In Beeper, open the chat with `@sh-bipebot:beeper.local` and send `help`.
