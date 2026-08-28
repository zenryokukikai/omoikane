package dashboard

// stylesOps: header ops menu dropdown.
const stylesOps = `/* ---- header ⚙ ops menu (issue #22) — pure CSS dropdown ---- */
.nav-ops { position: relative; }
.nav-ops > summary {
  list-style: none; cursor: pointer; user-select: none;
  padding: 0.15rem 0.45rem; border-radius: 6px; border: 1px solid transparent;
}
.nav-ops > summary::-webkit-details-marker { display: none; }
.nav-ops[open] > summary { border-color: var(--border); background: var(--bg-soft); }
.nav-ops-menu {
  position: absolute; right: 0; top: calc(100% + 0.35rem); z-index: 30;
  display: flex; flex-direction: column; min-width: 12rem;
  background: var(--surface); border: 1px solid var(--border); border-radius: 8px;
  box-shadow: 0 6px 20px rgba(0,0,0,0.12); padding: 0.4rem 0;
}
.nav-ops-menu a { padding: 0.35rem 0.9rem; font-weight: 500; font-size: 0.9rem; }
.nav-ops-menu a:hover { background: var(--bg-soft); }
.nav-ops-menu hr { border: none; border-top: 1px solid var(--border); margin: 0.3rem 0; }
/* Mobile (issue #69): with right:0 anchored to the ⚙ button, the
   12rem panel ran off the LEFT edge of the screen once the header
   wrapped and the button sat near the left. Re-anchor the panel to
   the header itself: dropping position:relative from .nav-ops makes
   the sticky header (a positioned ancestor) the containing block, so
   left/right pin the panel inside the viewport at ANY wrap position
   and top:100% lands it just under the full (wrapped) header. Wider
   tap targets while we're there. Desktop keeps the button-anchored
   dropdown untouched. */
@media (max-width: 720px) {
  .nav-ops { position: static; }
  .nav-ops-menu {
    left: 0.6rem; right: 0.6rem; top: calc(100% + 0.25rem);
    min-width: 0; max-height: 70vh; overflow-y: auto;
  }
  .nav-ops-menu a { padding: 0.6rem 1rem; }
}

`

// stylesEntryLists: entry row lists, form width fixes, /directives.
const stylesEntryLists = `/* ---- entry row lists (/entries, /search — issue #19) ---- */
.entry-rows { list-style: none; padding: 0; margin: 0.4rem 0; }
.entry-row {
  padding: 0.55rem 0.3rem; border-bottom: 1px solid var(--border);
}
.entry-row:last-child { border-bottom: none; }
.entry-row-main { display: flex; align-items: baseline; gap: 0.5rem; flex-wrap: wrap; }
.entry-row-title { font-weight: 600; text-decoration: none; overflow-wrap: anywhere; }
.entry-row-meta { margin-top: 0.15rem; font-size: 0.78rem; overflow-wrap: anywhere; }

/* Forms never force a min-width (issue #19: /agents invite form was
   406px wide at a 375px viewport). max-width alone cannot beat an
   intrinsically-sized ancestor chain — a <select> whose longest
   <option> is ~390px sizes its label, form, and details to match.
   Pin the controls to the container width instead. */
form label input, form label select { max-width: 100%; min-width: 0; }
.filter-form, .chat-newthread form { flex-wrap: wrap; }
.chat-newthread label { display: block; width: 100%; }
.chat-newthread label input, .chat-newthread label select { width: 100%; box-sizing: border-box; }

/* ---- /directives (issue #31) ---- */
.directive-form { display: flex; gap: 0.5rem; margin: 0.6rem 0 1rem; }
.directive-form textarea {
  flex: 1; border: 1px solid var(--border); border-radius: 8px;
  padding: 0.5rem 0.7rem; font: inherit; background: var(--bg); color: inherit; resize: vertical;
}
.directive-list { list-style: none; padding: 0; margin: 0; }
.directive-row { padding: 0.6rem 0.3rem; border-bottom: 1px solid var(--border); }
.directive-row:last-child { border-bottom: none; }
.directive-off .directive-text { color: var(--muted); text-decoration: line-through; }
.directive-text { overflow-wrap: anywhere; }
.directive-meta { font-size: 0.78rem; margin-top: 0.15rem; }

/* ---- /entries filter form (issue #119) ---- */
/* Each label wraps its own control (<label>type<select>…). Without
   layout rules the label text and its control are plain inline boxes,
   so the browser is free to line-break BETWEEN them — on a narrow
   screen every control drifts under the NEXT label's text and appears
   to belong to it. Make each label an inline-flex "one unbreakable
   chunk" (nowrap) so the text and its control never separate. */
.entries-filter { display: flex; flex-wrap: wrap; align-items: center; gap: 0.5rem 0.8rem; }
.entries-filter label { display: inline-flex; align-items: center; gap: 0.35rem; white-space: nowrap; }
.checkbox-inline { display: inline-flex; align-items: center; gap: 0.4rem; white-space: nowrap; }
/* Narrow (same 720px breakpoint as the rest of the dashboard): one
   item per line, label above its full-width control. */
@media (max-width: 720px) {
  .entries-filter label { flex-direction: column; align-items: stretch; width: 100%; gap: 0.2rem; white-space: normal; }
  .entries-filter input[type="text"], .entries-filter select { width: 100%; }
  /* The checkbox keeps text beside the box even when narrow — stacking
     would leave the □ floating alone above "include SUPERSEDED". */
  .entries-filter label.checkbox-inline { flex-direction: row; align-items: center; width: auto; }
  .entries-filter button[type="submit"] { width: 100%; }
}

`

// stylesHome: home front page.
const stylesHome = `/* ---- home front page (issue #21) ---- */
.home-journal {
  border: 1px solid var(--border); border-left: 3px solid var(--accent);
  border-radius: 8px; background: var(--surface); padding: 0.9rem 1.1rem; margin: 0.4rem 0 1.2rem;
}
.home-journal-head { display: flex; align-items: center; gap: 0.6rem; }
.home-journal-icon { font-size: 1.4rem; }
.home-journal-title { font-weight: 700; font-size: 1.05rem; text-decoration: none; }
.home-journal-sub { font-size: 0.75rem; }
.home-journal-teaser { margin: 0.6rem 0 0.4rem; color: var(--fg); font-size: 0.92rem; line-height: 1.6; }
.home-more { font-size: 0.85rem; margin-right: 0.9rem; }
.home-h { display: flex; align-items: baseline; gap: 0.8rem; }
.home-fresh { list-style: none; padding: 0; margin: 0.3rem 0 0.5rem; }
.home-fresh li {
  display: flex; align-items: baseline; gap: 0.6rem; padding: 0.35rem 0.2rem;
  border-bottom: 1px solid var(--border);
}
.home-fresh li:last-child { border-bottom: none; }
.home-fresh-title { flex: 1; min-width: 0; text-decoration: none; overflow-wrap: anywhere; }
.home-fresh-meta { flex-shrink: 0; font-size: 0.75rem; display: inline-flex; gap: 0.4rem; align-items: center; }
.home-fold { margin: 1rem 0; border: 1px solid var(--border); border-radius: 8px; background: var(--surface); }
.home-fold > summary {
  cursor: pointer; padding: 0.6rem 0.9rem; font-weight: 600; user-select: none;
}
.home-fold[open] > summary { border-bottom: 1px solid var(--border); }
.home-fold > *:not(summary) { margin-left: 0.9rem; margin-right: 0.9rem; }
.home-fold > table { margin-bottom: 0.9rem; width: calc(100% - 1.8rem); }
.home-fold-note { margin-top: 0.6rem; }
@media (max-width: 720px) {
  .home-fresh li { flex-direction: column; gap: 0.15rem; }
}

`
