# Rewrite Approach

## Network Stack

The rewrite replaces the earlier custom UDP gossip design with libp2p:

- peer identity: persisted Ed25519 keypair
- local discovery: mDNS service name `yap-v1`
- transport: libp2p host with default secure channels; current runtime defaults to TCP listeners, with QUIC kept as an experimental opt-in
- group messaging: GossipSub topic per swarm

This reduces custom network code while keeping the app peer-to-peer and tolerant of churn.

## Security Model

Transport encryption alone is not sufficient because room membership is an application concern. `yap` adds a second layer:

- each swarm owns a 32-byte room key
- chat and presence payloads are AES-256-GCM encrypted before publication
- invite pairing is required before a room key is shared

Pairing is intentionally manual on both sides so users can compare the presented device card before trust is persisted.

## Pairing Protocol

Pairing runs over `/yap/pair/1`.

1. Invitee discovers the inviter on the LAN and opens a pair stream.
2. Invitee sends the invite code plus its identity card.
3. Inviter validates the code and asks the local user to approve the requester.
4. If approved, the inviter sends its own identity card and swarm name.
5. Invitee asks the local user to approve the responder.
6. If approved, the inviter sends the swarm bundle: swarm ID, swarm name, room key, and trusted seed peers.
7. Both sides store trust locally.

Invite codes are short base32 tokens with expiry and bounded use.

## Presence

Presence is soft-state and room-scoped:

- `join` is published when a room opens
- `heartbeat` is published every 15 seconds
- `leave` is published on clean exit
- peers move from `online` to `stale` to `offline` based on elapsed heartbeat time

This keeps the UI responsive even when peers disappear abruptly.

## UI Shape

The interface keeps the same home/chat views, but saved swarms stay connected in
the background while the app is running:

- Home: saved swarms, nearby peers, unread activity, pending invites, and pairing prompts
- Chat: selected swarm timeline, peer sidebar, status line, and multiline composer

The UI only talks to the `internal/app` service. It never mutates persistence or libp2p state directly.

## Persistence Rules

- identity is global to the device
- swarms are saved independently and can be reopened later
- transcripts are retained locally and reconciled with trusted peers when they are online
- metadata writes are atomic
- transcript compaction keeps the last 1000 events
