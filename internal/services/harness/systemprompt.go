package harness

import (
	"strings"
	"text/template"

	_ "embed"
)

//go:embed systemprompt.md.tmpl
var systemPromptText string

var systemPromptTmpl = template.Must(template.New("systemprompt").Option("missingkey=error").Parse(systemPromptText))

// SystemPrompt is the system prompt of every agent run: the harness rules for the issue, the checkout dir, the issue
// branch and the profile's verify command, then the profile prompt.
func SystemPrompt(cfg Config, dir string, p Profile) string {
	var b strings.Builder

	// Execute errors only on a failed write; strings.Builder writes do not fail.
	_ = systemPromptTmpl.Execute(&b, map[string]any{
		"IID": cfg.Key.IID, "ProjectPath": cfg.ProjectPath, "Dir": dir, "Branch": cfg.Key.IssueBranch(),
		"Verify": strings.TrimSpace(p.Verify), "Profile": strings.TrimSpace(p.Prompt),
	})

	return strings.TrimSpace(b.String())
}
