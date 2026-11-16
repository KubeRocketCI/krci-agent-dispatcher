// Package profiles reads the profiles file (profiles.yaml): profile entries, the project mapping and the default
// profile. Parse and Config are pure; File is the Profiles port adapter, with an in-memory fake in profilestest.
package profiles

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate/templates"
)

// Config is one parsed profiles file. broken holds the rule of each profile entry that does not parse.
type Config struct {
	profiles       map[string]models.ProfileEntry
	broken         map[string]string
	projects       map[string]string
	defaultProfile string
}

const invalidFile = "profiles file is not valid"

var oneLine = strings.NewReplacer("\r", " ", "\n", " ")

// fileError is a profiles file that does not parse; the cause holds the reason.
func fileError(format string, args ...any) *models.ProfileError {
	return &models.ProfileError{Rule: invalidFile, Cause: oneLine.Replace(fmt.Sprintf(format, args...))}
}

// Parse reads a profiles file: a YAML map with the keys profiles, projects and defaultProfile. A file of another
// shape, a profile listed twice or a project listed twice ignoring case is a *models.ProfileError without a
// profile. A profile entry of another shape fails only its own projects (Resolve, Check). Env values are taken as
// written. Entries are not checked here.
func Parse(b []byte) (Config, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return Config{}, fileError("%v", err)
	}

	c := Config{profiles: map[string]models.ProfileEntry{}, broken: map[string]string{}, projects: map[string]string{}}

	if len(doc.Content) == 0 {
		return c, nil
	}

	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return Config{}, fileError("line %d: the file must be a map", top.Line)
	}

	for k, v := range pairs(top) {
		var err error

		switch k.Value {
		case "profiles":
			err = c.parseProfiles(v)
		case "projects":
			err = c.parseProjects(v)
		case "defaultProfile":
			c.defaultProfile, err = scalar(v, "defaultProfile must be a string")
		default:
			err = fmt.Errorf("line %d: unknown key %q", k.Line, k.Value)
		}

		if err != nil {
			return Config{}, fileError("%v", err)
		}
	}

	return c, nil
}

func (c *Config) parseProfiles(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: profiles must be a map of names to entries", n.Line)
	}

	for k, v := range pairs(n) {
		name := k.Value
		if _, ok := c.profiles[name]; ok {
			return fmt.Errorf("line %d: profile %q is listed twice", k.Line, name)
		}

		p, rule := parseEntry(name, v)
		c.profiles[name] = p

		if rule != "" {
			c.broken[name] = rule
		}
	}

	return nil
}

func (c *Config) parseProjects(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: projects must be a map of project paths to profiles", n.Line)
	}

	for k, v := range pairs(n) {
		key := strings.ToLower(k.Value)
		if _, ok := c.projects[key]; ok {
			return fmt.Errorf("line %d: project %q is listed twice", k.Line, key)
		}

		name, err := scalar(v, fmt.Sprintf("profile of project %q must be a string", key))
		if err != nil {
			return err
		}

		c.projects[key] = name
	}

	return nil
}

// parseEntry returns the profile entry of n and the rule of the first part that does not parse; empty when all do.
// Env is ordered by name.
func parseEntry(name string, n *yaml.Node) (models.ProfileEntry, string) {
	p := models.ProfileEntry{Name: name}

	if n.Kind != yaml.MappingNode {
		return p, "entry must be a map with the keys image, egress, env"
	}

	for k, v := range pairs(n) {
		var rule string

		switch k.Value {
		case "image":
			var err error
			if p.Image, err = scalar(v, ""); err != nil {
				rule = "image must be a string"
			}
		case "egress":
			p.Egress, rule = parseEgress(v)
		case "env":
			p.Env, rule = parseEnv(v)
		default:
			rule = fmt.Sprintf("unknown key %q", k.Value)
		}

		if rule != "" {
			return p, rule
		}
	}

	return p, ""
}

func parseEgress(n *yaml.Node) ([]string, string) {
	const rule = "egress must be a list of hosts"

	if n.Kind != yaml.SequenceNode {
		return nil, rule
	}

	hosts := make([]string, 0, len(n.Content))

	for _, h := range n.Content {
		if h.Kind != yaml.ScalarNode {
			return nil, rule
		}

		hosts = append(hosts, h.Value)
	}

	return hosts, ""
}

func parseEnv(n *yaml.Node) ([]models.EnvVar, string) {
	if n.Kind != yaml.MappingNode {
		return nil, "env must be a map of names to values"
	}

	var env []models.EnvVar

	for k, v := range pairs(n) {
		if v.Kind != yaml.ScalarNode {
			return nil, fmt.Sprintf("env value of %q must be a scalar", k.Value)
		}

		env = append(env, models.EnvVar{Name: k.Value, Value: v.Value})
	}

	slices.SortStableFunc(env, func(a, b models.EnvVar) int { return strings.Compare(a.Name, b.Name) })

	return env, ""
}

// pairs yields the keys and values of mapping node n.
func pairs(n *yaml.Node) func(func(k, v *yaml.Node) bool) {
	return func(yield func(k, v *yaml.Node) bool) {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if !yield(n.Content[i], n.Content[i+1]) {
				return
			}
		}
	}
}

// scalar returns the text of scalar node n; another node is an error with rule.
func scalar(n *yaml.Node, rule string) (string, error) {
	if n.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("line %d: %s", n.Line, rule)
	}

	return n.Value, nil
}

// Resolve returns the profile entry of projectPath, checked with templates.CheckProfile against d: the project
// mapping, matched ignoring case, else the default profile. Failures are *models.ProfileError.
func (c Config) Resolve(d templates.Deployment, projectPath string) (models.ProfileEntry, error) {
	key := strings.ToLower(projectPath)

	name, mapped := c.projects[key]

	switch {
	case mapped && name == "":
		return models.ProfileEntry{}, emptyMapping(key)
	case !mapped:
		name = c.defaultProfile
	}

	if name == "" {
		return models.ProfileEntry{}, &models.ProfileError{Rule: "no project mapping and no defaultProfile"}
	}

	if rules := c.rules(d, name); len(rules) > 0 {
		return models.ProfileEntry{}, &models.ProfileError{Profile: name, Rule: rules[0]}
	}

	return c.profiles[name], nil
}

func emptyMapping(path string) *models.ProfileError {
	return &models.ProfileError{Rule: fmt.Sprintf("profile of project %q is empty", path)}
}

// rules returns every rule profile name breaks: not defined, not parsed, or the rules of templates.CheckProfile.
func (c Config) rules(d templates.Deployment, name string) []string {
	p, ok := c.profiles[name]

	switch {
	case !ok:
		return []string{"profile is not defined"}
	case c.broken[name] != "":
		return []string{c.broken[name]}
	}

	return templates.CheckProfile(d, p)
}

// Check returns every problem of c against d, ordered by profile name, then the project mapping and the default
// profile: each rule a profile entry breaks, an empty or undefined profile of a project and an undefined default
// profile. Empty when every project resolves.
func (c Config) Check(d templates.Deployment) []*models.ProfileError {
	var problems []*models.ProfileError

	for _, name := range slices.Sorted(maps.Keys(c.profiles)) {
		for _, rule := range c.rules(d, name) {
			problems = append(problems, &models.ProfileError{Profile: name, Rule: rule})
		}
	}

	for _, path := range slices.Sorted(maps.Keys(c.projects)) {
		switch name := c.projects[path]; {
		case name == "":
			problems = append(problems, emptyMapping(path))
		case !c.defined(name):
			problems = append(problems, &models.ProfileError{
				Profile: name, Rule: fmt.Sprintf("profile of project %q is not defined", path),
			})
		}
	}

	if c.defaultProfile != "" && !c.defined(c.defaultProfile) {
		problems = append(problems, &models.ProfileError{Profile: c.defaultProfile, Rule: "defaultProfile is not defined"})
	}

	return problems
}

func (c Config) defined(name string) bool {
	_, ok := c.profiles[name]
	return ok
}
