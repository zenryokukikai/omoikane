package opencrab

import (
	"strings"
	"testing"
)

// Pins the gateway-era reply contract (issue #132) and the delegated-
// research contract (issue #141). On the gateway path the agent's turn
// output is delivered as the say, so the template must never resurrect
// the REST-era posting recipe: no /v1/librarian/chat POST, no
// chat.status broadcasts, no "posting is the completion condition"
// ritual — the agent has no thread_id to post to, and a successful post
// would double-send the reply. And research is delegated wholesale to
// kb-ask, so the template must not carry a KB search/read recipe.
func TestInstructionsGatewayContract(t *testing.T) {
	const persona = "丁寧で簡潔。絵文字は使わない。"
	instr := Instructions("しおり", "Kojira", persona, "https://kb.example.com")

	// REST-era posting ritual, and the old KB search/read recipe, must be gone.
	for _, banned := range []string{
		"/v1/librarian/chat",
		"/v1/events/broadcast",
		"chat.status",
		"完了条件",
		"完了通知",
		"author_role",
		"/v1/search",             // research recipe is delegated, not embedded
		"/v1/entries/",           // ditto
		"https://kb.example.com", // the KB URL is no longer embedded at all
	} {
		if strings.Contains(instr, banned) {
			t.Errorf("instructions must not contain %q:\n%s", banned, instr)
		}
	}

	// Gateway reply contract + delegated-research contract.
	for _, required := range []string{
		"自動配送",             // the reply is delivered automatically
		"thread_id を探さない",  // no thread_id hunting
		"2通目を送らない",         // one reply per request
		"分からないことは分からないと言う", // honesty rule stays
		"見つからなかった",         // empty result still gets a reply in own words
		"/app/bin/kb-ask",  // research is delegated to the kb-ask engine
		"あなた自身は検索も精読もしない",  // the agent does not search/read itself
		"委任は1依頼につき1回",      // one delegation per request
		"再実行しない",           // no retry loop on timeout/error
		"[[entry_id]]",     // citation format (produced by kb-ask) stays
		persona,            // persona embedded verbatim
		"しおり", "Kojira",
	} {
		if !strings.Contains(instr, required) {
			t.Errorf("instructions missing %q:\n%s", required, instr)
		}
	}
}

// Research is delegated to kb-ask, so a supplied kbURL is never embedded
// into the instructions (issue #141): the agent needs no KB URL of its own.
func TestInstructionsDelegatesResearch(t *testing.T) {
	instr := Instructions("n", "u", "p", "https://kb.internal.example/base")
	if strings.Contains(instr, "https://kb.internal.example/base") {
		t.Fatalf("kbURL must not be embedded once research is delegated:\n%s", instr)
	}
	if !strings.Contains(instr, `/app/bin/kb-ask "<質問文>"`) {
		t.Fatalf("delegation command missing:\n%s", instr)
	}
}
