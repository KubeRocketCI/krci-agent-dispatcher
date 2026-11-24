// Package templates renders the per-issue ActorTemplate and EgressPolicy without metadata. Pure: no I/O.
package templates

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"text/template"
	"time"

	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"sigs.k8s.io/yaml"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/turn"
)

//go:embed actortemplate.yaml.tmpl egresspolicy.yaml.tmpl
var files embed.FS

// Deployment holds the settings shared by every issue actor. Atespace is set as metadata by the adapter.
// HarnessImage is the harness image mounted at /opt/krci-agent. TurnTimeout and HarnessPoll are rendered in whole
// seconds. WorkerSelector is rendered as the worker selector match labels; empty renders no worker selector.
type Deployment struct {
	Atespace         string
	WorkerSelector   models.WorkerSelector
	SecretsNamespace string
	HarnessImage     string
	GitLabURL        models.GitLabURL
	Model            string
	AgentSteps       int
	TurnLimit        int
	TurnTimeout      time.Duration
	HarnessPoll      time.Duration
}

// Params are the values of one issue's actor: the deployment settings and the actor spec with its profile entry.
type Params struct {
	Deployment
	models.ActorSpec
}

var errNoGitLabURL = errors.New("gitlab url is empty")

// Render returns the actor template and egress policy of p (text/template → YAML → JSON → protojson). It fails
// on an empty GitLab URL, on a spec that does not pass models.ActorSpec.Validate and on a profile entry that
// CheckProfile refuses.
func Render(p Params) (*ateapi.ActorTemplate, *ateapi.EgressPolicy, error) {
	if p.GitLabURL.String() == "" {
		return nil, nil, errNoGitLabURL
	}

	if err := p.Validate(); err != nil {
		return nil, nil, err
	}

	rules, err := clashes(p)
	if err != nil {
		return nil, nil, err
	}

	if len(rules) > 0 {
		return nil, nil, errors.New("profile " + rules[0])
	}

	return renderAll(p)
}

// sampleSpec stands in for the issue when CheckProfile renders a profile entry.
var sampleSpec = models.ActorSpec{
	ProjectID: 1, ProjectPath: "group/project", IID: 1, SessionID: "check", BotUsername: "bot",
}

// CheckProfile returns every rule profile entry p breaks with deployment d: the rules of models.ProfileEntry.Check,
// then an env name the actor template sets or p lists twice, and an egress host of a fixed egress rule. Without a
// GitLab URL in d, the GitLab host is not checked. Empty when the actor of p renders.
func CheckProfile(d Deployment, p models.ProfileEntry) []string {
	params := Params{Deployment: d, ActorSpec: sampleSpec}
	params.Profile = p

	found, err := clashes(params)
	if err != nil {
		found = []string{err.Error()}
	}

	return append(p.Check(), found...)
}

// renderAll renders both templates of p without checks.
func renderAll(p Params) (*ateapi.ActorTemplate, *ateapi.EgressPolicy, error) {
	funcs := template.FuncMap{
		"json": func(v any) (string, error) {
			b, err := json.Marshal(v)
			return string(b), err
		},
		"secret": func(path string) string {
			return "ate-secret://k8s.io/default/" + p.SecretsNamespace + "/" + path
		},
		"seconds":         func(d time.Duration) int64 { return int64(d / time.Second) },
		"contractVersion": func() int { return turn.ContractVersion },
	}

	tmpl := &ateapi.ActorTemplate{}
	if err := render(funcs, "actortemplate.yaml.tmpl", p, tmpl); err != nil {
		return nil, nil, err
	}

	pol := &ateapi.EgressPolicy{}
	if err := render(funcs, "egresspolicy.yaml.tmpl", p, pol); err != nil {
		return nil, nil, err
	}

	return tmpl, pol, nil
}

// clashes returns the env names of the profile entry of p that the actor template of p without that entry's env
// sets, or that the entry lists twice, and the entry's egress hosts that the fixed egress rules name.
func clashes(p Params) ([]string, error) {
	base := p
	base.Profile.Env, base.Profile.Egress = nil, nil

	tmpl, pol, err := renderAll(base)
	if err != nil {
		return nil, err
	}

	fixedEnv := map[string]bool{}
	for _, e := range firstContainer(tmpl).GetEnv() {
		fixedEnv[e.GetName()] = true
	}

	fixedHosts := map[string]bool{}

	for _, r := range pol.GetRules() {
		for _, h := range r.GetHttps().GetHostnames() {
			fixedHosts[h] = true
		}
	}

	var rules []string

	listed := map[string]bool{}

	for _, e := range p.Profile.Env {
		switch {
		case fixedEnv[e.Name]:
			rules = append(rules, fmt.Sprintf("env name %q is set by the actor template", e.Name))
		case listed[e.Name]:
			rules = append(rules, fmt.Sprintf("env name %q is listed twice", e.Name))
		}

		listed[e.Name] = true
	}

	for _, h := range p.Profile.Egress {
		if fixedHosts[h] {
			rules = append(rules, fmt.Sprintf("egress host %q has a fixed egress rule", h))
		}
	}

	return rules, nil
}

func render(funcs template.FuncMap, name string, p Params, out proto.Message) error {
	t, err := template.New(name).Funcs(funcs).Option("missingkey=error").ParseFS(files, name)
	if err != nil {
		return fmt.Errorf("parse %s: %w", name, err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, p); err != nil {
		return fmt.Errorf("execute %s: %w", name, err)
	}

	j, err := yaml.YAMLToJSON(buf.Bytes())
	if err != nil {
		return fmt.Errorf("%s: yaml: %w", name, err)
	}

	if err := protojson.Unmarshal(j, out); err != nil {
		return fmt.Errorf("%s: protojson: %w", name, err)
	}

	return nil
}

// SessionOf returns the SESSION_ID env of the first container that sets it; empty when none does.
func SessionOf(tmpl *ateapi.ActorTemplate) string { return containerEnv(tmpl, "SESSION_ID") }

// ProfileOf returns the AGENT_PROFILE env of the first container that sets it; empty when none does.
func ProfileOf(tmpl *ateapi.ActorTemplate) string { return containerEnv(tmpl, "AGENT_PROFILE") }

// ImageOf returns the image of the first container; empty without one.
func ImageOf(tmpl *ateapi.ActorTemplate) string { return firstContainer(tmpl).GetImage() }

func containerEnv(tmpl *ateapi.ActorTemplate, name string) string {
	for _, c := range tmpl.GetContainers() {
		for _, e := range c.GetEnv() {
			if e.GetName() == name {
				return e.GetValue()
			}
		}
	}

	return ""
}

func firstContainer(tmpl *ateapi.ActorTemplate) *ateapi.Container {
	if cs := tmpl.GetContainers(); len(cs) > 0 {
		return cs[0]
	}

	return nil
}
