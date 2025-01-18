package core

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/libs/pubsub/query"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/types"

	dbm "github.com/cometbft/cometbft-db"

	abci "github.com/tendermint/tendermint/abci/types"
	cmtstate "github.com/tendermint/tendermint/proto/tendermint/state"
	cmtstore "github.com/tendermint/tendermint/proto/tendermint/store"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
	rpctypes "github.com/tendermint/tendermint/rpc/jsonrpc/types"
	sm "github.com/tendermint/tendermint/state"
)

func TestBlockchainInfo(t *testing.T) {
	cases := []struct {
		min, max     int64
		base, height int64
		limit        int64
		resultLength int64
		wantErr      bool
	}{

		// min > max
		{0, 0, 0, 0, 10, 0, true},  // min set to 1
		{0, 1, 0, 0, 10, 0, true},  // max set to height (0)
		{0, 0, 0, 1, 10, 1, false}, // max set to height (1)
		{2, 0, 0, 1, 10, 0, true},  // max set to height (1)
		{2, 1, 0, 5, 10, 0, true},

		// negative
		{1, 10, 0, 14, 10, 10, false}, // control
		{-1, 10, 0, 14, 10, 0, true},
		{1, -10, 0, 14, 10, 0, true},
		{-9223372036854775808, -9223372036854775788, 0, 100, 20, 0, true},

		// check base
		{1, 1, 1, 1, 1, 1, false},
		{2, 5, 3, 5, 5, 3, false},

		// check limit and height
		{1, 1, 0, 1, 10, 1, false},
		{1, 1, 0, 5, 10, 1, false},
		{2, 2, 0, 5, 10, 1, false},
		{1, 2, 0, 5, 10, 2, false},
		{1, 5, 0, 1, 10, 1, false},
		{1, 5, 0, 10, 10, 5, false},
		{1, 15, 0, 10, 10, 10, false},
		{1, 15, 0, 15, 10, 10, false},
		{1, 15, 0, 15, 20, 15, false},
		{1, 20, 0, 15, 20, 15, false},
		{1, 20, 0, 20, 20, 20, false},
	}

	for i, c := range cases {
		caseString := fmt.Sprintf("test %d failed", i)
		min, max, err := filterMinMax(c.base, c.height, c.min, c.max, c.limit)
		if c.wantErr {
			require.Error(t, err, caseString)
		} else {
			require.NoError(t, err, caseString)
			require.Equal(t, 1+max-min, c.resultLength, caseString)
		}
	}
}

func TestBlockResults(t *testing.T) {
	results := &cmtstate.ABCIResponses{
		DeliverTxs: []*abci.ResponseDeliverTx{
			{Code: 0, Data: []byte{0x01}, Log: "ok"},
			{Code: 0, Data: []byte{0x02}, Log: "ok"},
			{Code: 1, Log: "not ok"},
		},
		EndBlock:   &abci.ResponseEndBlock{},
		BeginBlock: &abci.ResponseBeginBlock{},
	}

	globalEnv = &Environment{}
	globalEnv.StateStore = sm.NewStore(dbm.NewMemDB(), sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	err := globalEnv.StateStore.SaveABCIResponses(100, results)
	require.NoError(t, err)
	globalEnv.BlockStore = mockBlockStore{height: 100}
	SetEnvironment(globalEnv)

	testCases := []struct {
		height  int64
		wantErr bool
		wantRes *ctypes.ResultBlockResults
	}{
		{-1, true, nil},
		{0, true, nil},
		{101, true, nil},
		{100, false, &ctypes.ResultBlockResults{
			Height:                100,
			TxsResults:            results.DeliverTxs,
			BeginBlockEvents:      results.BeginBlock.Events,
			EndBlockEvents:        results.EndBlock.Events,
			ValidatorUpdates:      results.EndBlock.ValidatorUpdates,
			ConsensusParamUpdates: results.EndBlock.ConsensusParamUpdates,
		}},
	}

	for _, tc := range testCases {
		res, err := BlockResults(&rpctypes.Context{}, &tc.height)
		if tc.wantErr {
			assert.Error(t, err)
		} else {
			assert.NoError(t, err)
			assert.Equal(t, tc.wantRes, res)
		}
	}
}

func TestEncodeDataRootTuple(t *testing.T) {
	height := uint64(2)
	dataRoot, err := hex.DecodeString("82dc1607d84557d3579ce602a45f5872e821c36dbda7ec926dfa17ebc8d5c013")
	require.NoError(t, err)

	expectedEncoding, err := hex.DecodeString(
		// hex representation of height padded to 32 bytes
		"0000000000000000000000000000000000000000000000000000000000000002" +
			// data root
			"82dc1607d84557d3579ce602a45f5872e821c36dbda7ec926dfa17ebc8d5c013",
	)
	require.NoError(t, err)
	require.NotNil(t, expectedEncoding)

	actualEncoding, err := EncodeDataRootTuple(height, *(*[32]byte)(dataRoot))
	require.NoError(t, err)
	require.NotNil(t, actualEncoding)

	// Check that the length of packed data is correct
	assert.Equal(t, len(actualEncoding), 64)
	assert.Equal(t, expectedEncoding, actualEncoding)
}

func TestDataCommitmentResults(t *testing.T) {
	env := &Environment{}
	height := int64(2826)

	blocks := randomBlocks(height)
	blockStore := mockBlockStore{
		height: height,
		blocks: blocks,
	}
	env.BlockStore = blockStore

	testCases := []struct {
		beginQuery int
		endQuery   int
		expectPass bool
	}{
		{10, 15, true},
		{2727, 2828, false},
		{10, 9, false},
		{0, 1000, false},
		{0, 10, false},
		{10, 8, false},
		// to test the end exclusive support for ranges.
		// the end block could be equal to (height+1), but the data commitment would only
		// take up to height. So we should be able to send request having end block equal
		// to (height+1).
		{int(env.BlockStore.Height()) - 100, int(env.BlockStore.Height()) + 1, true},
	}

	for i, tc := range testCases {
		env.BlockIndexer = mockBlockIndexer{
			height:          height,
			beginQueryBlock: tc.beginQuery,
			endQueryBlock:   tc.endQuery,
		}
		SetEnvironment(env)

		actualCommitment, err := DataCommitment(&rpctypes.Context{}, uint64(tc.beginQuery), uint64(tc.endQuery))
		if tc.expectPass {
			require.Nil(t, err, "should generate the needed data commitment.")

			size := tc.endQuery - tc.beginQuery
			dataRootEncodedTuples := make([][]byte, size)
			for i := 0; i < size; i++ {
				encodedTuple, err := EncodeDataRootTuple(
					uint64(blocks[tc.beginQuery+i].Height),
					*(*[32]byte)(blocks[tc.beginQuery+i].DataHash),
				)
				require.NoError(t, err)
				dataRootEncodedTuples[i] = encodedTuple
			}
			expectedCommitment := merkle.HashFromByteSlices(dataRootEncodedTuples)

			assert.Equal(
				t,
				expectedCommitment,
				actualCommitment.DataCommitment.Bytes(),
				i,
			)
		} else {
			require.NotNil(t, err, "couldn't generate the needed data commitment.")
		}
	}
}

func TestDataRootInclusionProofResults(t *testing.T) {
	env := &Environment{}
	env.StateStore = sm.NewStore(
		dbm.NewMemDB(), sm.StoreOptions{
			DiscardABCIResponses: false,
		},
	)

	height := int64(2826)
	env.BlockStore = mockBlockStore{height: height}
	SetEnvironment(env)

	blocks := randomBlocks(height)
	blockStore := mockBlockStore{
		height: height,
		blocks: blocks,
	}
	env.BlockStore = blockStore

	testCases := []struct {
		height     int
		firstQuery int
		lastQuery  int
		expectPass bool
	}{
		{8, 10, 15, false},
		{10, 0, 15, false},
		{10, 10, 15, true},
		{13, 10, 15, true},
		{14, 10, 15, true},
		{15, 10, 15, false},
		{17, 10, 15, false},
	}

	for i, tc := range testCases {
		env.BlockIndexer = mockBlockIndexer{
			height:          height,
			beginQueryBlock: tc.firstQuery,
			endQueryBlock:   tc.lastQuery,
		}

		proof, err := DataRootInclusionProof(
			&rpctypes.Context{},
			int64(tc.height),
			uint64(tc.firstQuery),
			uint64(tc.lastQuery),
		)
		if tc.expectPass {
			require.Nil(t, err, "should generate block height data root inclusion proof.", i)

			size := tc.lastQuery - tc.firstQuery
			dataRootEncodedTuples := make([][]byte, size)
			for i := 0; i < size; i++ {
				encodedTuple, err := EncodeDataRootTuple(
					uint64(blocks[tc.firstQuery+i].Height),
					*(*[32]byte)(blocks[tc.firstQuery+i].DataHash),
				)
				require.NoError(t, err)
				dataRootEncodedTuples[i] = encodedTuple
			}
			commitment := merkle.HashFromByteSlices(dataRootEncodedTuples)

			err = proof.Proof.Verify(commitment, dataRootEncodedTuples[tc.height-tc.firstQuery])
			require.NoError(t, err)
		} else {
			require.NotNil(t, err, "shouldn't be able to generate proof.")
		}
	}
}

type mockBlockStore struct {
	height int64
	blocks []*types.Block
}

func (mockBlockStore) Base() int64                                       { return 1 }
func (store mockBlockStore) Height() int64                               { return store.height }
func (store mockBlockStore) Size() int64                                 { return store.height }
func (mockBlockStore) LoadBaseMeta() *types.BlockMeta                    { return nil }
func (mockBlockStore) LoadBlockByHash(hash []byte) *types.Block          { return nil }
func (mockBlockStore) LoadBlockPart(height int64, index int) *types.Part { return nil }
func (mockBlockStore) LoadBlockMetaByHash(hash []byte) *types.BlockMeta  { return nil }
func (mockBlockStore) LoadBlockCommit(height int64) *types.Commit        { return nil }
func (mockBlockStore) LoadSeenCommit(height int64) *types.Commit         { return nil }
func (mockBlockStore) PruneBlocks(height int64) (uint64, error)          { return 0, nil }
func (mockBlockStore) SaveBlock(block *types.Block, blockParts *types.PartSet, seenCommit *types.Commit) {
}
func (mockBlockStore) SaveTxInfo(block *types.Block, txResponseCodes []uint32, logs []string) error {
	return nil
}

func (store mockBlockStore) LoadBlockMeta(height int64) *types.BlockMeta {
	if height > store.height {
		return nil
	}
	block := store.blocks[height]
	return &types.BlockMeta{
		BlockID: types.BlockID{Hash: block.Hash(), PartSetHeader: block.MakePartSet(types.BlockPartSizeBytes).Header()},
		Header:  block.Header,
	}
}

func (store mockBlockStore) LoadBlock(height int64) *types.Block {
	if height > store.height {
		return nil
	}
	return store.blocks[height]
}

func (store mockBlockStore) LoadTxInfo(hash []byte) *cmtstore.TxInfo {
	for _, block := range store.blocks {
		for i, tx := range block.Data.Txs {
			// Check if transaction hash matches
			if bytes.Equal(tx.Hash(), hash) {
				return &cmtstore.TxInfo{
					Height: block.Header.Height,
					//nolint:gosec
					Index: uint32(i),
					Code:  uint32(0),
				}
			}
		}
	}
	return nil
}

// mockBlockIndexer used to mock the set of indexed blocks and return a predefined one.
type mockBlockIndexer struct {
	height          int64
	beginQueryBlock int // used not to have to parse any query
	endQueryBlock   int // used not to have to parse any query
}

func (indexer mockBlockIndexer) Has(height int64) (bool, error)            { return true, nil }
func (indexer mockBlockIndexer) Index(types.EventDataNewBlockHeader) error { return nil }

// Search returns a list of block heights corresponding to the values of `indexer.endQueryBlock`
// and `indexer.beginQueryBlock`.
// Doesn't use the query parameter for anything.
func (indexer mockBlockIndexer) Search(ctx context.Context, _ *query.Query) ([]int64, error) {
	size := indexer.endQueryBlock - indexer.beginQueryBlock + 1
	results := make([]int64, size)
	for i := 0; i < size; i++ {
		results[i] = int64(indexer.beginQueryBlock + i)
	}
	return results, nil
}

// randomBlocks generates a set of random blocks up to (and including) the provided height.
func randomBlocks(height int64) []*types.Block {
	blocks := make([]*types.Block, height+1)
	for i := int64(0); i <= height; i++ {
		blocks[i] = randomBlock(i)
	}
	return blocks
}

func makeTxs(height int64) (txs []types.Tx) {
	for i := 0; i < 10; i++ {
		numBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(numBytes, uint64(height))

		txs = append(txs, types.Tx(append(numBytes, byte(i))))
	}
	return txs
}

// randomBlock generates a Block with a certain height and random data hash.
func randomBlock(height int64) *types.Block {
	return &types.Block{
		Header: types.Header{
			Height:   height,
			DataHash: cmtrand.Bytes(32),
		},
		Data: types.Data{
			Txs: makeTxs(height),
		},
	}
}
