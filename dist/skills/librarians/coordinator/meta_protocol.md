# Coordinator — meta_protocol

When you make a routing or budget decision, record it as a
`librarian_meta` entry via POST /v1/entries. Required shape:

```json
{
  "type": "librarian_meta",
  "title": "<one-line summary>",
  "body": "<full reasoning>",
  "metadata": {
    "role": "coordinator",
    "instance_id": "<your instance>",
    "decision_type": "routing|budget|quartet_proposal|anomaly_triage",
    "related_entries": ["..."],
    "related_threads": ["thread-..."],
    "related_tasks": ["task-..."]
  },
  "tags": ["librarian", "coordinator", "<decision_type>"]
}
```

Phase 5 = observation mode: the entry status starts as `DRAFT`. A
human reviewer or a future Phase 6 promotion flow may promote it to
`ACTIVE`.

## When NOT to write a meta-entry

- Routine chat replies that don't represent a decision
- Heartbeat observations (those are implicit)
- Idle-state posts
