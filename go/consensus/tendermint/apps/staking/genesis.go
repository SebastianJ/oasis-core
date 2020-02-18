package staking

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/tendermint/tendermint/abci/types"

	"github.com/oasislabs/oasis-core/go/common/crypto/signature"
	"github.com/oasislabs/oasis-core/go/common/quantity"
	abciAPI "github.com/oasislabs/oasis-core/go/consensus/tendermint/api"
	stakingState "github.com/oasislabs/oasis-core/go/consensus/tendermint/apps/staking/state"
	genesis "github.com/oasislabs/oasis-core/go/genesis/api"
	staking "github.com/oasislabs/oasis-core/go/staking/api"
)

func (app *stakingApplication) initParameters(ctx *abciAPI.Context, state *stakingState.MutableState, st *staking.Genesis) error {
	if err := st.Parameters.SanityCheck(); err != nil {
		return fmt.Errorf("staking/tendermint: sanity check failed: %w", err)
	}

	if err := state.SetConsensusParameters(ctx, &st.Parameters); err != nil {
		return fmt.Errorf("staking/tendermint: failed to set consensus parameters: %w", err)
	}
	return nil
}

func (app *stakingApplication) initCommonPool(ctx *abciAPI.Context, st *staking.Genesis, totalSupply *quantity.Quantity) error {
	if !st.CommonPool.IsValid() {
		return errors.New("staking/tendermint: invalid genesis state CommonPool")
	}
	if err := totalSupply.Add(&st.CommonPool); err != nil {
		ctx.Logger().Error("InitChain: failed to add common pool",
			"err", err,
		)
		return errors.Wrap(err, "staking/tendermint: failed to add common pool")
	}

	return nil
}

func (app *stakingApplication) initLedger(
	ctx *abciAPI.Context,
	state *stakingState.MutableState,
	st *staking.Genesis,
	totalSupply *quantity.Quantity,
) error {
	for id, v := range st.Ledger {
		if !v.General.Balance.IsValid() {
			ctx.Logger().Error("InitChain: invalid genesis general balance",
				"id", id,
				"general_balance", v.General.Balance,
			)
			return errors.New("staking/tendermint: invalid genesis general balance")
		}
		if !v.Escrow.Active.Balance.IsValid() {
			ctx.Logger().Error("InitChain: invalid genesis active escrow balance",
				"id", id,
				"escrow_balance", v.Escrow.Active.Balance,
			)
			return errors.New("staking/tendermint: invalid genesis active escrow balance")
		}
		if !v.Escrow.Debonding.Balance.IsValid() {
			ctx.Logger().Error("InitChain: invalid genesis debonding escrow balance",
				"id", id,
				"debonding_balance", v.Escrow.Debonding.Balance,
			)
			return errors.New("staking/tendermint: invalid genesis debonding escrow balance")
		}

		// Make sure that the stake accumulator is empty as otherwise it could be inconsistent with
		// what is registered in the genesis block.
		if len(v.Escrow.StakeAccumulator.Claims) > 0 {
			ctx.Logger().Error("InitChain: non-empty stake accumulator",
				"id", id,
			)
			return errors.New("staking/tendermint: non-empty stake accumulator in genesis")
		}

		if err := totalSupply.Add(&v.General.Balance); err != nil {
			ctx.Logger().Error("InitChain: failed to add general balance",
				"err", err,
			)
			return errors.Wrap(err, "staking/tendermint: failed to add general balance")
		}
		if err := totalSupply.Add(&v.Escrow.Active.Balance); err != nil {
			ctx.Logger().Error("InitChain: failed to add active escrow balance",
				"err", err,
			)
			return errors.Wrap(err, "staking/tendermint: failed to add active escrow balance")
		}
		if err := totalSupply.Add(&v.Escrow.Debonding.Balance); err != nil {
			ctx.Logger().Error("InitChain: failed to add debonding escrow balance",
				"err", err,
			)
			return errors.Wrap(err, "staking/tendermint: failed to add debonding escrow balance")
		}

		if err := state.SetAccount(ctx, id, v); err != nil {
			return fmt.Errorf("staking/tendermint: failed to set account: %w", err)
		}
	}
	return nil
}

func (app *stakingApplication) initTotalSupply(
	ctx *abciAPI.Context,
	state *stakingState.MutableState,
	st *staking.Genesis,
	totalSupply *quantity.Quantity,
) error {
	if totalSupply.Cmp(&st.TotalSupply) != 0 {
		ctx.Logger().Error("InitChain: total supply mismatch",
			"expected", st.TotalSupply,
			"actual", totalSupply,
		)
		return fmt.Errorf("total supply mismatch")
	}

	if err := state.SetCommonPool(ctx, &st.CommonPool); err != nil {
		return fmt.Errorf("failed to set common pool: %w", err)
	}
	if err := state.SetTotalSupply(ctx, totalSupply); err != nil {
		return fmt.Errorf("failed to set total supply: %w", err)
	}
	return nil
}

func (app *stakingApplication) initDelegations(ctx *abciAPI.Context, state *stakingState.MutableState, st *staking.Genesis) error {
	for escrowID, delegations := range st.Delegations {
		delegationShares := quantity.NewQuantity()
		for delegatorID, delegation := range delegations {
			if err := delegationShares.Add(&delegation.Shares); err != nil {
				ctx.Logger().Error("InitChain: failed to add delegation shares",
					"err", err,
				)
				return errors.Wrap(err, "staking/tendermint: failed to add delegation shares")
			}
			if err := state.SetDelegation(ctx, delegatorID, escrowID, delegation); err != nil {
				return fmt.Errorf("failed to set delegation: %w", err)
			}
		}

		acc, err := state.Account(ctx, escrowID)
		if err != nil {
			return fmt.Errorf("failed to fetch account: %w", err)
		}
		if acc.Escrow.Active.TotalShares.Cmp(delegationShares) != 0 {
			ctx.Logger().Error("InitChain: total shares mismatch",
				"escrow_id", escrowID,
				"expected", acc.Escrow.Active.TotalShares,
				"actual", delegationShares,
			)
			return errors.New("staking/tendermint: total shares mismatch")
		}
	}
	return nil
}

func (app *stakingApplication) initDebondingDelegations(ctx *abciAPI.Context, state *stakingState.MutableState, st *staking.Genesis) error {
	for escrowID, delegators := range st.DebondingDelegations {
		debondingShares := quantity.NewQuantity()
		for delegatorID, delegations := range delegators {
			for idx, delegation := range delegations {
				if err := debondingShares.Add(&delegation.Shares); err != nil {
					ctx.Logger().Error("InitChain: failed to add debonding delegation shares",
						"err", err,
					)
					return errors.Wrap(err, "staking/tendermint: failed to add debonding delegation shares")
				}

				if err := state.SetDebondingDelegation(ctx, delegatorID, escrowID, uint64(idx), delegation); err != nil {
					return fmt.Errorf("failed to set debonding delegation: %w", err)
				}
			}
		}

		acc, err := state.Account(ctx, escrowID)
		if err != nil {
			return fmt.Errorf("failed to fetch account: %w", err)
		}
		if acc.Escrow.Debonding.TotalShares.Cmp(debondingShares) != 0 {
			ctx.Logger().Error("InitChain: debonding shares mismatch",
				"escrow_id", escrowID,
				"expected", acc.Escrow.Debonding.TotalShares,
				"actual", debondingShares,
			)
			return errors.New("staking/tendermint: debonding shares mismatch")
		}
	}
	return nil
}

// InitChain initializes the chain from genesis.
func (app *stakingApplication) InitChain(ctx *abciAPI.Context, request types.RequestInitChain, doc *genesis.Document) error {
	st := &doc.Staking

	var (
		state       = stakingState.NewMutableState(ctx.State())
		totalSupply quantity.Quantity
	)

	if err := app.initParameters(ctx, state, st); err != nil {
		return err
	}

	if err := app.initCommonPool(ctx, st, &totalSupply); err != nil {
		return err
	}

	if err := app.initLedger(ctx, state, st, &totalSupply); err != nil {
		return err
	}

	if err := app.initTotalSupply(ctx, state, st, &totalSupply); err != nil {
		return err
	}

	if err := app.initDelegations(ctx, state, st); err != nil {
		return err
	}

	if err := app.initDebondingDelegations(ctx, state, st); err != nil {
		return err
	}

	ctx.Logger().Debug("InitChain: allocations complete",
		"common_pool", st.CommonPool,
		"total_supply", totalSupply,
	)

	return nil
}

// Genesis exports current state in genesis format.
func (sq *stakingQuerier) Genesis(ctx context.Context) (*staking.Genesis, error) {
	totalSupply, err := sq.state.TotalSupply(ctx)
	if err != nil {
		return nil, err
	}

	commonPool, err := sq.state.CommonPool(ctx)
	if err != nil {
		return nil, err
	}

	accounts, err := sq.state.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	ledger := make(map[signature.PublicKey]*staking.Account)
	for _, acctID := range accounts {
		var acct *staking.Account
		acct, err = sq.state.Account(ctx, acctID)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch account: %w", err)
		}
		// Make sure that export resets the stake accumulator state as that should be re-initialized
		// during genesis (a genesis document with non-empty stake accumulator is invalid).
		acct.Escrow.StakeAccumulator = staking.StakeAccumulator{}
		ledger[acctID] = acct
	}

	delegations, err := sq.state.Delegations(ctx)
	if err != nil {
		return nil, err
	}
	debondingDelegations, err := sq.state.DebondingDelegations(ctx)
	if err != nil {
		return nil, err
	}

	params, err := sq.state.ConsensusParameters(ctx)
	if err != nil {
		return nil, err
	}

	gen := staking.Genesis{
		Parameters:           *params,
		TotalSupply:          *totalSupply,
		CommonPool:           *commonPool,
		Ledger:               ledger,
		Delegations:          delegations,
		DebondingDelegations: debondingDelegations,
	}
	return &gen, nil
}
