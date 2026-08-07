package dashboard

import (
	"context"
	"strings"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// /directives renders empty state, then the registered directive.
func TestDirectivesPage(t *testing.T) {
	srv, st, tok := mountAuthed(t)
	code, body := get(t, srv, "/directives", tok)
	if code != 200 || !strings.Contains(string(body), "まだ指示はありません") {
		t.Fatalf("empty: code=%d", code)
	}
	if _, err := st.CreateDirective(context.Background(), "scout", "量子化に注目", "alice"); err != nil {
		t.Fatal(err)
	}
	_, body = get(t, srv, "/directives", tok)
	bs := string(body)
	if !strings.Contains(bs, "量子化に注目") || !strings.Contains(bs, "無効化") {
		t.Fatalf("directive not rendered")
	}
	_ = store.Directive{}
}
