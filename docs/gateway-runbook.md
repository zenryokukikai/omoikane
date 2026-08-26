# omoikane-gate operator runbook

Issue #104, slice G3 (E2E prep). This runbook brings the `omoikane-gate`
binary up against a real opencrab protocol-2 core. It is **deployment
scaffolding**: the binary is complete and unit-tested, but **not yet
end-to-end verified**, because the platform's real UDS core does not
exist yet. Two values (below) are filled in at compose-confirmation
time; everything else here is final.

## What omoikane-gate is

One process. It holds one protocol-2 connection **per ACTIVE personal
librarian** (platform ruling: one process, N sockets) over a **single
Unix-domain socket to the opencrab core**, and translates between the
external gate wire and omoikane's chat HTTP surface:

- `effect(say)` from the core → `POST /v1/librarian/chat` (assistant reply)
- librarian activity → `chat.status` broadcasts (`POST /v1/events/broadcast`)
- human `chat.message` (SSE `GET /v1/events`) → `event(said)` down the socket

It listens on **no** network port. It is purely an outbound client to
two places: the core's Unix socket (which it *dials*), and omoikane's
HTTP API (`KB_BASE_URL`). Discovery of which librarians to serve is
`GET /v1/gateway/librarians`, re-polled on an interval so newly
provisioned librarians connect without a restart.

## Prerequisites

1. **omoikane API reachable** at `KB_BASE_URL` (the running `kb-server`;
   default listen `:8080`). The gate needs network reachability to it.
2. **A gateway-scoped omoikane API token** (`GATEWAY_TOKEN`), issued
   **USER-LESS** — `user_id` empty, scope `gateway`. Mint it with the
   omoikane admin CLI leaving `user_id` blank. A user-bound token (an
   agent-role user especially) would let the author-stamping path
   compound that user's authority onto the gateway's; the server stamp
   path is fail-closed regardless (#104 G3c), but user-less is the
   correct issue form.
3. **The opencrab core's Unix socket** reachable at the path in
   `OPENCRAB_GATE_SOCKET`. In a container deployment this is a
   **named volume shared** between the core container and this gate
   container (see the compose fragment). The gate refuses to start if
   the path is empty; if the path is set but the socket is not yet
   present, the gate dials, fails, and retries with backoff.
4. **The omoikane-talk gate kind and per-librarian instances registered**
   on the core's admin plane. That registration is omoikane-side work
   (`internal/opencrab.GateProvisioner`) driven by the librarian save
   flow; it is a prerequisite for any instance to be connectable, not a
   step this runbook performs. Facts an operator/E2E needs to confirm the
   registration match:
   - kind id: `omoikane-talk`
   - protocol major: `2`
   - origin scope: `instance`
   - ingress discovery: `prebound`
   - address form (binding address = /talk thread id): `^thread-[0-9a-f]{8}$`
   - catch-up mode: `none`
   - instance config digest: SHA-256 of the empty object `{}` — the
     `config_digest` every `omoikane-talk` hello presents. Compute it
     with: `printf '{}' | shasum -a 256` →
     `44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a`.

## Environment variables

Names are read verbatim by `internal/gate/runtime/config.go`
(`FromEnv`). Do not rename them.

| Env var | Required | Default | Meaning |
| --- | --- | --- | --- |
| `OPENCRAB_GATE_SOCKET` | **yes** | — | The opencrab core's Unix socket path. Empty ⇒ refuse to start (a gate with no core is a misconfiguration, not a degraded mode). **Filled at confirmation time.** |
| `KB_BASE_URL` | **yes** | — | omoikane API base, `scheme://host` (no trailing slash needed). Empty ⇒ refuse to start. |
| `GATEWAY_TOKEN` | **yes** | — | Gateway-scoped bearer token, issued user-less (see prereq 2). Empty ⇒ refuse to start. **Filled at confirmation time.** |
| `GATE_DISCOVERY_INTERVAL` | no | `60s` | How often the librarian roster is re-polled to pick up newly provisioned librarians. Positive Go duration (e.g. `30s`, `2m`). |
| `GATE_RECONNECT_MIN` | no | `1s` | Lower bound of the per-instance / SSE reconnect backoff (exponential, doubling from min to max). Positive Go duration. |
| `GATE_RECONNECT_MAX` | no | `60s` | Upper bound of the reconnect backoff. Positive Go duration; must be `>= GATE_RECONNECT_MIN`. |
| `GATE_HELLO_REVISION` | no | `1` | Active config revision presented in the hello frame. Positive integer. The omoikane-talk instance config is immutable (empty object, no revision flow), so leave at `1` unless a future revision flow appears. |

Misconfiguration is reported **all at once** at startup (all missing
required values in one error), and the process exits `2` without
connecting.

## Startup sequence

1. Set the environment (compose `env_file: gateway.env`, or `-e` flags).
2. Start the process: container `omoikane-gate` (see compose fragment),
   or locally `go run ./cmd/omoikane-gate` with the env exported.
3. On start it logs (slog text to **stderr**):
   `omoikane-gate starting socket=<path> kb=<base>`.
4. It immediately syncs the roster (`GET /v1/gateway/librarians`), then
   opens the SSE stream and, for each librarian row that carries a
   non-empty `gate_instance_id`, dials the socket and performs the
   protocol-2 hello/ready handshake.

## Verifying it connected

Look for these log lines (all `level=INFO`, on stderr):

- `event stream connected` — the SSE subscription to omoikane is up.
- `serving gate instance instance_id=… user_id=… agent_id=…` — a runner
  was started for a discovered instance.
- `gate instance connected instance_id=… user_id=… epoch=…` — the
  hello/ready handshake with the core **succeeded** for that instance.
  This is the "connected" signal per instance.

And on the socket side:

- The socket file exists at `OPENCRAB_GATE_SOCKET` (created by the core;
  the gate is the dialer, not the listener).

Warnings you may see while things settle (not fatal):

- `librarian roster fetch failed; keeping current set` — omoikane
  unreachable; existing runners keep serving, retried next interval.
- `gate connection ended; reconnecting` — socket dropped or not yet
  present; retried with backoff.
- `event stream ended; reconnecting` — SSE dropped; retried with backoff.

The `scripts/gateway-smoke.sh` helper automates the three checks
(roster, socket present, connected log lines).

## Librarian discovery

`GET /v1/gateway/librarians` (Bearer `GATEWAY_TOKEN`) returns the
connection roster:

```json
{ "librarians": [
  { "user_id": "...", "agent_id": "plib-...", "name": "...",
    "gate_instance_id": "..." }
] }
```

Only rows with a **non-empty** `gate_instance_id` are connectable (an
empty one means the librarian is ACTIVE but not yet registered on the
admin plane). The gate connects exactly those, and drops runners for
rows that vanish from the roster (librarian deactivated / instance
removed).

## Logs

slog **text** format on **stderr**. Under the compose fragment:
`docker compose -f deploy/omoikane-gate.compose.yml logs -f omoikane-gate`.

## Shutdown / restart

`SIGINT` / `SIGTERM` triggers a graceful stop: every instance runner is
stopped and the SSE loop exits; the process logs `omoikane-gate stopped`
and exits `0`. Restart is safe and stateless — the gate holds no local
state; it re-discovers the roster and re-binds from scratch (reconnect
replay is origin-idempotent, so re-sent history dedupes core-side).
`restart: unless-stopped` in compose is appropriate.

## Filling in the two platform-provided values

Everything above is final **except** two values, deferred until the
opencrab core exists and the compose is confirmed with the platform:

1. **`OPENCRAB_GATE_SOCKET`** — the real core socket path. Genuinely
   provided by opencrab (the core owns and creates the socket). In the
   container deployment this is the in-container mount path of the
   **named volume shared with the core**; agree the exact path with the
   platform at confirmation time and set the same path on both
   containers.
2. **`GATEWAY_TOKEN`** — the gateway-scoped bearer token. Provenance
   nuance: this is **minted operator-side** with the omoikane admin CLI
   (user-less, scope `gateway`), *not* literally handed over by
   opencrab. It is grouped with the socket path only because both are
   filled at the same confirmation step. Keep it out of the repo; supply
   it via `gateway.env` (git-ignored) or a secret store.

Until both are real, this stack stays on the `feat/gateway-integration`
branch and is **not** merged to main (platform counterpart not yet
running; no e2e possible).

## Known gaps

- **Not end-to-end verified.** The binary is unit-tested against a
  scripted fake core (`net.Pipe`) and an `httptest` omoikane, but no
  real protocol-2 UDS core exists yet to verify against. Treat first
  contact with the real core as the E2E gate.
- **Instance registration depends on upstream opencrab#763.** The
  subject resolver that maps an omoikane agent to a gate `subject_id` is
  a stub (`StubSubjectResolver` → `ErrSubjectUnresolved`); until #763
  lands, `EnsureInstance` is a logged no-op, so `gate_instance_id` stays
  empty and no instance is connectable. This is an omoikane-side / core
  prerequisite, not something this runbook or the compose fragment can
  unblock.
- **No documentation drift found** between this runbook and the code:
  every env var above matches `internal/gate/runtime/config.go`
  verbatim, and the binary reads no env var not listed here.
