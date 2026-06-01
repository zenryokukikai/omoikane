---
name: omoikane — Quiet Archive
colors:
  paper: "#FAF8F3"
  surface: "#FEFDFA"
  ink: "#1F1D1A"
  muted: "#5F5A54"
  faint: "#E6E1D8"
  hairline: "#EDE9E1"
  accent: "#8A4B2A"
  accent-soft: "#EFE3D8"
  on-accent: "#FFFFFF"
  status-draft: "#5F5A52"
  status-active: "#3E5A40"
  status-retired: "#615B52"
  status-warn: "#83451F"
typography:
  display:
    fontFamily: "\"Iowan Old Style\", Charter, Georgia, \"Hiragino Mincho ProN\", \"Yu Mincho\", serif"
    fontSize: 2rem
    fontWeight: 600
    lineHeight: 1.25
    letterSpacing: "-0.01em"
  h2:
    fontFamily: "\"Iowan Old Style\", Charter, Georgia, \"Hiragino Mincho ProN\", serif"
    fontSize: 1.3rem
    fontWeight: 600
    lineHeight: 1.3
  body:
    fontFamily: "system-ui, -apple-system, \"Hiragino Sans\", \"Noto Sans JP\", \"Yu Gothic\", sans-serif"
    fontSize: 1rem
    fontWeight: 400
    lineHeight: 1.7
  small:
    fontFamily: "system-ui, -apple-system, \"Hiragino Sans\", \"Noto Sans JP\", sans-serif"
    fontSize: 0.85rem
    fontWeight: 400
    lineHeight: 1.5
  mono:
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, \"Cascadia Code\", monospace"
    fontSize: 0.85rem
    fontWeight: 400
    lineHeight: 1.5
spacing:
  xs: 4px
  sm: 8px
  md: 16px
  lg: 24px
  xl: 40px
  xxl: 64px
rounded:
  sm: 4px
  md: 8px
  lg: 12px
  pill: 999px
components:
  page:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    typography: "{typography.body}"
  header:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.ink}"
    padding: 16px
  card:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.ink}"
    rounded: "{rounded.md}"
    padding: 24px
  journal:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.ink}"
    typography: "{typography.body}"
    rounded: "{rounded.md}"
    padding: 40px
  link:
    textColor: "{colors.accent}"
  button-primary:
    backgroundColor: "{colors.accent}"
    textColor: "{colors.on-accent}"
    rounded: "{rounded.sm}"
    padding: 8px
  badge:
    backgroundColor: "{colors.faint}"
    textColor: "{colors.muted}"
    rounded: "{rounded.pill}"
    typography: "{typography.small}"
    padding: 4px
  rule:
    backgroundColor: "{colors.hairline}"
    height: 1px
---

## Overview

omoikane is a knowledge base its agents fill and its people read. The
dashboard has two readers: an **agent-ops auditor** scanning entries,
and a **human reading the morning journal over coffee**. The identity
serves the second without failing the first.

The mood is a **quiet archive** — a calm reading room, not a control
panel. Warm paper, ink-dark text, one restrained earth accent, and
typography that makes long Japanese-and-English prose comfortable.
Restraint is the brief: nothing competes with the words.

Three rules govern every decision:

1. **The text is the interface.** Type, measure, and spacing come
   first; chrome stays out of the way.
2. **One accent, used sparingly.** Terracotta marks interaction and
   nothing else. Colour is a signal, not decoration.
3. **Hairlines over shadows.** Structure comes from thin rules and
   whitespace, not heavy boxes or drop shadows.

## Colors

A warm-neutral base with a single earth accent.

- **paper** `#FAF8F3` — the page. Warm off-white, easy on the eye for
  long reads.
- **surface** `#FEFDFA` — cards, the journal sheet, the header. A hair
  brighter than paper so panels lift without a border fighting them.
- **ink** `#1F1D1A` — body and headings. Warm near-black, ~16:1 on
  paper.
- **muted** `#5F5A54` — secondary text, metadata, captions. ≥4.5:1 on
  paper (AA for normal text).
- **faint** `#E6E1D8` / **hairline** `#EDE9E1` — borders and rules.
- **accent** `#8A4B2A` — the one interaction colour: links, the active
  nav item, primary buttons, the journal's section marks. ≥4.5:1 on
  paper, so it is safe as link text. Use it ONLY for interaction or to
  mark "this is the live/important thing." Never for decoration.
- **accent-soft** `#EFE3D8` — a pale terracotta tint for hover fills
  and badge backgrounds.
- **on-accent** `#FFFFFF` — text on an accent fill (buttons).
- **status-*** — desaturated so they read as quiet labels, not
  alarms: draft (grey), active (muted green), retired (faint grey),
  warn (burnt orange). They tint badge text, not whole rows.

Contrast: every text/background pair above meets WCAG AA (4.5:1 for
normal text, 3:1 for large). When in doubt, darken the foreground.

## Typography

Reading comfort is the whole game, in two scripts.

- **display / h2** — a serif (Iowan/Charter/Georgia, Hiragino Mincho /
  Yu Mincho for Japanese). The serif gives the journal and entry
  titles a little editorial gravity — a broadsheet, not an app.
- **body** — a humanist system sans (system-ui, Hiragino Sans, Noto
  Sans JP) at 1rem / **line-height 1.7**. The generous leading is for
  Japanese paragraphs as much as English.
- **small** — 0.85rem for metadata, badges, footers.
- **mono** — entry ids (`T-XXXX`), counts, code. ui-monospace.

Measure: cap body text at ~70ch (`max-width: 70ch`) in reading
contexts (journal, entry body) so lines don't sprawl. Headings may be
wider. Do not justify; ragged-right reads better in mixed JA/EN.

## Layout

- A single centred column, **max-width ~880px** for app pages, with a
  narrower **~70ch reading column** inside journal and entry bodies.
- Whitespace is the primary divider. Use the spacing scale (8/16/24/40)
  generously; let sections breathe.
- The header is a thin, surface-coloured bar with a hairline bottom
  rule — present but recessive.
- Tables (the audit/list views) stay dense and legible: hairline row
  separators, no zebra fills, mono for ids.

## Elevation & Depth

Mostly flat. Depth comes from the surface/paper value step and a
hairline, not shadows. At most ONE soft, low shadow is allowed to lift
the journal sheet or a focused card
(`0 1px 2px rgba(31,29,26,0.05)`). No layered shadows, no glow.

## Shapes

Soft, not bubbly. `rounded.sm` (4px) for inputs and buttons,
`rounded.md` (8px) for cards and the journal sheet, `pill` for badges.
Corners are calm; nothing is sharp-cornered and nothing is a balloon.

## Components

- **header** — surface bar, hairline bottom rule, ink wordmark, accent
  on the active item only.
- **card** — surface, 8px radius, hairline border OR the single soft
  shadow (not both), 24px padding.
- **journal** — the morning read: surface sheet, 40px padding, body
  type, ~70ch reading column, serif title, accent section marks, dates
  and entry ids in muted/mono. This is the page the design is *for*.
- **link** — accent text, underline on hover; the active nav item is
  accent.
- **button-primary** — accent fill, white text, 4px radius.
- **badge** — pill, accent-soft fill, status-coloured text, small type;
  used for type/status, kept quiet.
- **rule** — a hairline; the preferred divider everywhere.

## Do's and Don'ts

**Do**
- Lead with type and whitespace; let the words carry the page.
- Use accent only for interaction or the one "live/important" mark.
- Keep reading columns to ~70ch and line-height ~1.7.
- Prefer a hairline or the surface/paper step to a border-plus-shadow.
- Test JA and EN together — the journal is bilingual.

**Don't**
- Don't add a second accent or saturated status colours that shout.
- Don't use drop shadows for hierarchy (one soft lift, max).
- Don't justify text or let lines run full-width in reading views.
- Don't decorate with colour; if it isn't a signal, it's ink/muted.
- Don't let chrome (headers, badges, buttons) outweigh the content.
