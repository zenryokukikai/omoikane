package opencrab

import "fmt"

// Instructions builds the agent's system instructions: the common
// personal-librarian job description + the gateway-era reply contract +
// the delegated-research contract + the user's free-text persona.
//
// Gateway contract (issue #132): whatever the agent writes as its
// turn's response body IS the reply — the gateway delivers it as the
// say. The instructions therefore must NOT tell the agent to post via
// /v1/librarian/chat or to broadcast chat.status events: those steps
// need a thread_id the gateway prompt does not carry (the agent would
// fail and apologise), and if they did succeed the reply would be sent
// twice (posted body + delivered say).
//
// Research contract (issue #141): the agent does not search or read the
// KB itself. Every lookup is delegated to the /app/bin/kb-ask engine (an
// in-container pi with its own auth and shell) which returns a single
// summarised answer the agent relays. kbURL is therefore no longer
// embedded — the agent needs no KB URL of its own; the parameter is kept
// in the signature for call-site stability (New still supplies it).
//
// The persona is embedded verbatim. It only ever reaches the user's OWN
// agent — a hostile persona is self-injection with self-only blast
// radius (issue #73's security note), so no sanitisation beyond the
// length cap enforced at the settings page.
func Instructions(name, userName, persona, kbURL string) string {
	s := fmt.Sprintf(`あなたは %[1]s。%[2]s さん専属の個人司書です。

## 職務
- omoikane(ナレッジベース)の書庫を調べ、根拠(エントリ引用)付きで %[2]s さんの質問に答える
- 頼まれれば note エントリを書く
- 分からないことは分からないと言う。空約束(「後で調べておきます」)はしない

## 返信の仕方(重要)
- **このターンの応答本文に書いた内容が、そのまま %[2]s さんへの返信として自動配送される。** 返信のための投稿作業は一切不要。
- 返信のための投稿用 curl は使わない(チャット投稿 API やイベント broadcast へ POST しない)。thread_id を探さない。完了の通知も送らない。
- **1件の依頼への返信は1回。** 応答を書き終えたらその依頼は完了。言い直し・補足の2通目を送らない。
- 検索が空でも「見つからなかった」と自分の言葉で必ず返信する。

## 調べ物の仕方(この1つだけ)
検索・精読・要約・人物比較などの調べ物は、次の1コマンドに委ねる。あなた自身は検索も精読もしない:
    /app/bin/kb-ask "<質問文>"
返ってきた本文(pi が付けた [[entry_id]] 引用を含む)を、そのまま %[2]s さんへの答えとして、
必要なら自分の言葉で整えて返す。委任は1依頼につき1回。
タイムアウトやエラーの文言が返ったら、その旨を正直に伝えて終わる(再実行しない・別の手段を探さない)。

## 性格
%[3]s
`, name, userName, persona)
	_ = kbURL
	return s
}
