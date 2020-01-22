package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputeParamSets(t *testing.T) {
	zippedParams := map[string][]string{
		"testParam1": []string{"1", "2", "3"},
		"testParam2": []string{"a", "b"},
	}

	expectedParamSets := []map[string]string{
		{"testParam1": "1", "testParam2": "a"},
		{"testParam1": "2", "testParam2": "a"},
		{"testParam1": "3", "testParam2": "a"},
		{"testParam1": "1", "testParam2": "b"},
		{"testParam1": "2", "testParam2": "b"},
		{"testParam1": "3", "testParam2": "b"},
	}

	require.Equal(t, expectedParamSets, computeParamSets(zippedParams, map[string]string{}))
}
