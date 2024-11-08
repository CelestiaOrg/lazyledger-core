package coregrpc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tendermint/tendermint/crypto/encoding"

	"github.com/tendermint/tendermint/proto/tendermint/crypto"

	"github.com/tendermint/tendermint/libs/rand"

	abci "github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/proto/tendermint/types"
	"github.com/tendermint/tendermint/rpc/core"
	rpctypes "github.com/tendermint/tendermint/rpc/jsonrpc/types"
	eventstypes "github.com/tendermint/tendermint/types"
)

type broadcastAPI struct {
}

func (bapi *broadcastAPI) Ping(ctx context.Context, req *RequestPing) (*ResponsePing, error) {
	// kvstore so we can check if the server is up
	return &ResponsePing{}, nil
}

func (bapi *broadcastAPI) BroadcastTx(ctx context.Context, req *RequestBroadcastTx) (*ResponseBroadcastTx, error) {
	// NOTE: there's no way to get client's remote address
	// see https://stackoverflow.com/questions/33684570/session-and-remote-ip-address-in-grpc-go
	res, err := core.BroadcastTxCommit(&rpctypes.Context{}, req.Tx)
	if err != nil {
		return nil, err
	}

	return &ResponseBroadcastTx{
		CheckTx: &abci.ResponseCheckTx{
			Code: res.CheckTx.Code,
			Data: res.CheckTx.Data,
			Log:  res.CheckTx.Log,
		},
		DeliverTx: &abci.ResponseDeliverTx{
			Code: res.DeliverTx.Code,
			Data: res.DeliverTx.Data,
			Log:  res.DeliverTx.Log,
		},
	}, nil
}

type BlockAPI struct {
	sync.Mutex
	heightListeners      map[chan NewHeightEvent]struct{}
	newBlockSubscription eventstypes.Subscription
}

func NewBlockAPI() *BlockAPI {
	return &BlockAPI{
		// TODO(rach-id) make 1000 configurable if there is a need for it
		heightListeners: make(map[chan NewHeightEvent]struct{}, 1000),
	}
}

func (blockAPI *BlockAPI) StartNewBlockEventListener(ctx context.Context) {
	env := core.GetEnvironment()
	if blockAPI.newBlockSubscription == nil {
		var err error
		blockAPI.newBlockSubscription, err = env.EventBus.Subscribe(
			ctx,
			fmt.Sprintf("new-block-grpc-subscription-%s", rand.Str(6)),
			eventstypes.EventQueryNewBlock,
			500,
		)
		if err != nil {
			env.Logger.Error("Failed to subscribe to new blocks", "err", err)
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-blockAPI.newBlockSubscription.Cancelled():
			env.Logger.Error("cancelled grpc subscription. retrying")
			ok, err := blockAPI.retryNewBlocksSubscription(ctx)
			if err != nil {
				blockAPI.closeAllListeners()
				return
			}
			if !ok {
				// this will happen when the context is done. we can stop here
				return
			}
		case event, ok := <-blockAPI.newBlockSubscription.Out():
			if !ok {
				env.Logger.Error("new blocks subscription closed. re-subscribing")
				ok, err := blockAPI.retryNewBlocksSubscription(ctx)
				if err != nil {
					blockAPI.closeAllListeners()
					return
				}
				if !ok {
					// this will happen when the context is done. we can stop here
					return
				}
				continue
			}
			newBlockEvent, ok := event.Events()[eventstypes.EventTypeKey]
			if !ok || len(newBlockEvent) == 0 || newBlockEvent[0] != eventstypes.EventNewBlock {
				continue
			}
			data, ok := event.Data().(eventstypes.EventDataNewBlock)
			if !ok {
				env.Logger.Debug("couldn't cast event data to new block")
				continue
			}
			blockAPI.broadcastToListeners(ctx, data.Block.Height, data.Block.Hash())
		}
	}

}

func (blockAPI *BlockAPI) retryNewBlocksSubscription(ctx context.Context) (bool, error) {
	env := core.GetEnvironment()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	blockAPI.Lock()
	defer blockAPI.Unlock()
	for i := 1; i < 6; i++ {
		select {
		case <-ctx.Done():
			return false, nil
		case <-ticker.C:
			var err error
			blockAPI.newBlockSubscription, err = env.EventBus.Subscribe(
				ctx,
				fmt.Sprintf("new-block-grpc-subscription-%s", rand.Str(6)),
				eventstypes.EventQueryNewBlock,
				500,
			)
			if err != nil {
				env.Logger.Error("Failed to subscribe to new blocks. retrying", "err", err, "retry_number", i)
			} else {
				return true, nil
			}
		}
	}
	return false, errors.New("couldn't recover from failed blocks subscription. stopping listeners")
}

func (blockAPI *BlockAPI) broadcastToListeners(ctx context.Context, height int64, hash []byte) {
	defer func() {
		if r := recover(); r != nil {
			core.GetEnvironment().Logger.Debug("failed to write to heights listener", "err", r)
		}
	}()
	for ch := range blockAPI.heightListeners {
		select {
		case <-ctx.Done():
			return
		case ch <- NewHeightEvent{Height: height, Hash: hash}:
		}
	}
}

func (blockAPI *BlockAPI) addHeightListener() chan NewHeightEvent {
	blockAPI.Lock()
	defer blockAPI.Unlock()
	ch := make(chan NewHeightEvent, 50)
	blockAPI.heightListeners[ch] = struct{}{}
	return ch
}

func (blockAPI *BlockAPI) removeHeightListener(ch chan NewHeightEvent) {
	blockAPI.Lock()
	defer blockAPI.Unlock()
	delete(blockAPI.heightListeners, ch)
}

func (blockAPI *BlockAPI) closeAllListeners() {
	blockAPI.Lock()
	defer blockAPI.Unlock()
	for channel := range blockAPI.heightListeners {
		delete(blockAPI.heightListeners, channel)
	}
}

func (blockAPI *BlockAPI) BlockByHash(req *BlockByHashRequest, stream BlockAPI_BlockByHashServer) error {
	blockStore := core.GetEnvironment().BlockStore
	blockMeta := blockStore.LoadBlockMetaByHash(req.Hash)
	if blockMeta == nil {
		return fmt.Errorf("nil block meta for block hash %d", req.Hash)
	}
	commit := blockStore.LoadBlockCommit(blockMeta.Header.Height)
	if commit == nil {
		return fmt.Errorf("nil commit for block hash %d", req.Hash)
	}
	protoCommit := commit.ToProto()

	validatorSet, err := core.GetEnvironment().StateStore.LoadValidators(blockMeta.Header.Height)
	if err != nil {
		return err
	}
	protoValidatorSet, err := validatorSet.ToProto()
	if err != nil {
		return err
	}

	for i := 0; i < int(blockMeta.BlockID.PartSetHeader.Total); i++ {
		part, err := blockStore.LoadBlockPart(blockMeta.Header.Height, i).ToProto()
		if err != nil {
			return err
		}
		if part == nil {
			return fmt.Errorf("nil block part %d for block hash %d", i, req.Hash)
		}
		if !req.Prove {
			part.Proof = crypto.Proof{}
		}
		isLastPart := i == int(blockMeta.BlockID.PartSetHeader.Total)-1
		resp := BlockByHashResponse{
			BlockPart: part,
			IsLast:    isLastPart,
		}
		if i == 0 {
			resp.BlockMeta = blockMeta.ToProto()
			resp.ValidatorSet = protoValidatorSet
			resp.Commit = protoCommit
		}
		err = stream.Send(&resp)
		if err != nil {
			return err
		}
	}
	return nil
}

func (blockAPI *BlockAPI) BlockByHeight(req *BlockByHeightRequest, stream BlockAPI_BlockByHeightServer) error {
	blockStore := core.GetEnvironment().BlockStore

	blockMeta := blockStore.LoadBlockMeta(req.Height)
	if blockMeta == nil {
		return fmt.Errorf("nil block meta for height %d", req.Height)
	}

	commit := blockStore.LoadSeenCommit(req.Height)
	if commit == nil {
		return fmt.Errorf("nil block commit for height %d", req.Height)
	}
	protoCommit := commit.ToProto()

	validatorSet, err := core.GetEnvironment().StateStore.LoadValidators(req.Height)
	if err != nil {
		return err
	}
	protoValidatorSet, err := validatorSet.ToProto()
	if err != nil {
		return err
	}

	for i := 0; i < int(blockMeta.BlockID.PartSetHeader.Total); i++ {
		part, err := blockStore.LoadBlockPart(req.Height, i).ToProto()
		if err != nil {
			return err
		}
		if part == nil {
			return fmt.Errorf("nil block part %d for height %d", i, req.Height)
		}
		if !req.Prove {
			part.Proof = crypto.Proof{}
		}
		isLastPart := i == int(blockMeta.BlockID.PartSetHeader.Total)-1
		resp := BlockByHeightResponse{
			BlockPart: part,
			IsLast:    isLastPart,
		}
		if i == 0 {
			resp.BlockMeta = blockMeta.ToProto()
			resp.ValidatorSet = protoValidatorSet
			resp.Commit = protoCommit
		}
		err = stream.Send(&resp)
		if err != nil {
			return err
		}
	}
	return nil
}

func (blockAPI *BlockAPI) BlockMetaByHash(_ context.Context, req *BlockMetaByHashRequest) (*BlockMetaByHashResponse, error) {
	blockMeta := core.GetEnvironment().BlockStore.LoadBlockMetaByHash(req.Hash)
	if blockMeta == nil {
		return nil, fmt.Errorf("nil block meta for block hash %d", req.Hash)
	}
	return &BlockMetaByHashResponse{
		BlockMeta: blockMeta.ToProto(),
	}, nil
}

func (blockAPI *BlockAPI) Status(_ context.Context, _ *StatusRequest) (*StatusResponse, error) {
	status, err := core.Status(nil)
	if err != nil {
		return nil, err
	}

	protoPubKey, err := encoding.PubKeyToProto(status.ValidatorInfo.PubKey)
	if err != nil {
		return nil, err
	}
	return &StatusResponse{
		NodeInfo: status.NodeInfo.ToProto(),
		SyncInfo: &SyncInfo{
			LatestBlockHash:     status.SyncInfo.LatestBlockHash,
			LatestAppHash:       status.SyncInfo.LatestAppHash,
			LatestBlockHeight:   status.SyncInfo.LatestBlockHeight,
			LatestBlockTime:     status.SyncInfo.LatestBlockTime,
			EarliestBlockHash:   status.SyncInfo.EarliestBlockHash,
			EarliestAppHash:     status.SyncInfo.EarliestAppHash,
			EarliestBlockHeight: status.SyncInfo.EarliestBlockHeight,
			EarliestBlockTime:   status.SyncInfo.EarliestBlockTime,
			CatchingUp:          status.SyncInfo.CatchingUp,
		},
		ValidatorInfo: &ValidatorInfo{
			Address:     status.ValidatorInfo.Address,
			PubKey:      &protoPubKey,
			VotingPower: status.ValidatorInfo.VotingPower,
		},
	}, nil
}

func (blockAPI *BlockAPI) BlockMetaByHeight(_ context.Context, req *BlockMetaByHeightRequest) (*BlockMetaByHeightResponse, error) {
	blockMeta := core.GetEnvironment().BlockStore.LoadBlockMeta(req.Height)
	if blockMeta == nil {
		return nil, fmt.Errorf("nil block meta for block height %d", req.Height)
	}
	return &BlockMetaByHeightResponse{
		BlockMeta: blockMeta.ToProto(),
	}, nil
}

func (blockAPI *BlockAPI) Commit(_ context.Context, req *CommitRequest) (*CommitResponse, error) {
	commit := core.GetEnvironment().BlockStore.LoadSeenCommit(req.Height)
	if commit == nil {
		return nil, fmt.Errorf("nil block commit for height %d", req.Height)
	}
	protoCommit := commit.ToProto()

	return &CommitResponse{
		Commit: &types.Commit{
			Height:     protoCommit.Height,
			Round:      protoCommit.Round,
			BlockID:    protoCommit.BlockID,
			Signatures: protoCommit.Signatures,
		},
	}, nil
}

func (blockAPI *BlockAPI) ValidatorSet(_ context.Context, req *ValidatorSetRequest) (*ValidatorSetResponse, error) {
	validatorSet, err := core.GetEnvironment().StateStore.LoadValidators(req.Height)
	if err != nil {
		return nil, err
	}
	protoValidatorSet, err := validatorSet.ToProto()
	if err != nil {
		return nil, err
	}
	return &ValidatorSetResponse{
		ValidatorSet: protoValidatorSet,
	}, nil
}

func (blockAPI *BlockAPI) SubscribeNewHeights(_ *SubscribeNewHeightsRequest, stream BlockAPI_SubscribeNewHeightsServer) error {
	heightListener := blockAPI.addHeightListener()
	defer blockAPI.removeHeightListener(heightListener)

	for {
		select {
		case event, ok := <-heightListener:
			if !ok {
				return errors.New("blocks subscription closed from the service side")
			}
			if err := stream.Send(&event); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}
