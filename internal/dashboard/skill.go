package dashboard

import (
	"net/http"
	"strings"
	"text/template"
)

// skillVersion is bumped manually when the skill contract changes
// meaningfully (new required tools, new register flow, etc.). Minor
// prose edits don't bump it.
const skillVersion = "0.1.0"

// agentSkillTmpl is the single, canonical SKILL.md template,
// served at /skill.md. Earlier the codebase had a separate "human
// onboarding" /skill.md and a standard-path /skills/omoikane/SKILL.md;
// they duplicated content and drifted. The endpoints were collapsed
// to one URL (/skill.md), one template (templates/agent-skill.md.tmpl).
var agentSkillTmpl *template.Template

func loadAgentSkillTemplate() (*template.Template, error) {
	if agentSkillTmpl != nil {
		return agentSkillTmpl, nil
	}
	raw, err := templatesFS.ReadFile("templates/agent-skill.md.tmpl")
	if err != nil {
		return nil, err
	}
	t, err := template.New("agent-skill").Parse(string(raw))
	if err != nil {
		return nil, err
	}
	agentSkillTmpl = t
	return t, nil
}

// serveAgentSkillMD serves the canonical Agent-Skills-standard
// SKILL.md at /skill.md. An agent runtime that follows the spec
// (pi.dev, Claude Code, OpenAI Codex...) fetches this URL and
// installs it however the runtime expects (the skill itself doesn't
// prescribe a host path — that's an environment decision).
func (h *Handler) serveAgentSkillMD(w http.ResponseWriter, r *http.Request) {
	t, err := loadAgentSkillTemplate()
	if err != nil {
		http.Error(w, "skill template missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Skill-Version", skillVersion)
	_ = t.Execute(w, map[string]string{
		"BaseURL":      publicBase(r),
		"SkillVersion": skillVersion,
	})
}

// publicBase derives the externally-visible base URL for the current
// request. Honours X-Forwarded-Proto so it works behind a reverse
// proxy that terminates TLS.
func publicBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "localhost:8095"
	}
	return scheme + "://" + host
}
