# yap

`yap` is a LAN-first peer-to-peer chat client written in Go. It uses a terminal UI for everyday use, libp2p for transport and discovery, and a room key shared through an explicit pairing flow.

## Install

<!-- BEGIN GENERATED INSTALL -->
Current release: `v0.1.3`

Linux (`amd64`):

```bash
VERSION=v0.1.3; mkdir -p ~/.local/bin && curl -L https://github.com/colin-hofer/yap/releases/download/${VERSION}/yap_${VERSION#v}_linux_amd64.tar.gz | tar -xz -C /tmp && install -m 755 /tmp/yap ~/.local/bin/yap
```

Linux (`arm64`):

```bash
VERSION=v0.1.3; mkdir -p ~/.local/bin && curl -L https://github.com/colin-hofer/yap/releases/download/${VERSION}/yap_${VERSION#v}_linux_arm64.tar.gz | tar -xz -C /tmp && install -m 755 /tmp/yap ~/.local/bin/yap
```

macOS (Apple Silicon):

```bash
VERSION=v0.1.3; mkdir -p ~/.local/bin && curl -L https://github.com/colin-hofer/yap/releases/download/${VERSION}/yap_${VERSION#v}_darwin_arm64.tar.gz | tar -xz -C /tmp && install -m 755 /tmp/yap ~/.local/bin/yap
```

macOS (Intel):

```bash
VERSION=v0.1.3; mkdir -p ~/.local/bin && curl -L https://github.com/colin-hofer/yap/releases/download/${VERSION}/yap_${VERSION#v}_darwin_amd64.tar.gz | tar -xz -C /tmp && install -m 755 /tmp/yap ~/.local/bin/yap
```

Windows (`PowerShell`, `amd64`):

```powershell
$version='v0.1.3'; $dir="$HOME\AppData\Local\yap"; New-Item -ItemType Directory -Force -Path $dir | Out-Null; Invoke-WebRequest "https://github.com/colin-hofer/yap/releases/download/$version/yap_$($version.TrimStart('v'))_windows_amd64.zip" -OutFile "$dir\yap.zip"; Expand-Archive -Path "$dir\yap.zip" -DestinationPath $dir -Force
```

Windows (`PowerShell`, `arm64`):

```powershell
$version='v0.1.3'; $dir="$HOME\AppData\Local\yap"; New-Item -ItemType Directory -Force -Path $dir | Out-Null; Invoke-WebRequest "https://github.com/colin-hofer/yap/releases/download/$version/yap_$($version.TrimStart('v'))_windows_arm64.zip" -OutFile "$dir\yap.zip"; Expand-Archive -Path "$dir\yap.zip" -DestinationPath $dir -Force
```

After install, make sure the install directory is on your `PATH`:

- Linux/macOS: `~/.local/bin`
- Windows: `%USERPROFILE%\AppData\Local\yap`

Once installed, `yap update` downloads the latest GitHub release for your current OS/arch and replaces the installed binary in place.
<!-- END GENERATED INSTALL -->

## Quick Start

1. Run `yap`.
2. Press `n` to create a swarm and enter a name.
3. Press `i` to generate an invite code.
4. On another machine, run `yap`, select the nearby peer, press `j`, and enter the code.
5. Approve pairing on both sides and start chatting.

## Goals

- Zero-config local discovery on the same network
- No central server or relay in v1
- Stable device identity with saved trust
- Shared room chat that tolerates peers joining, leaving, and reconnecting
- Background swarm connections so saved swarms can continue receiving messages while you view another swarm or return home
- A full-screen terminal UI that is usable without memorizing commands

## Architecture

The implementation follows a layered design:

- `internal/store`: persisted identity, swarm metadata, and local transcripts
- `internal/crypto`: invite codes, room key generation, fingerprints, and AES-GCM helpers
- `internal/p2p`: libp2p host setup, mDNS discovery, pairing streams, GossipSub topics, and presence
- `internal/app`: application state, persistence integration, auto-join behavior, and UI-facing events
- `internal/ui`: Bubble Tea interface for the home screen, pairing prompts, and the chat view

`docs/approach.md` contains the protocol and persistence details used by the rewrite.

## User Model

- A device has one persisted identity.
- A swarm is a saved room with a stable ID, room key, and trusted peers.
- Pairing is explicit and mutual:
  - the inviter creates an expiring invite code for a swarm
  - the invitee discovers the inviter on the LAN and enters the code
  - both sides confirm the other side's identity card before trust is stored

## Commands

```bash
yap
yap open <swarm-name-or-id>
yap join <invite-code>
yap update
yap version
```

## Home Screen Controls

- `tab`: switch focus between saved swarms and nearby peers
- `↑` / `↓`: move the selection in the focused list
- `enter`: open the selected swarm
- `n`: create a new swarm
- `i`: generate an invite for the selected swarm
- `j`: join a selected nearby peer with an invite code
- `d`: remove the selected swarm locally
- `u`: quit the TUI and install the latest GitHub release for the current OS/arch
- `q`: quit

## Chat Controls

- `enter`: send the current composer contents
- `shift+enter`: insert a newline
- `tab`: switch focus between the transcript and composer
- `y`: copy the focused transcript entry
- `d`: remove the current swarm locally
- `esc`: leave the current chat and return to the home screen
- `ctrl+c`: quit

## Persistence

On Linux, state defaults to `$XDG_STATE_HOME/yap` or `~/.local/state/yap`.

Files:

- `identity.json`: device identity and display name
- `swarms/<id>.json`: room metadata and trusted peers
- `transcripts/<id>.jsonl`: retained transcript, compacted to the most recent 1000 events and reconciled with trusted peers when they are online

All app-managed files are written with private permissions.

## Transport Note

The current build defaults to TCP libp2p listeners for stability on Go 1.26.x. QUIC can be enabled experimentally with:

```bash
YAP_TRANSPORT=quic yap
```

This is opt-in because the pinned libp2p dependency line currently pulls a `quic-go` version that can panic on Go 1.26 during handshake.
