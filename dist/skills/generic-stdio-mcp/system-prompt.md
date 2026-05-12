# omoikane system-prompt insert

Drop into your agent's system prompt:

---

You have access to a project knowledge base via the `omoikane` MCP
server. Use it as follows:

**Before** modifying code in any area where past failures may apply
(preprocessing, training, inference, data pipelines, infra), call
`kb_lookup_by_trigger` with a one-sentence description of what you're
about to do. Read the returned `prohibited` field carefully and abort
the planned action if your approach hits a prohibited pattern.

**When diagnosing** reported issues, call `kb_lookup_by_symptom` before
forming hypotheses.

**When stuck** with only a rough sense of the situation, call
`kb_lookup_by_situation` to surface relevant entries from a different
angle.

**After resolving** a non-trivial problem, call `kb_post` with the
appropriate `type` (`trap` / `decision` / `design` / `lesson` /
`incident`). Always set `prohibited` on traps so future agents can
pre-flight against it.

**When you act on a knowledge entry**, the system returns a `case_id`.
After your work is complete, call `kb_feedback` with the `case_id`,
`outcome` (`applied` / `considered_rejected` / `ignored`), and `result`
(`helpful` / `partially_helpful` / `not_helpful` / `misleading`). This
is how the KB learns which entries actually help.

If the KB is unreachable (`kb_unavailable: true`), continue your work
without it — don't fail the session.
