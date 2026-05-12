# omoikane — Agent Knowledge Base (v0.7)

汎用ナレッジサーバー。AI コーディングエージェント(Claude Code / OpenCode / Cursor / Cline 等)が、過去の経験 — **罠(trap)** / **判断記録(decision)** / **設計知見(design)** / **教訓(lesson)** / **未解決インシデント(incident)** — を参照・蓄積・発見できる基盤。最終的には **司書コミュニティ(8 役割エージェント)による Level C 完全自走運用** を目指す。

正準仕様は [`docs/design.md`](docs/design.md)。

## 実装ステータス

- **Phase 1 MVP**: 完了(エントリ + プロジェクト + temporal validity + OCC + 認証 + 監査 + ダッシュボード + CLI + 検索)
- **Phase 2 reverse-index lookups**: 完了(`/v1/lookup/by-trigger` 規則層+FTS、`by-symptom`、`by-tags`、`tag_aliases`、`trigger_rules.yaml`、MCP stdio adapter)
- **Phase 3 feedback loop**: 完了(`usage_cases` + `entry_signals` + `review_queue`、`relations` graph と auto-supersede on `conflicts_with`、`situations` 逆引き、`incident_clusters` 自動クラスタリング、`/v1/lookup/by-situation`、`helpfulness_score` ランキング、MCP `kb_feedback`/`kb_link`/`kb_relations`/`kb_lookup_by_situation`)

## Phase 1 MVP の範囲

- SQLite + FTS5 永続化
  - テーブル: `projects` / `entries` / `tags` / `entry_history` / `users` / `api_tokens` / `audit_log`
  - エントリの **temporal validity**(`valid_from` / `valid_to` / `superseded_by` / `invalidation_reason`)
  - **version 列による楽観ロック (OCC)** — PATCH 時に `If-Match` ヘッダで保護
- REST API:
  - `POST/GET /v1/projects`、`GET /v1/projects/{id}`
  - `POST /v1/entries`(タグ抽出のみの最小 enrichment)
  - `GET /v1/entries/{id}` — `?as_of=2026-05-01T00:00:00Z` で過去スナップショット
  - `PATCH /v1/entries/{id}` — `If-Match: <version>` 必須
  - `DELETE /v1/entries/{id}` — soft delete (`status=ARCHIVED`)
  - `GET /v1/entries` — pagination (`limit` / `offset`、レスポンスに `pagination.total`)
  - `GET /v1/entries/{id}/history`
  - `POST /v1/search` — FTS5 全文検索
- Bearer トークン認証(SHA-256 ハッシュ、scope: `read` / `write` / `admin`)
- **エラーコード taxonomy** — `docs/error-codes.md` に正準リスト、すべて `{"error":{"code","message","details"}}` 形式
- **シークレット/PII スキャナ** — 書き込み時に AWS キー / GitHub トークン / JWT / メール / クレカ等を検出して 422 で拒否
- **監査ログ (`audit_log`)** が Phase 1 から有効。書き込み API すべてを記録
- 監査用ダッシュボード(SSR、読み取り専用、`?as_of=` 対応)
- CLI `kb`(`post` / `get` / `search` / `history` / `list` / `projects` / `config`)
- OpenAPI 3 定義
- テストカバレッジ目標: store 80%+ / API 70%+

## ビルド・起動

```bash
make build           # bin/kb-server, bin/kb をビルド (sqlite_fts5 タグ付き)
make test            # 全テスト
make test-cover      # カバレッジ計測
make run             # サーバー起動 (port 8080)
```

主な環境変数:

| 変数 | デフォルト | 説明 |
|---|---|---|
| `KB_HTTP_ADDR` | `:8080` | HTTP リスナー |
| `KB_DB_PATH` | `./kb.db` | SQLite ファイル |
| `KB_DASHBOARD_OPEN` | `0` | `1` でダッシュボード認証無効(開発時のみ) |
| `KB_LLM_PROVIDER` | (空) | `anthropic` / `openai` / `local` / (空) |
| `KB_LLM_MODEL` | provider 依存 | |
| `KB_LLM_API_KEY` | (空) | |
| `KB_LLM_ENDPOINT` | provider 既定 | |
| `KB_LLM_MONTHLY_BUDGET_USD` | `0`(無制限) | |
| `KB_REQUEST_BODY_MAX` | `1048576` | バイト |
| `KB_SECRETS_MODE` | `enforce` | `enforce` / `warn` / `off` |
| `KB_TRIGGER_RULES_PATH` | (空) | Phase 2: `trigger_rules.yaml` ファイルへのパス。空ならスキップ |
| `KB_CLUSTER_INTERVAL` | `0` | Phase 3: 背後の incident クラスタリング間隔(例 `30m`)。`0` で無効 |
| `KB_CLUSTER_THRESHOLD` | `0.4` | Phase 3: Jaccard 類似度しきい値 |
| `KB_CLUSTER_MIN_MEMBERS` | `2` | Phase 3: クラスタとして発行する最小メンバー数 |

## 初回セットアップ

```bash
# 1. 初回起動でマイグレーションが走り kb.db が生成される
./bin/kb-server &

# 2. admin トークン発行
./bin/kb-server admin-token --user admin --scopes read,write,admin

# 3. CLI 設定
./bin/kb config set url http://localhost:8080
./bin/kb config set token <発行されたトークン>

# 4. プロジェクト + エントリ
./bin/kb projects create --id demo --name "Demo project"
./bin/kb post --project demo --type trap --title "..." --file sample.md
./bin/kb search "warmup"
./bin/kb get T-XXXX --as-of 2026-05-01T00:00:00Z
```

## ライセンス

内部利用専用。外部公開・SaaS 化は Out of scope。
