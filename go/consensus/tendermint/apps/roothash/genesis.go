package roothash

import (
	"context"

	"github.com/tendermint/tendermint/abci/types"

	"github.com/oasislabs/oasis-core/go/common"
	"github.com/oasislabs/oasis-core/go/common/crypto/signature"
	"github.com/oasislabs/oasis-core/go/consensus/tendermint/abci"
	registryState "github.com/oasislabs/oasis-core/go/consensus/tendermint/apps/registry/state"
	roothashState "github.com/oasislabs/oasis-core/go/consensus/tendermint/apps/roothash/state"
	genesisAPI "github.com/oasislabs/oasis-core/go/genesis/api"
	"github.com/oasislabs/oasis-core/go/registry/api"
	roothashAPI "github.com/oasislabs/oasis-core/go/roothash/api"
	storageAPI "github.com/oasislabs/oasis-core/go/storage/api"
)

func (app *rootHashApplication) InitChain(ctx *abci.Context, request types.RequestInitChain, doc *genesisAPI.Document) error {
	st := doc.RootHash

	state := roothashState.NewMutableState(ctx.State())
	state.SetConsensusParameters(&st.Parameters)

	// The per-runtime roothash state is done primarily via DeliverTx, but
	// also needs to be done here since the genesis state can have runtime
	// registrations.
	//
	// Note: This could use the genesis state, but the registry has already
	// carved out it's entries by this point.

	regState := registryState.NewMutableState(ctx.State())
	runtimes, _ := regState.Runtimes()
	for _, v := range runtimes {
		ctx.Logger().Info("InitChain: allocating per-runtime state",
			"runtime", v.ID,
		)
		app.onNewRuntime(ctx, v, &st)
	}

	return nil
}

func (rq *rootHashQuerier) Genesis(ctx context.Context) (*roothashAPI.Genesis, error) {
	runtimes := rq.state.Runtimes()

	// Get per-runtime states.
	rtStates := make(map[common.Namespace]*api.RuntimeGenesis)
	for _, rt := range runtimes {
		rtState := api.RuntimeGenesis{
			StateRoot: rt.CurrentBlock.Header.StateRoot,
			// State is always empty in Genesis regardless of StateRoot.
			State:           storageAPI.WriteLog{},
			StorageReceipts: []signature.Signature{},
			Round:           rt.CurrentBlock.Header.Round,
		}

		rtStates[rt.Runtime.ID] = &rtState
	}

	params, err := rq.state.ConsensusParameters()
	if err != nil {
		return nil, err
	}

	genesis := &roothashAPI.Genesis{
		Parameters:    *params,
		RuntimeStates: rtStates,
	}
	return genesis, nil
}
