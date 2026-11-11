package models

import (
	"errors"
	"strconv"
	"strings"
)

// Role is a Git provider project role; on GitLab its value is the access level.
type Role int

// Project roles, lowest first.
const (
	Guest      Role = 10
	Reporter   Role = 20
	Developer  Role = 30
	Maintainer Role = 40
	Owner      Role = 50
)

var (
	roleNames = map[Role]string{
		Guest: "guest", Reporter: "reporter", Developer: "developer", Maintainer: "maintainer", Owner: "owner",
	}
	// triggerRoles are the roles ParseRole accepts: the roles that can push.
	triggerRoles = map[string]Role{"developer": Developer, "maintainer": Maintainer, "owner": Owner}
	errRole      = errors.New("must be one of developer, maintainer, owner")
)

// ParseRole returns the trigger role named name, trimmed and case-insensitive: developer, maintainer or owner.
// The error never holds name.
func ParseRole(name string) (Role, error) {
	r, ok := triggerRoles[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return 0, errRole
	}

	return r, nil
}

// IsTrigger reports whether r is a role ParseRole returns.
func (r Role) IsTrigger() bool { return r == Developer || r == Maintainer || r == Owner }

// String returns the lower-case role name; an unknown role is "role(<n>)".
func (r Role) String() string {
	if n, ok := roleNames[r]; ok {
		return n
	}

	return "role(" + strconv.Itoa(int(r)) + ")"
}

// StartRefusedText is the note for a start request whose author is below the trigger role r.
func StartRefusedText(r Role) string {
	n := r.String()

	return "Only " + strings.ToUpper(n[:1]) + n[1:] + "s or higher can start the agent."
}
