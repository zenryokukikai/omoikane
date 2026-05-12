package dashboard

const stylesheet = `
:root {
  --bg: #fafafa;
  --fg: #1a1a1a;
  --muted: #666;
  --border: #d8d8d8;
  --accent: #2a6fdb;
  --hover: #e8eef9;
  --badge-bg: #eef;
  --code-bg: #f3f3f3;
}
* { box-sizing: border-box; }
html, body {
  margin: 0; padding: 0;
  background: var(--bg); color: var(--fg);
  font: 15px/1.5 -apple-system, "Helvetica Neue", "Hiragino Sans", system-ui, sans-serif;
}
header {
  background: #fff; border-bottom: 1px solid var(--border);
  padding: 0.75rem 1.25rem; display: flex; align-items: center; gap: 1rem;
  position: sticky; top: 0; z-index: 10;
}
header a { text-decoration: none; color: var(--fg); font-weight: 600; }
header a:hover { color: var(--accent); }
header .spacer { flex: 1; }
header form { display: inline-flex; gap: 0.25rem; }
header input[type=search] {
  border: 1px solid var(--border); padding: 0.4rem 0.6rem;
  border-radius: 4px; min-width: 280px; font: inherit;
}
header .header-search { display: inline-flex; gap: 0.25rem; }
header .header-search button {
  padding: 0.35rem 0.8rem; font: inherit; cursor: pointer;
  background: var(--accent); color: #fff; border: none; border-radius: 4px;
}
header .header-search button:hover { background: #1a5fcb; }
header .header-invite-form { display: inline-flex; margin: 0; }
header button.header-invite,
header .header-invite {
  display: inline-flex; align-items: center; gap: 0.3rem;
  padding: 0.3rem 0.7rem; background: #fff7d6; border: 1px solid #f0d97c;
  border-radius: 14px; font: inherit; font-size: 0.85rem; font-weight: 600;
  color: #6a5300; text-decoration: none; cursor: pointer;
  transition: background 0.15s;
}
header button.header-invite:hover,
header .header-invite:hover { background: #fdeaa3; color: #6a5300; }
header .header-user {
  display: inline-flex; align-items: center; gap: 0.4rem;
  padding: 0.2rem 0.5rem; border-radius: 18px;
  text-decoration: none; transition: background 0.15s;
}
header .header-user:hover { background: var(--hover); }
header .header-user-name {
  font-size: 0.85rem; font-weight: 500; color: var(--fg);
  max-width: 180px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
header .avatar {
  width: 28px; height: 28px; border-radius: 50%; object-fit: cover;
  border: 1px solid var(--border); background: #eee;
  display: inline-flex; align-items: center; justify-content: center;
}
header .avatar-placeholder {
  background: var(--accent); color: #fff; font-weight: 600;
  font-size: 0.85rem; text-transform: uppercase;
}
main { max-width: 1100px; margin: 0 auto; padding: 1.25rem; }
h1 { font-size: 1.5rem; margin: 0 0 1rem; }
h2 { font-size: 1.15rem; margin: 1.5rem 0 0.5rem; border-bottom: 1px solid var(--border); padding-bottom: 0.25rem; }
table {
  width: 100%; border-collapse: collapse;
  background: #fff; border: 1px solid var(--border); border-radius: 6px; overflow: hidden;
}
th, td { padding: 0.5rem 0.75rem; text-align: left; border-bottom: 1px solid #eee; vertical-align: top; }
th { background: #f4f4f4; font-size: 0.85rem; text-transform: uppercase; letter-spacing: 0.05em; }
tr:last-child td { border-bottom: none; }
tr:hover { background: var(--hover); }
a { color: var(--accent); }
.badge { display: inline-block; background: var(--badge-bg); padding: 1px 6px; border-radius: 4px;
         font-size: 0.78rem; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; margin-right: 0.25rem; }
.badge-status-ACTIVE   { background: #e6f4e6; color: #295c29; }
.badge-status-DRAFT    { background: #fff5d6; color: #6a5300; }
.badge-status-ARCHIVED { background: #eee; color: #555; }
.badge-status-INVESTIGATING { background: #fde8e0; color: #8b3a00; }
.badge-status-SUPERSEDED, .badge-status-DUPLICATE, .badge-status-RESOLVED { background: #efefef; color: #555; }
.muted { color: var(--muted); font-size: 0.9rem; }
.field { margin: 1rem 0; }
.field > .label {
  font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.06em;
  color: var(--muted); margin-bottom: 0.25rem;
}
.body { white-space: pre-wrap; background: #fff; padding: 1rem; border: 1px solid var(--border); border-radius: 6px; }
.body pre, code { background: var(--code-bg); padding: 0 0.25em; border-radius: 3px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.92em; }
.body pre { padding: 0.6rem 0.8rem; overflow-x: auto; }
/* Markdown-rendered body: turn off pre-wrap since headings/lists handle their own whitespace */
.body.md { white-space: normal; }
.body.md h1, .body.md h2, .body.md h3, .body.md h4 { margin: 0.7em 0 0.4em; line-height: 1.3; }
.body.md h1 { font-size: 1.25rem; border-bottom: 1px solid var(--border); padding-bottom: 0.25rem; }
.body.md h2 { font-size: 1.1rem; }
.body.md h3 { font-size: 1.0rem; }
.body.md h4 { font-size: 0.95rem; color: var(--muted); }
.body.md p { margin: 0.5em 0; }
.body.md p:first-child { margin-top: 0; }
.body.md p:last-child { margin-bottom: 0; }
.body.md ul, .body.md ol { margin: 0.5em 0; padding-left: 1.5em; }
.body.md li { margin: 0.2em 0; }
.body.md li > p { margin: 0; }
.body.md blockquote { margin: 0.5em 0; padding: 0.3em 0.9em; border-left: 3px solid var(--border); color: var(--muted); background: #fafafa; }
.body.md a { color: var(--accent); }
.body.md table { margin: 0.7em 0; border-collapse: collapse; }
.body.md table th, .body.md table td { padding: 0.3rem 0.6rem; border: 1px solid var(--border); }
.body.md table th { background: #f4f4f4; }
.body.md hr { border: 0; border-top: 1px solid var(--border); margin: 1em 0; }
.body.md input[type=checkbox] { margin-right: 0.4em; }
.body.md del { color: var(--muted); }
footer { padding: 1rem; text-align: center; color: var(--muted); font-size: 0.85rem; }
.empty { padding: 2rem; text-align: center; color: var(--muted); background: #fff;
         border: 1px dashed var(--border); border-radius: 6px; }
.banner { padding: 0.6rem 1rem; background: #fff7d6; border: 1px solid #f0d97c; border-radius: 6px; margin-bottom: 1rem; }
.subnav { margin-bottom: 1rem; color: var(--muted); font-size: 0.9rem; }
.subnav a { text-decoration: none; }
.signals { max-width: 720px; }
.badge-status-OPEN { background: #e6f4e6; color: #295c29; }
.badge-status-PROMOTED { background: #e8eef9; color: #1a3d80; }
.badge-status-DISMISSED { background: #eee; color: #555; }
a.wiki { text-decoration: underline dotted; }
a.wiki:hover { text-decoration: underline; }
ul.hier { list-style: none; padding-left: 0; }
ul.hier li { padding: 0.4rem 0.75rem; background: #fff; border: 1px solid var(--border); border-radius: 6px; margin-bottom: 0.4rem; }

/* Chat room */
details.chat-newthread, details.chat-close {
  background: #fff; border: 1px solid var(--border); border-radius: 6px;
  padding: 0.5rem 1rem; margin: 1rem 0;
}
details.chat-newthread summary, details.chat-close summary { cursor: pointer; padding: 0.3rem 0; }
details.chat-newthread form, details.chat-close form {
  display: flex; gap: 0.6rem; align-items: end; flex-wrap: wrap; margin-top: 0.5rem;
}
details.chat-newthread label, details.chat-close label, .chat-post label {
  display: flex; flex-direction: column; font-size: 0.85rem; color: var(--muted);
}
.chat-stream { display: flex; flex-direction: column; gap: 0.7rem; margin: 1rem 0; }
.chat-msg {
  background: #fff; border: 1px solid var(--border); border-radius: 6px;
  padding: 0.6rem 0.9rem; max-width: 80%;
}
.chat-msg-human { align-self: flex-end; background: #eef5ff; border-color: #c6dcff; }
.chat-msg-coordinator { border-left: 3px solid #f5a623; }
.chat-msg-cataloger   { border-left: 3px solid #6b8e23; }
.chat-msg-curator     { border-left: 3px solid #8a4fff; }
.chat-msg-detective   { border-left: 3px solid #2a6fdb; }
.chat-msg-conservator { border-left: 3px solid #2f7a52; }
.chat-msg-scout       { border-left: 3px solid #d65a8a; }
.chat-msg-summarizer  { border-left: 3px solid #6b6b6b; }
.chat-msg-judge       { border-left: 3px solid #c93b3b; }
.chat-meta { display: flex; gap: 0.5rem; align-items: center; font-size: 0.8rem; margin-bottom: 0.3rem; }
.chat-author-human       { background: #eef5ff; color: #1a3d80; }
.chat-author-coordinator { background: #fdf0d5; color: #6f4500; }
.chat-author-cataloger   { background: #eef7d8; color: #3f5a14; }
.chat-author-curator     { background: #ece0ff; color: #4a2099; }
.chat-author-detective   { background: #e0ebff; color: #1a3d80; }
.chat-author-conservator { background: #d8efe1; color: #1e5234; }
.chat-author-scout       { background: #ffe0ed; color: #832353; }
.chat-author-summarizer  { background: #ebebeb; color: #444; }
.chat-author-judge       { background: #ffdbd8; color: #8a1e1e; }
.chat-body { white-space: pre-wrap; }
/* Markdown-rendered chat body — same flow as .body.md but tighter spacing */
.chat-body.md { white-space: normal; }
.chat-body.md p { margin: 0.3em 0; }
.chat-body.md p:first-child { margin-top: 0; }
.chat-body.md p:last-child { margin-bottom: 0; }
.chat-body.md ul, .chat-body.md ol { margin: 0.3em 0; padding-left: 1.5em; }
.chat-body.md li { margin: 0.1em 0; }
.chat-body.md li > p { margin: 0; }
.chat-body.md h1, .chat-body.md h2, .chat-body.md h3 { margin: 0.5em 0 0.3em; line-height: 1.2; font-size: 1rem; }
.chat-body.md pre { background: var(--code-bg); padding: 0.5rem 0.8rem; border-radius: 4px; overflow-x: auto; margin: 0.4em 0; }
.chat-body.md code { background: var(--code-bg); padding: 0 0.25em; border-radius: 3px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.92em; }
.chat-body.md pre code { background: transparent; padding: 0; }
.chat-body.md blockquote { margin: 0.3em 0; padding: 0.2em 0.6em; border-left: 3px solid var(--border); color: var(--muted); }
.chat-body.md a { color: var(--accent); }
form.chat-post {
  background: #fff; border: 1px solid var(--border); border-radius: 6px;
  padding: 0.8rem 1rem; margin: 1rem 0; display: flex; flex-direction: column; gap: 0.5rem;
}
.chat-post-row { display: flex; gap: 0.6rem; align-items: end; flex-wrap: wrap; }
form.chat-post textarea { font: inherit; padding: 0.5rem; border: 1px solid var(--border); border-radius: 4px; resize: vertical; }
form.chat-post button { align-self: flex-end; padding: 0.4rem 1rem; }

/* @mention inline decoration — same palette as the author badges */
.mention { padding: 0 4px; border-radius: 3px; font-weight: 600; font-size: 0.92em; }
.mention-human       { background: #eef5ff; color: #1a3d80; }
.mention-coordinator { background: #fdf0d5; color: #6f4500; }
.mention-cataloger   { background: #eef7d8; color: #3f5a14; }
.mention-curator     { background: #ece0ff; color: #4a2099; }
.mention-detective   { background: #e0ebff; color: #1a3d80; }
.mention-conservator { background: #d8efe1; color: #1e5234; }
.mention-scout       { background: #ffe0ed; color: #832353; }
.mention-summarizer  { background: #ebebeb; color: #444; }
.mention-judge       { background: #ffdbd8; color: #8a1e1e; }

/* Login page */
.login { max-width: 480px; margin: 3rem auto; padding: 2rem; background: #fff; border: 1px solid var(--border); border-radius: 8px; }
.login h1 { margin-top: 0; }
.btn-google {
  display: inline-flex; align-items: center; gap: 0.7rem;
  padding: 0.7rem 1.2rem; background: #fff; color: #333;
  border: 1px solid #d0d0d0; border-radius: 4px;
  font-weight: 600; text-decoration: none; font-size: 1rem;
  box-shadow: 0 1px 3px rgba(0,0,0,0.08); transition: background 0.15s;
}
.btn-google:hover { background: #f4f4f4; }
.btn-google .g-logo {
  display: inline-flex; align-items: center; justify-content: center;
  width: 22px; height: 22px; background: linear-gradient(45deg, #4285f4 0%, #ea4335 100%);
  color: #fff; border-radius: 2px; font-weight: bold; font-family: serif;
}
.login-token-fallback { margin-top: 2rem; }
.login-future { margin-top: 1.5rem; font-style: italic; }
table.claim-summary { width: 100%; margin: 1rem 0; }
table.claim-summary th { background: transparent; text-transform: none; letter-spacing: 0; font-size: 0.85rem; color: var(--muted); width: 30%; }
.login form button.btn-google { padding: 0.7rem 1.4rem; font-weight: 600; cursor: pointer; background: #2a6fdb; color: #fff; border: none; }
.login form button.btn-google:hover { background: #1a5fcb; }
.banner-success { background: #e6f4e6; border-color: #b8dab8; }
.banner-success p { margin: 0.3rem 0; }
input.copy-target {
  width: 100%; padding: 0.6rem 0.8rem; font: 1.05rem ui-monospace, SFMono-Regular, Menlo, monospace;
  border: 1px solid var(--border); border-radius: 4px; background: #fff;
  margin: 0.4rem 0; cursor: text;
}
input.copy-target:focus { outline: 2px solid var(--accent); }
form button { padding: 0.45rem 1rem; font: inherit; cursor: pointer; background: var(--accent); color: #fff; border: none; border-radius: 4px; }
form button:hover { background: #1a5fcb; }
form input[type=text] { padding: 0.4rem 0.6rem; border: 1px solid var(--border); border-radius: 4px; font: inherit; min-width: 320px; }
`
