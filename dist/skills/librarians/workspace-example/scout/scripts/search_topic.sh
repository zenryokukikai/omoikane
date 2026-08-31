#!/usr/bin/env bash
# Directed search for ONE topic (operator directive): arXiv full-text
# search + Hacker News (Algolia) — candidates the normal feeds may
# never surface. Emits the same candidate JSON shape as
# fetch_candidates.sh, already seen-filtered.
#   search_topic.sh "<query terms>"
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
Q="${1:?query required}"
TMP=$(mktemp); trap 'rm -f "$TMP"' EXIT

# arXiv full-text search, newest first.
# arXiv search syntax: AND the terms across all: fields (a single
# all:-prefixed multiword string is unreliable). The search endpoint
# can be slow — generous timeout, retries.
AXQ=$(python3 -c "
import urllib.parse, sys
terms = sys.argv[1].split()
q = ' AND '.join('all:' + t for t in terms)
print(urllib.parse.quote(q))
" "$Q")
xml=$(curl --retry 2 --retry-delay 2 -sS -m 40 "https://export.arxiv.org/api/query?search_query=${AXQ}&sortBy=submittedDate&sortOrder=descending&max_results=8" 2>/dev/null || true)
if [ -n "$xml" ]; then
    ARXIV_XML="$xml" python3 - <<'PY' >> "$TMP" || true
import re, json, html, os
x = os.environ.get("ARXIV_XML", "")
for e in re.findall(r"<entry>(.*?)</entry>", x, re.S):
    t = re.search(r"<title>(.*?)</title>", e, re.S)
    u = re.search(r"<id>(.*?)</id>", e)
    s = re.search(r"<summary>(.*?)</summary>", e, re.S)
    if not (t and u):
        continue
    title = html.unescape(re.sub(r"\s+", " ", t.group(1)).strip())
    summ = html.unescape(re.sub(r"\s+", " ", s.group(1)).strip()) if s else ""
    print(json.dumps({"source": "directed_arxiv", "url": u.group(1).strip(),
                      "title": title, "body": summ[:2000],
                      "extra": f"[directed arxiv] {summ[:2000]}"}))
PY
fi

# Hacker News search (Algolia, key-free), last 30 days, by relevance.
SINCE=$(( $(date +%s) - 30*24*3600 ))
curl --retry 3 -sS -m 15 "https://hn.algolia.com/api/v1/search?query=$(python3 -c "import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))" "$Q")&tags=story&numericFilters=created_at_i%3E${SINCE}&hitsPerPage=8" 2>/dev/null \
  | jq -c '.hits[]? | select(.url != null) | {source:"directed_hn", url:.url, title:(.title // ""),
        body:"", extra:("[directed hn] points=" + ((.points // 0)|tostring) + ", " + ((.num_comments // 0)|tostring) + " comments")}' >> "$TMP" || true

# Hugging Face model search (resilience leg — arXiv fulltext search
# has outages; HF covers "did someone ship a model for this" topics).
curl --retry 3 -sS -m 15 "https://huggingface.co/api/models?search=$(python3 -c "import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))" "$Q")&sort=lastModified&direction=-1&limit=6" 2>/dev/null \
  | jq -c '.[]? | {source:"directed_hf", url:("https://huggingface.co/" + (.id // "")),
        title:(.id // ""), body:"",
        extra:("[directed hf model] likes=" + ((.likes // 0)|tostring)
               + (if (.pipeline_tag // "") != "" then ", task=" + .pipeline_tag else "" end))}' >> "$TMP" || true

jq -c -s 'unique_by(.url)' "$TMP" | python3 "$SCRIPT_DIR/seen_store.py" filter
