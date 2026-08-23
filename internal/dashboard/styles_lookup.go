package dashboard

// stylesLookup: reverse-lookup form/chips/browse list, use-case lists and detail, project primer.
const stylesLookup = `/* Reverse-lookup page form. */
.lookup-form { display: flex; flex-wrap: wrap; gap: 0.5rem; align-items: center; margin: 1rem 0 1.4rem; }
.lookup-form input[type=text] { flex: 1 1 22rem; padding: 0.45rem 0.7rem; border: 1px solid var(--border); border-radius: 6px; background: var(--surface); color: var(--fg); font: inherit; }
.lookup-form input.lookup-domain { flex: 0 1 9rem; }
.lookup-form select { padding: 0.45rem 0.5rem; border: 1px solid var(--border); border-radius: 6px; background: var(--surface); color: var(--fg); font: inherit; }
.lookup-form button { padding: 0.45rem 1.1rem; border: 1px solid var(--accent); border-radius: 6px; background: var(--accent); color: var(--bg); font: inherit; cursor: pointer; }
.lookup-form button:hover { background: var(--accent-strong); border-color: var(--accent-strong); }

/* Reverse-index chips on the entry page. */
.rev-chips { display: flex; flex-wrap: wrap; gap: 0.4rem; margin-top: 0.3rem; }
.rev-chip { display: inline-block; padding: 0.2rem 0.6rem; border: 1px solid var(--border); border-radius: 999px; background: var(--surface); color: var(--accent); font-size: 0.85rem; text-decoration: none; }
.rev-chip:hover { background: var(--hover); color: var(--accent-strong); border-color: var(--accent); }
.rev-domain { color: var(--muted); font-size: 0.78em; }

/* Reverse-index browse list (/lookup query-empty state) — card-style rows */
.rev-list { list-style: none; padding: 0; margin: 1rem 0 1.5rem; }
.rev-row { padding: 0.95rem 0.2rem; border-bottom: 1px solid var(--hairline); }
.rev-row:last-child { border-bottom: none; }
.rev-row-head { display: flex; align-items: baseline; flex-wrap: wrap; gap: 0.45rem; }
.rev-title { font-family: var(--font-serif); font-size: 1.02rem; text-decoration: none; color: var(--fg); margin-left: 0.2rem; }
.rev-title:hover { text-decoration: underline; color: var(--accent-strong); }
.rev-id { font-family: var(--font-mono); font-size: 0.78rem; margin-left: auto; }
.rev-chips-sym, .rev-chips-trg { margin-top: 0.4rem; align-items: baseline; }
.rev-tag { font-size: 0.78rem; color: var(--muted); margin-right: 0.3rem; min-width: 4.2rem; display: inline-block; }
.rev-more { font-size: 0.78rem; align-self: center; padding: 0.2rem 0.4rem; }
.rev-meta { font-family: var(--font-mono); font-size: 0.75rem; margin-top: 0.45rem; }

/* UseCase browse list (/lookup default mode) — name is the headline. */
.uc-list { list-style: none; padding: 0; margin: 1rem 0 1.5rem; }
.uc-row { padding: 0.6rem 0.2rem; border-bottom: 1px solid var(--hairline); }
.uc-row:last-child { border-bottom: none; }
.uc-row-head { display: flex; align-items: baseline; flex-wrap: wrap; gap: 0.5rem; }
.uc-name { font-family: var(--font-serif); font-size: 1.25rem; text-decoration: none; color: var(--fg); }
.uc-name:hover { color: var(--accent-strong); text-decoration: underline; }
.uc-altname { font-size: 0.9rem; }
.uc-count { margin-left: auto; font-family: var(--font-mono); font-size: 0.8rem; }
.uc-desc { margin: 0.2rem 0 0; color: var(--fg); font-size: 0.92rem; }
.uc-samples { list-style: none; padding: 0.4rem 0 0 1rem; margin: 0; border-left: 2px solid var(--hairline); }
.uc-samples li { font-size: 0.88rem; padding: 0.15rem 0; }
.uc-samples a { text-decoration: none; color: var(--fg); }
.uc-samples a:hover { color: var(--accent-strong); }

/* UseCase detail page */
.uc-detail-name { font-family: var(--font-serif); margin-bottom: 0.1rem; }
.uc-detail-altname { font-size: 0.9rem; margin-top: 0; }
.uc-detail-desc { max-width: 70ch; margin: 0.6rem 0 1.2rem; }

/* Entry list on a UseCase page — title row plus cataloger summary preview
   (middle layer between use-case → entry detail). */
.uc-entry-list { list-style: none; padding: 0; margin: 0.6rem 0 1.5rem; }
.uc-entry-row { padding: 0.6rem 0.2rem; border-bottom: 1px solid var(--hairline); }
.uc-entry-row:last-child { border-bottom: none; }
.uc-entry-head { display: flex; align-items: baseline; flex-wrap: wrap; gap: 0.5rem; }
.uc-entry-title { font-family: var(--font-serif); font-size: 1.08rem; text-decoration: none; color: var(--fg); }
.uc-entry-title:hover { color: var(--accent-strong); text-decoration: underline; }
.uc-entry-meta { margin-left: auto; font-family: var(--font-mono); font-size: 0.8rem; }
.uc-entry-summary { margin: 0.35rem 0 0 0.4rem; padding: 0.4rem 0.7rem; border-left: 2px solid var(--hairline);
                    background: var(--surface); border-radius: 4px; font-size: 0.92rem; max-width: 80ch; }
.uc-entry-summary-title { font-weight: 600; margin-bottom: 0.2rem; color: var(--muted); font-size: 0.85rem; }
.uc-entry-summary-body { color: var(--fg); }
/* Rendered-markdown headings inside the summary card are scoped down so the
   digest reads as a compact note, not a full document. */
.uc-entry-summary-body h1 { font-size: 1.0rem; margin: 0 0 0.3rem; }
.uc-entry-summary-body h2 { font-size: 0.9rem; margin: 0.6rem 0 0.2rem; color: var(--muted); }
.uc-entry-summary-body h3 { font-size: 0.85rem; margin: 0.5rem 0 0.2rem; color: var(--muted); }
.uc-entry-summary-body p { margin: 0.3rem 0; }
.uc-entry-summary-body ul, .uc-entry-summary-body ol { margin: 0.3rem 0 0.3rem 1.2rem; }
.uc-entry-summary-body code { font-size: 0.85em; }

/* Cross-entry synthesis panel on the category page — the distilled common
   insight, the payoff of grouping many entries under one problem-kind. */
.uc-synthesis { margin: 1rem 0 1.5rem; padding: 0.8rem 1.1rem; border-left: 3px solid var(--accent-strong);
                background: var(--surface); border-radius: 0 6px 6px 0; }
.uc-synthesis-head { font-size: 1.05rem; margin: 0 0 0.4rem; }
.uc-synthesis-head .muted { font-size: 0.7em; font-weight: normal; }
.uc-synthesis-body { max-width: 80ch; }
.uc-synthesis-body h2, .uc-synthesis-body h3 { font-size: 0.95rem; margin: 0.6rem 0 0.2rem; }

/* Project domain primer — collapsible overview on the entry page so a
   reader without the project's domain knowledge can decode its terms. */
.project-primer { margin: 0.6rem 0 1.2rem; border: 1px solid var(--border); border-radius: 6px;
                  background: var(--surface); padding: 0.4rem 0.8rem; }
.project-primer summary { cursor: pointer; color: var(--muted); font-size: 0.9rem; }
.project-primer summary:hover { color: var(--accent-strong); }
.project-primer-body { margin-top: 0.6rem; max-width: 80ch; }
.project-primer-body h1, .project-primer-body h2 { font-size: 1.0rem; margin: 0.6rem 0 0.2rem; }
.project-primer-desc { margin: 0.4rem 0 1rem; font-size: 0.9rem; }

`

// stylesChrome: misc chrome: lang toggle, banners, subnav, signals badges, wiki links, hier list.
const stylesChrome = `/* Language toggle in the header */
.lang-toggle { font-size: 0.85rem; }
.pager-info { font-family: var(--font-mono); font-size: 0.8rem; color: var(--muted); }
.empty { padding: 2rem; text-align: center; color: var(--muted); background: var(--surface);
         border: 1px dashed var(--border); border-radius: 6px; }
.banner { padding: 0.6rem 1rem; background: var(--hover); border: 1px solid var(--border); border-radius: 6px; margin-bottom: 1rem; }
.subnav { margin-bottom: 1rem; color: var(--muted); font-size: 0.9rem; }
.subnav a { text-decoration: none; }
.signals { max-width: 720px; }
.badge-status-OPEN { background: #e6f4e6; color: #295c29; }
.badge-status-PROMOTED { background: #e8eef9; color: #1a3d80; }
.badge-status-DISMISSED { background: #eee; color: #555; }
a.wiki { text-decoration: underline dotted; }
a.wiki:hover { text-decoration: underline; }
/* Wiki-link target that doesn't exist (yet) — render as muted text,
   no click-through. Hover shows the missing id for debugging. */
span.wiki-broken {
    color: var(--muted);
    text-decoration: line-through;
    cursor: help;
}
ul.hier { list-style: none; padding-left: 0; }
ul.hier li { padding: 0.4rem 0.75rem; background: var(--surface); border: 1px solid var(--border); border-radius: 6px; margin-bottom: 0.4rem; }

`
