// Package beacon implements the random beacon backend.
package beacon

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/oasislabs/ekiden/go/beacon/api"
	"github.com/oasislabs/ekiden/go/beacon/tendermint"
	commonFlags "github.com/oasislabs/ekiden/go/ekiden/cmd/common/flags"
	"github.com/oasislabs/ekiden/go/tendermint/service"
	ticker "github.com/oasislabs/ekiden/go/ticker/api"
)

const (
	cfgDebugDeterministic = "beacon.debug.deterministic"
)

// New constructs a new Backend based on the configuration flags.
func New(ctx context.Context, timeSource ticker.Backend, tmService service.TendermintService) (api.Backend, error) {
	backend := commonFlags.ConsensusBackend()
	switch strings.ToLower(backend) {
	case tendermint.BackendName:
		return tendermint.New(ctx, timeSource, tmService, flagsToConfig())
	default:
		return nil, fmt.Errorf("beacon: unsupported backend: '%v'", backend)
	}
}

func flagsToConfig() *api.Config {
	return &api.Config{
		DebugDeterministic: viper.GetBool(cfgDebugDeterministic),
	}
}

// RegisterFlags registers the configuration flags with the provided
// command.
func RegisterFlags(cmd *cobra.Command) {
	if !cmd.Flags().Parsed() {
		cmd.Flags().Bool(cfgDebugDeterministic, false, "enable deterministic beacon output (UNSAFE)")
	}

	for _, v := range []string{
		cfgDebugDeterministic,
	} {
		viper.BindPFlag(v, cmd.Flags().Lookup(v)) //nolint: errcheck
	}
}
