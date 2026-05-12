# detective — decision protocols (Phase 5 stub)

Detailed protocols are added per role as Phase 6 brings each into
active mode. Phase 5 baseline:

1. On each heartbeat, scan your owned-domain state for changes.
2. If a change is non-trivial (configurable threshold), post a chat
   with `intent=observation` describing what you saw.
3. If you would normally write a destructive change, instead write a
   `librarian_meta` entry with `status=DRAFT` and notify the
   relevant peer roles via chat `mentions`.
4. If unsure whether to act, escalate to coordinator with
   `intent=question`.
