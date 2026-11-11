package intake

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/consttest"
)

func TestKindsListsEveryKind(t *testing.T) {
	consts, err := consttest.Declared("intake.go", "Kind")
	require.NoError(t, err)

	require.Len(t, Kinds(), len(consts), "a Kind constant is missing from kinds")

	for i, k := range Kinds() {
		assert.Equal(t, Kind(i), k, "kinds is not in declaration order")
		assert.NotEmpty(t, k.String(), "%s has no name in kindNames", consts[i].Name)
	}
}
