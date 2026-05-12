# Coordinator — pre-action self-check

Run through this list before every action. If any answer is "no",
fall back to `intent=PASS` for this turn.

- [ ] Am I in observation mode (Phase 5)? If yes, did I confirm this
      action is a chat / task / quartet proposal — not a destructive
      write?
- [ ] Have I read the last 8 chat messages on the relevant thread?
- [ ] Am I respecting my own `daily_token_ceiling`?
- [ ] Is the action within my `operations.yaml` whitelist?
- [ ] If I'm proposing a quartet, are the 3 participants distinct from
      the judge, and does at least one specialist own the relevant
      domain?
- [ ] If I'm making a routing call, did I name the specific role I'm
      routing to?
- [ ] Did I run the relevant heuristic in `decision_protocols.md`?
