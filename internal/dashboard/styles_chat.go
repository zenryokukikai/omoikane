package dashboard

// stylesChat: chat room: threads, message stream, author/mention palettes, post form.
const stylesChat = `/* Chat room */
details.chat-newthread, details.chat-close {
  background: var(--surface); border: 1px solid var(--border); border-radius: 6px;
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
  background: var(--surface); border: 1px solid var(--border); border-radius: 6px;
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
  background: var(--surface); border: 1px solid var(--border); border-radius: 6px;
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

`

// stylesLogin: login page, claim summary, copy target, generic form buttons.
const stylesLogin = `/* Login page */
.login { max-width: 480px; margin: 3rem auto; padding: 2rem; background: var(--surface); border: 1px solid var(--border); border-radius: 8px; }
.login h1 { margin-top: 0; }
.btn-google {
  display: inline-flex; align-items: center; gap: 0.7rem;
  padding: 0.7rem 1.2rem; background: var(--surface); color: #333;
  border: 1px solid #d0d0d0; border-radius: 4px;
  font-weight: 600; text-decoration: none; font-size: 1rem;
  box-shadow: 0 1px 3px rgba(0,0,0,0.08); transition: background 0.15s;
}
.btn-google:hover { background: var(--badge-bg); }
.btn-google .g-logo {
  display: inline-flex; align-items: center; justify-content: center;
  width: 22px; height: 22px; background: linear-gradient(45deg, #4285f4 0%, #ea4335 100%);
  color: #fff; border-radius: 2px; font-weight: bold; font-family: serif;
}
.login-token-fallback { margin-top: 2rem; }
.login-future { margin-top: 1.5rem; font-style: italic; }
table.claim-summary { width: 100%; margin: 1rem 0; }
table.claim-summary th { background: transparent; text-transform: none; letter-spacing: 0; font-size: 0.85rem; color: var(--muted); width: 30%; }
.login form button.btn-google { padding: 0.7rem 1.4rem; font-weight: 600; cursor: pointer; background: var(--accent); color: #fff; border: none; }
.login form button.btn-google:hover { background: var(--accent-strong); }
.banner-success { background: #e6f4e6; border-color: #b8dab8; }
.banner-success p { margin: 0.3rem 0; }
input.copy-target {
  width: 100%; padding: 0.6rem 0.8rem; font: 1.05rem ui-monospace, SFMono-Regular, Menlo, monospace;
  border: 1px solid var(--border); border-radius: 4px; background: var(--surface);
  margin: 0.4rem 0; cursor: text;
}
input.copy-target:focus { outline: 2px solid var(--accent); }
form button { padding: 0.45rem 1rem; font: inherit; cursor: pointer; background: var(--accent); color: #fff; border: none; border-radius: 4px; }
form button:hover { background: var(--accent-strong); }
form input[type=text] { padding: 0.4rem 0.6rem; border: 1px solid var(--border); border-radius: 4px; font: inherit; min-width: 320px; }

`

// stylesProfile: profile page (/u/{id}), role badges, claim card, profile edit.
const stylesProfile = `/* Profile page (/u/{id}) */
.profile-card {
  background: var(--surface); border: 1px solid var(--border); border-radius: 8px;
  padding: 1.5rem; margin: 1rem 0;
}
.profile-head { display: flex; align-items: center; gap: 1rem; margin-bottom: 1rem; }
.profile-headtext h1 { margin: 0; }
.profile-headtext p { margin: 0.3rem 0 0; }
.avatar-lg {
  width: 64px; height: 64px; border-radius: 50%; object-fit: cover;
  border: 1px solid var(--border); flex-shrink: 0;
}
.avatar-lg.avatar-placeholder {
  display: inline-flex; align-items: center; justify-content: center;
  font-size: 1.8rem; background: var(--accent); color: #fff;
}
.profile-bio, .profile-parent { margin: 1rem 0; }
.profile-bio .body { white-space: pre-wrap; }
.badge-role-admin  { background: #ffd9d9; color: #8a1e1e; }
.badge-role-member { background: #e8eef9; color: #1a3d80; }
.badge-role-agent  { background: #ece0ff; color: #4a2099; }
.role-form { display: inline-flex; gap: 0.3rem; }
.role-form select { font: inherit; padding: 0.2rem 0.4rem; border: 1px solid var(--border); border-radius: 4px; }
.role-form button { padding: 0.25rem 0.7rem; font-size: 0.85rem; }
.claim-card {
  max-width: 560px; margin: 3rem auto; padding: 2rem;
  background: var(--surface); border: 1px solid var(--border); border-radius: 8px;
}
.claim-card h1 { margin-top: 0; }
.profile-edit { margin-top: 1.5rem; }
.profile-edit form { display: flex; flex-direction: column; gap: 0.6rem; }
.profile-edit textarea {
  font: inherit; padding: 0.5rem 0.7rem; border: 1px solid var(--border);
  border-radius: 4px; resize: vertical; min-width: 100%; width: 100%;
}
.profile-edit input[type=text] { width: 100%; min-width: 100%; }
.profile-edit button { align-self: flex-start; }

`

// stylesAttachment: attachment unfurl in entry body / chat message.
const stylesAttachment = `/* Attachment unfurl — rendered inline in entry body / chat message. */
.attachment {
  display: block;
  margin: 1rem 0;
}
figure.attachment {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 0.6rem;
  margin: 1rem 0;
}
figure.attachment img,
figure.attachment video,
figure.attachment audio {
  display: block;
  max-width: 100%;
  height: auto;
  border-radius: 4px;
  margin: 0 auto;
}
figure.attachment figcaption {
  margin-top: 0.4rem;
  font-size: 0.85rem;
  color: var(--muted);
  text-align: center;
}
a.attachment-file {
  display: inline-block;
  padding: 0.4rem 0.8rem;
  background: var(--badge-bg);
  color: var(--fg);
  border: 1px solid var(--border);
  border-radius: 4px;
  text-decoration: none;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.9rem;
}
a.attachment-file:hover { background: var(--hover); }

`
