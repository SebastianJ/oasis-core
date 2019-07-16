// Package tests si a collection of roothash implementation test cases.
package tests

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oasislabs/ekiden/go/common"
	"github.com/oasislabs/ekiden/go/common/crypto/hash"
	"github.com/oasislabs/ekiden/go/common/crypto/signature"
	registry "github.com/oasislabs/ekiden/go/registry/api"
	registryTests "github.com/oasislabs/ekiden/go/registry/tests"
	"github.com/oasislabs/ekiden/go/roothash/api"
	"github.com/oasislabs/ekiden/go/roothash/api/block"
	"github.com/oasislabs/ekiden/go/roothash/api/commitment"
	scheduler "github.com/oasislabs/ekiden/go/scheduler/api"
	storage "github.com/oasislabs/ekiden/go/storage/api"
	"github.com/oasislabs/ekiden/go/storage/mkvs/urkel"
	ticker "github.com/oasislabs/ekiden/go/ticker/api"
	tickerTests "github.com/oasislabs/ekiden/go/ticker/tests"
)

const (
	recvTimeout = 2 * time.Second
	nrRuntimes  = 3
)

type runtimeState struct {
	id           string
	rt           *registryTests.TestRuntime
	genesisBlock *block.Block

	computeCommittee *testCommittee
	mergeCommittee   *testCommittee
}

// RootHashImplementationTests exercises the basic functionality of a
// roothash backend.
func RootHashImplementationTests(t *testing.T, backend api.Backend, timeSource ticker.SetableBackend, scheduler scheduler.Backend, storage storage.Backend, registry registry.Backend) {
	seedBase := []byte("RootHashImplementationTests")

	require := require.New(t)

	// Ensure that we leave the registry empty when we are done.
	rtStates := make([]*runtimeState, 0, nrRuntimes)
	defer func() {
		if len(rtStates) > 0 {
			// This is entity deregistration based, and all of the
			// runtimes used in this test share the entity.
			rtStates[0].rt.Cleanup(t, registry)
		}

		registryTests.EnsureRegistryEmpty(t, registry)
	}()

	// Populate the registry.
	runtimes := make([]*registryTests.TestRuntime, 0, nrRuntimes)
	for i := 0; i < nrRuntimes; i++ {
		t.Logf("Generating runtime: %d", i)
		seed := append([]byte{}, seedBase...)
		seed = append(seed, byte(i))

		rt, err := registryTests.NewTestRuntime(seed, nil)
		require.NoError(err, "NewTestRuntime")

		rtStates = append(rtStates, &runtimeState{
			id: strconv.Itoa(i),
			rt: rt,
		})
		runtimes = append(runtimes, rt)
	}
	registryTests.BulkPopulate(t, registry, runtimes, seedBase)
	for _, rt := range runtimes {
		rt.MustRegister(t, registry)
	}

	// Run the various tests. (Ordering matters)
	for _, v := range rtStates {
		t.Run("GenesisBlock/"+v.id, func(t *testing.T) {
			testGenesisBlock(t, backend, v)
		})
	}
	t.Run("EpochTransitionBlock", func(t *testing.T) {
		testEpochTransitionBlock(t, backend, timeSource, scheduler, rtStates)
	})
	t.Run("SucessfulRound", func(t *testing.T) {
		testSucessfulRound(t, backend, storage, rtStates)
	})

	// TODO: Test the various failures.
}

func testGenesisBlock(t *testing.T, backend api.Backend, state *runtimeState) {
	require := require.New(t)

	id := state.rt.Runtime.ID
	ch, sub, err := backend.WatchBlocks(id)
	require.NoError(err, "WatchBlocks")
	defer sub.Close()

	var genesisBlock *block.Block
	select {
	case blk := <-ch:
		header := blk.Block.Header

		require.EqualValues(header.Version, 0, "block version")
		require.EqualValues(0, header.Round, "block round")
		require.Equal(block.Normal, header.HeaderType, "block header type")
		require.True(header.IORoot.IsEmpty(), "block I/O root empty")
		require.True(header.StateRoot.IsEmpty(), "block root hash empty")
		genesisBlock = blk.Block
	case <-time.After(recvTimeout):
		t.Fatalf("failed to receive block")
	}

	blk, err := backend.GetLatestBlock(context.Background(), id)
	require.NoError(err, "GetLatestBlock")
	require.EqualValues(genesisBlock, blk, "retreived block is genesis block")

	// We need to wait for the indexer to index the block. We could have a channel
	// to subscribe to these updates and this would not be needed.
	time.Sleep(1 * time.Second)

	blk, err = backend.GetBlock(context.Background(), id, 0)
	require.NoError(err, "GetBlock")
	require.EqualValues(genesisBlock, blk, "retreived block is genesis block")
}

func testEpochTransitionBlock(t *testing.T, backend api.Backend, timeSource ticker.SetableBackend, scheduler scheduler.Backend, states []*runtimeState) {
	require := require.New(t)

	// Before an epoch transition there should just be a genesis block.
	for _, v := range states {
		genesisBlock, err := backend.GetLatestBlock(context.Background(), v.rt.Runtime.ID)
		require.NoError(err, "GetLatestBlock")
		require.EqualValues(0, genesisBlock.Header.Round, "genesis block round")

		v.genesisBlock = genesisBlock
	}

	// GetEpoch.
	epoch, err := scheduler.GetEpoch(context.Background(), 0)
	require.NoError(err, "GetEpoch")

	// Subscribe to blocks for all of the runtimes.
	var blkChannels []<-chan *api.AnnotatedBlock
	for i := range states {
		v := states[i]
		ch, sub, err := backend.WatchBlocks(v.rt.Runtime.ID)
		require.NoError(err, "WatchBlocks")
		defer sub.Close()

		blkChannels = append(blkChannels, ch)
	}

	// Advance the epoch.
	tickerTests.MustAdvanceEpoch(t, timeSource, scheduler)

	// Check for the expected post-epoch transition events.
	for i, state := range states {
		blkCh := blkChannels[i]
		state.testEpochTransitionBlock(t, scheduler, epoch, blkCh)
	}
}

func (s *runtimeState) testEpochTransitionBlock(t *testing.T, scheduler scheduler.Backend, epoch uint64, ch <-chan *api.AnnotatedBlock) {
	require := require.New(t)

	nodes := make(map[signature.MapKey]*registryTests.TestNode)
	for _, node := range s.rt.TestNodes() {
		nodes[node.Node.ID.ToMapKey()] = node
	}

	s.computeCommittee, s.mergeCommittee = mustGetCommittee(t, s.rt, epoch+1, scheduler, nodes)

	// Wait to receive an epoch transition block.
	for {
		select {
		case blk := <-ch:
			header := blk.Block.Header

			if header.HeaderType != block.EpochTransition {
				continue
			}

			require.True(header.IsParentOf(&s.genesisBlock.Header), "parent is parent of genesis block")
			require.True(header.IORoot.IsEmpty(), "block I/O root empty")
			require.EqualValues(s.genesisBlock.Header.StateRoot, header.StateRoot, "state root preserved")

			// Nothing more to do after the epoch transition block was received.
			return
		case <-time.After(recvTimeout):
			t.Fatalf("failed to receive block")
		}
	}
}

func testSucessfulRound(t *testing.T, backend api.Backend, storage storage.Backend, states []*runtimeState) {
	for _, state := range states {
		state.testSuccessfulRound(t, backend, storage)
	}
}

func (s *runtimeState) testSuccessfulRound(t *testing.T, backend api.Backend, storageBackend storage.Backend) {
	require := require.New(t)

	rt, computeCommittee, mergeCommittee := s.rt, s.computeCommittee, s.mergeCommittee

	child, err := backend.GetLatestBlock(context.Background(), rt.Runtime.ID)
	require.NoError(err, "GetLatestBlock")

	ch, sub, err := backend.WatchBlocks(rt.Runtime.ID)
	require.NoError(err, "WatchBlocks")
	defer sub.Close()

	// Generate a dummy I/O root.
	ctx := context.Background()
	tree := urkel.New(nil, nil)
	err = tree.Insert(ctx, block.IoKeyInputs, []byte("testInputSet"))
	require.NoError(err, "tree.Insert")
	err = tree.Insert(ctx, block.IoKeyOutputs, []byte("testOutputSet"))
	require.NoError(err, "tree.Insert")
	err = tree.Insert(ctx, block.IoKeyTags, []byte("testTagSet"))
	require.NoError(err, "tree.Insert")
	ioWriteLog, ioRoot, err := tree.Commit(ctx, child.Header.Namespace, child.Header.Round+1)
	require.NoError(err, "tree.Commit")

	var emptyRoot hash.Hash
	emptyRoot.Empty()

	// Create the new block header that the leader and nodes will commit to.
	parent := &block.Block{
		Header: block.Header{
			Version:      0,
			Namespace:    child.Header.Namespace,
			Round:        child.Header.Round + 1,
			Timestamp:    uint64(time.Now().Unix()),
			HeaderType:   block.Normal,
			PreviousHash: child.Header.EncodedHash(),
			IORoot:       ioRoot,
			StateRoot:    ioRoot,
		},
	}
	require.True(parent.Header.IsParentOf(&child.Header), "parent is parent of child")
	parent.Header.StorageSignatures = mustStore(
		t,
		storageBackend,
		child.Header.Namespace,
		child.Header.Round+1,
		[]storage.ApplyOp{
			storage.ApplyOp{SrcRound: child.Header.Round + 1, SrcRoot: emptyRoot, DstRoot: ioRoot, WriteLog: ioWriteLog},
			// NOTE: Twice to get a receipt over both roots which we set to the same value.
			storage.ApplyOp{SrcRound: child.Header.Round, SrcRoot: emptyRoot, DstRoot: ioRoot, WriteLog: ioWriteLog},
		},
	)

	// Generate all the compute commitments.
	var toCommit []*registryTests.TestNode
	var computeCommits []commitment.ComputeCommitment
	toCommit = append(toCommit, computeCommittee.workers...)
	for _, node := range toCommit {
		commitBody := commitment.ComputeBody{
			CommitteeID: computeCommittee.committee.EncodedMembersHash(),
			Header: commitment.ComputeResultsHeader{
				PreviousHash: parent.Header.PreviousHash,
				IORoot:       parent.Header.IORoot,
				StateRoot:    parent.Header.StateRoot,
			},
			StorageSignatures: parent.Header.StorageSignatures,
		}
		// `err` shadows outside.
		commit, err := commitment.SignComputeCommitment(node.Signer, &commitBody) // nolint: vetshadow
		require.NoError(err, "SignSigned")

		computeCommits = append(computeCommits, *commit)
	}

	// Generate all the merge commitments.
	var mergeCommits []commitment.MergeCommitment
	toCommit = []*registryTests.TestNode{}
	toCommit = append(toCommit, mergeCommittee.workers...)
	for _, node := range toCommit {
		commitBody := commitment.MergeBody{
			ComputeCommits: computeCommits,
			Header:         parent.Header,
		}
		// `err` shadows outside.
		commit, err := commitment.SignMergeCommitment(node.Signer, &commitBody) // nolint: vetshadow
		require.NoError(err, "SignSigned")

		mergeCommits = append(mergeCommits, *commit)
	}

	err = backend.MergeCommit(context.Background(), rt.Runtime.ID, mergeCommits)
	require.NoError(err, "MergeCommit")

	// Ensure that the round was finalized.
	for {
		select {
		case blk := <-ch:
			header := blk.Block.Header

			// Ensure that WatchBlocks uses the correct latest block.
			require.True(header.Round >= child.Header.Round, "WatchBlocks must start at child block")

			if header.Round == child.Header.Round {
				require.EqualValues(child.Header, header, "old block is equal")
				continue
			}

			// Can't direcly compare headers, some backends rewrite the timestamp.
			require.EqualValues(parent.Header.Version, header.Version, "block version")
			require.EqualValues(parent.Header.Namespace, header.Namespace, "block namespace")
			require.EqualValues(parent.Header.Round, header.Round, "block round")
			// Timestamp
			require.EqualValues(parent.Header.HeaderType, header.HeaderType, "block header type")
			require.EqualValues(parent.Header.PreviousHash, header.PreviousHash, "block previous hash")
			require.EqualValues(parent.Header.IORoot, header.IORoot, "block I/O root")
			require.EqualValues(parent.Header.StateRoot, header.StateRoot, "block root hash")

			// We need to wait for the indexer to index the block. We could have a channel
			// to subscribe to these updates and this would not be needed.
			time.Sleep(1 * time.Second)

			// Check if we can fetch the block via GetBlock.
			gblk, err := backend.GetBlock(context.Background(), rt.Runtime.ID, header.Round)
			require.NoError(err, "GetBlock")
			require.EqualValues(blk.Block, gblk, "GetBlock")

			// Nothing more to do after the block was received.
			return
		case <-time.After(recvTimeout):
			t.Fatalf("failed to receive block")
		}
	}
}

type testCommittee struct {
	committee     *scheduler.Committee
	leader        *registryTests.TestNode
	workers       []*registryTests.TestNode
	backupWorkers []*registryTests.TestNode
}

func mustGetCommittee(
	t *testing.T,
	rt *registryTests.TestRuntime,
	epoch uint64,
	sched scheduler.Backend,
	nodes map[signature.MapKey]*registryTests.TestNode,
) (computeCommittee *testCommittee, mergeCommittee *testCommittee) {
	require := require.New(t)

	ch, sub := sched.WatchCommittees()
	defer sub.Close()

	for {
		select {
		case committee := <-ch:
			if uint64(committee.ValidFor) < epoch {
				continue
			}
			if !rt.Runtime.ID.Equal(committee.RuntimeID) {
				continue
			}
			if committee.Kind != scheduler.KindCompute && committee.Kind != scheduler.KindMerge {
				continue
			}

			var ret testCommittee
			ret.committee = committee
			for _, member := range committee.Members {
				node := nodes[member.PublicKey.ToMapKey()]
				require.NotNil(node, "member is one of the nodes")
				switch member.Role {
				case scheduler.Worker:
					ret.workers = append(ret.workers, node)
				case scheduler.BackupWorker:
					ret.backupWorkers = append(ret.backupWorkers, node)
				case scheduler.Leader:
					ret.leader = node
				}
			}

			if committee.Kind.NeedsLeader() {
				require.Len(ret.workers, int(rt.Runtime.ReplicaGroupSize)-1, "workers exist")
				require.NotNil(ret.leader, "leader exist")
			} else {
				require.Len(ret.workers, int(rt.Runtime.ReplicaGroupSize), "workers exist")
				require.Nil(ret.leader, "no leader")
			}
			require.Len(ret.backupWorkers, int(rt.Runtime.ReplicaGroupBackupSize), "workers exist")

			switch committee.Kind {
			case scheduler.KindCompute:
				computeCommittee = &ret
			case scheduler.KindMerge:
				mergeCommittee = &ret
			}

			if computeCommittee == nil || mergeCommittee == nil {
				continue
			}

			return
		case <-time.After(recvTimeout):
			t.Fatalf("failed to receive committee event")
		}
	}
}

func mustStore(t *testing.T, store storage.Backend, ns common.Namespace, round uint64, ops []storage.ApplyOp) []signature.Signature {
	require := require.New(t)

	receipts, err := store.ApplyBatch(context.Background(), ns, round, ops)
	require.NoError(err, "Apply")

	signatures := []signature.Signature{}
	for _, receipt := range receipts {
		signatures = append(signatures, receipt.Signed.Signature)
	}
	return signatures
}
