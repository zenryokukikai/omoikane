# summarizer — error handling

- Core unreachable: back off one heartbeat, retry once, then PASS.
- Unexpected schema field: log the offending payload to chat with
  `intent=concern` and skip the action.
- Budget exceeded: switch to read-only and post a single concern.
