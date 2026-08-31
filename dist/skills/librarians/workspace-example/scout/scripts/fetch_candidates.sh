#!/usr/bin/env bash
# Fetch external candidates for the scout to evaluate, from public,
# key-free sources (the operator-configured allow-list for this scout):
#   - Hacker News top stories (IT news)            — HN Firebase API
#   - arXiv recent submissions in CS/audio cats    — arXiv API
#   - Hugging Face daily papers (curated)          — HF API
#   - Hugging Face trending models (last 7d likes) — HF API
#   - はてなブックマーク テクノロジー hotentry (JP) — RSS (bookmark count = score)
#   - Publickey (JP enterprise-IT news, curated)   — Atom feed
#   - Vendor AI news: OpenAI / Google AI / DeepMind — official RSS
#     (Anthropic / xAI have no public feed; HN・はてブ cover their launches)
#
# Emits a compact JSON array on stdout, each item:
#   {"source":"hn"|"arxiv"|"hf_paper"|"hf_model","url":"...","title":"...","extra":"..."}
# where `extra` is the HN score / arXiv abstract / HF summary or likes.
#
# Already-seen URLs (this workspace's seen-file) are filtered OUT here so
# the LLM only ever evaluates fresh items. The seen-file is appended by
# post_finding.sh / mark_seen.sh after evaluation.
#
# Env knobs (optional):
#   SCOUT_HN_LIMIT      default 25   top-N HN stories to consider
#   SCOUT_ARXIV_CATS    default "cs.CL cs.LG cs.SD eess.AS cs.CV"
#   SCOUT_ARXIV_PER_CAT default 6    recent papers per category
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SEEN="python3 $SCRIPT_DIR/seen_store.py"   # SQLite-backed dedup (scales to 100k+)

HN_LIMIT="${SCOUT_HN_LIMIT:-25}"
ARXIV_CATS="${SCOUT_ARXIV_CATS:-cs.CL cs.LG cs.SD eess.AS cs.CV}"
ARXIV_PER_CAT="${SCOUT_ARXIV_PER_CAT:-6}"

TMP=$(mktemp); trap 'rm -f "$TMP"' EXIT

# ---- Hacker News top stories ----
hn_ids=$(curl --retry 5 --retry-connrefused -sS -m 15 "https://hacker-news.firebaseio.com/v0/topstories.json" \
    | jq -r ".[:$HN_LIMIT][]" 2>/dev/null || true)
for id in $hn_ids; do
    item=$(curl --retry 5 --retry-connrefused -sS -m 10 "https://hacker-news.firebaseio.com/v0/item/$id.json" 2>/dev/null || echo '{}')
    echo "$item" | jq -c '
        select(.type=="story" and (.url // "") != "")
        | {source:"hn", url:.url, title:(.title // ""), body:"",
           pubdate:(if .time then (.time|todate) else "" end),
           extra:("HN score " + ((.score // 0)|tostring) + ", " + ((.descendants // 0)|tostring) + " comments")}'
done >> "$TMP" || true

# ---- arXiv recent per category ----
for cat in $ARXIV_CATS; do
    xml=$(curl --retry 5 --retry-connrefused -sS -m 20 "https://export.arxiv.org/api/query?search_query=cat:${cat}&sortBy=submittedDate&sortOrder=descending&max_results=${ARXIV_PER_CAT}" 2>/dev/null || true)
    [ -z "$xml" ] && continue
    printf %s "$xml" > "$TMP.payload"; ARXIV_XML_FILE="$TMP.payload" python3 - "$cat" <<'PY' >> "$TMP" || true
import sys, re, json, html, os
cat = sys.argv[1]
x = open(os.environ["ARXIV_XML_FILE"]).read()
for e in re.findall(r"<entry>(.*?)</entry>", x, re.S):
    t = re.search(r"<title>(.*?)</title>", e, re.S)
    u = re.search(r"<id>(.*?)</id>", e)
    s = re.search(r"<summary>(.*?)</summary>", e, re.S)
    p = re.search(r"<published>(.*?)</published>", e)
    if not (t and u):
        continue
    title = html.unescape(re.sub(r"\s+", " ", t.group(1)).strip())
    url = u.group(1).strip()
    summ = html.unescape(re.sub(r"\s+", " ", s.group(1)).strip()) if s else ""
    pubdate = (p.group(1).strip()[:10] if p else "")
    # Full abstract: the quantitative results (accuracy, speedups,
    # benchmark deltas) live in the LAST sentences — truncating here
    # blinds the judge to the numbers the entry must cite.
    print(json.dumps({"source": "arxiv", "url": url,
                      "title": title, "body": summ[:2000],
                      "pubdate": pubdate,
                      "extra": f"[{cat}] {summ[:2000]}"}))
PY
done

# ---- Hugging Face daily papers (curated) ----
HF_PAPERS_LIMIT="${SCOUT_HF_PAPERS_LIMIT:-12}"
curl --retry 5 --retry-connrefused -sS -m 15 "https://huggingface.co/api/daily_papers?limit=${HF_PAPERS_LIMIT}" 2>/dev/null \
    | jq -c '.[] | {
        source:"hf_paper",
        url:("https://huggingface.co/papers/" + (.paper.id // "")),
        title:(.paper.title // ""),
        body:((.paper.summary // "")[:2000]),
        pubdate:(((.publishedAt // .paper.publishedAt // "")|tostring)[:10]),
        extra:("[HF daily papers] " + ((.paper.summary // "")[:2000]))
      } | select(.title != "" and (.url | endswith("/papers/") | not))' >> "$TMP" 2>/dev/null || true

# ---- Hugging Face trending models (last-7d likes) ----
HF_MODELS_LIMIT="${SCOUT_HF_MODELS_LIMIT:-10}"
curl --retry 5 --retry-connrefused -sS -m 15 "https://huggingface.co/api/models?sort=likes7d&direction=-1&limit=${HF_MODELS_LIMIT}" 2>/dev/null \
    | jq -c '.[] | {
        source:"hf_model",
        url:("https://huggingface.co/" + (.id // "")),
        title:(.id // ""),
        body:(if (.pipeline_tag // "") != "" then "task=" + .pipeline_tag else "" end),
        pubdate:(((.createdAt // .lastModified // "")|tostring)[:10]),
        extra:("[HF trending model] likes=" + ((.likes // 0)|tostring)
               + (if (.pipeline_tag // "") != "" then ", task=" + .pipeline_tag else "" end))
      } | select(.title != "")' >> "$TMP" 2>/dev/null || true

# ---- はてなブックマーク テクノロジー ホットエントリ (JP tech, HN-analog) ----
# Bookmark count plays the role of the HN score for the judge.
HATENA_LIMIT="${SCOUT_HATENA_LIMIT:-15}"
hatena_xml=$(curl --retry 5 --retry-connrefused -sS -m 15 "https://b.hatena.ne.jp/hotentry/it.rss" 2>/dev/null || true)
if [ -n "$hatena_xml" ]; then
    printf %s "$hatena_xml" > "$TMP.payload"; HATENA_XML_FILE="$TMP.payload" HATENA_LIMIT="$HATENA_LIMIT" python3 - <<'PY' >> "$TMP" || true
import os, re, json, html
x = open(os.environ["HATENA_XML_FILE"]).read()
limit = int(os.environ.get("HATENA_LIMIT", "15"))
for e in re.findall(r"<item[ >](.*?)</item>", x, re.S)[:limit]:
    t = re.search(r"<title>(.*?)</title>", e, re.S)
    u = re.search(r"<link>(.*?)</link>", e, re.S)
    d = re.search(r"<description>(.*?)</description>", e, re.S)
    b = re.search(r"<hatena:bookmarkcount>(\d+)</hatena:bookmarkcount>", e)
    dt = re.search(r"<dc:date>(.*?)</dc:date>", e)
    if not (t and u):
        continue
    title = html.unescape(re.sub(r"\s+", " ", t.group(1)).strip())
    desc = re.sub(r"<[^>]+>", " ", html.unescape(d.group(1))).strip() if d else ""
    desc = re.sub(r"\s+", " ", desc)
    bkm = b.group(1) if b else "0"
    print(json.dumps({"source": "hatena_it", "url": u.group(1).strip(),
                      "title": title, "body": desc[:2000],
                      "pubdate": (dt.group(1)[:10] if dt else ""),
                      "extra": f"[はてブIT hot, {bkm} bookmarks] {desc[:600]}"},
                     ensure_ascii=False))
PY
fi

# ---- Publickey (JP enterprise-IT news, curated) ----
PUBLICKEY_LIMIT="${SCOUT_PUBLICKEY_LIMIT:-10}"
pk_xml=$(curl --retry 5 --retry-connrefused -sS -m 15 "https://www.publickey1.jp/atom.xml" 2>/dev/null || true)
if [ -n "$pk_xml" ]; then
    printf %s "$pk_xml" > "$TMP.payload"; PK_XML_FILE="$TMP.payload" PK_LIMIT="$PUBLICKEY_LIMIT" python3 - <<'PY' >> "$TMP" || true
import os, re, json, html
x = open(os.environ["PK_XML_FILE"]).read()
limit = int(os.environ.get("PK_LIMIT", "10"))
for e in re.findall(r"<entry>(.*?)</entry>", x, re.S)[:limit]:
    t = re.search(r"<title[^>]*>(.*?)</title>", e, re.S)
    u = re.search(r'<link[^>]*rel="alternate"[^>]*href="([^"]+)"', e) or \
        re.search(r'<link[^>]*href="([^"]+)"', e)
    s = re.search(r"<summary[^>]*>(.*?)</summary>", e, re.S) or \
        re.search(r"<content[^>]*>(.*?)</content>", e, re.S)
    dt = re.search(r"<published>(.*?)</published>", e) or re.search(r"<updated>(.*?)</updated>", e)
    if not (t and u):
        continue
    title = html.unescape(re.sub(r"\s+", " ", t.group(1)).strip())
    summ = re.sub(r"<[^>]+>", " ", html.unescape(s.group(1))).strip() if s else ""
    summ = re.sub(r"\s+", " ", summ)
    print(json.dumps({"source": "publickey", "url": u.group(1).strip(),
                      "title": title, "body": summ[:2000],
                      "pubdate": (dt.group(1)[:10] if dt else ""),
                      "extra": f"[Publickey] {summ[:600]}"},
                     ensure_ascii=False))
PY
fi

# ---- Vendor AI news (official blogs with public feeds) ----
# Anthropic and xAI publish NO feed (404 as of 2026-07) — their launches
# reach us via HN / はてブ instead. Newest-first; cap per feed.
VENDOR_LIMIT="${SCOUT_VENDOR_LIMIT:-8}"
VENDOR_FEEDS="openai_news|https://openai.com/news/rss.xml
google_ai|https://blog.google/technology/ai/rss/
deepmind|https://deepmind.google/blog/rss.xml"
while IFS='|' read -r vname vurl; do
    [ -z "$vname" ] && continue
    vxml=$(curl --retry 3 --retry-connrefused -sS -m 20 -L -A "omoikane-scout/0.1" "$vurl" 2>/dev/null || true)
    [ -z "$vxml" ] && continue
    printf %s "$vxml" > "$TMP.payload"; FEED_XML_FILE="$TMP.payload" FEED_NAME="$vname" FEED_LIMIT="$VENDOR_LIMIT" python3 - <<'PY' >> "$TMP" || true
import os, re, json, html
x = open(os.environ["FEED_XML_FILE"]).read()
name = os.environ.get("FEED_NAME", "feed")
limit = int(os.environ.get("FEED_LIMIT", "8"))
# One parser for both RSS (<item>) and Atom (<entry>).
chunks = re.findall(r"<item[ >](.*?)</item>", x, re.S) or \
         re.findall(r"<entry[ >]?(.*?)</entry>", x, re.S)
for e in chunks[:limit]:
    t = re.search(r"<title[^>]*>(?:<!\[CDATA\[)?(.*?)(?:\]\]>)?</title>", e, re.S)
    u = re.search(r'<link[^>]*rel="alternate"[^>]*href="([^"]+)"', e) or \
        re.search(r'<link[^>]*href="([^"]+)"', e) or \
        re.search(r"<link>(.*?)</link>", e, re.S)
    s = re.search(r"<description[^>]*>(?:<!\[CDATA\[)?(.*?)(?:\]\]>)?</description>", e, re.S) or \
        re.search(r"<summary[^>]*>(?:<!\[CDATA\[)?(.*?)(?:\]\]>)?</summary>", e, re.S) or \
        re.search(r"<content[^>]*>(?:<!\[CDATA\[)?(.*?)(?:\]\]>)?</content>", e, re.S)
    dt = re.search(r"<pubDate>(.*?)</pubDate>", e) or \
         re.search(r"<published>(.*?)</published>", e) or \
         re.search(r"<updated>(.*?)</updated>", e)
    if not (t and u):
        continue
    title = html.unescape(re.sub(r"\s+", " ", t.group(1)).strip())
    url = u.group(1).strip()
    summ = re.sub(r"<[^>]+>", " ", html.unescape(s.group(1))).strip() if s else ""
    summ = re.sub(r"\s+", " ", summ)
    print(json.dumps({"source": name, "url": url,
                      "title": title, "body": summ[:2000],
                      "pubdate": (dt.group(1).strip()[:16] if dt else ""),
                      "extra": f"[{name}] {summ[:600]}"},
                     ensure_ascii=False))
PY
done <<< "$VENDOR_FEEDS"

# ---- dedup: drop URLs already in the SQLite seen-store, emit JSON array ----
# unique_by(.url) collapses dup candidates within this run; seen_store.py
# filter drops anything seen on a prior run (indexed O(log M) lookups).
jq -c -s 'unique_by(.url)' "$TMP" | $SEEN filter
