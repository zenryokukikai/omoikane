package opencrab

import (
	"strings"
	"testing"
)

// Pins the gateway-era reply contract (issue #132). On the gateway path
// the agent's turn output is delivered as the say, so the template must
// never resurrect the REST-era posting recipe: no /v1/librarian/chat
// POST, no chat.status broadcasts, no "posting is the completion
// condition" ritual — the agent has no thread_id to post to, and a
// successful post would double-send the reply.
func TestInstructionsGatewayContract(t *testing.T) {
	const persona = "丁寧で簡潔。絵文字は使わない。"
	instr := Instructions("しおり", "Kojira", persona, "https://kb.example.com")

	// REST-era posting ritual must be gone.
	for _, banned := range []string{
		"/v1/librarian/chat",
		"/v1/events/broadcast",
		"chat.status",
		"完了条件",
		"完了通知",
		"author_role",
	} {
		if strings.Contains(instr, banned) {
			t.Errorf("instructions must not contain %q (REST-era posting recipe):\n%s", banned, instr)
		}
	}

	// Gateway reply contract: the response body IS the delivered reply.
	for _, required := range []string{
		"自動配送",                             // the reply is delivered automatically
		"thread_id を探さない",                  // no thread_id hunting
		"2通目を送らない",                         // one reply per request
		"https://kb.example.com/v1/search", // read path stays
		"/v1/entries/",                     // read path stays
		".kb.curlrc",                       // auth for the read path stays
		"GET や ?q= は使えない",                  // search transport guidance stays
		"挨拶や雑談には検索は不要",                     // no pointless searches
		"[[entry_id]]",                     // citation format stays
		"見つからなかった",                         // empty search still gets a reply in own words
		"分からないことは分からないと言う",                 // honesty rule stays
		persona, // persona embedded verbatim
		"しおり", "Kojira",
	} {
		if !strings.Contains(instr, required) {
			t.Errorf("instructions missing %q:\n%s", required, instr)
		}
	}
}

// Empty kbURL falls back to the local default.
func TestInstructionsDefaultKBURL(t *testing.T) {
	instr := Instructions("n", "u", "p", "")
	if !strings.Contains(instr, "http://localhost:8080/v1/search") {
		t.Fatalf("default kbURL not applied:\n%s", instr)
	}
}
