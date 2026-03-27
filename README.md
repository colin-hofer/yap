# yap

`yap` is a LAN-first peer-to-peer chat client written in Go. This repo also contains the `yap-relay` daemon and the blue/green deploy tool for publishing that relay to a public host.

## Install

<!-- BEGIN GENERATED INSTALL -->
Current release: `v1.0.1`

Linux (`amd64`):

```bash
VERSION=v1.0.1; mkdir -p ~/.local/bin && curl -L https://github.com/colin-hofer/yap/releases/download/${VERSION}/yap_${VERSION#v}_linux_amd64.tar.gz | tar -xz -C /tmp && install -m 755 /tmp/yap ~/.local/bin/yap
```

Linux (`arm64`):

```bash
VERSION=v1.0.1; mkdir -p ~/.local/bin && curl -L https://github.com/colin-hofer/yap/releases/download/${VERSION}/yap_${VERSION#v}_linux_arm64.tar.gz | tar -xz -C /tmp && install -m 755 /tmp/yap ~/.local/bin/yap
```

macOS (Apple Silicon):

```bash
VERSION=v1.0.1; mkdir -p ~/.local/bin && curl -L https://github.com/colin-hofer/yap/releases/download/${VERSION}/yap_${VERSION#v}_darwin_arm64.tar.gz | tar -xz -C /tmp && install -m 755 /tmp/yap ~/.local/bin/yap
```

macOS (Intel):

```bash
VERSION=v1.0.1; mkdir -p ~/.local/bin && curl -L https://github.com/colin-hofer/yap/releases/download/${VERSION}/yap_${VERSION#v}_darwin_amd64.tar.gz | tar -xz -C /tmp && install -m 755 /tmp/yap ~/.local/bin/yap
```

Windows (`PowerShell`, `amd64`):

```powershell
$version='v1.0.1'; $dir="$HOME\AppData\Local\yap"; New-Item -ItemType Directory -Force -Path $dir | Out-Null; Invoke-WebRequest "https://github.com/colin-hofer/yap/releases/download/$version/yap_$($version.TrimStart('v'))_windows_amd64.zip" -OutFile "$dir\yap.zip"; Expand-Archive -Path "$dir\yap.zip" -DestinationPath $dir -Force
```

Windows (`PowerShell`, `arm64`):

```powershell
$version='v1.0.1'; $dir="$HOME\AppData\Local\yap"; New-Item -ItemType Directory -Force -Path $dir | Out-Null; Invoke-WebRequest "https://github.com/colin-hofer/yap/releases/download/$version/yap_$($version.TrimStart('v'))_windows_arm64.zip" -OutFile "$dir\yap.zip"; Expand-Archive -Path "$dir\yap.zip" -DestinationPath $dir -Force
```

After install, make sure the install directory is on your `PATH`:

- Linux/macOS: `~/.local/bin`
- Windows: `%USERPROFILE%\AppData\Local\yap`

Once installed, `yap update` downloads the latest GitHub release for your current OS/arch and replaces the installed binary in place.
<!-- END GENERATED INSTALL -->

## Quick Start

1. Run `yap`.
2. Press `n` to create a swarm and enter a name.
3. With the swarms list focused, press `i` to generate an invite code for the selected swarm.
4. On another machine, run `yap`, select the nearby peer, press `j`, and enter the code.
5. Approve pairing on both sides and start chatting.

## WAN Relay

`yap` can now pair and chat over a static libp2p relay.

- Run the relay server with `go build ./cmd/yap-relay` and start the binary with:
  - `SERVER_ADDR` for the local listener, for example `127.0.0.1:18081`
  - `HEALTH_ADDR` for the local HTTP health server, for example `127.0.0.1:19081`
  - `YAP_RELAY_PUBLIC_ADDR` for the public proxy address advertised to clients, for example `/dns4/relay.example.com/tcp/4001`
- `yap` now defaults to this repo's deployed relay at `/dns4/colinhofer.com/tcp/4001`.
- `YAP_RELAY_ADDR` is optional and can override the default relay.
  - It accepts either the full public relay multiaddr including the relay peer ID, for example `/dns4/relay.example.com/tcp/4001/p2p/<relay-peer-id>`.
  - Or just the bare public relay multiaddr, for example `/dns4/relay.example.com/tcp/4001`; `yap` will discover the relay peer ID at runtime.
- Invites now copy a share token like `Y1-<inviter-peer-id>-<code>`.
  - By default, `yap join <invite>` dials the inviter through the deployed relay.
  - Without a relay, the same token still works on LAN if you select the inviter in the nearby list and paste the token.

## Monorepo Tooling

This repo now contains everything needed for the client, the relay, and relay deployment:

- `./cmd/yap`: terminal chat client
- `./cmd/yap-relay`: public libp2p relay daemon
- `./cmd/deploy`: relay blue/green deploy command
- `./deploy/relay.env`: deploy config for the relay host

Build commands:

```bash
go build ./cmd/yap
go build ./cmd/yap-relay
go build ./cmd/deploy
```

Deploy the relay from the repo root:

```bash
go run ./cmd/deploy
```

The deploy command reads `deploy/relay.env` by default, builds `./cmd/yap-relay`, uploads it to the configured host, starts the inactive slot, validates `/healthz`, switches HAProxy on the public port, and then stops the previous slot.

After the first deploy, read the active unit logs on the server to get the relay peer ID and final client dial address:

```bash
ssh root@104.236.76.237 'slot=$(cat /var/lib/yap-relay/active_slot); journalctl -u yap-relay-$slot.service -n 100 --no-pager'
```

If you need to override the built-in relay, set client machines to the full relay address:

```bash
export YAP_RELAY_ADDR='/dns4/colinhofer.com/tcp/4001/p2p/<relay-peer-id>'
```

## Architecture

The implementation follows a layered design:

- `internal/store`: persisted identity, swarm metadata, and local transcripts
- `internal/crypto`: invite codes, room key generation, fingerprints, and AES-GCM helpers
- `internal/p2p`: libp2p host setup, mDNS discovery, pairing streams, room-scoped GossipSub channels, and presence
- `internal/app`: application state, persistence integration, auto-join behavior, and UI-facing events
- `internal/ui`: Bubble Tea interface for the home screen, pairing prompts, and the chat view

## User Model

- A device has one persisted identity.
- A swarm is a saved room with a stable ID, room key, owner, config version, and trusted peers.
- Pairing is explicit and mutual:
  - the room owner creates an expiring invite code for a swarm
  - the invitee discovers the inviter on the LAN and enters the code
  - both sides confirm the other side's identity card before trust is stored
- The room owner is also the only peer allowed to rotate the room key, revoke a member, or admit new members in a way the rest of the room will accept.

## Commands

```bash
yap
yap --debug
yap open <swarm-name-or-id>
yap --debug join <invite>
yap join <invite>
yap-relay
yap update
yap version
```

## Debug Logging

Run the client with `--debug` to write structured logs to `debug.log` in the same state directory that stores your identity, swarms, and transcripts.

Examples:

```bash
yap --debug
yap --debug join '<invite>'
```

On Linux the default path is usually:

```bash
~/.local/state/yap/debug.log
```

## Home Screen Controls

- `tab`: switch focus between saved swarms and nearby peers
- `↑` / `↓`: move the selection in the focused list
- `enter`: open the selected swarm when the swarms list is focused
- `n`: create a new swarm
- `r`: rename this device/user
- `i`: generate an invite for the selected swarm when the swarms list is focused
- `R`: rotate the room key for the selected swarm when the swarms list is focused
- `j`: join the selected nearby peer with an invite code when the nearby list is focused
- `v`: inspect the selected swarm or nearby peer
- `d`: remove the selected swarm locally when the swarms list is focused
- `u`: quit the TUI and install the latest GitHub release for the current OS/arch
- `q`: quit

## Chat Controls

- `enter`: send the current composer contents
- `shift+enter`: insert a newline when the terminal supports key disambiguation
- `tab`: complete an `@mention` while typing
- `x`: revoke the selected peer when the peers sidebar is focused
- `R`: rotate the room key when the swarms sidebar is focused
- `v`: inspect the current or highlighted swarm
- `pgup` / `pgdn`: scroll the transcript
- `esc`: leave the current chat and return to the home screen
- `ctrl+c`: quit

The chat view keeps focus in the composer. The transcript is read-only and can be scrolled with the keyboard or mouse.

## Persistence

On Linux, state defaults to `$XDG_STATE_HOME/yap` or `~/.local/state/yap`.

Files:

- `identity.json`: device identity and display name
- `swarms/<id>.json`: room metadata and trusted peers
- `transcripts/<id>.jsonl`: retained transcript journal, reconciled with trusted peers when they are online, and compacted back to the most recent 1000 events

All app-managed files are written with private permissions.

## Transport Note

Internally, each swarm maps to a libp2p GossipSub channel named like `yap/swarm/<room-topic-hash>/v3`, where the topic hash is derived from the shared room key. That channel is just the pubsub transport used to move encrypted chat and presence messages between trusted peers in the same swarm.

Membership changes and room-key rotations are not broadcast on the old room topic. Instead, the room owner pushes the new swarm config directly to the remaining trusted peers over an authenticated libp2p stream, and those peers reopen the swarm on the new topic derived from the rotated room key. When a peer is revoked, that removed peer gets a separate revocation notice so the old room is closed locally and shown as revoked instead of silently going dead.

The current build defaults to TCP libp2p listeners for stability on Go 1.26.x. QUIC can be enabled experimentally with:

```bash
YAP_TRANSPORT=quic yap
```

This is opt-in because the pinned libp2p dependency line currently pulls a `quic-go` version that can panic on Go 1.26 during handshake.
