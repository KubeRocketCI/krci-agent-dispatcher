package consttest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const src = `package p

type Color string

type Kind int

const (
	Red   Color = "red"
	Green Color = "green"
	Size        = 3
)

const (
	First Kind = iota
	Second
	Third
	Other = "x"
	Blue Color = "blue"
)
`

func TestDeclared(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.go")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))

	colors, err := Declared(path, "Color")
	require.NoError(t, err)
	assert.Equal(t, []Const{{"Red", "red"}, {"Green", "green"}, {"Blue", "blue"}}, colors)

	kinds, err := Declared(path, "Kind")
	require.NoError(t, err)
	assert.Equal(t, []Const{{Name: "First"}, {Name: "Second"}, {Name: "Third"}}, kinds)
}

func TestDeclaredParseError(t *testing.T) {
	_, err := Declared(filepath.Join(t.TempDir(), "missing.go"), "Color")
	require.Error(t, err)
}
