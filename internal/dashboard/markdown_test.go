package dashboard

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kojira/omoikane/internal/store"
)

func mustCreateUserAndProject(t *testing.T, s *store.Store, userID, projectID string) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateUser(ctx, &store.User{ID: userID, Name: userID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProject(ctx, &store.Project{ID: projectID, Name: projectID}); err != nil {
		t.Fatal(err)
	}
}

func mustUploadAttachment(t *testing.T, s *store.Store, projectID, userID, mime, caption string, payload []byte) *store.Attachment {
	t.Helper()
	a, err := s.CreateAttachment(context.Background(), store.CreateAttachmentParams{
		ProjectID: projectID, Mime: mime, Role: "screenshot",
		Caption: caption, UploadedBy: userID,
		Content: bytes.NewReader(payload), MaxBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRenderMarkdownHeadingsAndEmphasis(t *testing.T) {
	out := string(renderMarkdown("# H1\n\n**bold** and *italic*"))
	for _, want := range []string{"<h1", ">H1<", "<strong>bold</strong>", "<em>italic</em>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}

func TestRenderMarkdownCodeBlocks(t *testing.T) {
	in := "inline `code` here\n\n```go\nfunc x() {}\n```"
	out := string(renderMarkdown(in))
	if !strings.Contains(out, "<code>code</code>") {
		t.Fatalf("inline: %s", out)
	}
	if !strings.Contains(out, "<pre>") || !strings.Contains(out, "func x() {}") {
		t.Fatalf("fenced: %s", out)
	}
}

func TestRenderMarkdownLists(t *testing.T) {
	out := string(renderMarkdown("- one\n- two\n- three"))
	for _, want := range []string{"<ul>", "<li>one</li>", "<li>two</li>", "<li>three</li>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}

func TestRenderMarkdownTables(t *testing.T) {
	in := "| a | b |\n|---|---|\n| 1 | 2 |"
	out := string(renderMarkdown(in))
	if !strings.Contains(out, "<table>") || !strings.Contains(out, "<th>a</th>") {
		t.Fatalf("table: %s", out)
	}
}

func TestRenderMarkdownRefusesRawHTMLByDefault(t *testing.T) {
	out := string(renderMarkdown("<script>alert(1)</script> ok"))
	// goldmark with WithUnsafe NOT set escapes raw HTML
	if strings.Contains(out, "<script>") {
		t.Fatalf("raw HTML leaked: %s", out)
	}
}

func TestRenderContentWikiLinksSurviveMarkdown(t *testing.T) {
	out := string(renderContent("see [[T-ABC]] for details", "", nil))
	if !strings.Contains(out, `href="/entries/T-ABC"`) {
		t.Fatalf("wiki: %s", out)
	}
}

func TestRenderContentMentionsSurviveMarkdown(t *testing.T) {
	out := string(renderContent("ping @curator please", "", nil))
	if !strings.Contains(out, `mention-curator`) {
		t.Fatalf("mention: %s", out)
	}
}

func TestRenderContentMarkdownPlusEverything(t *testing.T) {
	in := "## heading\n\n- bullet with [[T-XYZ]]\n- @judge please review\n\n`inline`"
	out := string(renderContent(in, "", nil))
	for _, want := range []string{
		"<h2", "<li>", "/entries/T-XYZ", "mention-judge", "<code>inline</code>",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}

func TestRenderContentUnfurlsImageAttachment(t *testing.T) {
	s := newDashStore(t)
	mustCreateUserAndProject(t, s, "alice", "demo")
	a := mustUploadAttachment(t, s, "demo", "alice", "image/png", "before-shot", []byte("PNG"))

	md := "Improvement comparison:\n\n![worst frame run020](attached:" + a.ID + ")"
	out := string(renderContent(md, "", s))

	if !strings.Contains(out, `<figure class="attachment attachment-image">`) {
		t.Errorf("missing figure wrapper: %s", out)
	}
	if !strings.Contains(out, `src="/v1/attachments/`+a.ID+`/content"`) {
		t.Errorf("src not rewritten: %s", out)
	}
	if !strings.Contains(out, `alt="worst frame run020"`) {
		t.Errorf("alt from markdown not preserved: %s", out)
	}
	if !strings.Contains(out, `<figcaption>worst frame run020</figcaption>`) {
		t.Errorf("figcaption missing: %s", out)
	}
	if strings.Contains(out, `attached:`+a.ID) {
		t.Errorf("raw attached: scheme leaked: %s", out)
	}
}

func TestRenderContentUnfurlsVideoAsVideoTag(t *testing.T) {
	s := newDashStore(t)
	mustCreateUserAndProject(t, s, "alice", "demo")
	a := mustUploadAttachment(t, s, "demo", "alice", "video/mp4", "demo clip", []byte("MP4"))
	md := "![demo](attached:" + a.ID + ")"
	out := string(renderContent(md, "", s))
	if !strings.Contains(out, `<video src="/v1/attachments/`+a.ID+`/content" controls`) {
		t.Errorf("video tag not produced: %s", out)
	}
	if strings.Contains(out, "<img") {
		t.Errorf("video should not be rendered as img: %s", out)
	}
}

func TestRenderContentUnfurlsUnknownMimeAsDownloadLink(t *testing.T) {
	s := newDashStore(t)
	mustCreateUserAndProject(t, s, "alice", "demo")
	a := mustUploadAttachment(t, s, "demo", "alice", "application/json", "metrics dump", []byte(`{"k":1}`))
	md := "metrics: ![](attached:" + a.ID + ")"
	out := string(renderContent(md, "", s))
	if !strings.Contains(out, `class="attachment attachment-file"`) {
		t.Errorf("missing file class: %s", out)
	}
	if !strings.Contains(out, `href="/v1/attachments/`+a.ID+`/content"`) {
		t.Errorf("href not rewritten: %s", out)
	}
	if !strings.Contains(out, "metrics dump") {
		t.Errorf("stored caption fallback missing: %s", out)
	}
}

// When rendering as part of a `?token=...` dashboard page, the
// attachment src URL must carry the same token so the browser's
// <img>/<video> fetch can authenticate (no session cookie path).
func TestRenderContentAttachmentSrcCarriesToken(t *testing.T) {
	s := newDashStore(t)
	mustCreateUserAndProject(t, s, "alice", "demo")
	a := mustUploadAttachment(t, s, "demo", "alice", "image/png", "x", []byte("PNG"))
	md := "![x](attached:" + a.ID + ")"
	out := string(renderContent(md, "secret-tok", s))
	if !strings.Contains(out, "/v1/attachments/"+a.ID+"/content?token=secret-tok") {
		t.Errorf("token not propagated into src: %s", out)
	}
}

// When token is empty (cookie-auth user), src is the bare content
// URL — browser sends the cookie automatically.
func TestRenderContentAttachmentSrcNoTokenWhenEmpty(t *testing.T) {
	s := newDashStore(t)
	mustCreateUserAndProject(t, s, "alice", "demo")
	a := mustUploadAttachment(t, s, "demo", "alice", "image/png", "x", []byte("PNG"))
	md := "![x](attached:" + a.ID + ")"
	out := string(renderContent(md, "", s))
	if strings.Contains(out, "?token=") {
		t.Errorf("empty token should not produce ?token= query: %s", out)
	}
}

func TestRenderContentMissingAttachmentRendersPlaceholder(t *testing.T) {
	s := newDashStore(t)
	md := "stale ref: ![old](attached:a-deadbeef)"
	out := string(renderContent(md, "", s))
	if !strings.Contains(out, `[missing attachment a-deadbeef]`) {
		t.Errorf("placeholder missing: %s", out)
	}
}

func TestRenderContentSafeEvenWithTryHTMLInjection(t *testing.T) {
	// User content with raw HTML — must be escaped, not rendered.
	out := string(renderContent("hi <img src=x onerror=alert(1)>", "", nil))
	if strings.Contains(out, "<img") {
		t.Fatalf("img leaked: %s", out)
	}
}
