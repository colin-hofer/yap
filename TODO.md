# TODO

- [x] Restructure chat session code (currently in `chat/chat.go`) into focused packages for networking, command handling, and state.
- [ ] Unify config UX (init command and runtime commands) with clearer feedback for partial failures.
- [ ] Enhance peer management with retries, last-seen tracking, and pruning of unreachable peers.
- [ ] Make the listener resilient to transient errors by restarting or backing off instead of exiting.
- [ ] Add automated tests covering configuration merging, peer management, encryption paths, and packet handling.
- [x] Support the usage pattern `yap with <name>` as a shortcut for selecting a config profile.
