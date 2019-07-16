// Package tests is a collection of worker test cases.
package tests

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oasislabs/ekiden/go/common/crypto/hash"
	"github.com/oasislabs/ekiden/go/common/crypto/signature"
	"github.com/oasislabs/ekiden/go/common/runtime"
	roothash "github.com/oasislabs/ekiden/go/roothash/api"
	"github.com/oasislabs/ekiden/go/roothash/api/block"
	scheduler "github.com/oasislabs/ekiden/go/scheduler/api"
	storage "github.com/oasislabs/ekiden/go/storage/api"
	"github.com/oasislabs/ekiden/go/storage/mkvs/urkel"
	ticker "github.com/oasislabs/ekiden/go/ticker/api"
	tickerTests "github.com/oasislabs/ekiden/go/ticker/tests"
	"github.com/oasislabs/ekiden/go/worker/txnscheduler"
	"github.com/oasislabs/ekiden/go/worker/txnscheduler/committee"
)

const recvTimeout = 5 * time.Second

// WorkerImplementationTests runs the worker implementation tests.
//
// NOTE: This test suite must be run before all other backend-specific
// suites as it requires that no epoch transitions have taken place
// after the node was registered.
func WorkerImplementationTests(
	t *testing.T,
	worker *txnscheduler.Worker,
	runtimeID signature.PublicKey,
	rtNode *committee.Node,
	ticker ticker.SetableBackend,
	roothash roothash.Backend,
	storage storage.Backend,
	scheduler scheduler.Backend,
) {
	// Wait for worker to start and register.
	<-worker.Initialized()

	// Subscribe to state transitions.
	stateCh, sub := rtNode.WatchStateTransitions()
	defer sub.Close()

	// Run the various test cases. (Ordering matters.)
	t.Run("InitialEpochTransition", func(t *testing.T) {
		testInitialEpochTransition(t, stateCh, ticker, scheduler)
	})

	t.Run("QueueCall", func(t *testing.T) {
		testQueueCall(t, runtimeID, stateCh, rtNode, roothash, storage)
	})

	// TODO: Add more tests.
}

func testInitialEpochTransition(t *testing.T, stateCh <-chan committee.NodeState, ticker ticker.SetableBackend, scheduler scheduler.Backend) {
	// Perform an epoch transition, so that the node gets elected leader.
	tickerTests.MustAdvanceEpoch(t, ticker, scheduler)

	// Node should transition to WaitingForBatch state.
	waitForNodeTransition(t, stateCh, committee.WaitingForBatch)
}

func testQueueCall(
	t *testing.T,
	runtimeID signature.PublicKey,
	stateCh <-chan committee.NodeState,
	rtNode *committee.Node,
	roothash roothash.Backend,
	st storage.Backend,
) {
	// Subscribe to roothash blocks.
	blocksCh, sub, err := roothash.WatchBlocks(runtimeID)
	require.NoError(t, err, "WatchBlocks")
	defer sub.Close()

	select {
	case <-blocksCh:
	case <-time.After(recvTimeout):
		t.Fatalf("failed to receive block")
	}

	// Queue a test call.
	testCall := []byte("hello world")
	err = rtNode.QueueCall(context.Background(), testCall)
	require.NoError(t, err, "QueueCall")

	// Node should transition to WaitingForFinalize state.
	waitForNodeTransition(t, stateCh, committee.WaitingForFinalize)

	// Node should transition to WaitingForBatch state and a block should be
	// finalized containing our batch.
	waitForNodeTransition(t, stateCh, committee.WaitingForBatch)

	select {
	case annBlk := <-blocksCh:
		blk := annBlk.Block
		// Check that correct block was generated.
		require.EqualValues(t, block.Normal, blk.Header.HeaderType)

		ctx := context.Background()
		tree, err := urkel.NewWithRoot(ctx, st, nil, storage.Root{
			Namespace: blk.Header.Namespace,
			Round:     blk.Header.Round,
			Hash:      blk.Header.IORoot,
		})
		require.NoError(t, err, "NewWithRoot")

		rawInputs, err := tree.Get(ctx, block.IoKeyInputs)
		require.NoError(t, err, "Get(inputs)")
		rawOutputs, err := tree.Get(ctx, block.IoKeyOutputs)
		require.NoError(t, err, "Get(outputs)")

		batch := runtime.Batch([][]byte{testCall})
		rawBatch := batch.MarshalCBOR()

		require.EqualValues(t, rawBatch, rawInputs)
		// NOTE: Mock host produces output equal to input.
		require.EqualValues(t, rawBatch, rawOutputs)

		// NOTE: Mock host produces an empty state root.
		var stateRoot hash.Hash
		stateRoot.Empty()
		require.EqualValues(t, stateRoot, blk.Header.StateRoot)
	case <-time.After(recvTimeout):
		t.Fatalf("failed to receive block")
	}
}

func waitForNodeTransition(t *testing.T, stateCh <-chan committee.NodeState, expectedState committee.StateName) {
	timeout := time.After(recvTimeout)
	for {
		select {
		case newState := <-stateCh:
			if expectedState == newState.Name() {
				return
			}
		case <-timeout:
			t.Fatalf("failed to receive transition to %s state", expectedState)
			return
		}
	}
}
