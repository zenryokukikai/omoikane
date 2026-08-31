package dashboard

// stylesReading: reading views: measure cap, journal sheet, journal index, home callout.
const stylesReading = `/* ---- Reading views (journal + entry body): the pages the design is for ---- */
/* Cap the measure for comfortable bilingual reading. */
.reading { max-width: 70ch; }
.reading .body.md { font-size: 1.05rem; line-height: 1.8; }
.reading .body.md h1, .reading .body.md h2, .reading .body.md h3 {
  font-family: var(--font-serif); font-weight: 600;
}
.reading .body.md h1 { font-size: 1.5rem; }
.reading .body.md h2 {
  font-size: 1.2rem; border-bottom: none;
  margin-top: 1.6em; padding-bottom: 0;
}
/* a quiet terracotta tick before each section heading */
.reading .body.md h2::before {
  content: ""; display: inline-block; width: 0.5rem; height: 0.5rem;
  background: var(--accent); border-radius: 1px;
  margin-right: 0.5rem; vertical-align: 0.12em;
}
.reading .body.md li { margin: 0.35em 0; }

/* The journal sheet: a calm surface page, the morning read. */
.journal-sheet {
  background: var(--surface);
  border: 1px solid var(--hairline);
  border-radius: 8px;
  box-shadow: var(--shadow);
  padding: 2.5rem 2.75rem;
  margin: 0 auto;
}
.journal-sheet h1.journal-date {
  font-family: var(--font-serif); margin: 0 0 0.15rem;
}
.journal-sheet .journal-written {
  font-family: var(--font-mono); font-size: 0.8rem;
  letter-spacing: 0.03em; margin-bottom: 1.4rem;
}
@media (max-width: 640px) { .journal-sheet { padding: 1.5rem 1.25rem; } }

/* Journal index: a list of mornings. */
.journal-list { list-style: none; padding: 0; margin: 1rem 0; }
.journal-list li {
  border-bottom: 1px solid var(--hairline);
  padding: 0.9rem 0.2rem;
}
.journal-list li:last-child { border-bottom: none; }
.journal-list .j-date {
  font-family: var(--font-mono); font-size: 0.85rem; color: var(--muted);
  display: inline-block; min-width: 7.5rem;
}
.journal-list .j-title { font-family: var(--font-serif); font-size: 1.05rem; }
.journal-list a { text-decoration: none; }
.journal-list a:hover .j-title { text-decoration: underline; }
.journal-list .j-written {
  display: block; font-family: var(--font-mono); font-size: 0.78rem;
  letter-spacing: 0.03em; margin-top: 0.2rem; margin-left: 7.7rem;
}
@media (max-width: 640px) { .journal-list .j-written { margin-left: 0; } }

/* "This morning" callout on home. */
.journal-today {
  display: block; background: var(--surface);
  border: 1px solid var(--hairline); border-left: 3px solid var(--accent);
  border-radius: 8px; box-shadow: var(--shadow);
  padding: 1rem 1.25rem; margin: 0 0 1.5rem; text-decoration: none; color: var(--fg);
}
.journal-today:hover { background: var(--hover); }
.journal-today .jt-label {
  font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.08em; color: var(--accent);
}
.journal-today .jt-title { font-family: var(--font-serif); font-size: 1.15rem; margin-top: 0.2rem; }

`

// stylesComments: entry review comments, author card, bookmark toggle.
const stylesComments = `/* Entry review comments (§23.21) — humans + agents */
.comments { margin-top: 2.5rem; border-top: 1px solid var(--border); padding-top: 1rem; }
.cmt-list { display: flex; flex-direction: column; gap: 0.75rem; margin-bottom: 1.25rem; }
.cmt {
  border: 1px solid var(--border); border-radius: 6px; padding: 0.6rem 0.8rem;
  background: var(--surface);
}
.cmt-resolved { opacity: 0.6; }
.cmt-reply { margin: 0.5rem 0 0 1.25rem; border-left: 2px solid var(--border); background: transparent; }
.cmt-head { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; font-size: 0.85rem; }
.cmt-author { font-weight: 600; display: inline-flex; align-items: center; gap: 0.3rem; }
.cmt-avatar {
  width: 22px; height: 22px; border-radius: 50%; object-fit: cover;
  border: 1px solid var(--border); vertical-align: middle; flex-shrink: 0;
}
.cmt-avatar-placeholder {
  display: inline-flex; align-items: center; justify-content: center;
  width: 22px; height: 22px; border-radius: 50%;
  background: var(--accent); color: #fff; font-size: 0.7rem; font-weight: 600;
  text-transform: uppercase; flex-shrink: 0;
}
.entry-author-card {
  display: flex; align-items: center; gap: 0.5rem;
  margin: 0.25rem 0 1rem; padding: 0.5rem 0.75rem;
  background: var(--surface); border: 1px solid var(--border); border-radius: 8px;
}
.entry-author-avatar {
  width: 36px; height: 36px; border-radius: 50%; object-fit: cover;
  border: 2px solid var(--accent); flex-shrink: 0;
}
.entry-author-avatar-ph {
  display: inline-flex; align-items: center; justify-content: center;
  background: var(--accent); color: #fff; font-size: 0.85rem; font-weight: 700;
  text-transform: uppercase;
}
.entry-author-info { display: flex; flex-direction: column; gap: 0.1rem; }
.entry-author-name { font-weight: 600; font-size: 0.95rem; color: var(--fg); }
.entry-author-date { font-size: 0.75rem; color: var(--muted); }
.cmt-agent { color: var(--accent); }
.cmt-role {
  font-size: 0.7em; font-weight: normal; color: var(--muted);
  border: 1px solid var(--border); border-radius: 3px; padding: 0 0.3em;
}
.cmt-time { font-size: 0.78rem; }
.cmt-badge {
  font-size: 0.7rem; color: #fff; background: var(--accent); border-radius: 3px; padding: 0 0.4em;
}
.cmt-mention {
  font-size: 0.72rem; color: var(--accent); border: 1px solid var(--accent);
  border-radius: 3px; padding: 0 0.35em; font-weight: 600;
}
/* "↪ <author>" on a reply that answers a sibling rather than the thread
   root (issue #122). Unstyled it was .muted body text sitting in the head
   row at the same weight as the timestamp, so the one piece of structure
   a nested thread has read as noise. Give it the same faint outline the
   role chip uses, so it reads as a pointer, and let a long display name
   wrap instead of stretching the head row past a 320px screen. */
.cmt-replyto {
  font-size: 0.72rem; border: 1px solid var(--hairline); background: var(--bg);
  border-radius: 3px; padding: 0 0.4em; min-width: 0; overflow-wrap: anywhere;
}
.cmt-body { margin-top: 0.35rem; white-space: pre-wrap; line-height: 1.55; }
/* Markdown comments are HTML — pre-wrap would render the whitespace
   between block tags as phantom blank lines. Long URLs and inline code
   must break rather than push the thread wide (issue #20). */
.cmt-body.md { white-space: normal; overflow-wrap: anywhere; min-width: 0; }
.cmt-body.md pre { max-width: 100%; }
.cmt-body.md code { overflow-wrap: anywhere; }
@media (max-width: 720px) {
  .cmt-reply { margin-left: 0.6rem; padding-left: 0.5rem; }
}
.cmt-actions { margin-top: 0.4rem; display: flex; gap: 0.5rem; }
.cmt-btn {
  font-size: 0.75rem; background: none; border: 1px solid var(--border); border-radius: 4px;
  padding: 0.1rem 0.5rem; cursor: pointer; color: var(--muted);
}
.cmt-btn:hover { color: var(--accent); border-color: var(--accent); }
.cmt-form { display: flex; flex-direction: column; gap: 0.5rem; margin-top: 0.5rem; }
.cmt-form textarea {
  width: 100%; border: 1px solid var(--border); border-radius: 6px; padding: 0.5rem;
  font: inherit; resize: vertical; box-sizing: border-box;
}
.cmt-submit {
  align-self: flex-start; background: var(--accent); color: #fff; border: none;
  border-radius: 4px; padding: 0.35rem 0.9rem; cursor: pointer; font-size: 0.85rem;
}
.cmt-submit:hover { background: var(--accent-strong); }
.cmt-reply-form { margin: 0.5rem 0 0 1.25rem; }
/* Markdown comments: chat-grade compact spacing. Without these the
   browser defaults (1em top+bottom on p/ul) apply — the .md element
   rules are scoped to .body.md / .chat-body.md and never matched
   .cmt-body, which made comments look full of holes. */
.cmt-body.md p { margin: 0.3em 0; }
.cmt-body.md ul, .cmt-body.md ol { margin: 0.3em 0; padding-left: 1.4em; }
.cmt-body.md li { margin: 0.1em 0; }
.cmt-body.md li > p { margin: 0; }
.cmt-body.md h1, .cmt-body.md h2, .cmt-body.md h3, .cmt-body.md h4 {
  margin: 0.5em 0 0.25em; font-size: 1em; line-height: 1.3;
}
.cmt-body.md p:first-child, .cmt-body.md ul:first-child, .cmt-body.md ol:first-child { margin-top: 0; }
.cmt-body.md p:last-child, .cmt-body.md ul:last-child, .cmt-body.md ol:last-child { margin-bottom: 0; }
.cmt-body.md pre { overflow-x: auto; margin: 0.4em 0; }

/* Bookmark toggle (entry meta row) */
.bm-btn {
  border: 1px solid var(--border); background: var(--bg); color: var(--muted);
  border-radius: 999px; padding: 0.1rem 0.6rem; font-size: 0.78rem; cursor: pointer;
}
.bm-btn:hover { color: var(--accent); border-color: var(--accent); }
.bm-btn.bm-on { color: var(--accent); border-color: var(--accent); background: var(--bg-soft); }

`
