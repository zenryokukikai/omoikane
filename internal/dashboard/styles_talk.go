package dashboard

// stylesTalk: /talk per-user responder chat.
const stylesTalk = `/* ---- /talk — per-user responder chat ---- */
/* The chat page escapes the 880px article column and follows the
   viewport: full-width fluid layout, tall message pane. Bubbles keep
   a readable line length via their own cap. (:has-less browsers just
   keep the narrow column — harmless.) */
main:has(.talk-layout) { max-width: none; }
.talk-layout { display: flex; gap: 1rem; height: calc(100vh - 8.5rem); min-height: 60vh; }
.talk-side { flex: 0 0 220px; display: flex; flex-direction: column; gap: 0.5rem; }
.talk-new {
  display: block; text-align: center; padding: 0.45rem 0.6rem; border-radius: 6px;
  border: 1px dashed var(--border); color: var(--accent); text-decoration: none;
  font-size: 0.9rem;
}
.talk-new:hover, .talk-new.talk-active { border-style: solid; background: var(--bg-soft); }
.talk-threads { display: flex; flex-direction: column; gap: 0.15rem; overflow-y: auto; }
/* Thread-list disclosure (#131). The past-thread list lives in a <details>
   so mobile can collapse it (media block below); desktop must keep the
   plain always-open sidebar. Both wrappers are made layout-transparent
   with display:contents so .talk-new + .talk-threads stay the two direct
   flex children of .talk-side exactly as before — the <details> and its
   ::details-content pseudo add no box.
   ::details-content matters for a second reason: modern engines (Blink
   131+, WebKit 18.4+) hide a CLOSED details' content by putting
   content-visibility:hidden on that pseudo — which keeps a layout box
   (so getComputedStyle/getClientRects still report it "visible") but
   SKIPS PAINT. Overriding the child's display does NOT defeat that; only
   neutralizing the pseudo does. display:contents on ::details-content
   removes the pseudo's box, so there is nothing left to skip and the list
   paints regardless of the open state — no dependence on the open
   attribute, which is what keeps this working in Safari as well as Blink.
   The list is hidden again on mobile purely by toggling .talk-threads'
   own display (display:none removes its box outright), so the same
   ::details-content rule is harmless there. */
.talk-side-menu { display: contents; }
.talk-side-menu::details-content { display: contents; }
.talk-side-bar { display: none; }
.talk-thread {
  display: flex; flex-direction: column; padding: 0.4rem 0.6rem; border-radius: 6px;
  text-decoration: none; color: inherit;
}
.talk-thread:hover { background: var(--bg-soft); }
.talk-thread.talk-active { background: var(--bg-soft); box-shadow: inset 2px 0 0 var(--accent); }
.talk-thread-title {
  font-size: 0.85rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.talk-thread-date { font-size: 0.7rem; }
.talk-empty { font-size: 0.8rem; padding: 0.4rem 0.6rem; }
.talk-main {
  flex: 1; display: flex; flex-direction: column; min-width: 0;
  border: 1px solid var(--border); border-radius: 8px; background: var(--bg);
}
.talk-head {
  display: flex; align-items: center; gap: 0.6rem; padding: 0.7rem 1rem;
  border-bottom: 1px solid var(--border);
}
.talk-avatar { font-size: 1.6rem; }
.talk-avatar-sm { font-size: 1rem; }
/* Real portrait when the agent user has an avatar_url (uploaded
   attachment) — the emoji stays as the fallback. */
.talk-avatar-img {
  width: 40px; height: 40px; border-radius: 50%; object-fit: cover;
  border: 1px solid var(--border); flex-shrink: 0;
}
.talk-avatar-sm-img {
  width: 26px; height: 26px; border-radius: 50%; object-fit: cover;
  border: 1px solid var(--border); flex-shrink: 0; align-self: flex-end;
}
.talk-name { font-weight: 600; }
/* Personal librarian icon (#85): image variants in the header nav and
   the settings preview. Talk-page images reuse .talk-avatar-*-img. */
.nav-libicon { width: 18px; height: 18px; border-radius: 50%; object-fit: cover; vertical-align: -4px; }
.libicon-preview { width: 32px; height: 32px; border-radius: 50%; object-fit: cover; vertical-align: middle; margin-right: 0.3rem; border: 1px solid var(--border); }
.libicon-preview-text { font-size: 1.3rem; margin-right: 0.3rem; vertical-align: middle; }
.inline-check { display: flex; align-items: center; gap: 0.4rem; }
.talk-sub { font-size: 0.75rem; }
/* Row spacing is margin-based, not flex gap: rows arrive inside
   display:contents fragment wrappers (.talk-frag), and iOS Safari does
   not apply container gap around items promoted out of display:contents
   (#83 — bubbles rendered flush on mobile). */
.talk-messages { flex: 1; overflow-y: auto; padding: 0.4rem 1rem 1rem; display: flex; flex-direction: column; }
.talk-msg { display: flex; gap: 0.45rem; align-items: flex-end; margin-top: 0.6rem; }
.talk-msg-me { justify-content: flex-end; }
.talk-msg-bot { justify-content: flex-start; }
.talk-bubble {
  max-width: min(78%, 56rem); padding: 0.5rem 0.8rem; border-radius: 12px; font-size: 0.92rem;
  border: 1px solid var(--border); overflow-wrap: anywhere;
}
.talk-msg-me .talk-bubble { background: var(--accent); color: #fff; border-color: var(--accent); }
.talk-msg-me .talk-bubble a { color: #eaf2ff; }
.talk-msg-me .talk-time { color: rgba(255,255,255,0.75); }
.talk-msg-bot .talk-bubble { background: var(--bg-soft); }
.talk-bubble .md p:first-child { margin-top: 0; }
.talk-bubble .md p:last-child { margin-bottom: 0; }
.talk-time { font-size: 0.68rem; text-align: right; margin-top: 0.2rem; }
.talk-greeting { margin: auto; text-align: center; max-width: 34rem; }
/* Virtualized message list (#45): fragment wrappers are layout-transparent
   so prepended/appended windows nest without affecting flex spacing, and
   offscreen rows skip layout+paint entirely — long threads stay light. */
.talk-frag { display: contents; }
/* Upward infinite scroll does its own scroll compensation after a
   prepend; the browser's native scroll anchoring would correct AGAIN
   on the forced layout in between, jumping the view a whole window
   down (#57-1, reproduced in Chromium). One corrector only: ours. */
.talk-messages { overflow-anchor: none; }
.talk-msg { content-visibility: auto; contain-intrinsic-size: auto 90px; }
/* Own message awaiting server confirmation — dimmed until the POST
   returns the stored id, then the class drops and it turns solid. */
.talk-msg-sending { opacity: 0.45; transition: opacity 0.2s ease; }
.talk-top-sentinel { text-align: center; font-size: 0.8rem; padding: 0.4rem; }
.talk-pending {
  display: flex; align-items: center; gap: 0.45rem;
  margin-top: 0.6rem; /* row spacing is margin-based, see .talk-msg */
  padding: 0.3rem 0.4rem; font-size: 0.85rem; color: var(--muted);
}
/* display:flex above overrides the UA's [hidden]{display:none} (author
   origin wins), which kept the 考えております… line permanently visible
   no matter what JS set p.hidden to. Restate it at author level. */
.talk-pending[hidden] { display: none; }
.talk-dots::after { content: ""; animation: talkdots 1.5s steps(4) infinite; }
@keyframes talkdots { 0% { content: ""; } 25% { content: "."; } 50% { content: ".."; } 75% { content: "..."; } }
.talk-compose {
  display: flex; gap: 0.5rem; padding: 0.7rem; border-top: 1px solid var(--border);
}
.talk-compose textarea {
  flex: 1; resize: none; border: 1px solid var(--border); border-radius: 8px;
  padding: 0.5rem 0.7rem; font: inherit; background: var(--bg); color: inherit;
}
.talk-send {
  align-self: flex-end; background: var(--accent); color: #fff; border: none;
  border-radius: 8px; padding: 0.5rem 1.1rem; cursor: pointer; font-size: 0.9rem;
}
.talk-send:hover { background: var(--accent-strong); }
@media (max-width: 720px) {
  .talk-layout { flex-direction: column; height: auto; }
  .talk-side { flex: none; }
  .talk-main { min-height: 70vh; }
  /* #131: on a phone the thread list is a tap-to-open menu so the
     conversation is on screen first. The <details> is a real box again;
     the summary is a compact bar (⚙-menu idiom: native marker hidden, own
     caret), and the list only shows when the user opens it. */
  .talk-side-menu { display: block; }
  .talk-side-bar {
    display: flex; align-items: center; gap: 0.4rem; cursor: pointer;
    list-style: none; padding: 0.5rem 0.7rem; border-radius: 6px;
    border: 1px solid var(--border); font-size: 0.9rem; color: var(--accent);
  }
  .talk-side-bar::-webkit-details-marker { display: none; }
  .talk-side-bar::after { content: "▾"; margin-left: auto; transition: transform 0.15s ease; }
  .talk-side-menu[open] > .talk-side-bar { background: var(--bg-soft); }
  .talk-side-menu[open] > .talk-side-bar::after { transform: rotate(180deg); }
  .talk-side-menu:not([open]) > .talk-threads { display: none; }
  .talk-side-menu[open] > .talk-threads { margin-top: 0.4rem; max-height: 55vh; }
  /* #131 (header): on a phone the whole global chrome above the talk
     layout — the nav-link cluster, the search box, the Members/Invite/
     avatar row — is noise that pushes the conversation further down.
     While the talk layout is on screen, collapse the header to a slim
     line: only the brand (the unclassed first link) and the ⚙ ops menu
     survive. Page-scoped with body:has(.talk-layout): the header is a
     sibling of <main>, so this reaches it from OUTSIDE main — the same
     :has() dependency already shipped as main:has(.talk-layout) above.
     Every other page, and desktop /talk (>720px), keep the full header. */
  body:has(.talk-layout) header .nav-journal,
  body:has(.talk-layout) header .header-search,
  body:has(.talk-layout) header .header-invite-form,
  body:has(.talk-layout) header .header-user,
  body:has(.talk-layout) header > .muted,
  body:has(.talk-layout) header > .spacer { display: none; }
}
`
