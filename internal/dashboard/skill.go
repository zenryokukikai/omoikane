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

// skillTmpl is loaded from templates/skill.md.tmpl on first use.
// Parsed once via the same embedded FS the rest of the dashboard uses.
// text/template (not html/template) because the output is markdown for
// an agent, not HTML.
var skillTmpl *template.Template

func loadSkillTemplate() (*template.Template, error) {
	if skillTmpl != nil {
		return skillTmpl, nil
	}
	raw, err := templatesFS.ReadFile("templates/skill.md.tmpl")
	if err != nil {
		return nil, err
	}
	t, err := template.New("skill").Parse(string(raw))
	if err != nil {
		return nil, err
	}
	skillTmpl = t
	return t, nil
}

// serveSkillMD renders the skill markdown with the request's public
// base URL substituted. text/plain charset utf-8 so curl displays it
// correctly; not text/markdown because most browsers download .md
// files instead of displaying them.
func (h *Handler) serveSkillMD(w http.ResponseWriter, r *http.Request) {
	t, err := loadSkillTemplate()
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

// agentSkillTmpl is the Agent-Skills-standard (pi.dev / Claude Code /
// Codex compatible) SKILL.md. Distinct from /skill.md (the human-
// readable onboarding doc) because the standard requires specific
// YAML frontmatter and a focused description.
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

// serveAgentSkillMD serves the Agent-Skills-standard SKILL.md at
// /skills/omoikane/SKILL.md. An agent runtime that follows the spec
// (pi.dev, Claude Code, OpenAI Codex...) can drop this file into its
// skill directory (e.g. ~/.agents/skills/omoikane/SKILL.md) and the
// runtime will auto-discover it on next start.
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

// serveAgentSkillInstall returns a tiny POSIX-sh script the user can
// paste into the agent's environment to install the skill with one
// command. Targets ~/.agents/skills/omoikane/ which is the cross-
// runtime standard location accepted by pi.dev and Claude Code.
func (h *Handler) serveAgentSkillInstall(w http.ResponseWriter, r *http.Request) {
	base := publicBase(r)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("#!/bin/sh\n" +
		"# omoikane skill installer (Agent-Skills-standard runtimes:\n" +
		"# pi.dev, Claude Code, OpenAI Codex). Drops a single SKILL.md\n" +
		"# into the standard cross-runtime location.\n" +
		"set -e\n" +
		"target=\"${HOME}/.agents/skills/omoikane\"\n" +
		"mkdir -p \"$target\"\n" +
		"curl -sfL " + base + "/skills/omoikane/SKILL.md -o \"$target/SKILL.md\"\n" +
		"echo \"OK omoikane skill installed at $target/SKILL.md\"\n" +
		"echo \"  Restart your agent runtime to pick it up.\"\n" +
		"echo \"  Then ask the user for an omoikane invitation code to register.\"\n"))
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
