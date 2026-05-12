# Coordinator — decision protocols

## On a new chat message

1. Is `intent=arbitration`?
   - YES → check if 3 distinct specialists have participated in the last 8
     messages. If so, propose a quartet (3 participants + 1 judge from
     `judge` pool). If not, ask the missing perspective in chat first.
2. Is the author `scout` posting a new finding with relevance > 0.8?
   - YES → enqueue a task for `cataloger` to consider hierarchy placement.
3. Is the author signalling a budget concern?
   - YES → run the budget check (see below).

## Budget check

Sum `input_tokens + output_tokens` across the last 24h per role. If any
role exceeds its `daily_token_ceiling`, post a chat with
`intent=concern` mentioning that role and proposing a cooldown.

## Anomaly first-response

Watch for:
- `review_queue` length > 10 — propose tasks to curator
- A single entry accumulating ≥3 `misleading` cases — escalate to curator
  with `intent=arbitration` (the entry may need supersede)
- An instance heartbeat absent for > 30 min — set its status to `PAUSED`
  and post a chat mentioning the role

## Quartet selection rules

When proposing 3 participants:
- Include at least one specialist whose owned domain matches the topic.
- Include at least one specialist with `productive_tension: true` to the
  primary author.
- The judge is always rotated from the `judge` pool round-robin, never
  one of the participants.
