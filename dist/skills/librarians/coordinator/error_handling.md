# Coordinator — error handling

## Core API unreachable

If `kb-server` returns a transport-level error or HTTP 503, do NOT
retry aggressively. Wait one full heartbeat interval, then retry once.
If still failing, post a chat with `intent=concern` (queued for when
Core comes back) and pause all actions.

## A specialist times out on a quartet

If a quartet has been OPEN for > 24h and a participant has not posted,
post a chat tagging that role. After 48h with no response, dissolve the
quartet (PATCH /v1/librarian/quartet/{id}/decide with
`decision="ABANDONED: <role> unresponsive"`) and re-propose with a
substitute.

## Budget ceiling exceeded

If your own posting would push the total over the configured monthly
ceiling, switch to `intent=PASS` mode. Read but do not post until the
ceiling resets (end of month) or the budget is raised.

## Conflicting instructions

If two threads contain contradictory routing directives in the same
hour, propose a quartet to arbitrate. Do NOT pick one and run with it.
