package dashboard

// stylesBase: base tokens, reset, header, typography, tables, badges, markdown bodies, footer, pager.
const stylesBase = `
/* omoikane — Quiet Archive. Tokens mirror DESIGN.md at the repo root;
   change colours/type there first, then reflect here. */
:root {
  --bg: #FAF8F3;          /* paper */
  --surface: #FEFDFA;     /* cards, journal sheet, header */
  --fg: #1F1D1A;          /* ink */
  --muted: #5F5A54;
  --border: #E6E1D8;      /* faint */
  --hairline: #EDE9E1;
  --accent: #8A4B2A;      /* terracotta — interaction only */
  --accent-strong: #6E3A20;  /* hover/pressed */
  --hover: #EFE3D8;       /* accent-soft tint */
  --badge-bg: #E6E1D8;
  --code-bg: #F1EDE5;
  --shadow: 0 1px 2px rgba(31,29,26,0.05);
  --font-body: system-ui, -apple-system, "Hiragino Sans", "Noto Sans JP", "Yu Gothic", sans-serif;
  --font-serif: "Iowan Old Style", Charter, Georgia, "Hiragino Mincho ProN", "Yu Mincho", serif;
  --font-mono: ui-monospace, SFMono-Regular, Menlo, "Cascadia Code", monospace;
}
* { box-sizing: border-box; }
html, body {
  margin: 0; padding: 0;
  background: var(--bg); color: var(--fg);
  font-family: var(--font-body);
  font-size: 16px; line-height: 1.7;
  -webkit-font-smoothing: antialiased;
}
header {
  background: var(--surface); border-bottom: 1px solid var(--border);
  padding: 0.75rem 1.25rem; display: flex; align-items: center; gap: 1rem;
  position: sticky; top: 0; z-index: 10;
  /* Mobile: wrap instead of forcing a ~1000px min-width on every page
     (issue #18 — the single dominant cause of site-wide horizontal
     scroll at 375px). */
  flex-wrap: wrap; row-gap: 0.4rem;
}
header a { text-decoration: none; color: var(--fg); font-weight: 600; }
header a:hover { color: var(--accent); }
header a.nav-journal { color: var(--accent); font-weight: 600; }
header a.nav-journal:hover { color: var(--accent-strong); text-decoration: underline; }
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
header .header-search button:hover { background: var(--accent-strong); }
header .header-invite-form { display: inline-flex; margin: 0; }
header button.header-invite,
header .header-invite {
  display: inline-flex; align-items: center; gap: 0.3rem;
  padding: 0.3rem 0.7rem; background: var(--hover); border: 1px solid var(--border);
  border-radius: 14px; font: inherit; font-size: 0.85rem; font-weight: 600;
  color: var(--accent); text-decoration: none; cursor: pointer;
  transition: background 0.15s;
}
header button.header-invite:hover,
header .header-invite:hover { background: var(--badge-bg); color: var(--accent-strong); }
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
  border: 1px solid var(--border); background: var(--badge-bg);
  display: inline-flex; align-items: center; justify-content: center;
}
header .avatar-placeholder {
  background: var(--accent); color: #fff; font-weight: 600;
  font-size: 0.85rem; text-transform: uppercase;
}
/* Narrow screens. THIS BLOCK MUST STAY BELOW THE HEADER RULES ABOVE.
   It was written above them, and that alone was issue #123: its
   "min-width: 0" for the search input and the desktop
   "header input[type=search] { min-width: 280px }" have IDENTICAL
   specificity (0,1,2 both — one class + one attribute + elements), so
   the cascade fell through to source order and the desktop floor won at
   every viewport. The box could not shrink, and its 280px + gap + "go"
   button held EVERY page open at 343px on a 320px screen.
   Moving the block is the whole fix: nothing here needs extra weight,
   an !important, or a narrower selector — it only needs to come last,
   which is the rule this stylesheet already states at the top of
   styles.go ("later rules deliberately override earlier ones"). */
@media (max-width: 720px) {
  header { padding: 0.6rem 0.9rem; gap: 0.7rem; }
  /* The search cluster takes a row of its own and the input fills it. */
  header .header-search { flex-basis: 100%; }
  header .header-search input { flex: 1; min-width: 0; }
}
main { max-width: 880px; margin: 0 auto; padding: 1.5rem 1.25rem 2.5rem; }
h1 { font-family: var(--font-serif); font-size: 1.9rem; font-weight: 600; line-height: 1.25; letter-spacing: -0.01em; margin: 0 0 1rem; }
h2 { font-family: var(--font-serif); font-size: 1.3rem; font-weight: 600; margin: 1.75rem 0 0.6rem; border-bottom: 1px solid var(--hairline); padding-bottom: 0.3rem; }
table {
  width: 100%; border-collapse: collapse;
  background: var(--surface); border: 1px solid var(--border); border-radius: 6px; overflow: hidden;
}
/* Mobile: a wide table scrolls INSIDE its own box instead of widening
   the page (issue #19 — up to 844px-wide tables at 375px). display:
   block turns the table element into its own scroll container without
   any template changes; cell content keeps table layout. */
@media (max-width: 720px) {
  table { display: block; overflow-x: auto; -webkit-overflow-scrolling: touch; }
}
th, td { padding: 0.5rem 0.75rem; text-align: left; border-bottom: 1px solid #eee; vertical-align: top; }
th { background: var(--badge-bg); font-size: 0.85rem; text-transform: uppercase; letter-spacing: 0.05em; }
tr:last-child td { border-bottom: none; }
tr:hover { background: var(--hover); }
a { color: var(--accent); }
.badge { display: inline-block; background: var(--badge-bg); padding: 1px 6px; border-radius: 4px;
         font-size: 0.78rem; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; margin-right: 0.25rem; }
.badge-status-ACTIVE   { background: var(--badge-bg); color: #3E5A40; }
.badge-status-DRAFT    { background: var(--badge-bg); color: #5F5A52; }
.badge-status-ARCHIVED { background: var(--badge-bg); color: #615B52; }
.badge-status-INVESTIGATING { background: var(--badge-bg); color: #83451F; }
.badge-status-SUPERSEDED, .badge-status-DUPLICATE, .badge-status-RESOLVED { background: var(--badge-bg); color: #615B52; }
/* Space badge (issue #122): "🔒 <space>" says this entry is NOT public.
   Sharing the neutral --badge-bg with the id/type/status badges beside
   it made that the one badge in the row nobody could pick out. Take the
   accent tint + accent outline instead — the same "interaction accent
   means something" vocabulary the rest of the page uses — and the body
   font, since a space name is prose (Japanese), not an identifier.
   Padding drops to 0/5px so the 1px border keeps the outer box the same
   height as its neutral neighbours and the meta row stays on one line. */
.badge-space {
  background: var(--hover); color: var(--accent-strong);
  border: 1px solid var(--accent); padding: 0 5px;
  font-family: var(--font-body); font-size: 0.75rem; font-weight: 600;
}
.muted { color: var(--muted); font-size: 0.9rem; }
.field { margin: 1rem 0; }
.field > .label {
  font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.06em;
  color: var(--muted); margin-bottom: 0.25rem;
}
.body { white-space: pre-wrap; background: var(--surface); padding: 1rem; border: 1px solid var(--border); border-radius: 6px; }
/* overflow-wrap on inline code is load-bearing, not cosmetic (issue
   #123): an identifier like KB_OAUTH_GOOGLE_CLIENT_SECRET has no break
   opportunity, so it sets a min-content width its container cannot go
   below — that alone made /login 350px wide at a 320px viewport. */
.body pre, code { background: var(--code-bg); padding: 0 0.25em; border-radius: 3px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.92em; overflow-wrap: anywhere; }
.body pre { padding: 0.6rem 0.8rem; overflow-x: auto; }
/* JSON metadata blob: wrap rather than overflow horizontally — long JSON
   lines (chronicler stats, situation membership, etc.) routinely exceed
   the reading column width otherwise. */
.body pre.meta-json {
  white-space: pre-wrap; word-break: break-all; max-width: 100%;
  font-size: 0.78em; line-height: 1.45;
}
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
.body.md blockquote { margin: 0.5em 0; padding: 0.3em 0.9em; border-left: 3px solid var(--accent); color: var(--muted); background: var(--bg); }
.body.md a { color: var(--accent); }
.body.md table { margin: 0.7em 0; border-collapse: collapse; }
.body.md table th, .body.md table td { padding: 0.3rem 0.6rem; border: 1px solid var(--border); }
.body.md table th { background: var(--badge-bg); }
.body.md hr { border: 0; border-top: 1px solid var(--border); margin: 1em 0; }
.body.md input[type=checkbox] { margin-right: 0.4em; }
.body.md del { color: var(--muted); }
footer { padding: 1rem; text-align: center; color: var(--muted); font-size: 0.85rem; }
.pager { display: flex; align-items: center; justify-content: center; gap: 1rem; margin: 1.25rem 0; }
.pager-link { text-decoration: none; padding: 0.3rem 0.8rem; border: 1px solid var(--border); border-radius: 6px; color: var(--accent); font-size: 0.9rem; }
.pager-link:hover { background: var(--hover); color: var(--accent-strong); }
.pager-disabled { color: var(--muted); border-color: var(--hairline); pointer-events: none; opacity: 0.5; }

/* ---- .btn — the primary call-to-action (issue #122) ----
   The reported defect: /entries' "✏️ このスペースに書く" and /talk's
   "サインインして始める" carry class="btn" but no rule existed, so the
   page's single most important action rendered as a bare underlined
   link. A phone user reported it. The class is written on BOTH <a>
   (those two) and <button type=submit> (/entries/new "保存"), so one
   rule has to cover both element types: anchors need the colour and
   text-decoration reset, buttons need the font/border reset. Class
   specificity (0,1,0) beats the generic "form button" rule in
   stylesLogin (0,0,2), so a .btn submit gets THIS look regardless of
   section order.

   min-height is the tap target: 2.75rem = 44px, the smallest control a
   thumb hits reliably. Everything else is the existing vocabulary —
   --accent fill with --accent-strong on hover (as .header-search
   button and .cmt-submit already do), 6px radius (as .pager-link),
   --shadow from :root. */
.btn {
  display: inline-flex; align-items: center; justify-content: center; gap: 0.4rem;
  min-height: 2.75rem; padding: 0.5rem 1.15rem;
  background: var(--accent); color: #fff;
  border: 1px solid transparent; border-radius: 6px;
  font: inherit; font-weight: 600; line-height: 1.35;
  text-align: center; text-decoration: none; cursor: pointer;
  box-shadow: var(--shadow); transition: background 0.15s;
}
.btn:hover { background: var(--accent-strong); color: #fff; }
/* :focus-visible only — a mouse click must not leave a ring behind,
   but a keyboard user has to see where they are. Offset so the ring
   sits outside the fill instead of on top of it. */
.btn:focus-visible { outline: 2px solid var(--accent-strong); outline-offset: 2px; }
.btn:active { background: var(--accent-strong); box-shadow: none; transform: translateY(1px); }
/* /entries/new disables the button for the duration of the POST. */
.btn:disabled { opacity: 0.5; cursor: not-allowed; box-shadow: none; transform: none; }
/* Narrow screens (the dashboard's 720px breakpoint): every .btn today
   is a standalone call to action in its own <p>, so give it the full
   column — the same shape .entries-filter's submit already takes. This
   also removes any chance of the label overflowing at 320px. */
@media (max-width: 720px) {
  .btn { width: 100%; }
}

`
