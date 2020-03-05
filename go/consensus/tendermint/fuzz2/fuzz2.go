// +build gofuzz

package fuzz2

import (
	"context"
	"fmt"

	"github.com/tendermint/tendermint/abci/types"

	"github.com/oasislabs/oasis-core/go/common/cbor"
	"github.com/oasislabs/oasis-core/go/consensus/tendermint/abci"
	"github.com/oasislabs/oasis-core/go/consensus/tendermint/apps/beacon"
	epochtimemock "github.com/oasislabs/oasis-core/go/consensus/tendermint/apps/epochtime_mock"
	"github.com/oasislabs/oasis-core/go/consensus/tendermint/apps/keymanager"
	"github.com/oasislabs/oasis-core/go/consensus/tendermint/apps/registry"
	"github.com/oasislabs/oasis-core/go/consensus/tendermint/apps/roothash"
	"github.com/oasislabs/oasis-core/go/consensus/tendermint/apps/scheduler"
	"github.com/oasislabs/oasis-core/go/consensus/tendermint/apps/staking"
)

// Differences from fuzz:
// - fuzz2 is in memory
// - pruning is disabled in fuzz2
// - fuzz2 allows the fuzzer to send multiple transactions

type blockMessages struct {
	beginReq types.RequestBeginBlock
	txReqs []types.RequestDeliverTx
	endReq types.RequestEndBlock
}

type messages struct {
	initReq types.RequestInitChain
	blocks []blockMessages
}

func Fuzz(data []byte) int {
	var msgs messages
	if err := cbor.Unmarshal(data, &msgs); err != nil {
		return 0
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mux := abci.FuzzNewABCIMux(ctx)
	defer abci.FuzzMuxDoCleanup(mux)
	for _, app := range []abci.Application{
		epochtimemock.New(),
		beacon.New(),
		keymanager.New(),
		registry.New(),
		staking.New(),
		scheduler.New(),
		roothash.New(),
	} {
		if err := abci.FuzzMuxDoRegister(mux, app); err != nil {
			panic(fmt.Errorf("register %s: %w", app.Name(), err))
		}
	}

	mux.InitChain(msgs.initReq)
	for _, block := range msgs.blocks {
		mux.BeginBlock(block.beginReq)
		for _, tx := range block.txReqs {
			mux.DeliverTx(tx)
		}
		mux.EndBlock(block.endReq)
		mux.Commit()
	}

	return 1
}
