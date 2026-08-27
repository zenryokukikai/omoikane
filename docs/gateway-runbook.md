# omoikane-gate operator runbook

Issue #104 (E2E prep, V3 contract). This runbook brings the
`omoikane-gate` binary up against a real opencrab core speaking the
external gate **V3 minimal contract** (DESIGN-EXTGATE-V3.md — the sole
wire/admin authority). It is **deployment scaffolding**: the binary is
complete and unit-tested, but **not yet end-to-end verified**, because
the platform's real UDS core does not exist yet. Two values (below) are
filled in at compose-confirmation time; everything else here is final.

## What omoikane-gate is

One process. It holds one V3 connection **per ACTIVE personal
librarian** (platform ruling: one process, N sockets) over a **single
Unix-domain socket to the opencrab core**, and translates between the
external gate wire and omoikane's chat HTTP surface:

- `say` from the core → `POST /v1/librarian/chat` (assistant reply);
  the success answer on the wire is a plain `ok` — no message id
  travels back
- `activity` (started/ended) → `chat.status` broadcasts
  (`POST /v1/events/broadcast`)
- human `chat.message` (SSE `GET /v1/events`) → `said` down the socket

It listens on **no** network port. It is purely an outbound client to
two places: the core's Unix socket (which it *dials*), and omoikane's
HTTP API (`KB_BASE_URL`). Discovery of which librarians to serve is
`GET /v1/gateway/librarians`, re-polled on an interval so newly
provisioned librarians connect without a restart.

## Prerequisites

1. **omoikane API reachable** at `KB_BASE_URL` (the running `kb-server`;
   default listen `:8080`). The gate needs network reachability to it.
2. **A gateway-scoped omoikane API token** (`GATEWAY_TOKEN`), issued
   **USER-LESS** — `user_id` empty, scope `gateway`. Mint it with
   `kb-server admin-token -userless -name gateway -scopes
   read,write,gateway` (no user row is created; `-scopes` is mandatory
   and the `admin` scope is refused). A user-bound token (an
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
4. **Per-librarian instances registered** on the core's admin plane
   (V3 has no kind/schema registration — `kind_id` is an opaque value
   on the instance). That registration is omoikane-side work
   (`internal/opencrab.GateProvisioner`) driven by the librarian save
   flow; it is a prerequisite for any instance to be connectable, not a
   step this runbook performs. Facts an operator/E2E needs to confirm
   the registration match (instance PUT body:
   `{kind_id, subject_id, enabled, config_b64}`):
   - kind id (opaque): `omoikane-talk`
   - wire protocol: `2` (fixed in the hello frame)
   - binding address = the /talk thread id (`thread-` + 8 lower hex)
   - instance config: the empty object `{}` (`config_b64` = `e30=`);
     its digest — the `config_digest` every `omoikane-talk` hello
     presents — is `printf '{}' | shasum -a 256` →
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
| `GATE_HELLO_REVISION` | no | `1` | Active config revision presented in the hello frame. Positive integer. The core rejects a hello whose revision does not match the instance's active revision (`revision_mismatch` → close), so after any revision POST this value must be raised to match. Instances register at revision `1`; leave at `1` until a revision flow is used. |
| `GATE_STATIC_INSTANCES` | no | — | **Conformance static mode** (see the dedicated section below): comma-separated canonical lowercase instance UUIDs. Non-empty ⇒ no KB — `KB_BASE_URL`/`GATEWAY_TOKEN` become optional (the socket path stays required), roster discovery and the SSE loop are skipped, and every `say` answers `err(external_rejected)`. Leave unset in every real deployment. |

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
   non-empty `gate_instance_id`, dials the socket and performs the V3
   hello (hello success = RUNNING; there is no ready stage).

## Verifying it connected

Look for these log lines (all `level=INFO`, on stderr):

- `event stream connected` — the SSE subscription to omoikane is up.
- `serving gate instance instance_id=… user_id=… agent_id=…` — a runner
  was started for a discovered instance.
- `gate instance connected instance_id=… user_id=…` — the hello with
  the core **succeeded** for that instance (V3 has no connection epoch).
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

## conformance 静的モード (GATE_STATIC_INSTANCES)

ワイヤ適合試験 (issue #104 QC E2E) 用の特別モード。プラットフォーム側の
テストハーネスが **core 側** を演じ、この gate が**被験体**になる。

- `GATE_STATIC_INSTANCES` に instance UUID (canonical 小文字) をカンマ
  区切りで並べると有効になる。roster 発見は**一切行わず**、列挙された
  instance をそのまま dial / hello する。
- このモードでは **KB が存在しない**: `KB_BASE_URL` / `GATEWAY_TOKEN`
  は不要になる (`OPENCRAB_GATE_SOCKET` は引き続き必須)。SSE 受信ループ
  も replay も走らない。
- 挙動の対応表:

  | wire | 挙動 |
  | --- | --- |
  | `hello` | 通常どおり (revision は `GATE_HELLO_REVISION`) |
  | `bind` | ack して in-memory に記録 (replay なし) |
  | `say` | **常に** `err(code="external_rejected")` |
  | `activity` | no-op (表示先の KB がない) |
  | `said` | 送出されない (人間側の入力経路がない) |

- `say` が常に `external_rejected` なのは**正直な回答**であって
  スタブではない: KB がない以上 gateway は外部 I/O をゼロしか行わず、
  配送は「確実に受理されていない」— V3 の信頼原則 (fabrication 禁止)
  と、承認済み disposition #3 (unknown binding → external_rejected,
  zero I/O = 確実に未配送) に一致する。ハーネスはこれで err 経路を
  決定的に検証できる。
- **実運用では必ず未設定にする。** 静的モードの gate は /talk を一切
  配送しない。

## revision 更新 / instance 削除の手順

V3 契約 §5.5 (#104) により、ある gate instance の **LIVE 接続を
gate が保持している間**、プラットフォームはその instance への
**revision POST** と **instance DELETE** を `409 instance_active` で
拒否する (自動 invalidation スイープは廃止された)。順序は運用側の
責任になる:

- **revision 更新**: `omoikane-gate` を停止する (または gate がその
  instance の接続を落とすのを待つ) → revision を適用する → 新しい
  設定 (`GATE_HELLO_REVISION` 等) で gate を再起動する。接続が生きて
  いる限り revision POST は 409 で弾かれるので、停止が先。
- **instance 削除 (librarian 削除)**: librarian を deactivate して
  roster から外す → gate は次の roster poll でその instance の接続を
  自動切断する (ログ: `stopping gate instance (left roster)`) → その後
  instance を DELETE する。接続が残ったまま DELETE しても 409 になる
  だけなので、必ず roster 離脱 → 自動切断を先に確認する。

**binding PUT/DELETE はこの LIVE 拒否の対象外**であり、通常運用中も
そのまま実行できる (スレッドの binding 追加/close に gate の停止は
不要。新規 PUT は live 接続へ binding 単位で bind される — V3 §5.5)。

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

(Historical note: this stack was developed on `feat/gateway-integration`
and merged to main after the mutual E2E passed. The envs above stay
unset in production until cutover.)

## Verified core facts (2026-08-27, opencrab integration/transplant fb9a4127)

Measured against a locally built core with our gate connected — treat
these as operational facts, re-verify on major platform updates:

- The platform's canonical contract file is
  `docs/design/external-gate.md` in the opencrab repo (V3 content).
- Core-side operator Bearer env: `OPENCRAB_GATE_OPERATOR_TOKEN`
  (read once at startup, then scrubbed from the process env).
- The core's `[gate] listen_socket` must be an **absolute path shorter
  than SUN_LEN (~104 bytes)** — long scratch/volume paths fail loudly at
  startup. Pick short mount paths (e.g. `/run/gate/gate.sock`).
- Offline/dev builds: `cargo build -p opencrab-server
  --no-default-features` compiles Discord/Nostr out entirely — the
  recommended shape for any isolated verification run.
- Agent GET returns `200` with JSON `null` for an absent agent (not
  404) and carries `subject_id` when present — matches our
  RuntimeSubjectResolver's handling.

## Known gaps

- **Not end-to-end verified.** The binary is unit-tested against a
  scripted fake core (`net.Pipe`) and an `httptest` omoikane, but no
  real V3 UDS core exists yet to verify against. Treat first contact
  with the real core as the E2E gate.
- **Instance registration needs a runtime that exposes `subject_id`.**
  The production subject resolver (`RuntimeSubjectResolver`) reads
  `subject_id` from the runtime's `GET /api/agents/{id}` (upstream
  opencrab#763, landed). Against an older runtime without the field —
  or before the agent row exists (404) — the resolver answers
  `ErrSubjectUnresolved` and `EnsureInstance` is a logged no-op, so
  `gate_instance_id` stays empty and no instance is connectable until
  the runtime is upgraded. A 409 (multiple subject mappings) or any
  other runtime error fails the save loudly.
- **No documentation drift found** between this runbook and the code:
  every env var above matches `internal/gate/runtime/config.go`
  verbatim, and the binary reads no env var not listed here.
