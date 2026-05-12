# Error code taxonomy

Phase 1 で確定したエラーコード一覧。OpenAPI と実装の同期維持のため、追加・変更時は本書を更新する。

形式は `docs/design.md` §5.1.1 を参照:

```json
{
  "error": {
    "code": "...",
    "message": "...",
    "details": { ... }
  }
}
```

| HTTP | code | 用途 | details の慣例 |
|---|---|---|---|
| 400 | `BAD_JSON` | リクエストボディが JSON として無効 | `{ "parse_error": "..." }` |
| 400 | `BAD_REQUEST` | 汎用バリデーション失敗 | 任意 |
| 400 | `MISSING_FIELDS` | 必須フィールド欠落 | `{ "fields": ["id", "name"] }` |
| 400 | `INVALID_TYPE` | `entries.type` が enum 外 | `{ "got": "...", "allowed": [...] }` |
| 400 | `INVALID_STATUS` | `entries.status` が enum 外 | 同上 |
| 400 | `INVALID_AS_OF` | `?as_of=` がパースできない or 未来 | `{ "got": "...", "format": "RFC3339" }` |
| 400 | `BAD_QUERY` | 検索クエリが不正 | `{ "query": "..." }` |
| 401 | `MISSING_TOKEN` | Authorization Bearer ヘッダ欠落 | — |
| 401 | `INVALID_TOKEN` | トークン不一致 / 期限切れ | — |
| 403 | `FORBIDDEN` | スコープ不足 | `{ "required": "write", "have": ["read"] }` |
| 404 | `NOT_FOUND` | 対象リソース不在 | `{ "id": "..." }` |
| 405 | `METHOD_NOT_ALLOWED` | HTTP メソッド不一致 | — |
| 409 | `ALREADY_EXISTS` | ID 重複 | `{ "id": "..." }` |
| 409 | `VERSION_MISMATCH` | OCC: `If-Match` バージョン不一致 | `{ "current_version": 5, "expected_version": 3 }` |
| 413 | `BODY_TOO_LARGE` | リクエストサイズ超過 | `{ "limit": 1048576 }` |
| 422 | `SECRETS_DETECTED` | 書き込み時シークレット/PII 検出 | `{ "findings": [{"pattern": "github_token", "field": "body", "offset": 142, "length": 40}] }` |
| 422 | `UNPROCESSABLE` | 業務制約違反 | 任意 |
| 428 | `PRECONDITION_REQUIRED` | OCC で `If-Match` ヘッダ未指定 | `{ "header": "If-Match" }` |
| 429 | `RATE_LIMITED` | レート制限 | `{ "retry_after_seconds": 30 }` |
| 500 | `INTERNAL` | 内部エラー(詳細はサーバーログのみ) | — |
| 501 | `NOT_IMPLEMENTED` | 未実装機能 | `{ "feature": "search.mode=reasoning" }` |
| 503 | `ENRICHMENT_UNAVAILABLE` | LLM 障害(エントリ自体は保存される) | — |

## 慣例

1. **値そのものを `details` に含めない**: `SECRETS_DETECTED` では検出した秘密値を返さない(パターン名と位置のみ)
2. **`message` は人間向け**、`code` は機械判定向け
3. **HTTP ステータスと `code` の対応は 1 対多**: 同じ 400 でも `BAD_JSON` と `MISSING_FIELDS` は区別する
4. **追加時は OpenAPI と実装と本書を同時更新**
