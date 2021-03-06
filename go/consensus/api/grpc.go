package api

import (
	"context"

	"google.golang.org/grpc"

	cmnGrpc "github.com/oasislabs/oasis-core/go/common/grpc"
	"github.com/oasislabs/oasis-core/go/common/pubsub"
	"github.com/oasislabs/oasis-core/go/consensus/api/transaction"
	epochtime "github.com/oasislabs/oasis-core/go/epochtime/api"
	genesis "github.com/oasislabs/oasis-core/go/genesis/api"
)

var (
	// serviceName is the gRPC service name.
	serviceName = cmnGrpc.NewServiceName("Consensus")

	// methodSubmitTx is the SubmitTx method.
	methodSubmitTx = serviceName.NewMethod("SubmitTx", transaction.SignedTransaction{})
	// methodStateToGenesis is the StateToGenesis method.
	methodStateToGenesis = serviceName.NewMethod("StateToGenesis", int64(0))
	// methodEstimateGas is the EstimateGas method.
	methodEstimateGas = serviceName.NewMethod("EstimateGas", &EstimateGasRequest{})
	// methodGetSignerNonce is a GetSignerNonce method.
	methodGetSignerNonce = serviceName.NewMethod("GetSignerNonce", &GetSignerNonceRequest{})
	// methodGetEpoch is the GetEpoch method.
	methodGetEpoch = serviceName.NewMethod("GetEpoch", int64(0))
	// methodWaitEpoch is the WaitEpoch method.
	methodWaitEpoch = serviceName.NewMethod("WaitEpoch", epochtime.EpochTime(0))
	// methodGetBlock is the GetBlock method.
	methodGetBlock = serviceName.NewMethod("GetBlock", int64(0))
	// methodGetTransactions is the GetTransactions method.
	methodGetTransactions = serviceName.NewMethod("GetTransactions", int64(0))

	// methodWatchBlocks is the WatchBlocks method.
	methodWatchBlocks = serviceName.NewMethod("WatchBlocks", nil)

	// serviceDesc is the gRPC service descriptor.
	serviceDesc = grpc.ServiceDesc{
		ServiceName: string(serviceName),
		HandlerType: (*Backend)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: methodSubmitTx.ShortName(),
				Handler:    handlerSubmitTx,
			},
			{
				MethodName: methodStateToGenesis.ShortName(),
				Handler:    handlerStateToGenesis,
			},
			{
				MethodName: methodEstimateGas.ShortName(),
				Handler:    handlerEstimateGas,
			},
			{
				MethodName: methodGetSignerNonce.ShortName(),
				Handler:    handlerGetSignerNonce,
			},
			{
				MethodName: methodGetEpoch.ShortName(),
				Handler:    handlerGetEpoch,
			},
			{
				MethodName: methodWaitEpoch.ShortName(),
				Handler:    handlerWaitEpoch,
			},
			{
				MethodName: methodGetBlock.ShortName(),
				Handler:    handlerGetBlock,
			},
			{
				MethodName: methodGetTransactions.ShortName(),
				Handler:    handlerGetTransactions,
			},
		},
		Streams: []grpc.StreamDesc{
			{
				StreamName:    methodWatchBlocks.ShortName(),
				Handler:       handlerWatchBlocks,
				ServerStreams: true,
			},
		},
	}
)

func handlerSubmitTx( // nolint: golint
	srv interface{},
	ctx context.Context,
	dec func(interface{}) error,
	interceptor grpc.UnaryServerInterceptor,
) (interface{}, error) {
	rq := new(transaction.SignedTransaction)
	if err := dec(rq); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return nil, srv.(Backend).SubmitTx(ctx, rq)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: methodSubmitTx.FullName(),
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return nil, srv.(Backend).SubmitTx(ctx, req.(*transaction.SignedTransaction))
	}
	return interceptor(ctx, rq, info, handler)
}

func handlerStateToGenesis( // nolint: golint
	srv interface{},
	ctx context.Context,
	dec func(interface{}) error,
	interceptor grpc.UnaryServerInterceptor,
) (interface{}, error) {
	var height int64
	if err := dec(&height); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(Backend).StateToGenesis(ctx, height)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: methodStateToGenesis.FullName(),
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(Backend).StateToGenesis(ctx, req.(int64))
	}
	return interceptor(ctx, height, info, handler)
}

func handlerEstimateGas( // nolint: golint
	srv interface{},
	ctx context.Context,
	dec func(interface{}) error,
	interceptor grpc.UnaryServerInterceptor,
) (interface{}, error) {
	rq := new(EstimateGasRequest)
	if err := dec(rq); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(Backend).EstimateGas(ctx, rq)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: methodEstimateGas.FullName(),
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(Backend).EstimateGas(ctx, req.(*EstimateGasRequest))
	}
	return interceptor(ctx, rq, info, handler)
}

func handlerGetSignerNonce( // nolint: golint
	srv interface{},
	ctx context.Context,
	dec func(interface{}) error,
	interceptor grpc.UnaryServerInterceptor,
) (interface{}, error) {
	rq := new(GetSignerNonceRequest)
	if err := dec(rq); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(Backend).GetSignerNonce(ctx, rq)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: methodGetSignerNonce.FullName(),
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(Backend).GetSignerNonce(ctx, req.(*GetSignerNonceRequest))
	}
	return interceptor(ctx, rq, info, handler)
}

func handlerGetEpoch( // nolint: golint
	srv interface{},
	ctx context.Context,
	dec func(interface{}) error,
	interceptor grpc.UnaryServerInterceptor,
) (interface{}, error) {
	var height int64
	if err := dec(&height); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(Backend).GetEpoch(ctx, height)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: methodGetEpoch.FullName(),
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(Backend).GetEpoch(ctx, req.(int64))
	}
	return interceptor(ctx, height, info, handler)
}

func handlerWaitEpoch( // nolint: golint
	srv interface{},
	ctx context.Context,
	dec func(interface{}) error,
	interceptor grpc.UnaryServerInterceptor,
) (interface{}, error) {
	var epoch epochtime.EpochTime
	if err := dec(&epoch); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return nil, srv.(Backend).WaitEpoch(ctx, epoch)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: methodWaitEpoch.FullName(),
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return nil, srv.(Backend).WaitEpoch(ctx, req.(epochtime.EpochTime))
	}
	return interceptor(ctx, epoch, info, handler)
}

func handlerGetBlock( // nolint: golint
	srv interface{},
	ctx context.Context,
	dec func(interface{}) error,
	interceptor grpc.UnaryServerInterceptor,
) (interface{}, error) {
	var height int64
	if err := dec(&height); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(Backend).GetBlock(ctx, height)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: methodGetBlock.FullName(),
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(Backend).GetBlock(ctx, req.(int64))
	}
	return interceptor(ctx, height, info, handler)
}

func handlerGetTransactions( // nolint: golint
	srv interface{},
	ctx context.Context,
	dec func(interface{}) error,
	interceptor grpc.UnaryServerInterceptor,
) (interface{}, error) {
	var height int64
	if err := dec(&height); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(Backend).GetTransactions(ctx, height)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: methodGetTransactions.FullName(),
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(Backend).GetTransactions(ctx, req.(int64))
	}
	return interceptor(ctx, height, info, handler)
}

func handlerWatchBlocks(srv interface{}, stream grpc.ServerStream) error {
	if err := stream.RecvMsg(nil); err != nil {
		return err
	}

	ctx := stream.Context()
	ch, sub, err := srv.(Backend).WatchBlocks(ctx)
	if err != nil {
		return err
	}
	defer sub.Close()

	for {
		select {
		case blk, ok := <-ch:
			if !ok {
				return nil
			}

			if err := stream.SendMsg(blk); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// RegisterService registers a new consensus backend service with the
// given gRPC server.
func RegisterService(server *grpc.Server, service Backend) {
	server.RegisterService(&serviceDesc, service)
}

type consensusClient struct {
	conn *grpc.ClientConn
}

func (c *consensusClient) SubmitTx(ctx context.Context, tx *transaction.SignedTransaction) error {
	return c.conn.Invoke(ctx, methodSubmitTx.FullName(), tx, nil)
}

func (c *consensusClient) StateToGenesis(ctx context.Context, height int64) (*genesis.Document, error) {
	var rsp genesis.Document
	if err := c.conn.Invoke(ctx, methodStateToGenesis.FullName(), height, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (c *consensusClient) EstimateGas(ctx context.Context, req *EstimateGasRequest) (transaction.Gas, error) {
	var gas transaction.Gas
	if err := c.conn.Invoke(ctx, methodEstimateGas.FullName(), req, &gas); err != nil {
		return transaction.Gas(0), err
	}
	return gas, nil
}

func (c *consensusClient) GetSignerNonce(ctx context.Context, req *GetSignerNonceRequest) (uint64, error) {
	var nonce uint64
	if err := c.conn.Invoke(ctx, methodGetSignerNonce.FullName(), req, &nonce); err != nil {
		return nonce, err
	}
	return nonce, nil
}

func (c *consensusClient) WaitEpoch(ctx context.Context, epoch epochtime.EpochTime) error {
	return c.conn.Invoke(ctx, methodWaitEpoch.FullName(), epoch, nil)
}

func (c *consensusClient) GetEpoch(ctx context.Context, height int64) (epochtime.EpochTime, error) {
	var epoch epochtime.EpochTime
	if err := c.conn.Invoke(ctx, methodGetEpoch.FullName(), height, &epoch); err != nil {
		return epochtime.EpochTime(0), err
	}
	return epoch, nil
}

func (c *consensusClient) GetBlock(ctx context.Context, height int64) (*Block, error) {
	var rsp Block
	if err := c.conn.Invoke(ctx, methodGetBlock.FullName(), height, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (c *consensusClient) GetTransactions(ctx context.Context, height int64) ([][]byte, error) {
	var rsp [][]byte
	if err := c.conn.Invoke(ctx, methodGetTransactions.FullName(), height, &rsp); err != nil {
		return nil, err
	}
	return rsp, nil
}

func (c *consensusClient) WatchBlocks(ctx context.Context) (<-chan *Block, pubsub.ClosableSubscription, error) {
	ctx, sub := pubsub.NewContextSubscription(ctx)

	stream, err := c.conn.NewStream(ctx, &serviceDesc.Streams[0], methodWatchBlocks.FullName())
	if err != nil {
		return nil, nil, err
	}
	if err = stream.SendMsg(nil); err != nil {
		return nil, nil, err
	}
	if err = stream.CloseSend(); err != nil {
		return nil, nil, err
	}

	ch := make(chan *Block)
	go func() {
		defer close(ch)

		for {
			var blk Block
			if serr := stream.RecvMsg(&blk); serr != nil {
				return
			}

			select {
			case ch <- &blk:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, sub, nil
}

// NewConsensusClient creates a new gRPC consensus client service.
func NewConsensusClient(c *grpc.ClientConn) ClientBackend {
	return &consensusClient{c}
}
