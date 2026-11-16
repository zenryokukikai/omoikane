# omoikane-gate operator runbook

`omoikane-gate` carries personal-librarian `/talk` traffic between
omoikane and an opencrab core over the core's external gate Unix socket.
It speaks **protocol 3** (opencrab `docs/design/external-gate.md`,
"D-1099 改訂": the core owns sessions only). Verified end-to-end on
2026-11-16 against opencrab `stable-2026-11-15` (d2f7aad) with copies
of the production databases: an existing `/talk` thread continued its
conversation, a new thread got a reply, and a delegated `kb-ask`
research turn completed.

## What omoikane-gate is

One process. It holds one connection **per ACTIVE personal librarian**
(one gate instance each) to the core socket, and translates:

- human `chat.message` (SSE `GET /v1/events`) on a gateway-routed
  thread → `said` (`session` = the thread id, `origin` = the message
  id, `caller.role` = `owner` when the thread owner speaks)
- `say` from the core → `POST /v1/librarian/chat` (assistant reply);
  the success answer on the wire is a plain `ok`
- `activity` `started` / `ended` → `chat.status` broadcasts
  (`POST /v1/events/broadcast`); `read` is not displayed
- on every (re)connect: for each gateway-routed thread of the instance
  (`GET /v1/gateway/instances/{id}/threads`), replay human messages
  after the thread's cursor; the cursor advances after each confirmed
  send (`PUT /v1/gateway/threads/{id}/cursor`). The core answers a
  re-sent origin with its first seq, so the overlap is harmless.

Frames the core streams that omoikane does not display (`partial`,
`tool_activity`, `subtask_lifecycle`, `turn_failed`,
`provisional_result`) are ignored and the connection is kept.

It listens on **no** network port. It dials the core socket and calls
omoikane's HTTP API (`KB_BASE_URL`). Which librarians to serve comes
from `GET /v1/gateway/librarians`, re-polled on an interval.

## The protocol 3 model (what changed from protocol 2)

- **No admin plane.** There is no `gate-instances` / `gate-bindings`
  API and no operator token. A gate instance is created by the core on
  the first `hello` that names it. omoikane mints the instance id
  (UUIDv7) when the librarian is saved and keeps it on
  `user_librarians.gate_instance_id`.
- **No bind.** Every session-scoped frame carries the gateway's opaque
  session key. omoikane uses the `/talk` thread id. The core resolves
  `(instance, key)` to its own session, creating one for a new key.
  Threads bound under protocol 2 keep their conversation: the binding
  address was the same thread id, and the core's v65 migration keeps
  those sessions (`extgate-<old binding id>`).
- **hello** is `{id, m:"hello", protocol:3, instance_id, agent_id,
  operation_protocol:1, final_delivery:"automatic", operations:[]}`.
  omoikane declares no gateway operations, so each turn's final text
  arrives as a plain `say`. `subject_unknown` means the agent row is not
  on the runtime yet (save the librarian first, which provisions it).
- **No trust row.** The core no longer has a per-agent owner table
  (opencrab D-1097). The owner is asserted per message by the gate
  (`said.caller`). The REST lane (`POST /api/agents/{id}/messages`)
  resolves its caller from opencrab's own `api_principals`.

## Routing: which threads go through the gateway

A `/talk` thread goes through the gateway when **both** hold:

- the librarian has a gate instance id (`user_librarians.gate_instance_id`)
- the thread has a `talk_gate_bindings` row

The row is written when the thread is created (kb-server
`GATE_ENABLED`). Threads created before the librarian had an instance
have no row and keep REST dispatch. The gate learns a newly created
thread on its first message (an unknown thread id refreshes the routed
list once). `GATE_TALK_REST_FORCE` (kb-server) forces every thread back
to REST dispatch without touching any DB.

## Prerequisites

1. **omoikane API reachable** at `KB_BASE_URL`.
2. **A gateway-scoped omoikane API token** (`GATEWAY_TOKEN`), issued
   **USER-LESS**: `kb-server admin-token -userless -name gateway
   -scopes read,write,gateway`. A user-bound token would let the
   author-stamping path compound that user's authority onto the
   gateway's.
3. **The core's Unix socket** at `OPENCRAB_GATE_SOCKET` (the core's
   `[gate] listen_socket`). In containers this is a named volume shared
   by the core and the gate. The gate refuses to start if the path is
   empty; if the socket is not there yet it retries with backoff.
4. **kb-server with `GATE_ENABLED=1`** (a leftover `GATE_ADMIN_URL` from
   a protocol 2 deployment also counts as enabled; `GATE_OPERATOR_TOKEN`
   is no longer read) and `OPENCRAB_URL` set, so the librarian save
   flow provisions the agent and mints the instance id.

## Environment

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `OPENCRAB_GATE_SOCKET` | **yes** | — | The core's Unix socket path. |
| `KB_BASE_URL` | **yes** | — | omoikane API base, `scheme://host`. |
| `GATEWAY_TOKEN` | **yes** | — | User-less token with scope `gateway`. |
| `GATE_DISCOVERY_INTERVAL` | no | `60s` | Roster re-poll interval. |
| `GATE_RECONNECT_MIN` / `GATE_RECONNECT_MAX` | no | `1s` / `60s` | Per-instance reconnect backoff bounds. |
| `GATE_STATIC_INSTANCES` | no | — | Conformance static mode (below). |
| `GATE_STATIC_AGENT` | with static | — | `agent_id` the static instances present in hello. |

## Conformance static mode

`GATE_STATIC_INSTANCES` (comma-separated canonical lowercase UUIDs) plus
`GATE_STATIC_AGENT` runs the gate **without a KB**: each listed instance
helloes directly, there is no replay, activity is a no-op, and every
`say` answers `err(code="external_rejected")` — honest, since with no KB
the gateway performs zero external I/O. It exists so a core-side harness
can drive the wire with this gate as the subject.

## Operating notes

- **Live instance.** The core answers `hello` with `instance_active`
  while another connection for the same instance is live. Stop the old
  gate before starting a new one.
- **Instance ids are stable.** Never change a librarian's
  `gate_instance_id`: the core keys the instance (and through it, the
  librarian's sessions) by it.
- **Version skew.** A core older than protocol 3 rejects this gate's
  hello (`protocol_unsupported` or `bad_request`). Upgrade the core and
  the gate together.
- **Smoke test.** Post a human message to a routed thread as its owner
  and check that an assistant message lands within the turn time; the
  gate logs `gate instance connected` per instance.
