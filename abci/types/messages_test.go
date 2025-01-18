package types

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/assert"

	cmtproto "github.com/tendermint/tendermint/proto/tendermint/types"
)

func TestMarshalJSON(t *testing.T) {
	b, err := json.Marshal(&ResponseDeliverTx{})
	assert.Nil(t, err)
	// include empty fields.
	assert.True(t, strings.Contains(string(b), "code"))
	r1 := ResponseCheckTx{
		Code:      1,
		Data:      []byte("hello"),
		GasWanted: 43,
		Events: []Event{
			{
				Type: "testEvent",
				Attributes: []EventAttribute{
					{Key: []byte("pho"), Value: []byte("bo")},
				},
			},
		},
	}
	b, err = json.Marshal(&r1)
	assert.Nil(t, err)

	var r2 ResponseCheckTx
	err = json.Unmarshal(b, &r2)
	assert.Nil(t, err)
	assert.Equal(t, r1, r2)
}

func TestWriteReadMessageSimple(t *testing.T) {
	cases := []proto.Message{
		&RequestEcho{
			Message: "Hello",
		},
	}

	for _, c := range cases {
		buf := new(bytes.Buffer)
		err := WriteMessage(c, buf)
		assert.Nil(t, err)

		msg := new(RequestEcho)
		err = ReadMessage(buf, msg)
		assert.Nil(t, err)

		assert.True(t, proto.Equal(c, msg))
	}
}

func TestWriteReadMessage(t *testing.T) {
	cases := []proto.Message{
		&cmtproto.Header{
			Height:  4,
			ChainID: "test",
		},
		// Expanded test cases covering various ABCI message types
		&RequestCheckTx{
			Tx:   []byte("transaction data"),
			Type: CheckTxType_New,
		},
		&ResponseCheckTx{
			Code:      1,
			Data:      []byte("response data"),
			GasWanted: 1000,
			GasUsed:   500,
			Events: []Event{
				{
					Type: "test_event",
					Attributes: []EventAttribute{
						{Key: []byte("key1"), Value: []byte("value1")},
						{Key: []byte("key2"), Value: []byte("value2")},
					},
				},
			},
		},
		&RequestDeliverTx{
			Tx: []byte("deliver transaction"),
		},
		&ResponseDeliverTx{
			Code:      0,
			Data:      []byte("delivery successful"),
			GasWanted: 2000,
			GasUsed:   1500,
		},
		&RequestBeginBlock{
			Hash: []byte("block hash"),
			Header: cmtproto.Header{
				Height:  100,
				ChainID: "test-chain",
			},
			LastCommitInfo: LastCommitInfo{
				Round: 0,
				Votes: []VoteInfo{
					{
						Validator: Validator{
							Address: []byte("validator address"),
							Power:   1000,
						},
						SignedLastBlock: true,
					},
				},
			},
		},
		&ResponseBeginBlock{
			Events: []Event{
				{
					Type: "block_begin",
					Attributes: []EventAttribute{
						{Key: []byte("block"), Value: []byte("started")},
					},
				},
			},
		},
		&RequestEndBlock{
			Height: 150,
		},
		&ResponseEndBlock{
			ValidatorUpdates: []ValidatorUpdate{
				{
					Power: 500,
				},
			},
			Events: []Event{
				{
					Type: "block_end",
					Attributes: []EventAttribute{
						{Key: []byte("block"), Value: []byte("completed")},
					},
				},
			},
		},
	}

	for _, c := range cases {
		buf := new(bytes.Buffer)
		err := WriteMessage(c, buf)
		assert.Nil(t, err, "Failed to write message of type %T", c)

		// Create a new message of the same type to read into
		msg := proto.Clone(c)
		err = ReadMessage(buf, msg)
		assert.Nil(t, err, "Failed to read message of type %T", c)

		assert.True(t, proto.Equal(c, msg), "Message of type %T not equal after write and read", c)
	}
}

func TestWriteReadMessage2(t *testing.T) {
	phrase := "hello-world"
	cases := []proto.Message{
		&ResponseCheckTx{
			Data:      []byte(phrase),
			Log:       phrase,
			GasWanted: 10,
			Events: []Event{
				{
					Type: "testEvent",
					Attributes: []EventAttribute{
						{Key: []byte("abc"), Value: []byte("def")},
					},
				},
			},
		},
		&RequestDeliverTx{
			Tx: []byte("transaction payload"),
		},
		&ResponseDeliverTx{
			Code:      0,
			Data:      []byte("delivery successful"),
			Log:       "Transaction processed",
			GasWanted: 100,
			GasUsed:   50,
			Events: []Event{
				{
					Type: "transfer",
					Attributes: []EventAttribute{
						{Key: []byte("recipient"), Value: []byte("address1")},
						{Key: []byte("amount"), Value: []byte("1000")},
					},
				},
			},
		},
		&RequestBeginBlock{
			Hash: []byte("block-hash"),
			Header: cmtproto.Header{
				Height:  1000,
				ChainID: "celestia-testnet",
			},
			LastCommitInfo: LastCommitInfo{
				Round: 0,
				Votes: []VoteInfo{
					{
						Validator: Validator{
							Address: []byte("validator-address"),
							Power:   1000,
						},
						SignedLastBlock: true,
					},
				},
			},
		},
		&ResponseBeginBlock{
			Events: []Event{
				{
					Type: "block_begin",
					Attributes: []EventAttribute{
						{Key: []byte("block_height"), Value: []byte("1000")},
						{Key: []byte("proposer"), Value: []byte("validator-address")},
					},
				},
			},
		},
		&RequestEndBlock{
			Height: 1001,
		},
		&ResponseEndBlock{
			ValidatorUpdates: []ValidatorUpdate{
				{
					Power: 1200,
				},
			},
			ConsensusParamUpdates: nil,
			Events: []Event{
				{
					Type: "block_end",
					Attributes: []EventAttribute{
						{Key: []byte("block_height"), Value: []byte("1001")},
						{Key: []byte("status"), Value: []byte("completed")},
					},
				},
			},
		},
		&RequestEcho{
			Message: "ping",
		},
		&ResponseEcho{
			Message: "pong",
		},
	}

	for _, c := range cases {
		buf := new(bytes.Buffer)
		err := WriteMessage(c, buf)
		assert.Nil(t, err, "Failed to write message of type %T", c)

		// Create a new message of the same type to read into
		msg := proto.Clone(c)
		err = ReadMessage(buf, msg)
		assert.Nil(t, err, "Failed to read message of type %T", c)

		assert.True(t, proto.Equal(c, msg), "Message of type %T not equal after write and read", c)
	}
}
