package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestActorViewParts(t *testing.T) {
	cases := []struct {
		name              string
		v                 ActorView
		complete, partial bool
	}{
		{"none", ActorView{}, false, false},
		{"template only", ActorView{Template: true}, false, true},
		{"template and actor", ActorView{Template: true, Actor: true, State: Suspended}, false, true},
		{"actor without template", ActorView{Actor: true, State: Suspended}, false, true},
		{"complete", ActorView{Template: true, Actor: true, EgressPolicy: true, State: Suspended}, true, false},
		{"policy only", ActorView{EgressPolicy: true}, false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.complete, c.v.Complete())
			assert.Equal(t, c.partial, c.v.Partial())
		})
	}
}

func TestActorViewExists(t *testing.T) {
	assert.False(t, ActorView{}.Exists())
	assert.True(t, ActorView{EgressPolicy: true}.Exists())
	assert.True(t, ActorView{Template: true}.Exists())
}

func TestActorSpecValidate(t *testing.T) {
	ok := ActorSpec{ProjectPath: "krci/app", IID: 7, SessionID: "s1", BotUsername: "krci-agent"}
	assert.NoError(t, ok.Validate())

	for name, mod := range map[string]func(*ActorSpec){
		"project path": func(s *ActorSpec) { s.ProjectPath = "" },
		"issue IID":    func(s *ActorSpec) { s.IID = 0 },
		"session ID":   func(s *ActorSpec) { s.SessionID = "" },
		"bot username": func(s *ActorSpec) { s.BotUsername = "" },
	} {
		s := ok
		mod(&s)
		err := s.Validate()
		assert.ErrorContains(t, err, name, name)
	}
}
