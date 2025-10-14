# TODO

- [x] Refactor membership architecture to use a single source of truth
  - [x] Remove old `PeerManager` usage and pending maps from the chat session
  - [x] Store resolved peer addresses in a simple map keyed by canonical address strings
  - [x] Ensure the session uses the membership manager for all join/leave/peer state
- [x] Simplify address normalization and self-contact checks
  - [x] Centralize canonicalization logic inside the membership manager using `net/netip`
  - [x] Guard `contactPeer` and forwarding paths against local or duplicate addresses
- [x] Update command handlers and UI helpers
  - [x] `/peers`, `/group`, and `/switch` rely solely on membership snapshots
  - [x] Format membership summaries without leftover peer-manager assumptions
- [x] Clean up transport/session boundaries
  - [x] Only send JOIN/PEERS via structured messages from the membership manager
  - [x] Rebuild session start/forwarding logic using the streamlined structures
- [x] Run `gofmt`, rebuild, and smoke-test `/peers` to confirm accurate counts without duplicates or pending self
