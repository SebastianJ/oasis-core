package state

import (
	"context"
	"errors"

	"github.com/oasislabs/oasis-core/go/common/cbor"
	"github.com/oasislabs/oasis-core/go/common/keyformat"
	consensusGenesis "github.com/oasislabs/oasis-core/go/consensus/genesis"
	"github.com/oasislabs/oasis-core/go/consensus/tendermint/api"
	mkvs "github.com/oasislabs/oasis-core/go/storage/mkvs/urkel"
)

var (
	// parametersKeyFmt is the key format used for consensus parameters.
	//
	// Value is CBOR-serialized consensusGenesis.Parameters.
	parametersKeyFmt = keyformat.New(0xF1)
)

// ImmutableState is an immutable consensus backend state wrapper.
type ImmutableState struct {
	*api.ImmutableState
}

// NewImmutableState creates a new immutable consensus backend state wrapper.
func NewImmutableState(ctx context.Context, state api.ApplicationState, version int64) (*ImmutableState, error) {
	inner, err := api.NewImmutableState(ctx, state, version)
	if err != nil {
		return nil, err
	}

	return &ImmutableState{inner}, nil
}

// ConsensusParameters returns the consensus parameters.
func (s *ImmutableState) ConsensusParameters(ctx context.Context) (*consensusGenesis.Parameters, error) {
	raw, err := s.Tree.Get(ctx, parametersKeyFmt.Encode())
	if err != nil {
		return nil, api.UnavailableStateError(err)
	}
	if raw == nil {
		return nil, errors.New("state: expected consensus parameters to be present in app state")
	}

	var params consensusGenesis.Parameters
	if err := cbor.Unmarshal(raw, &params); err != nil {
		return nil, api.UnavailableStateError(err)
	}
	return &params, nil
}

// MutableState is a mutable consensus backend state wrapper.
type MutableState struct {
	*ImmutableState
}

// SetConsensusParameters sets the consensus parameters.
//
// NOTE: This method must only be called from InitChain/EndBlock contexts.
func (s *MutableState) SetConsensusParameters(ctx context.Context, params *consensusGenesis.Parameters) error {
	if err := s.CheckContextMode(ctx, []api.ContextMode{api.ContextInitChain, api.ContextEndBlock}); err != nil {
		return err
	}
	return s.Tree.Insert(ctx, parametersKeyFmt.Encode(), cbor.Marshal(params))
}

// NewMutableState creates a new mutable consensus backend state wrapper.
func NewMutableState(tree mkvs.KeyValueTree) *MutableState {
	return &MutableState{
		ImmutableState: &ImmutableState{
			&api.ImmutableState{Tree: tree},
		},
	}
}
