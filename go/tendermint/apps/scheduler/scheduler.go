package scheduler

import (
	"bytes"
	"crypto"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"github.com/pkg/errors"
	"github.com/tendermint/tendermint/abci/types"

	"github.com/oasislabs/ekiden/go/common/cbor"
	"github.com/oasislabs/ekiden/go/common/crypto/drbg"
	"github.com/oasislabs/ekiden/go/common/crypto/mathrand"
	"github.com/oasislabs/ekiden/go/common/crypto/signature"
	"github.com/oasislabs/ekiden/go/common/logging"
	"github.com/oasislabs/ekiden/go/common/node"
	genesis "github.com/oasislabs/ekiden/go/genesis/api"
	registry "github.com/oasislabs/ekiden/go/registry/api"
	scheduler "github.com/oasislabs/ekiden/go/scheduler/api"
	staking "github.com/oasislabs/ekiden/go/staking/api"
	"github.com/oasislabs/ekiden/go/tendermint/abci"
	"github.com/oasislabs/ekiden/go/tendermint/api"
	beaconapp "github.com/oasislabs/ekiden/go/tendermint/apps/beacon"
	registryapp "github.com/oasislabs/ekiden/go/tendermint/apps/registry"
	stakingapp "github.com/oasislabs/ekiden/go/tendermint/apps/staking"
	ticker "github.com/oasislabs/ekiden/go/ticker/api"
)

var (
	_ abci.Application = (*schedulerApplication)(nil)

	rngContextCompute              = []byte("EkS-ABCI-Compute")
	rngContextStorage              = []byte("EkS-ABCI-Storage")
	rngContextTransactionScheduler = []byte("EkS-ABCI-TransactionScheduler")
	rngContextMerge                = []byte("EkS-ABCI-Merge")

	errUnexpectedTransaction = errors.New("tendermint/scheduler: unexpected transaction")
)

type stakeAccumulator struct {
	snapshot       *stakingapp.Snapshot
	perEntityStake map[signature.MapKey][]staking.ThresholdKind

	unsafeBypass bool
}

func (acc *stakeAccumulator) checkAndAccumulate(id signature.PublicKey, kind staking.ThresholdKind) error {
	if acc.unsafeBypass {
		return nil
	}

	mk := id.ToMapKey()

	// The staking balance is per-entity.  Each entity can have multiple nodes,
	// that each can serve multiple roles.  Check the entity's balance to see
	// that it has sufficient stake for the current roles and the additional
	// role.
	kinds := make([]staking.ThresholdKind, 0, 1)
	if existing, ok := acc.perEntityStake[mk]; ok && len(existing) > 0 {
		kinds = append(kinds, existing...)
	}
	kinds = append(kinds, kind)

	if err := acc.snapshot.EnsureSufficientStake(id, kinds); err != nil {
		return err
	}

	// The entity has sufficient stake to qualify for the additional role,
	// update the accumulated roles.
	acc.perEntityStake[mk] = kinds

	return nil
}

func newStakeAccumulator(appState *abci.ApplicationState, ctx *abci.Context, unsafeBypass bool) (*stakeAccumulator, error) {
	snapshot, err := stakingapp.NewSnapshot(appState, ctx)
	if err != nil {
		return nil, err
	}

	return &stakeAccumulator{
		snapshot:       snapshot,
		perEntityStake: make(map[signature.MapKey][]staking.ThresholdKind),
		unsafeBypass:   unsafeBypass,
	}, nil
}

type schedulerApplication struct {
	logger *logging.Logger
	state  *abci.ApplicationState

	timeSource ticker.Backend

	cfg *scheduler.Config
}

func (app *schedulerApplication) Name() string {
	return AppName
}

func (app *schedulerApplication) TransactionTag() byte {
	return TransactionTag
}

func (app *schedulerApplication) Blessed() bool {
	return false
}

func (app *schedulerApplication) GetState(height int64) (interface{}, error) {
	return newImmutableState(app.state, height)
}

func (app *schedulerApplication) OnRegister(state *abci.ApplicationState, queryRouter abci.QueryRouter) {
	app.state = state

	// Register query handlers.
	queryRouter.AddRoute(QueryAllCommittees, nil, app.queryAllCommittees)
	queryRouter.AddRoute(QueryKindsCommittees, []scheduler.CommitteeKind{}, app.queryKindsCommittees)
	queryRouter.AddRoute(QueryGetEpoch, nil, app.queryGetEpoch)
}

func (app *schedulerApplication) OnCleanup() {}

func (app *schedulerApplication) SetOption(req types.RequestSetOption) types.ResponseSetOption {
	return types.ResponseSetOption{}
}

func (app *schedulerApplication) CheckTx(ctx *abci.Context, tx []byte) error {
	return errUnexpectedTransaction
}

func (app *schedulerApplication) ForeignCheckTx(ctx *abci.Context, other abci.Application, tx []byte) error {
	return nil
}

func (app *schedulerApplication) InitChain(ctx *abci.Context, req types.RequestInitChain, doc *genesis.Document) error {
	return nil
}

func (app *schedulerApplication) BeginBlock(ctx *abci.Context, request types.RequestBeginBlock) error {
	// TODO: We'll later have this for each type of committee.
	if changed, epoch := app.state.EpochChanged(app.timeSource); changed {
		beaconState := beaconapp.NewMutableState(app.state.DeliverTxTree())
		beacon, err := beaconState.GetBeacon()
		if err != nil {
			return errors.Wrap(err, "tendermint/scheduler: couldn't get beacon")
		}

		regState := registryapp.NewMutableState(app.state.DeliverTxTree())
		runtimes, err := regState.GetRuntimes()
		if err != nil {
			return errors.Wrap(err, "tendermint/scheduler: couldn't get runtimes")
		}
		nodes, err := regState.GetNodes()
		if err != nil {
			return errors.Wrap(err, "tendermint/scheduler: couldn't get nodes")
		}

		entityStake, err := newStakeAccumulator(app.state, ctx, app.cfg.DebugBypassStake)
		if err != nil {
			return errors.Wrap(err, "tendermint/scheduler: couldn't get stake snapshot")
		}
		kinds := []scheduler.CommitteeKind{scheduler.KindCompute, scheduler.KindStorage, scheduler.KindTransactionScheduler, scheduler.KindMerge}
		for _, kind := range kinds {
			if err := app.electAll(ctx, request, epoch, beacon, entityStake, runtimes, nodes, kind); err != nil {
				return errors.Wrap(err, fmt.Sprintf("tendermint/scheduler: couldn't elect %s committees", kind))
			}
		}
		ctx.EmitTag([]byte(app.Name()), api.TagAppNameValue)
		ctx.EmitTag(TagElected, cbor.Marshal(kinds))

		// Set the debonding period start time for all of the entities that
		// have nodes scheduled.
		if !app.cfg.DebugBypassStake {
			stakingState := stakingapp.NewMutableState(app.state.DeliverTxTree())
			now := uint64(ctx.Now().Unix())

			toUpdate := make([]signature.PublicKey, 0, len(entityStake.perEntityStake))
			for k, v := range entityStake.perEntityStake {
				if len(v) == 0 {
					continue
				}

				var id signature.PublicKey
				_ = id.UnmarshalBinary(k[:])
				toUpdate = append(toUpdate, id)
			}

			sort.Slice(toUpdate, func(i, j int) bool {
				return bytes.Compare(toUpdate[i], toUpdate[j]) == -1
			})

			for _, v := range toUpdate {
				stakingState.SetDebondStartTime(v, now)
			}

		}

		var kindNames []string
		for _, kind := range kinds {
			kindNames = append(kindNames, kind.String())
		}
		var runtimeIDs []string
		for _, rt := range runtimes {
			runtimeIDs = append(runtimeIDs, rt.ID.String())
		}
		app.logger.Debug("finished electing committees",
			"epoch", epoch,
			"kinds", kindNames,
			"runtimes", runtimeIDs,
		)
	}
	return nil
}

func (app *schedulerApplication) DeliverTx(ctx *abci.Context, tx []byte) error {
	return errUnexpectedTransaction
}

func (app *schedulerApplication) ForeignDeliverTx(ctx *abci.Context, other abci.Application, tx []byte) error {
	return nil
}

func (app *schedulerApplication) EndBlock(req types.RequestEndBlock) (types.ResponseEndBlock, error) {
	return types.ResponseEndBlock{}, nil
}

func (app *schedulerApplication) FireTimer(ctx *abci.Context, t *abci.Timer) {}

func (app *schedulerApplication) queryAllCommittees(s interface{}, r interface{}) ([]byte, error) {
	state := s.(*immutableState)
	committees, err := state.getAllCommittees()
	if err != nil {
		return nil, err
	}
	return cbor.Marshal(committees), nil
}

func (app *schedulerApplication) queryGetEpoch(s interface{}, r interface{}) ([]byte, error) {
	epoch, err := app.state.GetEpoch(app.timeSource)
	if err != nil {
		return nil, err
	}
	return cbor.Marshal(epoch), nil
}

func (app *schedulerApplication) queryKindsCommittees(s interface{}, r interface{}) ([]byte, error) {
	state := s.(*immutableState)
	request := *r.(*[]scheduler.CommitteeKind)
	committees, err := state.getKindsCommittees(request)
	if err != nil {
		return nil, err
	}
	return cbor.Marshal(committees), nil
}

func (app *schedulerApplication) isSuitableComputeWorker(n *node.Node, rt *registry.Runtime, ts time.Time) bool {
	if !n.HasRoles(node.RoleComputeWorker) {
		return false
	}
	for _, nrt := range n.Runtimes {
		if !nrt.ID.Equal(rt.ID) {
			continue
		}
		switch rt.TEEHardware {
		case node.TEEHardwareInvalid:
			if nrt.Capabilities.TEE != nil {
				return false
			}
			return true
		default:
			if nrt.Capabilities.TEE == nil {
				return false
			}
			if nrt.Capabilities.TEE.Hardware != rt.TEEHardware {
				return false
			}
			if err := nrt.Capabilities.TEE.Verify(ts); err != nil {
				app.logger.Warn("failed to verify node TEE attestaion",
					"err", err,
					"node", n,
					"time_stamp", ts,
					"runtime", rt.ID,
				)
				return false
			}
			return true
		}
	}
	return false
}

func (app *schedulerApplication) isSuitableStorageWorker(n *node.Node) bool {
	return n.HasRoles(node.RoleStorageWorker)
}

func (app *schedulerApplication) isSuitableTransactionScheduler(n *node.Node, rt *registry.Runtime) bool {
	if !n.HasRoles(node.RoleTransactionScheduler) {
		return false
	}
	for _, nrt := range n.Runtimes {
		if !nrt.ID.Equal(rt.ID) {
			continue
		}
		return true
	}
	return false
}

func (app *schedulerApplication) isSuitableMergeWorker(n *node.Node, rt *registry.Runtime) bool {
	if !n.HasRoles(node.RoleMergeWorker) {
		return false
	}
	for _, nrt := range n.Runtimes {
		if !nrt.ID.Equal(rt.ID) {
			continue
		}
		return true
	}
	return false
}

// Operates on consensus connection.
// Return error if node should crash.
// For non-fatal problems, save a problem condition to the state and return successfully.
func (app *schedulerApplication) elect(ctx *abci.Context, request types.RequestBeginBlock, epoch scheduler.EpochTime, beacon []byte, entityStake *stakeAccumulator, rt *registry.Runtime, nodes []*node.Node, kind scheduler.CommitteeKind) error {
	// Only generic compute runtimes need to elect all the committees.
	if !rt.IsCompute() && kind != scheduler.KindCompute {
		return nil
	}

	var nodeList []*node.Node
	var workerSize, backupSize int
	var rngCtx []byte
	switch kind {
	case scheduler.KindCompute:
		for _, n := range nodes {
			if err := entityStake.checkAndAccumulate(n.EntityID, staking.KindCompute); err != nil {
				continue
			}
			if app.isSuitableComputeWorker(n, rt, request.Header.Time) {
				nodeList = append(nodeList, n)
			}
		}
		workerSize = int(rt.ReplicaGroupSize)
		backupSize = int(rt.ReplicaGroupBackupSize)
		rngCtx = rngContextCompute
	case scheduler.KindStorage:
		for _, n := range nodes {
			if err := entityStake.checkAndAccumulate(n.EntityID, staking.KindStorage); err != nil {
				continue
			}
			if app.isSuitableStorageWorker(n) {
				nodeList = append(nodeList, n)
			}
		}
		workerSize = int(rt.StorageGroupSize)
		backupSize = 0
		rngCtx = rngContextStorage
	case scheduler.KindTransactionScheduler:
		for _, n := range nodes {
			if err := entityStake.checkAndAccumulate(n.EntityID, staking.KindCompute); err != nil {
				continue
			}
			if app.isSuitableTransactionScheduler(n, rt) {
				nodeList = append(nodeList, n)
			}
		}
		workerSize = int(rt.TransactionSchedulerGroupSize)
		backupSize = 0
		rngCtx = rngContextTransactionScheduler
	case scheduler.KindMerge:
		for _, n := range nodes {
			if err := entityStake.checkAndAccumulate(n.EntityID, staking.KindCompute); err != nil {
				continue
			}
			if app.isSuitableMergeWorker(n, rt) {
				nodeList = append(nodeList, n)
			}
		}
		// TODO: Allow independent group sizes.
		workerSize = int(rt.ReplicaGroupSize)
		backupSize = int(rt.ReplicaGroupBackupSize)
		rngCtx = rngContextMerge
	default:
		// This is a problem with this code. Don't try to handle it at runtime.
		return fmt.Errorf("tendermint/scheduler: invalid committee type: %v", kind)
	}
	nrNodes := len(nodeList)

	if workerSize == 0 {
		app.logger.Error("empty committee not allowed",
			"kind", kind,
			"runtime_id", rt.ID,
		)
		NewMutableState(app.state.DeliverTxTree()).dropCommittee(kind, rt.ID)
		return nil
	}
	if (workerSize + backupSize) > nrNodes {
		app.logger.Error("committee size exceeds available nodes",
			"kind", kind,
			"runtime_id", rt.ID,
			"worker_size", workerSize,
			"backup_size", backupSize,
			"nr_nodes", nrNodes,
		)
		NewMutableState(app.state.DeliverTxTree()).dropCommittee(kind, rt.ID)
		return nil
	}

	drbg, err := drbg.New(crypto.SHA512, beacon, rt.ID[:], rngCtx)
	if err != nil {
		return fmt.Errorf("tendermint/scheduler: couldn't instantiate DRBG: %s", err.Error())
	}
	rngSrc := mathrand.New(drbg)
	rng := rand.New(rngSrc)

	idxs := rng.Perm(nrNodes)

	var members []*scheduler.CommitteeNode

	for i := 0; i < (workerSize + backupSize); i++ {
		var role scheduler.Role
		switch {
		case i == 0:
			if kind.NeedsLeader() {
				role = scheduler.Leader
			} else {
				role = scheduler.Worker
			}
		case i >= workerSize:
			role = scheduler.BackupWorker
		default:
			role = scheduler.Worker
		}
		members = append(members, &scheduler.CommitteeNode{
			Role:      role,
			PublicKey: nodeList[idxs[i]].ID,
		})
	}

	NewMutableState(app.state.DeliverTxTree()).putCommittee(&scheduler.Committee{
		Kind:      kind,
		RuntimeID: rt.ID,
		Members:   members,
		ValidFor:  epoch,
	})
	return nil
}

// Operates on consensus connection.
func (app *schedulerApplication) electAll(ctx *abci.Context, request types.RequestBeginBlock, epoch scheduler.EpochTime, beacon []byte, entityStake *stakeAccumulator, runtimes []*registry.Runtime, nodes []*node.Node, kind scheduler.CommitteeKind) error {
	for _, runtime := range runtimes {
		if err := app.elect(ctx, request, epoch, beacon, entityStake, runtime, nodes, kind); err != nil {
			return err
		}
	}
	return nil
}

// New constructs a new scheduler application instance.
func New(
	timeSource ticker.Backend,
	cfg *scheduler.Config,
) abci.Application {
	return &schedulerApplication{
		logger:     logging.GetLogger("tendermint/scheduler"),
		timeSource: timeSource,
		cfg:        cfg,
	}
}
