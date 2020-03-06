// +build gofuzz

package abci

import (
	"context"

	"github.com/tendermint/iavl"
	dbm "github.com/tendermint/tm-db"

	"github.com/oasislabs/oasis-core/go/common/crypto/signature"
	"github.com/oasislabs/oasis-core/go/common/logging"
	"github.com/oasislabs/oasis-core/go/consensus/api/transaction"
)

func FuzzNewABCIMux(ctx context.Context) *abciMux {
	db := dbm.NewMemDB()
	deliverTxTree := iavl.NewMutableTree(db, 128)
	blockHeight, err := deliverTxTree.Load()
	if err != nil {
		db.Close()
		panic(err)
	}
	blockHash := deliverTxTree.Hash()
	var ownTxSigner signature.PublicKey
	err = ownTxSigner.UnmarshalHex("0000000000000000000000000000000000000000000000000000000000000061")
	if err != nil {
		db.Close()
		panic(err)
	}
	fakeMetricsCh := make(chan struct{})
	return &abciMux{
		logger: logging.GetLogger("abci-mux"),
		state: &applicationState{
			logger:          logging.GetLogger("abci-mux/state"),
			ctx:             ctx,
			db:              db,
			deliverTxTree:   deliverTxTree,
			blockHash:       blockHash,
			blockHeight:     blockHeight,
			ownTxSigner:     ownTxSigner,
			metricsCloseCh:  fakeMetricsCh,
			metricsClosedCh: fakeMetricsCh,
		},
		appsByName:     make(map[string]Application),
		appsByMethod:   make(map[transaction.MethodName]Application),
		lastBeginBlock: -1,
	}
}

func FuzzMuxDoCleanup(mux *abciMux) {
	mux.doCleanup()
}

func FuzzMuxDoRegister(mux *abciMux, app Application) error {
	return mux.doRegister(app)
}
