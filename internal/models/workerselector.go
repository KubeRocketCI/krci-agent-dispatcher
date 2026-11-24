package models

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// WorkerSelector is the match labels of an actor template: a worker hosts its actors only when every key equals
// the worker label of that key. Worker labels are the metadata.labels of the WorkerPool, so keys and values follow
// Kubernetes label syntax.
type WorkerSelector map[string]string

var (
	labelName = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	dnsPrefix = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
)

const (
	maxLabelName   = 63
	maxLabelPrefix = 253
)

// ParseWorkerSelector returns the selector of s: key=value pairs, comma separated, blanks around keys and values
// trimmed. Every pair needs a key and a value; a key appears once. The error never holds s.
func ParseWorkerSelector(s string) (WorkerSelector, error) {
	sel := WorkerSelector{}

	for i, pair := range strings.Split(s, ",") {
		if err := sel.add(pair); err != nil {
			return nil, fmt.Errorf("must be key=value pairs, comma separated: pair %d %w", i+1, err)
		}
	}

	return sel, nil
}

func (s WorkerSelector) add(pair string) error {
	if strings.TrimSpace(pair) == "" {
		return errors.New("is empty")
	}

	k, v, _ := strings.Cut(pair, "=")
	k, v = strings.TrimSpace(k), strings.TrimSpace(v)

	switch _, seen := s[k]; {
	case k == "":
		return errors.New("has no key")
	case v == "":
		return errors.New("has no value")
	case !isLabelKey(k):
		return errors.New("has an invalid key")
	case !isLabelValue(v):
		return errors.New("has an invalid value")
	case seen:
		return errors.New("repeats a key")
	}

	s[k] = v

	return nil
}

// String returns the pairs as key=value, sorted by key, comma separated: the form ParseWorkerSelector reads.
func (s WorkerSelector) String() string {
	pairs := make([]string, 0, len(s))
	for _, k := range slices.Sorted(maps.Keys(s)) {
		pairs = append(pairs, k+"="+s[k])
	}

	return strings.Join(pairs, ",")
}

// isLabelKey reports whether k is a Kubernetes label key: an optional DNS subdomain prefix of up to 253 characters
// and a slash, then a name of up to 63 alphanumerics, '-', '_' or '.', alphanumeric at both ends.
func isLabelKey(k string) bool {
	name := k

	if prefix, rest, ok := strings.Cut(k, "/"); ok {
		if len(prefix) > maxLabelPrefix || !dnsPrefix.MatchString(prefix) {
			return false
		}

		name = rest
	}

	return len(name) <= maxLabelName && labelName.MatchString(name)
}

// isLabelValue reports whether v is a non-empty Kubernetes label value: up to 63 alphanumerics, '-', '_' or '.',
// alphanumeric at both ends.
func isLabelValue(v string) bool {
	return len(v) <= maxLabelName && labelName.MatchString(v)
}
