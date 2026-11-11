// Package templates renders the per-issue ActorTemplate and EgressPolicy without metadata. Pure: no I/O.
package templates

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"text/template"

	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"sigs.k8s.io/yaml"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

//go:embed actortemplate.yaml.tmpl egresspolicy.yaml.tmpl
var files embed.FS

// Deployment holds the settings shared by every issue actor. Atespace is set as metadata by the adapter.
type Deployment struct {
	Atespace         string
	Pool             string
	SecretsNamespace string
	Image            string
	GitLabURL        models.GitLabURL
}

// Params are the values of one issue's actor: the deployment settings and the actor spec.
type Params struct {
	Deployment
	models.ActorSpec
}

var errNoGitLabURL = errors.New("gitlab url is empty")

// Render returns the actor template and egress policy of p (text/template → YAML → JSON → protojson). It fails
// on an empty GitLab URL and on a spec that does not pass models.ActorSpec.Validate.
func Render(p Params) (*ateapi.ActorTemplate, *ateapi.EgressPolicy, error) {
	if p.GitLabURL.String() == "" {
		return nil, nil, errNoGitLabURL
	}

	if err := p.Validate(); err != nil {
		return nil, nil, err
	}

	funcs := template.FuncMap{
		"json": func(v any) (string, error) {
			b, err := json.Marshal(v)
			return string(b), err
		},
		"secret": func(path string) string {
			return "ate-secret://k8s.io/default/" + p.SecretsNamespace + "/" + path
		},
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
