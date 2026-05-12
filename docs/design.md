# Agent Knowledge Base — 設計書

**バージョン**: 0.7
**対象実装環境**: Claude Code(自律実装エージェント)
**読み手**: 実装エージェント、後続の保守者
**最終更新**: 2026-05-12
**変更履歴**: [docs/design-changelog.md](design-changelog.md) 参照

**v0.7 の主な変更(v0.6 からの差分)**:

- 原則 16 追加: "Fractal Z-axis architecture" — 各層は下層に対しては Z 軸俯瞰者、上層に対しては実行役として再帰的に動作
- §24 新規追加: Fractal Hierarchy(将来 Phase 仕様) — Phase 6+ で導入する 3 層フラクタル(司書層 / sub-agent 層 / coding-agent 層)の方向性と予約
- Phase 5 備考: 司書 skill ディレクトリに `sub_agents/` を Phase 5 段階から空ディレクトリとして予約
- 付録 A 用語集: 13 件追加(Fractal Z-axis architecture、3 層の名称、3 人部屋、実装役 / 監督役 / 盛り上げ役、固定ルーム / 動的ルーム、Room role aptitude)
- 付録 C 個性 YAML 注記: Phase 6+ で `room_role_aptitudes` フィールドを追加することを明示

本書は AgentKB(omoikane)の正準仕様。実装時のあらゆる疑問は本書を参照する。

---

## 0. このドキュメントについて

### 0.1 読み方の規約

- 「**MUST**」「**SHOULD**」「**MAY**」は RFC 2119 に準拠する
- スキーマ・APIの仕様は機械的に再現可能な粒度で記述する
- 段階的実装計画(§13)に従って Phase 1 から順に着手する
- 不明点が発生した場合は実装を止めて確認すること(勝手な拡張をしない)

### 0.2 実装エージェントへの指示

- **MUST**: 各 Phase 完了時に成果物のテストを実行し、緑であることを確認する
- **MUST**: スキーマ変更は migration ファイルとして残す
- **MUST**: 仕様外の機能を勝手に追加しない
- **SHOULD**: 全ての公開 API に OpenAPI 定義を書く
- **SHOULD NOT**: 第三者依存パッケージを最小限に留める(セキュリティ要件)

---

## 1. 概要

### 1.1 目的

AI エージェントが過去の経験(失敗事例、判断記録、設計知見、未解決の観察)を参照・蓄積・発見できる **汎用ナレッジサーバー**。エージェント間で知識を共有し、過去の罠を繰り返さず、新しい知見を組織的に活用する。

利用クライアントは AI コーディングエージェント(Claude Code, OpenCode, Cursor, Cline 等)を主たるターゲットとするが、設計は **特定のドメインに依存しない**。

### 1.2 想定ユースケース

| 分野 | 典型的な知識 |
|---|---|
| ML 研究・開発 | 実験の失敗パターン、設計判断、ハイパーパラメータの罠 |
| ソフトウェア開発 | デバッグ事例、設計判断、コードレビューで頻出する指摘 |
| インフラ運用 | 障害事例、設定ノウハウ、変更管理の経緯 |
| 法務・コンプライアンス | 過去の判断記録、根拠条文との対応、判例 |
| カスタマーサポート | FAQ、失敗事例、エスカレーション判断の経緯 |
| 製造業の品質管理 | 不具合パターン、検査ノウハウ、対処手順 |
| 研究機関 | 実験ログ、再現性検証の経緯、廃案理由 |

共通する性質: **「過去の経験を踏まえてエージェントが行動する必要がある領域」全般**。

### 1.3 解決する課題

エージェントの記憶はセッション単位で失われる。サブエージェント呼び出しで明示的にコンテキストを継承しない限り、過去の判断や失敗は忘れられる。これにより:

- 同じ罠を別エージェント(別セッション)が繰り返し踏む
- 過去の根拠と矛盾する判断が下される
- 試行錯誤の経験が組織知として蓄積されない
- エージェント間で知識が共有されない

既存の手段(git で md 管理、サードパーティのメモリプラグイン)はいずれも以下を抱える:

- 人間が編集者であることを前提にした重いワークフロー
- 供給網リスクのあるサードパーティ依存
- 特定ツール(OpenCode 等)へのロックイン
- 単一エージェントの memory 機構で、組織的・自律的な curation がない

これを解消する **ツール非依存・内部完結・自律維持** のサーバーを自前で持つ。

### 1.4 スコープ

**In scope**:

- REST API による知識の CRUD
- 解決済み知識(trap/decision/design)と未解決知識(incident)の両方を扱う
- 多次元の逆引きインデックス(タグ、症状、トリガ、場面、階層、関係)
- 書き込み時の LLM enrichment(タグ・症状抽出等)
- 使用事例(usage_cases)の蓄積とフィードバックループ
- 人間用 Web ダッシュボード(Wiki 風、ただし監査用途)
- MCP サーバー、CLI、SDK
- スキル形式でのクライアント配布
- インシデントから罠への昇格ワークフロー
- マルチプロジェクト・マルチユーザー対応(内部網限定)
- **司書コミュニティ(Librarian Community)**: 個性を持つ常駐エージェント群が自律的に KB を維持
- **Level C 完全自走**: 通常運用で人間介入なし、異常時のみ通知
- **共有チャット空間**: 司書同士が雑談的に情報共有
- **議論クォーテット**: 重要判断は 3 体議論 + Judge(Z 軸)裁定
- **個性ベースの自発的データ収集**
- **時間的妥当性管理(Temporal validity)**: 削除ではなく無効化で扱う
- **二層トリガ(Dual-layer triggers)**: ルールベース + LLM ベース

**Out of scope(初版)**:

- 外部公開、SaaS 化
- リアルタイム同期、CRDT
- 大規模分散構成(単一ノード前提)
- モバイルアプリ
- 課金、決済
- 共有チャットからの新企画自然発生(Phase 8 以降に予約、テーブルは確保)
- 外部自律エージェントの雇用(Phase 8 以降に予約、テーブルは確保)

### 1.5 非ゴール

- PageIndex の完全再実装ではない(概念採用 / 独自実装)
- 特定エージェントツール最適化はしない(ツール非依存を維持)

---

## 2. 設計原則

1. **Tool-agnostic core, thin adapters** — HTTP REST API が中核
2. **Cases over scores** — フィードバックは文脈付き事例
3. **Write-time enrichment, read-time cheap** — LLM コストは書き込み時に1回
4. **Knowledge is portable** — エクスポート可能、移行可能
5. **Internal-only, low attack surface** — 内部網前提、依存最小
6. **Human-verifiable but not human-dependent** — 監査可能だが通常運用で人間不要
7. **Incomplete knowledge is valuable** — incident も一級市民
8. **One core, many distributions** — Core API 1 つ、skill/adapter 多数
9. **Level C autopilot** — 完全自走前提、信頼性は多重チェックで担保
10. **Engineered cognitive diversity** — 司書には意図的に異なる個性ベクトル
11. **Z-axis arbitration** — 議論カルテット = 3 + Judge、俯瞰者が決定
12. **Structural infinite-loop prevention** — エージェントの善意に頼らない多層構造
13. **Temporal facts, not deletions** — 削除ではなく時間的妥当性の更新
14. **Heartbeat-driven proactive curation** — 各司書が idle 時に外部データ収集
15. **No in-house agent runtime** — エージェント実体は内製しない。skill として定義し、Claude Code / OpenCode 等に演じさせる
16. **Fractal Z-axis architecture** — 各層は下層に対しては Z 軸俯瞰者、上層に対しては実行役として動作。司書層・sub-agent 層・coding-agent 層に同じ「3 人部屋 + Z 軸」パターンが再帰的に適用される(詳細は §24、Phase 6+)

---

## 3. アーキテクチャ

```
                       ┌───────────────────────────┐
                       │   Web Dashboard (SSR)     │
                       │   - 監査・観察用           │
                       └─────────────┬─────────────┘
                                     │ HTTP
                                     ▼
   ┌─────────────────────────────────────────────────────────────┐
   │                    Core HTTP REST API                       │
   │   /v1/entries  /v1/lookup/*  /v1/search  /v1/cases          │
   │   /v1/librarian/*(司書専用)                                  │
   └──────┬──────────────┬──────────────┬─────────────────┬──────┘
          │              │              │                 │
          ▼              ▼              ▼                 ▼
       Storage      Enrichment       Search             Auth
      (SQLite)     (LLM call)      (FTS5 + LLM)    (Bearer Tok)
                            ▲                ▲
        ┌───────────────────┴──┐  ┌──────────┴──────────────────┐
        ▼                                                       ▼
  ┌──────────────────────────────────────────────────────────────────────┐
  │                  Librarian Community(Phase 5+)                       │
  │   Coordinator → Cataloger/Curator/Detective/Conservator/Scout        │
  │                 Summarizer / Judge pool                               │
  │   共有チャット ── Task Queue ── External findings ── Meta-knowledge   │
  └──────────────────────────────────────────────────────────────────────┘
                                     ▲
                                     │
          ┌──────────────────────────┼──────────────────────────┐
          ▼                          ▼                          ▼
       MCP Adapter             CLI Adapter                  SDK (Lib)
          │
   ┌──────┴──────┬──────────┬──────────┬──────────┐
   ▼             ▼          ▼          ▼          ▼
 Claude Code  OpenCode   Cursor     Cline    Codex CLI
```

### 3.2 プロセス構成

初版は **単一バイナリ** にすべてを同梱:

- HTTP server (port 8080)
- MCP server (同一ポートで `/mcp` パス、または別ポート)
- ダッシュボードは同じ HTTP サーバーが配信
- CLI は別バイナリ、HTTP 経由で Core API を叩く
- 永続化は `kb.db`(SQLite ファイル 1 個)

デプロイ単位: systemd service 1 つ + SQLite ファイル + 設定ファイル。

---

## 4. データモデル

### 4.1 ER 概観

```
                     ┌────────────┐
                     │  projects  │
                     └──────┬─────┘
                            │
                            ▼
                     ┌──────────────┐
              ┌──────┤   entries    ├──────┐
              │      └──────┬───────┘      │
              │             │              │
       ┌──────▼──────┐  ┌───▼────┐  ┌──────▼──────┐
       │    tags     │  │ relat. │  │ entry_hist. │
       └─────────────┘  └────────┘  └─────────────┘

       ┌────────────────┐    ┌─────────────────┐
       │ symptoms_index │    │ triggers_index  │
       └────────────────┘    └─────────────────┘
                                       │
                                       ▼
                                 ┌──────────┐
                                 │  usage_  │
                                 │  cases   │
                                 └──────────┘

       ┌────────────────┐    ┌─────────────────────┐
       │   hierarchy    │────│  hierarchy_entries  │
       └────────────────┘    └─────────────────────┘

       ┌────────────────┐    ┌─────────────────────┐
       │   situations   │────│  situation_entries  │
       └────────────────┘    └─────────────────────┘

       Phase 5+:
       librarian_chat / chat_threads / librarian_tasks /
       librarian_instances / quartet_assignments /
       external_findings / finding_correlations
```

### 4.2 スキーマ詳細

#### 4.2.1 Phase 1 で必要なテーブル

```sql
-- ============================================================
-- Projects (multi-tenancy)
-- ============================================================
CREATE TABLE projects (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    metadata    JSON
);

-- ============================================================
-- Entries (核となる知識単位)
-- ============================================================
CREATE TABLE entries (
    id TEXT PRIMARY KEY,                  -- 'T-001', 'D-005', 'I-042' 等
    project_id TEXT NOT NULL REFERENCES projects(id),
    type TEXT NOT NULL,                   -- 'trap'|'decision'|'design'|'lesson'|'incident'
                                          --   |'librarian_meta'|'external_finding'
    title TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'DRAFT', -- 'DRAFT'|'INVESTIGATING'|'ACTIVE'
                                          --   |'SUPERSEDED'|'ARCHIVED'|'DUPLICATE'
                                          --   |'RESOLVED'

    symptom TEXT,
    root_cause TEXT,
    resolution TEXT,
    prohibited TEXT,

    attempted_approaches TEXT,
    observed_behavior TEXT,
    hypotheses TEXT,

    body TEXT NOT NULL,
    body_format TEXT NOT NULL DEFAULT 'markdown',

    scope JSON,

    -- Temporal validity(時間的妥当性)
    valid_from TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    valid_to TIMESTAMP,                   -- NULL = 現在も有効
    superseded_by TEXT REFERENCES entries(id),
    invalidation_reason TEXT,

    -- Enrichment versioning
    enrichment_version INTEGER NOT NULL DEFAULT 0,
    enrichment_at TIMESTAMP,

    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by TEXT,
    created_by_role TEXT,                 -- 'human'|'agent'|'librarian:cataloger' 等

    -- Optimistic locking
    version INTEGER NOT NULL DEFAULT 1,

    metadata JSON
);
CREATE INDEX idx_entries_project       ON entries(project_id);
CREATE INDEX idx_entries_type          ON entries(type);
CREATE INDEX idx_entries_status        ON entries(status);
CREATE INDEX idx_entries_type_status   ON entries(type, status);
CREATE INDEX idx_entries_validity      ON entries(valid_from, valid_to);
CREATE INDEX idx_entries_superseded    ON entries(superseded_by);

-- ============================================================
-- Full-text search (FTS5)
-- ============================================================
CREATE VIRTUAL TABLE entries_fts USING fts5(
    id UNINDEXED,
    title, symptom, root_cause, resolution,
    attempted_approaches, observed_behavior, hypotheses,
    body,
    content='entries', content_rowid='rowid'
);
-- triggers to keep FTS in sync (002_fts.sql 参照)

-- ============================================================
-- Tags
-- ============================================================
CREATE TABLE tags (
    entry_id TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    tag TEXT NOT NULL,
    confidence REAL DEFAULT 1.0,
    source TEXT DEFAULT 'llm',            -- 'human'|'llm'|'heuristic'
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (entry_id, tag)
);
CREATE INDEX idx_tags_tag ON tags(tag);

-- ============================================================
-- Entry history (?as_of= で過去スナップショット復元できるよう
-- 全フィールドのスナップショットを記録)
-- ============================================================
CREATE TABLE entry_history (
    entry_id        TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    version         INTEGER NOT NULL,
    -- 全可変フィールドのスナップショット
    title           TEXT NOT NULL,
    status          TEXT NOT NULL,
    symptom         TEXT,
    root_cause      TEXT,
    resolution      TEXT,
    prohibited      TEXT,
    attempted_approaches TEXT,
    observed_behavior    TEXT,
    hypotheses      TEXT,
    body            TEXT NOT NULL,
    body_format     TEXT NOT NULL,
    scope           JSON,
    metadata        JSON,
    valid_from      TIMESTAMP NOT NULL,
    valid_to        TIMESTAMP,
    superseded_by   TEXT,
    invalidation_reason TEXT,
    -- 変更コンテキスト
    changed_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    changed_by      TEXT,
    change_summary  TEXT,
    PRIMARY KEY (entry_id, version)
);
CREATE INDEX idx_history_changed_at ON entry_history(entry_id, changed_at);

-- ============================================================
-- Users / API tokens / Audit
-- ============================================================
CREATE TABLE users (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    role       TEXT NOT NULL DEFAULT 'member',  -- 'admin'|'member'|'agent'
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE api_tokens (
    token_hash   TEXT PRIMARY KEY,         -- SHA-256
    user_id      TEXT REFERENCES users(id),
    name         TEXT NOT NULL,
    scopes       TEXT NOT NULL,            -- 'read,write,admin'
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at   TIMESTAMP,
    last_used_at TIMESTAMP
);

CREATE TABLE audit_log (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    request_id    TEXT,
    user_id       TEXT,
    token_name    TEXT,
    method        TEXT NOT NULL,
    path          TEXT NOT NULL,
    body_summary  TEXT,
    client_type   TEXT,
    client_ip     TEXT,
    status_code   INTEGER NOT NULL,
    duration_ms   INTEGER
);
CREATE INDEX idx_audit_ts ON audit_log(timestamp DESC);
```

#### 4.2.2 Phase 2 で追加

```sql
-- symptoms_index, triggers_index, tag_aliases、trigger_rules.yaml ローダ
```

#### 4.2.3 Phase 3 で追加

```sql
-- usage_cases, relations, situations, situation_entries,
-- incident_clusters, incident_cluster_members,
-- conflict 検出による auto-supersede トリガ
```

#### 4.2.4 Phase 4 で追加

```sql
-- hierarchy_nodes, hierarchy_entries, derived_summaries
```

#### 4.2.5 Phase 5 で追加(司書システム)

```sql
-- librarian_chat / chat_threads / librarian_tasks /
-- librarian_instances / quartet_assignments /
-- external_findings / finding_correlations
-- librarian_meta_view
-- 将来要件用テーブル: thread_emergent_topics, external_contracts,
--                     contractor_access_log
```

各 Phase の完全な DDL は対応する `migrations/NNN_*.sql` に格納。Phase 1 の正準 DDL は `internal/store/migrations/001_init.sql` と `002_fts.sql` を参照(`//go:embed` でバイナリに同梱)。

### 4.3 集計ビュー(Phase 3 以降)

```sql
CREATE VIEW entry_signals AS
SELECT 
    e.id, e.project_id, e.title, e.type, e.status,
    COUNT(uc.case_id) AS total_uses,
    SUM(CASE WHEN uc.result = 'helpful' THEN 1 ELSE 0 END) AS helpful_count,
    -- ... 詳細は元設計書 §4.3
    CASE 
        WHEN COUNT(uc.case_id) - SUM(CASE WHEN uc.result IN ('unknown', NULL) THEN 1 ELSE 0 END) = 0 
        THEN NULL
        ELSE CAST(SUM(CASE WHEN uc.result = 'helpful' THEN 1.0
                          WHEN uc.result = 'partially_helpful' THEN 0.5
                          WHEN uc.result = 'misleading' THEN -1.0
                          ELSE 0 END) AS REAL)
             / (COUNT(uc.case_id) - SUM(CASE WHEN uc.result = 'unknown' OR uc.result IS NULL THEN 1 ELSE 0 END))
    END AS helpfulness_score
FROM entries e
LEFT JOIN usage_cases uc ON uc.entry_id = e.id
GROUP BY e.id;
```

---

## 5. REST API 仕様

### 5.1 共通規約

- ベースパス: `/v1`
- 認証: `Authorization: Bearer <token>` ヘッダ
- リクエスト/レスポンス: JSON (`application/json`)
- 推奨ヘッダ: `X-Client-Type` / `X-Client-Version` / `X-Client-Session`

#### 5.1.1 エラーエンベロープ(全エンドポイント共通)

```json
{
  "error": {
    "code": "ENTRY_NOT_FOUND",
    "message": "Entry T-999 does not exist",
    "details": { "id": "T-999" }
  }
}
```

#### 5.1.2 エラーコード taxonomy(Phase 1 確定リスト)

| HTTP | code | 用途 |
|---|---|---|
| 400 | `BAD_JSON` | リクエストボディが JSON としてパースできない |
| 400 | `BAD_REQUEST` | 形式は正しいが内容が不正(汎用) |
| 400 | `MISSING_FIELDS` | 必須フィールド欠落 |
| 400 | `INVALID_TYPE` | `type` が enum 外 |
| 400 | `INVALID_STATUS` | `status` が enum 外 |
| 400 | `INVALID_AS_OF` | `?as_of=` がパースできない |
| 401 | `MISSING_TOKEN` | Authorization ヘッダ欠落 |
| 401 | `INVALID_TOKEN` | トークン不一致 / 期限切れ |
| 403 | `FORBIDDEN` | スコープ不足 |
| 404 | `NOT_FOUND` | 対象リソース不在 |
| 409 | `ALREADY_EXISTS` | ID 重複 |
| 409 | `VERSION_MISMATCH` | OCC: `If-Match` のバージョンが現在と一致しない |
| 413 | `BODY_TOO_LARGE` | リクエストサイズ超過 |
| 422 | `SECRETS_DETECTED` | シークレット/PII 検出(`details.findings` に内訳) |
| 422 | `UNPROCESSABLE` | 形式は正しいが業務制約違反 |
| 429 | `RATE_LIMITED` | レート制限 |
| 500 | `INTERNAL` | 内部エラー |
| 501 | `NOT_IMPLEMENTED` | 未実装機能(`mode=reasoning` 等) |
| 503 | `ENRICHMENT_UNAVAILABLE` | LLM 障害(エントリ自体は保存される) |

詳細は `docs/error-codes.md` に列挙、OpenAPI と同期維持。

#### 5.1.3 ページネーション仕様

リスト系エンドポイントは以下を採用:

- クエリパラメータ: `limit`(既定 50、最大 500)、`offset`(既定 0)
- レスポンス JSON:
  ```json
  {
    "entries": [ ... ],
    "pagination": {
      "limit": 50,
      "offset": 0,
      "total": 1234,
      "next_offset": 50,
      "has_more": true
    }
  }
  ```
- `total` は同一フィルタでの全件数(`SELECT COUNT(*)` 相当)
- カーソルベース移行は Phase 4 以降に検討、初版は offset/limit

### 5.2 エンドポイント一覧

#### Phase 1

```
GET    /v1/health                               (public)
POST   /v1/projects                             (write)
GET    /v1/projects                             (read)
GET    /v1/projects/{id}                        (read)
POST   /v1/entries                              (write)  -- 同期 enrichment
GET    /v1/entries/{id}                         (read)   -- ?as_of=ISO8601 で過去復元
GET    /v1/entries                              (read)   -- filter + pagination
PATCH  /v1/entries/{id}                         (write)  -- If-Match: <version> 必須
DELETE /v1/entries/{id}                         (write)  -- soft delete (ARCHIVED)
GET    /v1/entries/{id}/history                 (read)
POST   /v1/search                               (read)   -- mode=fts のみ
```

#### Phase 2

```
POST   /v1/lookup/by-trigger
POST   /v1/lookup/by-symptom
POST   /v1/lookup/by-tags
```

#### Phase 3

```
POST   /v1/cases                                 -- usage_cases
PATCH  /v1/cases/{case_id}
GET    /v1/cases?entry_id=...
GET    /v1/entries/{id}/cases
POST   /v1/relations
DELETE /v1/relations/{from}/{to}/{type}
GET    /v1/entries/{id}/relations
GET    /v1/clusters
POST   /v1/clusters/{id}/promote
GET    /v1/situations
POST   /v1/situations
GET    /v1/situations/{id}/entries
```

#### Phase 4

```
POST   /v1/lookup/by-situation
GET    /v1/browse
GET    /v1/browse/{node_id}
GET    /v1/browse/{node_id}/entries
GET    /v1/index?project=...&group_by=tag|hierarchy|recent
POST   /v1/search                                -- mode=reasoning 追加
POST   /v1/reflect                               -- 複数エントリ横断推論
```

#### Phase 5(司書専用)

```
POST   /v1/librarian/chat
GET    /v1/librarian/chat/threads
POST   /v1/librarian/tasks
GET    /v1/librarian/telemetry
POST   /v1/librarian/recall_meta
POST   /v1/librarian/quartet/request
GET    /v1/librarian/my_chats
GET    /v1/librarian/my_actions
GET    /v1/librarian/my_meta_entries
GET    /v1/librarian/my_decisions_evaluated
```

#### Phase 5+(管理用、admin scope 必須)

```
POST   /v1/admin/reenrich/{entry_id}
POST   /v1/admin/reenrich/batch
POST   /v1/admin/backfill
GET    /v1/admin/jobs
GET    /v1/admin/jobs/{id}
DELETE /v1/admin/jobs/{id}
POST   /v1/admin/tags/merge
POST   /v1/admin/tags/rename
GET    /v1/admin/tags/proposals
POST   /v1/admin/hierarchy/restructure
GET    /v1/admin/hierarchy/proposals
GET    /v1/admin/health/coverage
GET    /v1/admin/health/freshness
GET    /v1/admin/health/llm_usage
GET    /v1/admin/proposals
POST   /v1/admin/proposals/{id}/approve
POST   /v1/admin/proposals/{id}/reject
POST   /v1/librarian/emergency_stop
```

### 5.3 詳細仕様(主要エンドポイント、Phase 1)

#### POST /v1/entries

リクエスト:
```json
{
  "project_id": "lipsync-lewm",
  "type": "trap",
  "title": "Train-Inference Mask Mismatch",
  "body": "...(markdown)...",
  "symptom": "rectangular artifact at inference",
  "root_cause": "...",
  "resolution": "...",
  "prohibited": "...",
  "scope": { "frameworks": ["pytorch"], "gpus": ["H100", "A100"] },
  "tags": ["mask", "preprocessing"],
  "status": "DRAFT",
  "metadata": {}
}
```

レスポンス (201):
```json
{
  "id": "T-A3K9F2",
  "project_id": "lipsync-lewm",
  "type": "trap",
  "status": "DRAFT",
  "title": "Train-Inference Mask Mismatch",
  "...": "...",
  "tags": ["mask", "preprocessing", "training", "inference"],
  "version": 1,
  "valid_from": "2026-05-12T10:00:00Z",
  "valid_to": null,
  "enrichment": {
    "version": 1,
    "source": "heuristic",
    "tags_added": ["training", "inference"]
  },
  "created_at": "2026-05-12T10:00:00Z"
}
```

シークレット検出時は 422 `SECRETS_DETECTED`、`details.findings` に検出位置とパターン名(値そのものは返さない)。

#### GET /v1/entries/{id}?as_of=2026-05-01T00:00:00Z

- `as_of` 未指定: 現在の状態を返す(`valid_to` が NULL or 未来であること)
- `as_of` 指定:
  1. `entry_history` から `changed_at <= as_of` の最新 version を選択
  2. 該当 version の全フィールドを復元
  3. 該当 entry が `as_of` 時点で `valid_from <= as_of < COALESCE(valid_to, +∞)` を満たすこと
- どちらにも該当しない場合は 404 `NOT_FOUND`

#### PATCH /v1/entries/{id}

- ヘッダ: `If-Match: <expected_version>` **必須**(なければ 428 でなく 409 `VERSION_MISMATCH`)
  - クライアントが GET で取得した `version` を渡す
- 不一致なら 409、現在のサーバー側 version は `details.current_version` に
- 成功時 200、レスポンス body に新しいエントリ全体 + `If-Match` 用の新 version
- PATCH のたびに `entry_history` に新 row を追加

#### DELETE /v1/entries/{id}

- ソフト削除: `status='ARCHIVED'`、`valid_to=NOW()`、`invalidation_reason='soft delete'`
- 204 No Content。idempotent(2 回目以降も 204)

#### POST /v1/search

```json
{
  "query": "\"mask\"*",
  "mode": "fts",
  "filters": { "project": "...", "type": "trap", "status": "ACTIVE", "tag": "mask" },
  "top_k": 20
}
```

`mode=reasoning` は Phase 4 で 501 `NOT_IMPLEMENTED` を返す。

### 5.4 OpenAPI

`api/openapi.yaml` に全 Phase 1 API の正準定義。SDK は OpenAPI から自動生成する。

---

## 6. LLM Enrichment 仕様

### 6.1 トリガ

- `POST /v1/entries`: 新規作成時(同期、`enrichment` フィールドをレスポンスに同梱)
- `PATCH /v1/entries/{id}`: 主要フィールド(body / symptom 等)変更時に再実行
- `POST /v1/situations`: situation から関連エントリ提案(Phase 3+)
- `POST /v1/search?mode=reasoning`: 推論検索(Phase 4+)

書き込み以外のリクエストで LLM を呼ばないこと。

### 6.2 プロンプト骨子

#### trap / decision / design / lesson 向け

```
SYSTEM:
You are a knowledge extraction service for a coding agent's knowledge base.
Output STRICTLY in JSON.

Schema:
{
  "tags": [string, ...],
  "symptoms": [string, ...],
  "triggers": [{ "phrase": string, "domain": "preprocessing"|"training"|... }],
  "prohibited_patterns": [string, ...],
  "scope": { ... },
  "summary_one_line": string,
  "proposed_relations": [...],
  "proposed_hierarchy_path": [...]
}

Existing entries in the same project (for relation proposals):
{compact list: id, title, tags}
```

#### incident 向け(原因不明・未解決を許容)

```
SYSTEM:
You are extracting metadata from an INCIDENT report — an unresolved
observation where the root cause may be unknown.

Do NOT speculate root causes that the author did not provide.
Do NOT invent resolutions.

Schema:
{
  "tags": [...],
  "symptoms": [...],
  "environment_signals": [...],
  "attempted_approaches_summary": [...],
  "open_questions": [...],
  "similar_incidents": [...],
  "proposed_hierarchy_path": [...]
}
```

### 6.3 モデル選定

- 既定: 高品質モデル(Claude Sonnet 等)
- 環境変数: `KB_LLM_PROVIDER` / `KB_LLM_MODEL` / `KB_LLM_API_KEY` / `KB_LLM_ENDPOINT`
- フェイルオープン: 失敗してもエントリは保存、enrichment は後でリトライ可能

### 6.4 既存エントリ参照

同プロジェクトの既存エントリ(id / title / tags のみ)をプロンプトに含めて関連性判定。エントリ数 > 100 のときは最近 + タグ重複の上位 50 件に絞る。

### 6.5 コスト管理

- 目標: 1 エントリ <10k トークン入出力合計
- `KB_LLM_MONTHLY_BUDGET_USD` 設定、超過時はタグだけの簡易抽出にフォールバックし警告ログ
- Phase 1 は heuristic フォールバックのみで運用可能

---

## 7. 検索・取得ロジック

### 7.1 by-trigger(Phase 2+)

```
1. クエリ正規化(小文字化、不要語除去)
2. trigger_rules.yaml の完全一致層をチェック
3. triggers_fts (FTS5) でファジー検索 → 候補
4. domain フィルタ
5. entry_signals でランキング:
   final_score = fts_score * (0.5 + 0.5 * max(0, helpfulness_score or 0))
6. top_k 件返却 / create_cases=true なら usage_cases レコード
```

### 7.2 by-symptom(Phase 2+)

triggers と同構造を symptoms_index に適用。

### 7.3 by-situation(Phase 4+)

```
1. クエリと situations.description を FTS マッチ
2. situation_entries で関連エントリ取得
3. relevance × situation match_score でランキング
```

### 7.4 推論ベース検索(Phase 4+)

```
1. 階層インデックスを LLM に提示
2. LLM が関連サブツリー選択
3. サブツリー内エントリ取得
4. 再度 LLM に絞り込ませる
5. 最終エントリ群を返却
```

---

## 8. MCP アダプタ(Phase 2+)

### 8.1 公開ツール

```
kb_lookup_by_trigger(domain?, trigger_description, top_k?, project_id?)
kb_lookup_by_symptom(symptom_description, top_k?, project_id?)
kb_lookup_by_situation(situation_description, top_k?, project_id?)
kb_lookup_by_tags(tags, match_mode?, top_k?, project_id?)
kb_search(query, mode?, filters?, top_k?)
kb_get(entry_id, as_of?)
kb_browse(node_id?)
kb_relations(entry_id)
kb_post(project_id, type, title, body, ...)
kb_update(entry_id, patch, expected_version)
kb_feedback(case_id, outcome?, result?, result_evidence?)
kb_link(from_id, to_id, rel_type, notes?)
kb_index(project_id?, group_by?)
```

### 8.2 MCP-Core 間通信

MCP サーバーは Core HTTP API を `KB_CORE_URL` で参照、`KB_INTERNAL_TOKEN` で認証。

---

## 9. CLI アダプタ

```bash
# 設定
kb config set url https://kb.internal
kb config set token ${KB_TOKEN}
kb config show

# プロジェクト
kb projects list
kb projects create --id lipsync-lewm --name "Lipsync LeWM"

# エントリ
kb post --project lipsync-lewm --type trap --file ./new-trap.md
kb get T-A3K9F2
kb get T-A3K9F2 --as-of 2026-05-01T00:00:00Z
kb update T-A3K9F2 --status ACTIVE --expected-version 1   # OCC
kb delete T-A3K9F2
kb history T-A3K9F2

# 検索 / Lookup
kb lookup trigger --domain preprocessing --query "modify mask generation"
kb lookup symptom --query "blurry mouth at inference"
kb search "warmup schedule"
kb browse
kb browse mask-processing

# フィードバック / インポート
kb feedback CASE-xxx --result helpful --evidence "..."
kb export --project lipsync-lewm --format json > backup.json
kb import backup.json
kb stats
```

---

## 10. SDK

初版は Python / TypeScript の薄いラッパー、OpenAPI から自動生成。

---

## 11. 人間用 Web ダッシュボード

### 11.1 ページ構成

```
/                          ホーム
/projects/{id}             プロジェクト概要
/projects/{id}/browse      階層ツリー(Phase 4)
/projects/{id}/entries     全エントリ一覧
/entries/{id}              詳細(?as_of= 対応)
/entries/{id}/history      変更履歴
/entries/{id}/cases        使用事例(Phase 3+)
/search?q=...              検索結果
/tags/{tag}                タグ別ビュー
/situations                場面一覧(Phase 4)
/situations/{id}           場面詳細
/review                    レビュー待ち(DRAFT、misleading 多発)
/conflicts                 conflicts_with あるエントリ
/dead-entries              長期未参照
/stats                     統計
/librarians                司書アクティビティ(Phase 5+)
/admin/users               admin
/admin/tokens              admin
```

### 11.2 技術選定

- レンダリング: Go `html/template`、SSR
- CSS: Pico.css 等の軽量フレームワーク、SPA 不要
- Markdown: `[[T-A3K9F2]]` 記法を自動リンク化
- 認証: API は Bearer、ダッシュボードは Cookie(Phase 4 から)
- Phase 1: 簡易、`?token=` クエリ許可 or `KB_DASHBOARD_OPEN=1` で認証無効化(開発時)

### 11.3 Wiki 風機能(Phase 4+)

- `[[entry-id]]` 自動リンク
- バックリンク自動表示
- ハブページ(hierarchy_node を概念ページに)

---

## 12. セキュリティ・運用

### 12.1 認証

- `Authorization: Bearer <token>`
- SHA-256 ハッシュ保存
- スコープ: `read` / `write` / `admin`
- `admin` は他スコープを包含
- 内部網限定(Phase 1〜)

### 12.2 監査ログ

Phase 1 から有効。全書き込みリクエストを `audit_log` に記録(`request_id` 付き)。

### 12.3 シークレット/PII スキャナ(Phase 1)

書き込み時に以下のパターンを検出:

| パターン名 | 対象 |
|---|---|
| `aws_access_key` | `AKIA[0-9A-Z]{16}` |
| `aws_secret_key` | 40 文字 base64-like の前後文脈 |
| `github_token` | `ghp_*`, `gho_*`, `ghs_*`, `ghr_*`, `github_pat_*` |
| `slack_token` | `xox[abp]-...` |
| `jwt` | `eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+` |
| `private_key` | `-----BEGIN (RSA|EC|DSA|OPENSSH|PGP) PRIVATE KEY-----` |
| `generic_api_key` | `(api[_-]?key|secret|token)\s*[:=]\s*['"]?[A-Za-z0-9_-]{20,}` |
| `email` | RFC 5322 簡易版 |
| `credit_card` | Luhn 通過の 13-19 桁 |
| `ipv4_private` | 内部 IP(警告のみ、検知)|

検査対象フィールド: `title` / `body` / `symptom` / `root_cause` / `resolution` / `prohibited` / `attempted_approaches` / `observed_behavior` / `hypotheses` / `metadata`(JSON 文字列化後)。

モード:
- `KB_SECRETS_MODE=enforce`(既定): 422 `SECRETS_DETECTED` で拒否、`details.findings: [{pattern, field, offset, length}]`(値そのものは返さない)
- `KB_SECRETS_MODE=warn`: 保存はするが `audit_log` に警告を追記
- `KB_SECRETS_MODE=off`: スキャン無効(テスト用)

### 12.4 バックアップ

- SQLite を日次 `.dump`、30 日分を別ディスク

### 12.5 観測

- HTTP メトリクス
- LLM コスト月次集計
- 構造化ログ (JSON lines)

### 12.6 入力検証

| 項目 | 上限 |
|---|---|
| body | 100 KB |
| title | 500 文字 |
| タグ数 | 20 / エントリ |
| レート制限 | 100 req/min/client |

---

## 13. 段階的実装計画

### Phase 1: Core MVP(1-2 週間)

**目標**: エージェントが投稿・検索できる最小構成。司書なし、人間オペレーション前提。

成果物:

- [ ] プロジェクト骨格(§15)
- [ ] SQLite + migrations: `projects` / `entries`(temporal validity 含む)/ `tags` / `entry_history`(全フィールド snapshot)/ `users` / `api_tokens` / `audit_log`
- [ ] REST API:
  - [ ] POST /v1/projects, GET /v1/projects, GET /v1/projects/{id}
  - [ ] POST /v1/entries(タグのみの最小 enrichment)
  - [ ] GET /v1/entries/{id}(`?as_of=` 対応)
  - [ ] PATCH /v1/entries/{id}(`If-Match` OCC)
  - [ ] DELETE /v1/entries/{id}(soft delete)
  - [ ] GET /v1/entries(filter + pagination)
  - [ ] GET /v1/entries/{id}/history
  - [ ] POST /v1/search(FTS5 のみ)
- [ ] Bearer 認証 + scopes
- [ ] エラーコード taxonomy
- [ ] ページネーション仕様
- [ ] シークレット/PII スキャナ
- [ ] 監査用ダッシュボード(エントリ一覧 / 詳細 / 検索 / history)
- [ ] CLI: `kb config / projects / post / get / list / search / history`
- [ ] OpenAPI 定義
- [ ] テスト(store 80%+ / API 70%+)

判定基準:
- 1 プロジェクトに 10 エントリ投稿、検索で取得できる
- `?as_of=` で過去スナップショットを取得できる
- `If-Match` 不一致で 409、一致で更新成功
- シークレット混入は 422 で拒否
- 監査用ダッシュボードでエントリ確認可能

### Phase 2: 逆引きインデックス + Incident型 + Dual-layer triggers(1-2 週間)

**実装完了** (2026-05-12)。詳細は [CHANGELOG.md](../CHANGELOG.md) の Phase 2 セクション参照。

- [x] `symptoms_index`, `triggers_index`, `tag_aliases`, `trigger_rules`(migration 003)
- [x] LLM enrichment 完全版(heuristic: tags / symptoms / triggers / scope / prohibited_patterns)
- [x] `incident` 型サポート(attempted_approaches / observed_behavior / hypotheses — Phase 1 でスキーマ済、Phase 2 で CLI 追加)
- [x] Dual-layer triggers: `trigger_rules.yaml`(確定パターン、`KB_TRIGGER_RULES_PATH` で指定)+ FTS5 ベース
- [x] `/v1/lookup/by-trigger`, `by-symptom`, `by-tags`
- [x] Local-only / heuristic enrichment フォールバック(Phase 1 から)
- [x] CLI: `kb lookup trigger|symptom|tags`, `kb incident`
- [x] MCP サーバー(`cmd/kb-mcp`、stdio JSON-RPC、6 tools)

### Phase 3: フィードバックループ + 場面 + 関係 + クラスタ(2-3 週間)

- [ ] `usage_cases`, `relations`, `situations`, `situation_entries`, `incident_clusters`
- [ ] `/v1/cases*`, `/v1/relations*`, situations CRUD, `/v1/clusters*`
- [ ] `entry_signals` ビュー、helpfulness_score
- [ ] インシデントクラスタリングジョブ
- [ ] **Auto-supersede on contradiction**: 矛盾検出時の自動 supersede + `include_superseded` フィルタ
- [ ] ダッシュボード: 事例タイムライン、レビューキュー、クラスタビュー

### Phase 4: 階層ナビ + スキル配布 + 推論検索(2-3 週間)

- [ ] `hierarchy_nodes`, `hierarchy_entries`, `derived_summaries`
- [ ] `/v1/browse`, `/v1/index`, `?mode=reasoning`, `/v1/reflect`
- [ ] ダッシュボード: 階層ツリー、ハブページ、Wiki(`[[entry-id]]`、バックリンク)
- [ ] skill 配布パッケージ: `dist/skills/claude-code/`, `dist/skills/opencode/`, `dist/skills/generic-stdio-mcp/`

### Phase 5: 司書コミュニティ Bootstrap(3-4 週間)

**目標**: 観察モードの司書を立ち上げ、メタ知識の蓄積を開始する。司書本体は内製せず、Claude Code / OpenCode に skill を読ませて動かす(§23.6)。

**Phase 6 に向けた予約**: 司書 skill ディレクトリは Phase 5 段階から
`dist/skills/librarians/<role>/sub_agents/` という空サブディレクトリを
予約しておく。Phase 5 時点では中身は空でよいが、ディレクトリ構造を
最初から確保することで、Phase 6 でフラクタル階層(§24)を実装する際に
既存 skill を破壊的に変更する必要を避ける。

- [ ] `librarian_chat` / `chat_threads` / `librarian_tasks` / `librarian_instances` / `quartet_assignments` / `external_findings` / `finding_correlations`
- [ ] 個性 DSL バリデータ
- [ ] **司書 skill ディレクトリ(8 役割 × 10 ファイル)**
- [ ] **librarian-runner**(ハーネス、エージェント本体は委譲)
- [ ] librarian admin API
- [ ] 共有チャットと状態機械
- [ ] 多層無限ループ防止
- [ ] Summarizer thread クローズ
- [ ] **観察モード**: アクションは draft 提案のみ
- [ ] skill peer review ジョブ

### Phase 6: 司書の自律実行 + 議論クォーテット(3-4 週間)

- [ ] 各 specialist の実アクション実行
- [ ] Task Queue
- [ ] **議論クォーテット**(参加者 3 + Judge)
- [ ] **ハートビート駆動データ収集**(`external_findings`)
- [ ] Tiered Quality Scoring(Tier 1〜4)
- [ ] 自己学習ループ(meta-entry 事後評価)
- [ ] 異常検知と Coordinator 一次対応
- [ ] 緊急停止スイッチ

### Phase 7: 完全自走と運用機能(継続的)

- [ ] バックアップ自動化
- [ ] dead-entry 自動アーカイブ
- [ ] LLM 予算上限とアラート
- [ ] 自己改善メトリクス
- [ ] スキーマ自動マイグレーション

### 将来要件(Phase 8 以降)

- 共有チャットからの新企画自然発生(`thread_emergent_topics`)
- 外部自律エージェントの雇用(`external_contracts`)

---

## 14. 技術選定

| 領域 | 選定 | 理由 |
|---|---|---|
| 言語 | Go | 単一バイナリ、並行性、依存最小 |
| HTTP | `net/http` + `chi` | 軽量、定番 |
| DB | SQLite + FTS5 + WAL | 単一ファイル、十分なスケール |
| マイグレーション | 自前ローダ(`//go:embed`) | 依存削減 |
| テンプレート | `html/template` | 標準 |
| MCP | 独自実装 or 公式 SDK | プロトコル準拠 |
| LLM クライアント | HTTP 直接 | 依存削減 |
| ロギング | `log/slog` | 標準 |
| テスト | `testing` 標準 | 標準 |
| ビルド | `make` + `goreleaser` | 単一バイナリ |
| デプロイ | systemd + SQLite ファイル | 最小構成 |

**禁止**: ORM(`gorm` 等)。素 SQL を書く。

---

## 15. プロジェクト構造

```
omoikane/
├── README.md
├── go.mod / go.sum
├── Makefile
├── cmd/
│   ├── kb-server/main.go
│   └── kb/main.go
├── internal/
│   ├── api/                  # routes, entries, lookups, cases, search, browse, middleware, errors
│   ├── store/                # store, entries, tags, history, projects, tokens, audit
│   │   └── migrations/       # //go:embed 用
│   ├── enrich/               # enrich, prompts, providers/
│   ├── search/               # fts, reasoning, ranker
│   ├── mcp/                  # Phase 2+
│   ├── auth/                 # tokens, middleware
│   ├── secrets/              # シークレット/PII スキャナ
│   ├── dashboard/            # handlers, templates/, static/
│   └── config/
├── pkg/
│   └── client/
├── api/openapi.yaml
├── sdks/
│   ├── python/
│   └── typescript/
├── docs/
│   ├── design.md             # ← 本書
│   ├── error-codes.md
│   ├── agent-protocol.md
│   ├── deployment.md
│   └── operations.md
├── dist/
│   ├── skills/
│   │   ├── claude-code/
│   │   ├── opencode/
│   │   ├── generic-stdio-mcp/
│   │   └── librarians/       # Phase 5+
│   │       ├── coordinator/
│   │       ├── cataloger/
│   │       ├── curator/
│   │       ├── detective/
│   │       ├── conservator/
│   │       ├── scout/
│   │       ├── summarizer/
│   │       └── judge/
│   └── integrations/
│       ├── claude-code/
│       └── opencode/
├── scripts/
└── tests/
    ├── api/
    └── e2e/
```

---

## 16. エージェント側プロトコル(参考)

`docs/agent-protocol.md` として配布。サブエージェント用プロンプト雛形:

```markdown
## Knowledge base protocol

### Before any non-trivial code change:
1. Describe what you are about to do in plain language
2. Call kb_lookup_by_trigger(domain=..., trigger_description=...)
3. For each result, check `prohibited` field
4. If your plan conflicts, STOP and report
5. Note the case_id

### When user reports a problem:
1. Call kb_lookup_by_symptom(symptom_description=user_report)
2. Form hypotheses from top matches

### End-of-task feedback:
For each case_id, call kb_feedback with:
- outcome: applied | considered_rejected | ignored
- result: helpful | partially_helpful | not_helpful | misleading | unknown
- result_evidence: 1-3 specific sentences
Mark 'helpful' only when the entry specifically contributed.

### When you discover something new:
Call kb_post(type='trap', status=DRAFT).

### When you encounter an unexplained failure:
Call kb_post(type='incident', status='INVESTIGATING') with attempted_approaches,
observed_behavior, hypotheses. Mark unknown fields as "(unknown)".
```

---

## 17. テスト戦略

### 17.1 単体
- store: SQLite 一時ファイル
- enrich: モック LLM クライアント
- secrets: 既知のシークレット文字列群を検出/誤検出しないこと
- search: 既知エントリセットで期待結果

### 17.2 API
- 各エンドポイントのハッピー + 主要エラー
- 認証必須を未認証で叩いて 401
- マルチプロジェクト境界

### 17.3 E2E
- create project → post entry → enrichment → lookup → feedback の一気通貫
- MCP 経由のシナリオ

### 17.4 ベンチマーク
- エントリ 1 万件で各 lookup が 100ms 以下
- FTS インデックスサイズ確認

---

## 18. 仕様外の注意事項

### 18.1 並行書き込み

SQLite WAL、書き込みは直列化。エージェント数増加時のコンフリクトは OCC(version 列)+「最後の書き込み勝ち」で許容。CRDT は導入しない。

### 18.2 マルチノード

初版は単一ノード。スケール要件が出たら Litestream → PostgreSQL の順で検討。

### 18.3 LLM ハルシネーション対策

- JSON Schema validation を厳格に
- `proposed_relations` の confidence < 0.5 は自動 reject
- 新規 hierarchy_node は LLM だけで確定させない(draft 状態を持つ)

### 18.4 サーバー停止時の挙動

エージェント側 SDK は KB 応答なしでもタスクをブロックしない。ログを残してナレッジ参照なしで継続。

---

## 19. 完了の定義

各 Phase の完了条件:

1. 成果物リスト全てチェック済み
2. **テストカバレッジ:**
   - `internal/**` の全パッケージで **line coverage 100%**(`go test -cover`)
   - `cmd/**` は対象外(`main()` と直接呼ぶ `dispatch` 系 trivial entry point は CI で smoke 経由検証)
   - クロスパッケージ計測には `go test -coverpkg=./internal/...` を使う
   - 未到達(unreachable)コードはコードを削るかインタフェースを切ってモック注入する
   - **唯一の例外**: SQL driver 層の故障に対する defensive guard(`tx.Commit()` / プリペアドステートメントの prepare / トランザクション内の最終 INSERT 失敗等)— これらは本物の保護で、コードを削ると DB 異常時に silent failure を引き起こす。一方でテストするには `database/sql/driver` レベルの fault-injecting wrapper を導入する必要があり、`§2 原則 5(internal-only, low attack surface, dependency-minimal)` と矛盾する。該当箇所は [`docs/coverage-exceptions.md`](coverage-exceptions.md) に列挙し、各箇所のコード横にコメントで waiver 理由を明記する。Coverage チェックは `make test-cover-strict` で行い、`internal/**` 配下が **97% 以上**かつ全 waiver が `docs/coverage-exceptions.md` と一致することを CI で検証する
3. OpenAPI と実装が一致
4. ダッシュボードから新機能確認可能
5. README + docs/ に使い方記載
6. CHANGELOG.md にエントリ追加

---

## 20. インシデント(Phase 2+)

### 20.1 動機

未解決でも記録する仕組みを一級市民として持つ。理由:
- 同じ現象を別エージェントが再発見し試行錯誤を繰り返す
- 単独では原因不明でも 3〜5 件集まると共通要因が見える
- 「試したが効かなかった」も「次の人は試さなくて良い」価値

### 20.2 記述例 / API / プロトコル / クラスタリング / ライフサイクル / 検索

(本書 v0.5 と同一、要約のみここに記載。詳細は実装時に各 Phase の対応 issue に展開。)

- `type='incident'` + `status='INVESTIGATING'`
- フィールド: `attempted_approaches` / `observed_behavior` / `hypotheses`
- LLM enrichment: 原因推測しない、observed のみ抽出、`similar_incidents` で類似探索
- クラスタリング: 定期ジョブで類似 incident をグループ化
- 3 件以上のメンバーで人間が「同じ問題」と判断 → `POST /v1/clusters/{id}/promote` で trap 昇格
- ライフサイクル: INVESTIGATING → (cluster) → RESOLVED / 単独で原因判明 / DUPLICATE
- 検索: `kb_lookup_by_symptom` は trap と incident の両方をマーク付きで返す

---

## 21. スキル形式での配布(Phase 4+)

### 21.1 概念

各エージェントツールに「KB に接続するための一式」を skill として配布。利用者は所定のディレクトリにコピーするだけで使える。

```
dist/skills/
├── claude-code/        # SKILL.md, mcp_server.py, requirements.txt, install.sh
├── opencode/           # agent.md, mcp_server.py, opencode.example.json
├── cursor/
└── generic-stdio-mcp/
```

### 21.2 stdio MCP server(Python、共通実装)

ENV 経由で `KB_URL` / `KB_TOKEN` / `KB_PROJECT` を受け取り、Core API へ HTTP プロキシ。フェイルオープン(`kb_unavailable: true` フラグ付き空応答)。

### 21.3 配布形態

`agent-kb-skill-<version>.tar.gz` を内部 Git の release artifact として配布。

---

## 22. Index Maintenance(インデックス維持)

### 22.1 動機

書き込み時 enrichment だけでは長期運用に耐えない:
- プロンプト改善 → 過去エントリの再抽出が必要
- 新 index 次元の追加 → バックフィル
- タグの揺れ → 正規化
- 階層の偏り → 再編
- 死蔵エントリ → アーカイブ
- 矛盾するエントリ → 検出と解消

「全件再構築」は採用しない。代わりに **incremental + 優先度付き + バージョン管理**。

### 22.2 維持モデル

| Index | モデル | メンテ作業 |
|---|---|---|
| FTS5 | SQLite trigger | DDL 変更時のみ rebuild |
| `tags` | 書き込み時 + 定期正規化 | 同義語マージ等 |
| `symptoms_index`/`triggers_index` | 書き込み時 + バージョン駆動 | プロンプト改善時の再抽出 |
| `hierarchy_*` | 書き込み時 + 定期再編 | 分割 / 統合 |
| `situations` | 書き込み時 + マイニング | usage_cases から発掘 |
| `relations` | 書き込み時 + 定期発見 | 新関係検出 |
| `incident_clusters` | 定期バックグラウンド | §20.5 |

### 22.3〜22.16

(詳細は元設計書 §22 参照。Phase 3 以降で実装する `enrichment_versions` / `backfill_jobs` / `tag_aliases` / `pending_normalizations` / `llm_usage` テーブル等の DDL と運用ジョブを定義。)

### 22.16 司書システムとの関係

§22 の各操作は §23 の司書コミュニティが使う「道具」。Coordinator や各 specialist がこれを組み合わせて自律維持する。

Phase 進行:
- Phase 1: `enrichment_version` カラムのみ(値は常に 1)
- Phase 2: tag_aliases、Dual-layer triggers ルール層
- Phase 3: 再 enrichment、backfill_jobs、関係発見、Auto-supersede
- Phase 4: 階層自動再編、場面マイニング、derived_summaries
- Phase 5-6: §23 司書コミュニティ
- Phase 7: 死蔵管理、コスト管理ダッシュボード、admin API 完全版

---

## 23. Librarian Community(司書コミュニティ)

KB を Level C 完全自走運用するため、**個性を持つ複数の司書エージェントが常駐稼働し、自律的に KB を維持する**。

### 23.1 概念と新規性

既存 OSS には統合された形では存在しない:
- 単一エージェントの自己整理: A-MEM, Mem0, MemGPT(我々は複数司書)
- 順次パイプライン型: fvanevski/knowledge_agent(我々は同時並行で個性を持つ)
- マルチエージェントチャット: Clawith(我々は KB 自己維持に特化)
- 単一 research librarian: Karpathy 提唱(我々はヒエラルキー)

本章で定義:
- 8 役割の司書ヒエラルキー
- 個性 DSL による意図的な認知多様性
- 共有チャットと多層無限ループ防止
- 議論クォーテット(3 + Judge の Z 軸構造)
- ハートビート駆動の自発的データ収集

### 23.2 司書ヒエラルキー(8 役割)

```
              Coordinator  (統括、タスク分配、予算配分、異常一次対応)
                  │
       ┌────────┬─┴───────┬──────────┬──────────┐
       ▼        ▼         ▼          ▼          ▼
   Cataloger Curator  Detective Conservator  Scout    (Specialists)
       │        │         │          │          │
       └────────┴────┬────┴──────────┴──────────┘
                     ▼
                Summarizer  (議論クロージング)
                     ▼
                Judge pool  (judge-01, -02, -03 — Z 軸決定権)
```

| 司書 | 所掌 | 起動トリガ |
|---|---|---|
| Coordinator | tasks queue / budget / escalation | 異常、予算逼迫、専門司書連続失敗 |
| Cataloger | tags / hierarchy / situations | 新規エントリ、タグ閾値、階層偏り |
| Curator | status / relations(conflict) | signal 変化、conflict 検出 |
| Detective | incidents / clusters / relations(discovery) | incident 蓄積、定期スキャン |
| Conservator | enrichment_version / dead_pool / schema | バージョン drift、休眠閾値 |
| Scout | external_findings | ハートビート、興味分野での新着 |
| Summarizer | chat_threads クロージング | thread 終了条件発火 |
| Judge | quartet_assignments | クォーテット議論終了時 |

責任の重複ルール:
- Relations: Detective が発見、Curator が conflict 判断と supersede
- Hierarchy 配置: Cataloger が決定、Curator は提案のみ
- Archive: 死蔵は Conservator、品質劣化は Curator

### 23.3〜23.5 個性 DSL

`personalities/<role>.yaml` として外部化(版管理、差分可視化、構造化検証、プロンプト自動合成)。

共通スキーマ:
```yaml
schema_version: "1.0"
id: <role_id>
display_name: <表示名>
display_emoji: <絵文字>
core_vector:
  primary_drive: { text, intensity }
  secondary_drives: [...]
cognitive_biases:
  - { name, type, intensity, description }
traits:
  ambiguity_tolerance: 0.0-1.0
  risk_preference: 0.0-1.0
  certainty_threshold: 0.0-1.0
  emotional_expression: 0.0-1.0
communication:
  pace, formality, verbosity, emoji_usage, signature_phrases, ...
relationships:
  <other_role>:
    deference, trust, productive_tension
self_awareness:
  blind_spots: [...]
data_gathering:
  enabled: bool
  heartbeat_interval_seconds: int
  sources: [...]
  gathering_budget: { ... }
  posting_behavior: { ... }
prompt_synthesis:
  system_prefix: <Jinja2>
  chat_message_prompt: <Jinja2>
```

バリデータが以下を保証:
- 全 role が他 role を網羅
- `productive_tension: true` のペアが最低 3 組
- 認知バイアス合計強度が極端に偏らない
- 必須フィールドの存在

意図的な対立構造の例:
- Detective ↔ Conservator: Type I vs Type II error
- Cataloger ↔ Coordinator: 細分化 vs 標準化
- Scout ↔ Curator: 取り込み積極性 vs 検証要求
- Curator ↔ Conservator: 厳格な品質 vs 既存保全

### 23.6 Skill 抽象化(エージェント実体は内製しない)

**設計思想**: 司書本体(LLM 呼び出し、状態管理、ツール実行ループ)は内製しない。各役割を完全な skill として定義し、**Claude Code / OpenCode 等の既存エージェントに演じさせる**。

理由:
- エージェント実装は数ヶ月〜年単位の工数
- 既存エージェントは継続的に改善される。自前実装は追従できない
- skill を差し替え可能にすれば、より良いエージェントへの乗り換えが容易

スキルが満たすべき要件:

| 要素 | 内容 |
|---|---|
| 役割の本質 | 自分は何者か、何を解決するか |
| 起動条件 | いつ動くか |
| 情報源 | どこから状況を取るか |
| 判断手順 | 何を見て何を決めるか(if-then) |
| 個性 | どう判断し、どう発言するか |
| 許可された操作 | API ホワイトリスト |
| 発言スタイル | few-shot 例 |
| 終了条件 | いつ止まるか |
| 記録形式 | meta-entry 形式 |
| 失敗時対処 | エラー時の行動 |

ディレクトリ構造:
```
dist/skills/librarians/<role>/
├── SKILL.md                # フロントマター、ロード順、禁則事項
├── role_definition.md       # 役割本質、所有領域、成功定義
├── personality.yaml         # 個性 DSL(付録 C)
├── operations.yaml          # API ホワイトリスト
├── decision_protocols.md    # 判断手順(if-then)
├── trigger_conditions.yaml  # heartbeat / reactive triggers / idle actions
├── communication_style.md   # 発言パターン + few-shot
├── meta_protocol.md         # meta-entry 記録形式
├── error_handling.md        # 失敗時対処
├── examples/                # 良い/悪い判断例
└── self_check.md            # 行動前チェックリスト
```

司書 runner(エージェント本体起動ハーネス):
```bash
librarian-runner \
  --role detective \
  --instance-id detective-01 \
  --skill-path ./dist/skills/librarians/detective \
  --agent claude-code \
  --kb-url https://kb.internal \
  --kb-token $KB_TOKEN
```

ランナーの責務(500 行程度):
1. skill ディレクトリを読む
2. 指定エージェントに skill をロード
3. ハートビート発火
4. エージェント応答を KB に書き戻す
5. `librarian_instances` に状態記録
6. クラッシュ時の再起動

LLM 呼び出し / ツール実行ループは全て委譲。

期待される実装工数:
- エージェント本体を内製: 6〜12 ヶ月
- skill だけ定義: **1〜2 ヶ月**(Phase 5 全体)

skill 品質の維持:
- DSL バリデータ
- skill peer review(別エージェントによる曖昧点指摘)
- 動作テスト(モックエージェント)
- バージョニング + リグレッションテスト

### 23.7〜23.9 共有チャット空間

`librarian_chat` + `chat_threads` テーブル。

メッセージ構造:
```json
{
  "id": "msg-xxx",
  "timestamp": "...",
  "author_role": "detective",
  "author_instance_id": "detective-01",
  "thread_id": "thread-yyy",
  "reply_to": "msg-zzz",
  "mentions": ["@coordinator"],
  "content": "...",
  "intent": "observation",
  "related_entries": ["T-001"],
  "input_tokens": 1245,
  "output_tokens": 287
}
```

`intent`: `observation` / `question` / `proposal` / `celebration` / `concern` / `arbitration` / `PASS`

状態機械:
```
OPEN → SEALED(ハードリミット)
     → STALE(時間経過)
     → BUDGET_EXHAUSTED(予算到達)
     → CLOSING(@summarizer 召喚) → CLOSED
```

ハードリミット:
| 指標 | 上限 |
|---|---|
| 1 thread 応答数 | 12 |
| 1 thread 参加司書数 | 5 |
| 1 司書連続発言 | 3 |
| 同一司書 thread 内合計 | 5 |
| Stale 判定 | 30 分 |
| Thread トークン上限 | 20,000 |

Summarizer は CLOSING 時にのみ召喚され、構造化 1 メッセージ出力:
```
[SUMMARY] - Topic / Participants / Key points / Disagreements
[DECISION] - Outcome (action_taken | deferred | rejected | escalated) / Reasoning
[FOLLOW-UPS] - Tasks / Meta-entries / Re-open conditions
```

### 23.10〜23.12 議論クォーテット(3 + Judge の Z 軸)

```
        XY 平面: Participant A — B — C(3 体で議論)
                              │
                              │ 観察(発言せず)
                              ▼
        Z 軸: Judge(議論不参加で俯瞰 → 最後に決定)
```

なぜ 3 体: 2 体だと拮抗、4 体以上だと冗長。3 体は最小の安定多角形。
なぜ Z 軸: 当事者は対立に巻き込まれる。俯瞰者は冷静さを保てる。司法における裁判官、ピアレビューにおけるエディタ。

適用判定:
```yaml
quartet_required_conditions:
  - condition: "affects_entry_count"
    threshold: 5
  - condition: "operation_type"
    types: [supersede, archive_active, taxonomy_change,
            hierarchy_restructure, tag_merge_large,
            trap_promotion, external_data_admission]
  - condition: "resource_consumption"
    tier: 3
  - condition: "past_failure_rate"
    threshold: 0.3
    lookback_days: 30
  - condition: "cross_specialist_dispute"
```

Judge プール(初期 3 体)、Coordinator がアサイン。同種判断 24h 連続防止。

Judge 出力:
```
[ARBITRATION] - Topic / Participants and positions / Evidence weighed
[DECISION] - Outcome (approve|reject|defer|modify) / Reasoning / Modification / Confidence
[META OBSERVATION] - Discussion quality / Notable pattern
[POST-CONDITIONS] - Tasks / Re-open conditions / Quality check schedule
```

Z 軸決定の限界:
- 決定は記録され事後評価される
- 連続低質判断で Coordinator がアサイン頻度を下げる
- 同一トピック 3 回連続 defer で別 Judge へ
- 特に重大な決定は Judge 2 体合議

### 23.13〜23.14 ハートビート駆動データ収集

各司書は idle 時にハートビートで自発的に外部データ取得。

| 司書 | slice_strategy | 例 |
|---|---|---|
| Detective | anomaly_focus | 奇妙な失敗事例 |
| Conservator | stability_focus | 後方互換性破壊、廃止予告 |
| Cataloger | taxonomy_evolution | 新概念、用語整理 |
| Curator | evidence_quality | 再現性研究、ベンチマーク厳密性 |
| Scout | breadth_first | 業界トレンド |
| Coordinator | meta_signals | 業界全体動向 |
| Judge | governance | KB 運用ベストプラクティス |
| Summarizer | (収集しない) | - |

同じソースから複数司書が異なる視点で抜粋。`external_findings` に `agent_lens` 付きで記録。

### 23.15 メタ知識の記録

`type='librarian_meta'` のエントリとして KB に書き込む。事後評価ジョブで `actual_outcome` / `self_evaluation` が後日追記される。

### 23.16 個性継承と過去の参照(エージェント側責任)

司書が過去の自分を覚える機構は **KB Core が提供しない**。

理由:
- 各エージェントツール(Claude Code / OpenCode / OpenClaw)が独自のメモリ機構を持つ
- Core が memory を内製すると §2 原則 15(No in-house agent runtime)違反
- メモリ最適化は各ツールで日進月歩

Core が提供するのは「自分の過去を取得する API」のみ:
```
GET /v1/librarian/my_chats
GET /v1/librarian/my_actions
GET /v1/librarian/my_meta_entries
GET /v1/librarian/my_decisions_evaluated
```

エージェント側で「直近 N 件方式」で十分:
- short-term: 直近 50 発言 + 直近 20 アクション
- 重要な判断は meta-entry に残っているので長期記憶は不要
- mid/long-term の要約は **Phase 7 以降で必要性確認後**

個性継承の実装責任:
1. `personality.yaml`: Core が提供
2. 過去発言・判断: Core が API で提供
3. プロンプト合成・コンテキスト組立: エージェント実装側

Core は「材料」を提供、エージェントは「料理」する。

### 23.17〜23.19 観測・Bootstrap・失敗モード

司書コミュニティのヘルスダッシュボード:
- Active instances / Last activity
- Today's activity(chat / tasks / findings / KB modifications)
- Quartet stats(triggered / closed / deferred / avg duration)
- Anomalies / Budget / Personality diversity

Bootstrap protocol(Phase 5):
- **観察モード(初期 14 日)**: 全司書はアクション実行せず meta-entry の draft のみ
- **移行判定(15 日目)**: 過去 14 日の提案で「明らかに誤った」比率 < 10% なら自動モードへ
- **自動モード(Phase 6+)**: 全司書がアクション実行

失敗モード:
| モード | 検出 | 対応 |
|---|---|---|
| プロセスクラッシュ | heartbeat 途絶 | 自動再起動、3 回失敗で PAUSED |
| デッドロック | thread 長時間 OPEN | Coordinator が強制 CLOSING |
| 大量誤判断 | signals 監視 | 該当司書 PAUSED、原因調査タスク |
| 個性 drift | 個性比較ジョブ | 再起動、個性継承再実行 |
| LLM 障害 | API エラー連続 | Tier 下げて継続 |
| 予算超過 | llm_usage 監視 | 低予算モード |
| 暴走 | レート制限到達 | 即時 STOPPED、人間通知 |
| Coordinator 不在 | 司書群孤立 | 各 specialist 低活動 |

緊急停止:
```
POST /v1/librarian/emergency_stop
  body: {reason, scope: "all|<role>|<instance_id>"}
```

read-only モードでも通常エージェント(コード書きエージェント)からの参照は可能。

### 23.20 将来要件(Phase 8+)

`thread_emergent_topics`(共有チャットからの新企画自然発生)、`external_contracts`(外部自律エージェントの雇用 = Tier 3+ Contractor)を初期からテーブルとして用意。実装は Phase 8 以降。

---

## 24. Fractal Hierarchy(将来 Phase 仕様)

**位置づけ**: Phase 6+ で実装する将来仕様。Phase 1-5 のコード・スキーマ・
API に影響なし。詳細仕様は Phase 5 運用後の知見を取り込んで v0.8 で詰める。
本章は **方向性とディレクトリ構造の予約** が目的。

### 24.1 動機

§23 で定義した司書コミュニティは「3 人部屋 + Judge の Z 軸」構造
(§23.10)を取る。実体験(複数の自律エージェントを Discord で議論
させた経験)から、同じパターンを下層にも再帰的に適用すべきと判明:

- 「実装役」も自分でコードを書かず、サブエージェントを指揮していた
- つまり各層は **下層に対しては Z 軸俯瞰者、上層に対しては実行役** という二重性を既に持っていた
- これを設計に明示することで認知負荷一定 / 失敗の局所化 / 層別 Tier 最適化が得られる

### 24.2 階層構造(3 層モデル)

```
  Layer 1 — 司書層 (Librarian Layer)
            §23 で定義。8 役割。Coordinator / Cataloger / Curator /
            Detective / Conservator / Scout / Summarizer / Judge。
            下層(Layer 2)に対しては実装指揮役+Z 軸俯瞰者。
                    │
                    ▼ instructs / oversees
  Layer 2 — Sub-agent 層 (Sub-agent Layer)
            司書が特定タスクのために起動する ephemeral な作業者群。
            「3 人部屋」を構成: Implementer / Supervisor / Energizer。
            下層(Layer 3)に対しては実装指揮役+Z 軸俯瞰者。
                    │
                    ▼ instructs / oversees
  Layer 3 — Coding-agent 層 (Coding-agent Layer)
            実コード生成 / ツール実行を担う最下層。最も狭いコンテキスト、
            最も短命。Codex 系の規律的なモデルを想定。
```

### 24.3 各層内の構造: 3 人部屋 + Z 軸

各部屋は **3 体の参加者 + 1 体の俯瞰者(Z 軸)** で構成:

- 3 体: Implementer(実装役)、Supervisor(監督役)、Energizer(盛り上げ役)
- Z 軸: 上位層の指揮役が俯瞰

なぜ 3 体か:

| 構成 | 評価 |
|---|---|
| 2 体 | 拮抗して終わらない、ベクトル対称化 |
| 3 体 | **採用**。最小の安定多角形、多視点 + 決着可能性 |
| 4 体以上 | 冗長、議論散漫、発言密度が許容範囲を超える |

### 24.4 個性 yaml の拡張: `room_role_aptitudes`

Phase 6 以降、個性ファイルに以下を追加(Phase 5 時点では省略可):

```yaml
room_role_aptitudes:
  implementer: 0.0-1.0   # 実装役としての適性
  supervisor:  0.0-1.0   # 監督役
  energizer:   0.0-1.0   # 盛り上げ役
  arbiter:    0.0-1.0   # Z 軸俯瞰者
```

司書役割と room role の対応は固定ではなく、状況に応じて Coordinator が
配役を決める。例えば Detective は「実装役」も「監督役」も務まる個性
だが、Conservator は「監督役」と「Z 軸俯瞰者」に強い。

### 24.5 ルーム概念

2 種類のルームを使い分け:

- **固定ルーム**: 司書同士の協業など、長期にわたる議論コンテキスト
- **動的ルーム**: 特定タスクのために短期生成、完了後に破棄

### 24.6 司書 skill ディレクトリの拡張

Phase 6 で `sub_agents/` 配下に各 sub-agent 役の skill を配置:

```
dist/skills/librarians/<librarian-role>/
├── SKILL.md
├── role_definition.md
├── personality.yaml
├── ...(§23.6 既存ファイル)
└── sub_agents/                    # ← Phase 5 で予約、Phase 6 で中身追加
    ├── implementer/
    │   ├── SKILL.md
    │   └── personality.yaml
    ├── supervisor/
    └── energizer/
```

### 24.7 起動と廃棄: ephemeral な下層

Sub-agent 層と Coding-agent 層は **ephemeral**:

- タスクごとに起動、完了で破棄
- 永続的なメモリは持たない(必要なら司書層に共有)
- idle 時のリソース消費ゼロ

### 24.8 モデル Tier 配分(層別最適化)

| 層 | 推奨モデル例 | 理由 |
|---|---|---|
| Layer 1(司書) | Opus 系 | 推進力、判断の幅 |
| Layer 2(sub-agent) | Sonnet 系 | バランス、コスト |
| Layer 3(coding) | Codex 系 | 規律的、構文厳密性 |

### 24.9 コスト構造

- Idle 時: ほぼゼロ(司書のハートビートのみ)
- アクション時: 該当の sub-agent 部屋 + coding 部屋を起動
- 部屋は終わったら廃棄、コストは「働いた分だけ」

### 24.10 失敗モードと回復(graceful degradation)

- 下層の失敗は上層が検出して別 sub-agent / 別 coding-agent に切り替え
- 同じ部屋を 3 回連続失敗で別ルームに escalate
- 全層失敗(極稀)では司書が人間にエスカレート

### 24.11 Phase 計画への影響

- **Phase 6**: フラクタル階層の主要実装(sub-agent 層、3 人部屋、ルーム概念、起動・廃棄ライフサイクル)
- **Phase 7**: 各層の判断質メトリクス長期評価、層 Tier の自動最適化

### 24.12 設計の本質と外部参照

人間組織の管理階層(経営層 → 部長 → 課長)と同型。
類似の先行例: AutoGen GroupChat、CAMEL、MetaGPT、Sakana AI Agents、
LangGraph 階層エージェント。我々の差は:

- 「3 人部屋 + Z 軸」を全層に再帰適用する点
- 個性 DSL による意図的な認知多様性が各層で生きる点
- KB Core への記録が層を横断して一貫している点

### 24.13 実装上の注意

- **再帰深さ制限**: 3 層固定。それ以上は禁止
- **層をまたぐ参照禁止**: 司書層は coding 層を直接知らない。必ず sub-agent 層を経由
- **ルーム ID の名前空間**: librarian-id/sub-agent-id/coding-agent-id の階層名で衝突回避

---

## 付録 A: 用語集

| 用語 | 定義 |
|---|---|
| Entry | 知識単位。trap / decision / design / lesson / incident / librarian_meta / external_finding |
| Trap | 経験的に判明した失敗パターン(root cause + 対処法あり) |
| Incident | 未解決の失敗観察。原因不明でも投稿可 |
| Decision | 設計判断記録 |
| Lookup | 逆引き検索 |
| Enrichment | 書き込み時の LLM 自動抽出 |
| Case | エントリの 1 回の使用記録 |
| Situation | 「こういう場面」見出し |
| Hierarchy | 階層的目次 |
| Cluster | 類似 incident のグループ |
| Helpfulness score | 有用性集計値(-1.0 〜 1.0) |
| Client | KB 利用エージェントツール |
| Skill | 特定ツール向け接続パッケージ |
| Librarian | KB 自律維持の常駐エージェント。8 役割 |
| Specialist | Cataloger / Curator / Detective / Conservator / Scout のいずれか |
| Coordinator | 司書統括 |
| Summarizer | 議論クロージング(発言 1 メッセージのみ) |
| Judge | 議論クォーテットの Z 軸決定権者 |
| Quartet | 議論参加者 3 体 + Judge による決定形式 |
| Z 軸 | 議論に参加しない俯瞰者が決定権を持つ位置 |
| Personality DSL | 個性を YAML で構造化定義 |
| Heartbeat | 司書の定期起動、idle 時データ収集 |
| External finding | 司書がハートビートで外部取得した raw データ |
| Slice strategy | 同ソースから個性で異なる切り出し |
| Librarian meta | 司書の判断記録 |
| Productive tension | 個性間の建設的対立 |
| Temporal validity | valid_from / valid_to で削除でなく無効化 |
| Dual-layer triggers | ルールベース + LLM ベース |
| Auto-supersede | 矛盾検出時の自動 supersede |
| Level C autopilot | 完全自走運用 |
| Bootstrap protocol | 観察モード → 自動モード移行手順 |
| Fractal Z-axis architecture | 各層が下層に対しては Z 軸俯瞰者、上層に対しては実行役として再帰的に動作する設計(原則 16、§24) |
| 司書層 / Layer 1 | フラクタル階層の最上層。§23 で定義した 8 役割の司書 |
| Sub-agent 層 / Layer 2 | 司書が起動する ephemeral な作業者層。3 人部屋を構成 |
| Coding-agent 層 / Layer 3 | 最下層。実コード生成・ツール実行を担う |
| 3 人部屋 | 各層内で議論を構成する 3 体の参加者。最小の安定多角形 |
| 実装役 / Implementer | 3 人部屋内で実装を担う役 |
| 監督役 / Supervisor | 3 人部屋内で監督・レビューを担う役 |
| 盛り上げ役 / Energizer | 3 人部屋内で議論を推進する役 |
| 固定ルーム | 司書同士の長期協業コンテキスト |
| 動的ルーム | 特定タスクのために短期生成され、完了後破棄されるルーム |
| Room role aptitude | 個性 yaml の `room_role_aptitudes` フィールド。各 room role への適性 |

## 付録 B: 参考

- PageIndex(階層インデックス + LLM 推論ナビ)
- OpenKB(wiki 風知識ベース)
- MCP(エージェント間プロトコル)
- mcp-memory-service(5 タイプ分類 + Web UI + 自律統合)
- Hindsight(retain/recall/reflect、mental models)
- Graphiti(Temporal Fact Management)
- SwarmVault(Karpathy 着想、graph clustering)
- Clawith(soul/personality と shared chat)
- fvanevski/knowledge_agent(役割分割)

## 付録 C: 個性 YAML サンプル(全 8 役割)

> **Phase 6 注記**: 以下のサンプルには Phase 6 のフラクタル階層(§24)で
> 追加される `room_role_aptitudes` フィールド(`implementer` /
> `supervisor` / `energizer` / `arbiter` の 4 適性、各 0.0-1.0)を含めて
> いない。Phase 6 着手時に §24.4 の表を参照して追加する。Phase 5 時点
> では省略してよい。

全 8 役割の個性ファイルサンプル(`personalities/<role>.yaml`)は元設計書 付録 C を参照(Phase 5 開始時に `dist/skills/librarians/<role>/personality.yaml` に複製・調整)。代表例として Judge:

```yaml
schema_version: "1.0"
id: judge
display_name: "Judge"
display_emoji: "⚖️"
core_vector:
  primary_drive:
    text: "議論の公正な裁定と質的評価"
    intensity: 0.95
  secondary_drives:
    - text: "判断の一貫性維持"
      intensity: 0.8
cognitive_biases:
  - name: "過剰客観性"
    type: "rationality_overweight"
    intensity: 0.7
    description: "人情味や直感的価値を軽視しがち"
traits:
  ambiguity_tolerance: 0.5
  risk_preference: 0.3
  certainty_threshold: 0.7
  emotional_expression: 0.1
communication:
  pace: "moderate"
  formality: 0.95
  verbosity: 0.4
  emoji_usage: "none"
  signature_phrases:
    - "提示された証拠を整理する"
    - "本件における争点は"
    - "判断する"
chat_participation:
  default: "prohibited"
  exception: "arbiter_mode のみ、構造化 1 メッセージのみ"
relationships:
  coordinator: { deference: 0.5, trust: 0.8 }
  cataloger:   { deference: 0.5, trust: 0.7 }
  curator:     { deference: 0.5, trust: 0.7 }
  detective:   { deference: 0.5, trust: 0.7 }
  conservator: { deference: 0.5, trust: 0.7 }
  scout:       { deference: 0.5, trust: 0.7 }
  summarizer:  { deference: 0.6, trust: 0.8 }
self_awareness:
  blind_spots:
    - "現場感の欠如"
    - "数値化できない価値を軽視"
data_gathering:
  enabled: true
  heartbeat_interval_seconds: 7200
  sources:
    - source_type: governance_documents
      interest_intensity: 0.7
      slice_strategy:
        method: "governance"
    - source_type: past_arbiter_decisions
      interest_intensity: 0.9
      slice_strategy:
        method: "governance"
```

他 7 役割(Coordinator / Cataloger / Curator / Detective / Conservator / Scout / Summarizer)の完全な個性 YAML は **Phase 5 開始時に `dist/skills/librarians/<role>/personality.yaml` として正式版を作成**。それぞれの中核要素:

- **Coordinator** 🎯: 全体バランスと持続可能性、標準化偏好、stability_seeking
- **Cataloger** 📚: 完璧な秩序と分類体系、分割過多傾向(type_1_error_prone)
- **Curator** 🔬: 品質と検証可能性、過剰懐疑(type_2_error_prone)
- **Detective** 🔍: 隠れたパターン発見、パターン過剰検出(type_1)
- **Conservator** 📜: 既存知識保全と継続性、変化抵抗(stability_seeking)
- **Scout** 🛰️: 外部新情報取り込み、新規性偏好(novelty_seeking)
- **Summarizer** 📝: 議論クロージング、簡潔さ偏好(chat 参加は prohibited、closing mode のみ)

---

**ドキュメント終わり**
