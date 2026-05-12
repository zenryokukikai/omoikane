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
.body pre, code { background: var(--code-bg); padding: 0 0.25em; border-radius: 3px; }
.body pre { padding: 0.6rem 0.8rem; overflow-x: auto; }
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
`
