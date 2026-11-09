// Package templates renders the per-issue ActorTemplate and EgressPolicy. Pure: no I/O.
package templates

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"net/url"
	"text/template"

	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"sigs.k8s.io/yaml"
)

//go:embed actortemplate.yaml.tmpl egresspolicy.yaml.tmpl
var files embed.FS

// Params are the values of one issue's actor.
type Params struct {
	Atespace         string
	Pool             string
	SecretsNamespace string
	Name             string
	Image            string
	GitLabURL        string
	ProjectPath      string
	BotUsername      string
	SessionID        string
	IID              int64
}

type data struct {
	Params
	GitLabHost string
}

// Render returns the actor template and egress policy of p (text/template → YAML → JSON → protojson).
func Render(p Params) (*ateapi.ActorTemplate, *ateapi.EgressPolicy, error) {
	u, err := url.Parse(p.GitLabURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, nil, fmt.Errorf("gitlab url %q: want https://<host>", p.GitLabURL)
	}

	d := data{Params: p, GitLabHost: u.Hostname()}
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
	if err := render(funcs, "actortemplate.yaml.tmpl", d, tmpl); err != nil {
		return nil, nil, err
	}

	pol := &ateapi.EgressPolicy{}
	if err := render(funcs, "egresspolicy.yaml.tmpl", d, pol); err != nil {
		return nil, nil, err
	}

	return tmpl, pol, nil
}

func render(funcs template.FuncMap, name string, d data, out proto.Message) error {
	t, err := template.New(name).Funcs(funcs).Option("missingkey=error").ParseFS(files, name)
	if err != nil {
		return fmt.Errorf("parse %s: %w", name, err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
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
