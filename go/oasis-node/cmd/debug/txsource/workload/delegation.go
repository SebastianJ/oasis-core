package workload

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"google.golang.org/grpc"

	"github.com/oasislabs/oasis-core/go/common/crypto/signature"
	memorySigner "github.com/oasislabs/oasis-core/go/common/crypto/signature/signers/memory"
	"github.com/oasislabs/oasis-core/go/common/logging"
	consensus "github.com/oasislabs/oasis-core/go/consensus/api"
	"github.com/oasislabs/oasis-core/go/consensus/api/transaction"
	runtimeClient "github.com/oasislabs/oasis-core/go/runtime/client/api"
	staking "github.com/oasislabs/oasis-core/go/staking/api"
)

const (
	// NameDelegation is the name of the delegation workload.
	NameDelegation = "delegation"

	// TODO: get from genesis params.
	delegationDebondingEpochPeriod = 1

	delegationNumAccounts = 10
	delegateAmount        = 100
)

var delegationLogger = logging.GetLogger("cmd/txsource/workload/delegation")

type delegation struct{}

func (delegation) Run(gracefulExit context.Context, rng *rand.Rand, conn *grpc.ClientConn, cnsc consensus.ClientBackend, rtc runtimeClient.RuntimeClient, fundingAccount signature.Signer) error {
	var err error
	ctx := context.Background()

	fac := memorySigner.NewFactory()
	accounts := make([]struct {
		signer              signature.Signer
		reckonedNonce       uint64
		delegatedTo         signature.PublicKey
		debondingUntilEpoch uint64
	}, delegationNumAccounts)

	for i := range accounts {
		accounts[i].signer, err = fac.Generate(signature.SignerEntity, rng)
		if err != nil {
			return fmt.Errorf("memory signer factory Generate account %d: %w", i, err)
		}

		// Fund the account with delegation amount.
		// Funds for fee's will be transferred before making transactions.
		if err = transferFunds(ctx, delegationLogger, cnsc, fundingAccount, accounts[i].signer.Public(), delegateAmount); err != nil {
			return fmt.Errorf("account funding failure: %w", err)
		}
	}

	client := staking.NewStakingClient(conn)

MAIN:
	for {
		// There are multiple loop branches so check for termination here.
		select {
		case <-time.After(1 * time.Second):
		case <-gracefulExit.Done():
			delegationLogger.Debug("time's up")
			return nil
		}

		// Get current epoch.
		epoch, err := cnsc.GetEpoch(ctx, consensus.HeightLatest)
		if err != nil {
			return fmt.Errorf("GetEpoch: %w", err)
		}

		var signedTx *transaction.SignedTransaction
		var gas transaction.Gas
		// TODO: also do 'ChangeComssionSchedule` transactions as part of this
		// workflow?
		switch rng.Intn(2) {
		case 0:
			delegationLogger.Debug("escrow tx flow")

			// Select an account that has no active delegations nor debonding
			// funds.
			perm := rng.Perm(delegationNumAccounts)
			fromPermIdx := -1
			var empty signature.PublicKey
			for i := range accounts {
				if accounts[perm[i]].delegatedTo == empty && accounts[perm[i]].debondingUntilEpoch < uint64(epoch) {
					fromPermIdx = i
					break
				}
			}
			if fromPermIdx == -1 {
				delegationLogger.Debug("all accounts already delegating or debonding, skipping delegation")
				continue MAIN
			}

			// Select an account to delegate to.
			// XXX: we could also cover the self-escrow case.
			toPermIdx := (fromPermIdx + 1) % delegationNumAccounts
			// Update local state.
			accounts[perm[fromPermIdx]].delegatedTo = accounts[perm[toPermIdx]].signer.Public()

			// Create escrow tx.
			escrow := &staking.Escrow{
				Account: accounts[perm[fromPermIdx]].delegatedTo,
			}
			if err = escrow.Tokens.FromInt64(delegateAmount); err != nil {
				return fmt.Errorf("escrow amount error: %w", err)
			}

			tx := staking.NewAddEscrowTx(accounts[perm[fromPermIdx]].reckonedNonce, &transaction.Fee{}, escrow)
			accounts[perm[fromPermIdx]].reckonedNonce++
			gas, err = cnsc.EstimateGas(ctx, &consensus.EstimateGasRequest{
				Caller:      accounts[perm[fromPermIdx]].signer.Public(),
				Transaction: tx,
			})
			if err != nil {
				return fmt.Errorf("failed to estimate gas: %w", err)
			}

			tx.Fee.Gas = gas
			feeAmount := int64(gas) * gasPrice
			if err = tx.Fee.Amount.FromInt64(feeAmount); err != nil {
				return fmt.Errorf("fee amount from int64: %w", err)
			}

			// Fund account to cover Escrow fees.
			// We only do one escrow per account at a time, so `delegateAmount`
			// funds (that are Escrowed) should already be in the balance.
			fundAmount := int64(gas) * gasPrice // transaction costs
			if err = transferFunds(ctx, delegationLogger, cnsc, fundingAccount, accounts[perm[fromPermIdx]].signer.Public(), fundAmount); err != nil {
				return fmt.Errorf("account funding failure: %w", err)
			}

			// Sign transaction.
			signedTx, err = transaction.Sign(accounts[perm[fromPermIdx]].signer, tx)
			if err != nil {
				return fmt.Errorf("transaction.Sign: %w", err)
			}
			delegationLogger.Debug("submitting escrow",
				"from", accounts[perm[fromPermIdx]].signer.Public(),
				"to", accounts[perm[fromPermIdx]].delegatedTo,
			)
		case 1:
			delegationLogger.Debug("reclaim escrow tx")

			// Select an account that has active delegation.
			perm := rng.Perm(delegationNumAccounts)
			fromPermIdx := -1
			var empty signature.PublicKey
			for i := range accounts {
				if accounts[perm[i]].delegatedTo != empty {
					fromPermIdx = i
					break
				}
			}
			if fromPermIdx == -1 {
				delegationLogger.Debug("no accounts delegating, skipping reclaim")
				continue MAIN
			}

			// Create ReclaimEscrow tx.
			reclaim := &staking.ReclaimEscrow{
				Account: accounts[perm[fromPermIdx]].delegatedTo,
			}
			// TODO: get shares by checking `Delegations` instead of hardocding tokens here.
			if err = reclaim.Shares.FromInt64(delegateAmount); err != nil {
				return fmt.Errorf("reclaim escrow amount error: %w", err)
			}

			tx := staking.NewReclaimEscrowTx(accounts[perm[fromPermIdx]].reckonedNonce, &transaction.Fee{}, reclaim)
			accounts[perm[fromPermIdx]].reckonedNonce++
			gas, err = cnsc.EstimateGas(ctx, &consensus.EstimateGasRequest{
				Caller:      accounts[perm[fromPermIdx]].signer.Public(),
				Transaction: tx,
			})
			if err != nil {
				return fmt.Errorf("failed to estimate gas: %w", err)
			}

			tx.Fee.Gas = gas
			feeAmount := int64(gas) * gasPrice
			if err = tx.Fee.Amount.FromInt64(feeAmount); err != nil {
				return fmt.Errorf("fee amount from int64: %w", err)
			}

			// Fund account to cover reclaim escrow fees.
			fundAmount := int64(gas) * gasPrice // transaction costs
			if err = transferFunds(ctx, delegationLogger, cnsc, fundingAccount, accounts[perm[fromPermIdx]].signer.Public(), fundAmount); err != nil {
				return fmt.Errorf("account funding failure: %w", err)
			}

			signedTx, err = transaction.Sign(accounts[perm[fromPermIdx]].signer, tx)
			if err != nil {
				return fmt.Errorf("transaction.Sign: %w", err)
			}

			delegationLogger.Debug("submitting reclaim escrow",
				"from", accounts[perm[fromPermIdx]].delegatedTo,
				"account", accounts[perm[fromPermIdx]].signer.Public(),
			)

			// Update local state.
			accounts[perm[fromPermIdx]].delegatedTo = empty
			// Add +1 to cover the case when epoch transition has/will happen
			// before the transaction is executed.
			accounts[perm[fromPermIdx]].debondingUntilEpoch = uint64(epoch) + delegationDebondingEpochPeriod + 1

		default:
			return fmt.Errorf("unimplemented delegation path")
		}

		// Submit transaction.
		if err = cnsc.SubmitTx(ctx, signedTx); err != nil {
			return fmt.Errorf("cnsc.SubmitTx: %w", err)
		}

		var account *staking.Account
		account, err = client.AccountInfo(ctx, &staking.OwnerQuery{
			Height: consensus.HeightLatest,
			Owner:  accounts[0].signer.Public(),
		})
		if err != nil {
			return fmt.Errorf("stakingClient.AccountInfo %s: %w", accounts[0].signer.Public(), err)
		}
		transferLogger.Debug("account info",
			"pub", accounts[0].signer.Public(),
			"info", account,
		)
	}
}
