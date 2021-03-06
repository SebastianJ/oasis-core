package byzantine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oasislabs/oasis-core/go/common/sgx/ias"
)

func TestFakeCapabilitySGX(t *testing.T) {
	_, fakeCapabilitiesSGX, err := initFakeCapabilitiesSGX()
	require.NoError(t, err, "initFakeCapabilitiesSGX failed")

	ias.SetSkipVerify()
	ias.SetAllowDebugEnclaves()
	require.NoError(t, fakeCapabilitiesSGX.TEE.Verify(time.Now()), "fakeCapabilitiesSGX not valid")
}
