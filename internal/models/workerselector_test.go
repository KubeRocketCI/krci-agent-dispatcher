package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkerSelectorStringIsSortedAndParsesBack(t *testing.T) {
	sel := WorkerSelector{"workload": "large", "tier": "gpu", "example.com/pool": "a"}

	assert.Equal(t, "example.com/pool=a,tier=gpu,workload=large", sel.String())

	back, err := ParseWorkerSelector(sel.String())
	require.NoError(t, err)
	assert.Equal(t, sel, back)
}
