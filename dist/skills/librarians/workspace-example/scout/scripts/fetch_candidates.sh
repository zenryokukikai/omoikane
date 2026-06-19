#!/usr/bin/env bash
# Fetch external candidates for the scout to evaluate, from public,
# key-free sources (the operator-configured allow-list for this scout):
#   - Hacker News top stories (IT news)            — HN Firebase API
#   - arXiv recent submissions in CS/audio cats    — arXiv API
#   - Hugging Face daily papers (curated)          — HF API
#   - Hugging Face trending models (last 7d likes) — HF API
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
    python3 - "$cat" <<PY >> "$TMP" || true
import sys, re, json, html
cat = sys.argv[1]
x = """$xml"""
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
    print(json.dumps({"source": "arxiv", "url": url, "title": title,
                      "body": summ[:600], "pubdate": pubdate,
                      "extra": f"[{cat}] {summ[:400]}"}))
PY
done

# ---- Hugging Face daily papers (curated) ----
HF_PAPERS_LIMIT="${SCOUT_HF_PAPERS_LIMIT:-12}"
curl --retry 5 --retry-connrefused -sS -m 15 "https://huggingface.co/api/daily_papers?limit=${HF_PAPERS_LIMIT}" 2>/dev/null \
    | jq -c '.[] | {
        source:"hf_paper",
        url:("https://huggingface.co/papers/" + (.paper.id // "")),
        title:(.paper.title // ""),
        body:((.paper.summary // "")[:600]),
        pubdate:(((.publishedAt // .paper.publishedAt // "")|tostring)[:10]),
        extra:("[HF daily papers] " + ((.paper.summary // "")[:380]))
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

# ---- dedup: drop URLs already in the SQLite seen-store, emit JSON array ----
# unique_by(.url) collapses dup candidates within this run; seen_store.py
# filter drops anything seen on a prior run (indexed O(log M) lookups).
jq -c -s 'unique_by(.url)' "$TMP" | $SEEN filter
