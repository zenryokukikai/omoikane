# Agent Knowledge Base — 変更履歴

**対象ドキュメント**: `docs/design.md`
**メンテナンス方針**: 設計書を改訂するたびに、対応するバージョンセクションを本書に追記する。

各バージョンの記述様式:
- **背景**: 何を解決しようとしたか
- **変更点**: 設計書のどの章・節がどう変わったか
- **設計判断の根拠**: なぜその設計を選んだか、検討した代替案
- **影響範囲**: 既存実装への影響、Phase 計画への影響

---

## v0.10(2026-06-01)

### 背景

書き込み時スキャナ(§12.3)が認証情報(秘密)と PII(email/カード/IP)を
同一視して enforce で全部ハードブロックしていた。結果、**メールを扱う
プロジェクトが entry を一切書けず**、SSH リモート `git@github.com:...` が
「email」と誤検知されて拒否されるなど、正当な利用を壊していた。

### 変更点

- §12.3: スキャナを**secret(認証情報)とPII(email/card)の2系統に分離**し、
  それぞれ独立スイッチ化。`KB_SECRETS_MODE`(既定enforce)/ `KB_PII_MODE`
  (**既定off**)。既定では PII を検閲せず(email/SSHリモートが書ける)、
  認証情報漏洩だけ拒否。PII が必要なプロジェクトを持つデプロイは
  `KB_PII_MODE=enforce|warn` で on にできる。実装は secrets.go を
  secretPatterns/piiPatterns + ScanSecrets/ScanPII に再構成、config に
  PiiMode 追加、rejectIfSecrets で両モード独立適用。
- example トークン対策を明記: secret は構造マッチなので example でも弾く →
  セキュリティ知識を書くときは `KB_SECRETS_MODE=warn|off`。

### 設計判断の根拠

- **PII 検閲は既定 off。** omoikane は単一組織内で共有され、プライバシー
  境界は**プロジェクトスコープの分離**で確保する。書き込み時 PII ブロックは
  誤検知だらけで使い勝手を著しく損なう。ただし完全削除でなく**スイッチで
  on にできる**形にして、PII を扱うプロジェクトの選択肢を残す。
- **secret(認証情報)ブロックは既定 enforce で維持。** 流出すると即悪用
  される鍵・トークンは拒否し続ける(github token の 422 テスト不変)。
  example で困る場合のみ warn/off に切れる。
- 安全側に倒しすぎてツールが使えなくなる < まず使い勝手の良いものを作る。
  ハードコードでなく**スイッチで運用者が安全と使い勝手を選べる**ようにする。

### 影響範囲

- 既定で email/カード/SSHリモートを含む entry が書けるようになる(本番反映)。
- secret 検知の既定挙動は不変。Phase 計画への影響なし。

---

## v0.12(2026-06-02)

### 背景

司書が整理した出力(cataloger 要約・detective 提案・curator 解決)が**英語のみ**で書かれており、人間がダッシュボードでレビューできず、また日本語キーの検索からも不可視になっていた。detective の SKILL は「cataloger の要約は英日併記である」ことを前提に横断言語 dedup を組んでいたが、**cataloger 正本にも共通土台 `_template` にも併記指示が一文字も無く**、源泉が要求していない性質を当てにする片肺仕様になっていた(design-discipline 違反の実例)。

### 変更点

- design.md に §23.15.1「司書出力の言語(英日併記)— 全 role 共通ハウスルール」を新設。
- 正本 `dist/skills/librarians/_template/SKILL.md` の「Writing in Phase 5」に bilingual ハウスルールを追加(全 role が継承する単一の source of truth)。
- 正本 `cataloger/AGENTS.md`、`workspace-example/{cataloger,detective,curator}/SKILL.md` の旧「Language preservation(任意翻訳)」を「英日併記 REQUIRED」へ強化。
- 実走 workspace(`omoikane-{cataloger,detective,curator}`)の SKILL を同じ規則に更新(次回スケジュール実行から反映)。

### 設計判断の根拠

- **構造は英語固定 / 散文は併記**:機械可読キー(`rel_type`/`kind`/`entry_id` 等)を英語で固定することで detective の横断検索と API のスケルトンを安定させ、人間可読の散文だけ両言語にする。全文翻訳は機械処理を壊し、英語のみは人間と日本語検索を取りこぼす。両者の中間が最小コストで両立する。
- **共通土台に一箇所だけ書く**:役割ごとに散らさず `_template` に置くことで、将来の新 role も自動継承し、仕様が一本化される。

### 影響範囲

既存の英語のみエントリは遡及変換しない(次回以降の出力から併記)。サーバ実装・API は無変更(本文テキストの規約のみ)。Phase 計画に影響なし。

---

## v0.11(2026-06-01)

### 背景

scout が毎日 external_finding を溜めるようになり、「前日分を朝イチで整理して
読める形にしたい」という運用ニーズが出た。配信(omoikane外)ではなく、KB内に
**日次ジャーナル記事**として残す形が、知識アーティファクトとして筋が良い。

### 変更点

- §23 役割表 + summarizer bundle: summarizer を「チャット要約 + 日次ジャーナル」
  に拡張。前日の external_finding + 新規知見 + 司書活動量を1本に蒸留。
- **ジャーナルだけ ACTIVE で投稿**(Phase 5「司書は DRAFT のみ」の明示的例外)。

### 設計判断の根拠

- 新ロール(9番目)を足さず、summarizer の essence「散在信号を durable な
  可読形に蒸留」にジャーナルを含める(チャットも日次も同じ蒸留)。
- **ジャーナルは ACTIVE**。読まれ検索されるために存在する記事であり、DRAFT
  では目的を果たせない。これは限定的・意図的な例外で、根拠を bundle と
  design に明記(他の司書出力・スレッド要約は DRAFT のまま)。

### 影響範囲

- サーバ実装変更なし(既存 entries/list API で前日分を取得、ACTIVE 投稿)。
- summarizer の runnable workspace を実装(リポジトリ外、朝イチ launchd)。

---

## v0.9(2026-05-31)

### 背景

detective を実運用に乗せた結果、提案(`duplicate_of`/`related`/
`conflicts_with`)を **消費して resolve する curator 側の経路が未設計**
だと判明。detective bundle は元々 `conflicts_with` の resolution を
curator に投げる前提だったが、(a) `duplicate_of` の解決アクションが
curator に無い、(b) Phase 5 では detective がエッジを作らず DRAFT 提案
しか出さないため、curator が提案を拾う導線が無い、という2つの穴があった。

### 変更点

- §20.2: dedup ループの閉じ方を明記。detective の `relation_proposal`
  DRAFT は curator の backlog に流れ(librarian_progress が curator に
  librarian_meta を残す既存挙動)、curator が検証→supersede/synthesize/
  coexist/reject を DRAFT 提案。**サーバ無改修**。
- 司書 bundle: curator に duplicate resolution と「detective 提案の消費・
  reject 記録」を追記。

### 設計判断の根拠

- **提案の運搬は librarian_meta DRAFT のまま、既存 backlog/progress に
  乗せる**(専用キュー API や chat 通知ではなく)。理由: (1) 新概念ゼロ・
  サーバ無改修で最もシンプル、(2) 提案が durable な entry として残り、
  curator の accept/reject が progress に記録される=「提案の受理率」を
  計測でき detective の精度を継続改善できる。chat は lookup 非対象で揮発的、
  専用キューは entry と状態二重持ちで、どちらも改善ループ計測を困難にする。
- 既存の supersede/synthesize/coexist 語彙を duplicate にも流用。新しい
  解決種別を発明しない。

### 影響範囲

- サーバ実装変更なし(`excludedTypesForRole` が既に curator に
  librarian_meta を流す設計だった)。
- curator の runnable workspace をこの設計で実装(リポジトリ外、ローカル
  検証後)。Phase 計画への影響なし。

---

## v0.8(2026-05-29)

### 背景

司書を実運用に乗せる過程で2つの設計判断が必要になった:

1. **重複・類似の判定をどこで行うか。** サーバ側のクラスタリング
   (`BuildIncidentClusters`) は symptom トークンの Jaccard 類似度で、
   `type='incident'` 限定・本番では既定無効。これは語彙的一致しか見ず、
   言い換えや**言語をまたぐ重複**(同一事象の日本語 trap と英語 trap は
   トークンが一致しない)を構造的に取りこぼす。多言語 KB では致命的。
2. **司書の実行粒度。** 役割定義は per-tick(1 件処理)だが、1 件ごとに
   ランタイムを cold-start するのは非効率。

### 変更点

- §20.2(incident/クラスタリング): サーバのクラスタリングは「粗い候補
  生成器」であり、**意味的な重複・関連判定は detective 司書(LLM)が担う**
  ことを明記。detective は search で候補を集め `duplicate_of`/`related`/
  `conflicts_with` 等を DRAFT 提案(Phase 5 非破壊)。
- §17 司書ランナー: **tick(役割契約の単位)と session(バッチ実行)**の
  区別を追記。session は複数 tick をバッチ実行してよいが、(a) 各エントリ
  独立判定、(b) progress/heartbeat は tick 単位、を守る。
- 司書 bundle(`dist/skills/librarians/`): detective に意味的重複発見と
  正準 rel_type(`related|duplicate_of|conflicts_with|see_also|depends_on`)
  を反映。従来 bundle が挙げていた `derived_from`/`related_to`/`similar_to`
  は store が受け付けない不正値だったため修正。cataloger にバッチ session
  の節を追加。

### 設計判断の根拠

- **サーバは dumb な infra に保つ**(`KB_LLM_PROVIDER` 既定無効)。LLM を
  サーバのホットパスに入れない方針は不変。よって意味判定は agent 層へ。
  これは「各層は下層に対し Z 軸俯瞰者」という既存原則とも整合(detective が
  エントリ群を俯瞰し判定)。
- **detective の提案条件は「具体的根拠を引用できる時のみ」**。共有 claim・
  メカニズム・lineage を名指しできなければ no_action。"plausibly close" や
  "同ドメイン" だけで提案を量産させない。Type II 最小化(過剰提案)の
  framing は同時に撤回(下流が拾うから雑でいい、と読まれて実際雑になる
  と、品質改善のループが回らない)。
- **バッチは役割契約に持ち込まない**。スケジューラ/workspace の関心事と
  して分離し、bundle は per-tick のまま単一の正に保つ。

### 影響範囲

- 既存実装(サーバ)への変更なし。クラスタリングジョブは現状のまま
  (粗い候補生成器として位置づけ直しただけ)。
- detective の runnable workspace はこの設計に合わせて実装(リポジトリ外、
  ローカル検証後に本番投入)。
- Phase 計画への影響なし(Phase 5 観察モードの枠内)。

### 背景

設計過程で参照した実体験(複数の自律エージェントを Discord 上で議論させた経験)から得られた知見を反映:

- 3 人部屋 + 俯瞰者 1 名の構造が最も効率的だった
- 「実装役」も実は自分でコードを書かず、サブエージェントを指揮する形だった
- つまり各層が下層に対しては Z 軸俯瞰者、上層に対しては実行役、という二重性を持つ
- Codex 系モデルは規律的、Opus 系は推進力が強い、という個性とモデルの相性
- 6 体エージェントでも発言ログが一瞬で大量になる、密度管理の必要性

これらをフラクタル階層として将来 Phase 仕様に位置づける。

### 変更点

#### 設計原則(§2)

- **原則 16 追加**: "Fractal Z-axis architecture"
  - 各層は下層に対しては Z 軸俯瞰者、上層に対しては実行役として動作
  - 司書層・sub-agent 層・coding-agent 層に同じ「3 人部屋 + Z 軸」パターンが再帰適用

#### Phase 計画(§13)

- Phase 5 備考に追記:司書 skill は最初から `sub_agents/` サブディレクトリを予約する設計とする
  - Phase 5 では中身は空でよい
  - Phase 6 以降のフラクタル階層実装時に既存 skill を破壊的に変更する必要を避けるため

#### §24 新規追加: Fractal Hierarchy(将来 Phase 仕様)

全 13 サブセクションで構成:

- 24.1 動機:単純実装の問題点
- 24.2 階層構造:3 層モデル(司書 → sub-agent → coding-agent)
- 24.3 各層内の構造:3 人部屋 + Z 軸
- 24.4 個性 yaml の拡張:`room_role_aptitudes` フィールド
- 24.5 ルーム概念:固定ルーム + 動的ルーム
- 24.6 司書 skill ディレクトリの拡張:`sub_agents/` 内包構造
- 24.7 起動と廃棄:ephemeral な下層
- 24.8 モデル Tier 配分:層別最適化
- 24.9 コスト構造:idle ほぼゼロ、アクション時のみ稼働
- 24.10 失敗モードと回復:graceful degradation
- 24.11 Phase 計画への影響:Phase 6-7 への追加項目
- 24.12 設計の本質と外部参照:類似実装との比較
- 24.13 実装上の注意:再帰深さ制限、層をまたぐ参照禁止など

#### 用語集(付録 A)

13 件追加:
- Fractal Z-axis architecture
- 司書層 / Layer 1、Sub-agent 層 / Layer 2、Coding-agent 層 / Layer 3
- 3 人部屋
- 実装役 / Implementer、監督役 / Supervisor、盛り上げ役 / Energizer
- 固定ルーム、動的ルーム
- Room role aptitude

#### 個性 YAML サンプル(付録 C)

注記追加:Phase 6 以降のフラクタル階層実装時には `room_role_aptitudes` フィールド(§24.4 の表参照)を追加する旨を明示。Phase 5 時点のサンプルでは省略してよい。

### 設計判断の根拠

#### なぜ 3 人部屋なのか

検討した代替案:

| 構成 | 評価 |
|---|---|
| 2 人 | 拮抗して終わらない、ベクトルが対称化しがち |
| 3 人 | **採用**。力学的に最小の安定多角形、多視点と決着可能性のバランス |
| 4 人以上 | 冗長、議論散漫、発言密度が許容範囲を超える |

3 という数は哲学的にも社会学的にも示唆的(弁証法、Simmel のトライアド、三権分立)。経験的にも、異質性が最も豊かに発生する最小単位。

#### なぜ Z 軸の俯瞰者なのか

検討した代替案:

- 案 α: 議論不参加者からランダム
- 案 β: ドメインから最も遠い specialist
- 案 γ: **採用**。Arbiter 用の専用役割(Judge)を作る
- 案 δ: 多数決(俯瞰者なし)

俯瞰者の独立性が決定の中立性を担保する。当事者は対立構造に巻き込まれて視野狭窄になる。司法における裁判官、ピアレビューにおけるエディタと同型構造。

#### なぜフラクタル(再帰)構造なのか

実体験では、実装役自身も「自分はコードを書かず、サブエージェントを指揮していた」ことが判明。つまり各層が下層に対しては俯瞰者、上層に対しては実行役、という二重性を既に持っていた。

これを設計に明示することで:

- 各層の認知負荷が一定(3-4 個の選択肢を扱うだけ)
- 同じパターンの再帰なので実装が単純化される
- 失敗が局所化される(下層の問題は上層が検出して別経路に切り替え)
- 層別にモデル Tier を最適化できる(Opus → Sonnet → Codex)

人間組織の管理階層(経営層 → 部長 → 課長)と同型で、自然な構造。

#### なぜ詳細を薄めにしたか

§24 は §23 より詳細度を一段下げている。理由:

- Phase 5 で司書層を実装して運用してみないと、sub-agent 層の最適化ポイントは見えない
- 詳細を書きすぎると Phase 6 着手時に実情と乖離する
- 「方向性とディレクトリ構造を予約しておく」が現時点での最大の価値

詳細仕様は Phase 6 着手時に v0.8 として詰める想定。

### 影響範囲

| 領域 | 影響 |
|---|---|
| Phase 1-4 | **影響なし**。§24 は将来仕様 |
| Phase 5 | skill ディレクトリ構造に `sub_agents/` を予約する点のみ |
| Phase 6 | フラクタル階層の実装が Phase 6 の主要タスクとして追加 |
| Phase 7 | 各層の判断質メトリクス長期評価、層 Tier の自動最適化が追加 |
| 既存スキーマ | 変更なし |
| 既存 API | 変更なし |

---

## v0.6(2026-05-12)

### 背景

v0.5 までの設計に対する 2 つの認識のずれを修正:

1. ML 特化の印象が強すぎる:設計書冒頭の例示が ML 系に偏っていたため、汎用知識ベースとしての位置づけが曖昧
2. 司書のメモリ機構を Core 側に組み込もうとしていた:エージェント実装側に各種メモリ機構が既に存在するため、責務の越境

### 変更点

#### §1.1〜1.2 の書き直し:ドメイン汎用化

- §1.1「目的」を ML コーディングエージェントから「過去の経験を踏まえて行動するエージェント全般」に拡張
- §1.2 新設:想定ユースケースを 7 分野で例示
  - ML 研究・開発
  - ソフトウェア開発
  - インフラ運用
  - 法務・コンプライアンス
  - カスタマーサポート
  - 製造業の品質管理
  - 研究機関

#### §23.16 全面書き直し:メモリはエージェント側責任

削除されたもの:

- librarian_memory_snapshots テーブル(将来予定)
- nightly_compaction ジョブ
- weekly_consolidation ジョブ
- mid-term / long-term の階層的メモリ設計
- ハートビート時のコンテキスト組み立てロジック

代わりに導入されたもの:

- KB Core は「自分の過去を取得する API」だけ提供:
  - GET /v1/librarian/my_chats
  - GET /v1/librarian/my_actions
  - GET /v1/librarian/my_meta_entries
  - GET /v1/librarian/my_decisions_evaluated
- 「思い出す」責任はエージェント側
- Claude Code、OpenCode、OpenClaw 等それぞれが自分のメモリ機構と組み合わせて活用
- 最初は「直近 N 件」で十分、要約機構は Phase 7 以降で必要性確認後に検討

### 設計判断の根拠

#### なぜ ML 特化を解消したか

設計書を読み返すと、技術的な抽象は汎用だが例示が ML に偏っていた。これにより:

- ML 系以外の用途を考えている読者に「自分には関係ない」と思わせるリスク
- 設計判断の根拠が「ML 研究の罠」に依存しているように見える
- 実際には設計はドメイン非依存

#### なぜメモリをエージェント側責任にしたか

設計原則 15「No in-house agent runtime」の自然な帰結。エージェント実体を内製しないなら、付属物(記憶、コンパクション)も内製しない。

理由:
- Claude Code、OpenCode、OpenClaw 等の各エージェントツールは独自の記憶機構を持つ
- メモリの最適化(コンパクション、検索、要約)はエージェントツール側で日進月歩で改善
- KB Core が同等品質を維持するのは現実的でない
- Core 側に組み込むと §2 原則 15 に違反

「原則を立てておくと、後から発生する設計判断が自動的に正解側に寄る」ことの好例。

#### なぜ最初は「直近 N 件」で十分か

- short-term memory: 直近 50 件の発言 + 直近 20 件のアクション
- これだけで議論の文脈は維持できる
- 「忘却」は自然に起きるが、本当に重要な判断は meta-entry に残っているので失われない
- 要約機構が必要かは実運用で初めて分かる

### 影響範囲

| 領域 | 影響 |
|---|---|
| Phase 1-4 | 影響なし |
| Phase 5 | メモリ実装が不要に。工数削減 |
| 既存スキーマ | librarian_memory_snapshots テーブルが不要(導入されていなかったので実害なし) |
| API | `/v1/librarian/my_*` 系の追加(4 エンドポイント) |

---

## v0.5(2026-05-12)

### 背景

v0.4 までの設計では、司書エージェント本体を内製する想定だった。これは:

- LLM 呼び出し、コンテキスト管理、ツール実行ループ、エラーハンドリング等の複雑な実装を要する
- 数ヶ月〜年単位の工数
- 既存のエージェントツール(Claude Code、OpenCode 等)は継続的に改善されているが、自前実装はこの進化に追従できない

これを根本的に見直し、エージェント実体は内製せず skill だけ定義する方針に転換。

### 変更点

#### 設計原則 15 追加

"No in-house agent runtime":エージェント実体は内製しない。司書役割は完全な skill として定義し、既存自律エージェント(Claude Code、OpenCode 等)に演じさせる。

#### §23.6 全面書き直し

8 サブセクションで以下を詳細化:

- 23.6.1 設計思想:内製しない理由
- 23.6.2 スキルが満たすべき要件:10 要素のチェックリスト
- 23.6.3 ディレクトリ構造:skill 配下の 11 種類のファイル
- 23.6.4 各ファイルの仕様
- 23.6.5 司書 runner:500 行程度のプロセスマネージャ
- 23.6.6 既存エージェント統合:Claude Code / OpenCode / 汎用 stdio MCP
- 23.6.7 期待される実装工数:内製 6-12 ヶ月 → skill-only で 1-2 ヶ月
- 23.6.8 スキル品質の担保

#### Phase 5 の再構成

- 司書 skill ディレクトリ(8 役割 × 10 ファイル相当)を中心成果物に
- 司書 runner はエージェント実体ではなく、Claude Code / OpenCode を呼ぶハーネス
- 既存エージェントとの統合スクリプト 2-3 種

### 設計判断の根拠

#### なぜ内製を放棄したか

3 つの観点で比較:

| 観点 | 内製 | skill-only |
|---|---|---|
| 実装コスト | 6-12 ヶ月 | 1-2 ヶ月 |
| 進化追従 | 困難 | 容易 |
| 差し替え可能性 | 困難 | 容易(skill だけ変えない) |

司書の本質は「役割と判断」であって「LLM 呼び出しの実装」ではない。後者は既存ツールに任せた方が品質も継続性も得られる。

#### スキルが満たすべき要件の根拠

汎用自律エージェントが skill だけを読んで司書役を完遂するには、以下が完全に揃っている必要がある:

| 要素 | 何を定義するか |
|---|---|
| 役割の本質 | 自分が何者で、何を解決するか |
| 起動条件 | いつ動くか |
| 情報源 | どこから状況を取るか |
| 判断手順 | 何を見て何を決めるか(if-then で記述可能) |
| 個性 | どう判断し、どう発言するか |
| 許可された操作 | 何ができるか(API ホワイトリスト) |
| 発言スタイル | どう書くか(few-shot 例を含む) |
| 終了条件 | いつ止まるか |
| 記録形式 | 何をメタエントリとして残すか |
| 失敗時対処 | エラー時の行動手順 |

「Claude Code に @<skill> を読み、role に従って起動してください」と指示するだけで動く水準であることが要件。

### 影響範囲

| 領域 | 影響 |
|---|---|
| Phase 1-4 | 影響なし |
| Phase 5 | 主要成果物がエージェント本体実装から skill 群作成に変更、工数 1-2 ヶ月へ圧縮 |
| 既存スキーマ | 影響なし |

---

## v0.4(2026-05-12)

### 背景

v0.3 までは Index Maintenance の仕組み(§22)があったが、運用主体が定義されていなかった。

人間レビューを最小限にしつつ自走可能なシステムを実現するため、「司書」という概念を導入。さらに以下の議論を経て司書コミュニティの全体構造が固まった:

- 司書を 1 体だけ作るか、複数体作るか → 複数体
- 役割を順次パイプラインにするか、並列にするか → 並列
- 同質か異質か → 個性を持つ異質な構成
- 議論の収束をどう保証するか → 3 人部屋 + Judge の Z 軸構造
- 無限ループをどう防ぐか → 多層機構(エージェントの善意に頼らない)
- 既存 OSS との差別化はあるか → 統合形態として既存に存在しない

### 変更点

#### 設計原則の拡張

原則 6 を分割し、原則 9-14 を新規追加:

- 9. Level C autopilot:完全自走運用前提
- 10. Engineered cognitive diversity:意図的な認知多様性
- 11. Z-axis arbitration:議論参加者ではなく俯瞰者が決定権を持つ
- 12. Structural infinite-loop prevention:多層構造による
- 13. Temporal facts, not deletions:削除ではなく時間的妥当性で扱う
- 14. Heartbeat-driven proactive curation:司書が自発的に外部データを取りに行く

#### スキーマ拡張(§4.2)

- entries テーブルに valid_from / valid_to / superseded_by / invalidation_reason / enrichment_version / version カラムを追加(Temporal validity と Optimistic locking)
- 司書系テーブル群を追加:
  - librarian_chat、chat_threads
  - librarian_tasks
  - quartet_assignments
  - librarian_instances
  - external_findings、finding_correlations
- 将来要件用テーブル(Phase 6+ 実装、初期は CREATE のみ):
  - thread_emergent_topics
  - external_contracts、contractor_access_log

#### §23 新規追加:Librarian Community(司書コミュニティ)

20 サブセクションで構成。8 役割、個性 DSL、共有チャット、議論クォーテット、ハートビート駆動データ収集、メタ知識記録、Bootstrap protocol、失敗モードと退避戦略、将来要件のアーキテクチャ的配慮。

#### 8 役割の定義

| 司書 | 所掌 | 主な操作 |
|---|---|---|
| Coordinator | タスク dispatch、予算配分 | 統括、異常一次対応 |
| Cataloger | tags、hierarchy、situations | 分類、タグ正規化、階層配置 |
| Curator | entries.status、relations(conflict) | 昇格・降格、supersede、refine |
| Detective | incidents、clusters、relations(discovery) | パターン発見、関係発見 |
| Conservator | enrichment_version、dead_pool、migrations | 再 enrichment、archive |
| Scout | external_findings | 外部監視、DRAFT 投稿 |
| Summarizer | chat_threads クロージング | 議論要約、決定の構造化 |
| Judge | quartet_assignments | Z 軸決定、議論質評価 |

#### Phase 計画の再構成(§13)

5 Phase → 7 Phase に再構成。将来要件(§23.20)は Phase 8 以降。

#### 既存 OSS 調査からの取り込み

mcp-memory-service、Hindsight、Graphiti、SwarmVault、Clawith を調査。Temporal validity / Dual-layer triggers / Auto-supersede on contradiction / Session boundary hooks / Reflect operation / Local-only heuristic enrichment を取り込み。

### 設計判断の根拠

#### なぜ 8 役割なのか

検討の経緯:

1. 初期案:Cataloger、Curator、Detective、Conservator の 4 役割
2. 「司書間の議論をクロージングするには別の役割が必要」→ Summarizer を追加(5 役割)
3. 「クォーテット形式で Z 軸決定する役割が要る」→ Judge プールを追加(6 役割)
4. 「外部データを取り込む役割が独立して必要」→ Scout を追加(7 役割)
5. 「全体統括が必要」→ Coordinator を追加(8 役割)

責任の単一所有権を保ちつつ、必要な役割を全て揃える結果として 8 になった。

#### なぜ意図的に異質な個性なのか

多くのマルチエージェントシステムは「エージェントを揃えよう」とする(Sycophancy 防止、不一致削減)。我々は逆に意図的に偏らせる:

- 認知多様性:同質だと同じ盲点を持つ
- Productive tension:対立構造が建設的に作用
- 議論の質的向上:異なる lens で見ることで本質が見える

#### なぜ多層の無限ループ防止か

エージェントの判断に頼って「これ以上応答しない」と決めさせるのは脆い。律儀に守る保証がない。

そこで構造で止める:

- Layer 1: ハードリミット(発言数、参加者数、累積トークン)
- Layer 2: 時間減衰(STALE 判定)
- Layer 3: トークン予算
- Layer 4: 構造的収束(Summarizer 召喚で他司書発言禁止)

これらすべてが独立に発火する。1 つ突破しても次が止める。

### 影響範囲

| 領域 | 影響 |
|---|---|
| ドキュメント全体 | 大幅拡張(2280 行 → 3883 行) |
| Phase 計画 | 5 Phase → 7 Phase |
| スキーマ | 8 テーブル追加 + entries 列追加 |
| API | librarian 系エンドポイント追加 |
| 既存実装(あれば) | データ移行が必要(temporal validity 列) |

---

## v0.3(2026-05-12)

### 背景

v0.2 までで Core 機能が固まったが、運用時間が経過するとインデックスが劣化する問題が未解決だった。これらを継続的に維持する仕組みを設計。

### 変更点

#### §22 新規追加:Index Maintenance

16 サブセクション。Enrichment バージョニング、再 Enrichment 優先度、新 Index 次元のバックフィル、タグ正規化、階層自動再編、関係発見、場面マイニング、ヘルスメトリクス、LLM コスト管理、死蔵エントリ、メンテ API、スケジュール例、可観測性、段階的導入。

### 設計判断の根拠

#### なぜ全件再構築を採用しないか

最も単純な解は「定期的に全エントリを LLM で再 enrichment」だが、コストが破綻する。代わりに incremental + 優先度付き + バージョン管理を採用。

#### なぜ「提案 → 人間承認」の枠組みなのか

v0.3 単独では人間承認の枠組みでスタート。ただし v0.4 で Level C 自走前提に変更され、この人間承認の枠組みは司書システムに置き換わる。

### 影響範囲

| 領域 | 影響 |
|---|---|
| ドキュメント | §22 新規追加 |
| Phase 計画 | Phase 1-5 にメンテ機能の段階導入を分散 |
| スキーマ | 影響軽微(enrichment_versions、backfill_jobs、tag_aliases、pending_normalizations、audit_log、llm_usage) |

---

## v0.2(2026-05-12)

### 背景

v0.1 では Core API、エントリ、検索の基本機能を固めた。これに対する 2 つの拡張要望:

1. 解決していない失敗観察(原因不明、対処不明)も価値があるので一級市民として扱いたい
2. クライアントごとに最適化された接続パッケージを配布したい

### 変更点

#### Incident 型の導入

- entries.type に 'incident' を追加
- incident 専用フィールド(attempted_approaches、observed_behavior、hypotheses)
- INVESTIGATING、DUPLICATE ステータスを追加
- incident 専用の LLM enrichment プロンプト(根本原因や禁止事項は推測しない)
- §20 新規:インシデント(未解決失敗観察)の扱い

#### Incident クラスタリング

- incident_clusters、incident_cluster_members テーブル
- 類似 incident のパターン検出ジョブ
- クラスタが閾値超過すると trap への昇格提案
- 昇格時、incident は削除せず resolved_by 関係を張る

#### スキル形式での配布

- §21 新規:スキル形式での配布
- dist/skills/<tool>/ ディレクトリ構造
- 共通 stdio MCP server スクリプト(Python)
- Claude Code、OpenCode、汎用 stdio MCP それぞれ向け SKILL.md / agent.md
- install.sh による自動設定

### 設計判断の根拠

#### なぜ Incident を一級市民にするか

「変な現象を見たが原因不明」「色々試したがダメだった」を捨てると、同じ現象を別エージェントが再発見し、また同じ試行錯誤を繰り返す。

#### なぜ incident → trap の昇格で incident を削除しないか

- 元の観察記録(試行錯誤の履歴)が学習データとして価値を持つ
- 後で「実は別の trap が本当の原因」と判明した時に履歴が辿れる
- クラスタリングのトレーニングデータとして使える

### 影響範囲

| 領域 | 影響 |
|---|---|
| ドキュメント | §20、§21 新規追加 |
| スキーマ | entries に新規列、incident_clusters、incident_cluster_members |
| API | incident 投稿、cluster 操作 |

---

## v0.1(2026-05-12)

### 背景

新規 ML 研究開発(リップシンクパイプライン、LeWM)の現場で、サブエージェント呼び出し時に過去の罠が引き継がれない問題が頻発。既存の解決手段(git で md 管理、サードパーティのメモリプラグイン)はいずれも:

- 人間が編集者であることを前提にした重いワークフロー
- 供給網リスクのあるサードパーティ依存
- 特定ツール(OpenCode 等)へのロックイン

を抱えるため、ツール非依存・内部完結のサーバーを自前で持つ方針を採用。

### 変更点

初版として §1-§19(概要、原則、アーキテクチャ、データモデル、REST API、enrichment、検索、MCP/CLI/SDK、Web ダッシュボード、セキュリティ、Phase 計画、技術選定、プロジェクト構造、テスト、完了の定義)を定義。

### 設計判断の根拠

#### なぜ Core HTTP REST API を中核にしたか

- MCP のみだと MCP 非対応のツールから使えない
- CLI のみだと自動化が制限される
- SDK のみだと言語別に実装が必要
- HTTP REST API があれば、その上のアダプタとして MCP / CLI / SDK を全て構成可能

#### なぜ Go + SQLite を選定したか

- 単一バイナリ配布で運用が単純
- SQLite は WAL モードで十分なスループット
- FTS5 で全文検索組み込み
- 外部 DB に依存しない
- ORM 禁止 → 素の SQL でスキーマ進化の見通しが立つ

#### なぜ逆引き(by-trigger、by-symptom、by-situation)を中心にしたか

通常の全文検索は「キーワードが含まれる」を返すが、エージェントが必要なのは「これからやろうとしている操作に関連する罠」「観察した症状に該当する事例」のような **概念逆引き**。

事前計算された逆引きインデックステーブル(triggers_index、symptoms_index)を持つことで、LLM 推論なしの高速な逆引きを実現。

### 影響範囲

該当なし(初版)

---

## メンテナンスメモ

### バージョン番号の規則

- メジャー番号(現状 0):本番リリース前
- マイナー番号:設計判断の追加・変更
- 細かい修正(誤字、表現改善)はバージョン番号を上げない

### 設計書と本変更履歴の同期

- 設計書を改訂したら、必ず本書に対応セクションを追加
- 設計書冒頭の "バージョン" と "v.X の主な変更" は最新版のみ詳細表示、過去バージョンは項目だけ
- 詳細は本変更履歴を参照する形

### 変更時のチェックリスト

設計書を改訂する際:

- [ ] バージョン番号を更新
- [ ] 設計書冒頭の "v.X の主な変更" を更新
- [ ] 本変更履歴に新セクションを追加(背景、変更点、設計判断の根拠、影響範囲)
- [ ] 用語集に新規用語があれば追加
- [ ] Phase 計画に影響があれば明示
