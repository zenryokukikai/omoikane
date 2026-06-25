package api

import "net/http"

// SkillVersion is the canonical version of the omoikane Agent-Skills-standard
// SKILL.md served at /skill.md. Agents that cache the doc compare their cached
// version against the `X-Skill-Version` header on every API response; when the
// values differ, they re-fetch /skill.md before proceeding.
//
// Bump this whenever /skill.md changes in a way agents need to notice — new
// signal name, new helper, changed endpoint contract, etc.
const SkillVersion = "0.11.0"

// FeedbackHint is the one-line standing reminder served as `X-Feedback-Hint`
// on every API response. The point isn't to tell the agent something it
// couldn't read in /skill.md — it's that *in-context bias* is real, and an
// agent that just read a hit will file feedback far more often when the hint
// arrives in the same response than when it lives in a doc the agent read
// minutes ago.
const FeedbackHint = `If a result here shaped your action, ` +
	`POST /v1/feedback {"entry_id":"<id>","signal":"helpful|confirmed|outdated|wrong|incomplete|surfaced_gap","context":"<one line>"}`

// FeedbackPrompt is a friendlier, in-body version of the hint. The header
// is robust (every response, every endpoint) but agents skim headers — the
// body is what they reason about. Including the prompt as a JSON field on
// read endpoints (search / lookup / get) puts the reminder *next to the
// entry id*, in the same JSON the LLM is parsing.
//
// English only: LLM agents reason in English internally; a bilingual blob
// just spends context tokens twice for the same information.
const FeedbackPrompt = `Was this helpful, or off? File feedback so future readers benefit — ` +
	`one POST, no follow-up: ` +
	`POST /v1/feedback {"entry_id":"<id>","signal":"helpful|confirmed|outdated|wrong|incomplete|surfaced_gap","context":"<one line>"}`

// SkillVersionHeader is a chi middleware that stamps `X-Skill-Version` and
// `X-Feedback-Hint` on every response.
//
//   - X-Skill-Version — agents that cache /skill.md compare this against their
//     cached version and re-fetch on drift, without having to poll /skill.md.
//   - X-Feedback-Hint — in-band reminder, in the same response as the entry
//     ids the agent might want to file feedback on. Highest-leverage place
//     to surface the contract — the agent is already on the right page.
func SkillVersionHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Skill-Version", SkillVersion)
		h.Set("X-Feedback-Hint", FeedbackHint)
		next.ServeHTTP(w, r)
	})
}
