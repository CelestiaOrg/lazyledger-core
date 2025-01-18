package state_test

import (
	"bytes"
	"context"
	"io"

	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	abci "github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/crypto"
	"github.com/tendermint/tendermint/crypto/ed25519"
	cryptoenc "github.com/tendermint/tendermint/crypto/encoding"
	"github.com/tendermint/tendermint/crypto/tmhash"
	"github.com/tendermint/tendermint/libs/log"
	mmock "github.com/tendermint/tendermint/mempool/mock"
	cmtproto "github.com/tendermint/tendermint/proto/tendermint/types"
	cmtversion "github.com/tendermint/tendermint/proto/tendermint/version"
	"github.com/tendermint/tendermint/proxy"
	sm "github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/state/mocks"
	sf "github.com/tendermint/tendermint/state/test/factory"
	"github.com/tendermint/tendermint/test/factory"
	"github.com/tendermint/tendermint/types"
	cmttime "github.com/tendermint/tendermint/types/time"
	"github.com/tendermint/tendermint/version"

	db "github.com/cometbft/cometbft-db"
)

var (
	chainID             = "execution_chain"
	testPartSize uint32 = types.BlockPartSizeBytes
	nTxsPerBlock        = 10
	namespace           = "namespace"
	height              = 1
)

func TestApplyBlock(t *testing.T) {
	app := &testApp{}
	cc := proxy.NewLocalClientCreator(app)
	proxyApp := proxy.NewAppConns(cc)
	err := proxyApp.Start()
	require.Nil(t, err)
	defer proxyApp.Stop() //nolint:errcheck // ignore for tests

	state, stateDB, _ := makeState(1, 1)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})

	blockExec := sm.NewBlockExecutor(stateStore, log.TestingLogger(), proxyApp.Consensus(),
		mmock.Mempool{}, sm.EmptyEvidencePool{})

	block := makeBlock(state, 1)
	blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: block.MakePartSet(testPartSize).Header()}

	state, retainHeight, err := blockExec.ApplyBlock(state, blockID, block, nil)
	require.Nil(t, err)
	assert.EqualValues(t, retainHeight, 1)

	// TODO check state and mempool
	assert.EqualValues(t, 1, state.Version.Consensus.App, "App version wasn't updated")
}

func TestApplyBlockWithBlockStore(t *testing.T) {
	app := &testApp{}
	cc := proxy.NewLocalClientCreator(app)
	proxyApp := proxy.NewAppConns(cc)
	err := proxyApp.Start()
	require.Nil(t, err)
	defer proxyApp.Stop() //nolint:errcheck // ignore for tests
	blockStore := mocks.NewBlockStore(t)

	state, stateDB, _ := makeState(1, 1)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})

	blockExec := sm.NewBlockExecutor(stateStore, log.TestingLogger(), proxyApp.Consensus(),
		mmock.Mempool{}, sm.EmptyEvidencePool{}, sm.WithBlockStore(blockStore))

	block := makeBlock(state, 1)
	blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: block.MakePartSet(testPartSize).Header()}

	// Check that SaveTxInfo is called with correct arguments
	blockStore.On("SaveTxInfo", block, mock.AnythingOfType("[]uint32"), mock.AnythingOfType("[]string")).Return(nil)

	_, _, err = blockExec.ApplyBlock(state, blockID, block, nil)
	require.Nil(t, err)
}

// TestBeginBlockValidators ensures we send absent validators list.
func TestBeginBlockValidators(t *testing.T) {
	app := &testApp{}
	cc := proxy.NewLocalClientCreator(app)
	proxyApp := proxy.NewAppConns(cc)
	err := proxyApp.Start()
	require.Nil(t, err)
	defer proxyApp.Stop() //nolint:errcheck // no need to check error again

	state, stateDB, _ := makeState(2, 2)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})

	prevHash := state.LastBlockID.Hash
	prevParts := types.PartSetHeader{}
	prevBlockID := types.BlockID{Hash: prevHash, PartSetHeader: prevParts}

	var (
		now        = cmttime.Now()
		commitSig0 = types.NewCommitSigForBlock(
			[]byte("Signature1"),
			state.Validators.Validators[0].Address,
			now)
		commitSig1 = types.NewCommitSigForBlock(
			[]byte("Signature2"),
			state.Validators.Validators[1].Address,
			now)
		absentSig = types.NewCommitSigAbsent()
	)

	testCases := []struct {
		desc                     string
		lastCommitSigs           []types.CommitSig
		expectedAbsentValidators []int
	}{
		{"none absent", []types.CommitSig{commitSig0, commitSig1}, []int{}},
		{"one absent", []types.CommitSig{commitSig0, absentSig}, []int{1}},
		{"multiple absent", []types.CommitSig{absentSig, absentSig}, []int{0, 1}},
	}

	for _, tc := range testCases {
		lastCommit := types.NewCommit(1, 0, prevBlockID, tc.lastCommitSigs)

		// block for height 2
		block, _ := state.MakeBlock(
			2,
			factory.MakeData(factory.MakeTenTxs(2)),
			lastCommit,
			nil,
			state.Validators.GetProposer().Address,
		)

		_, err = sm.ExecCommitBlock(proxyApp.Consensus(), block, log.TestingLogger(), stateStore, 1)
		require.Nil(t, err, tc.desc)

		// -> app receives a list of validators with a bool indicating if they signed
		ctr := 0
		for i, v := range app.CommitVotes {
			if ctr < len(tc.expectedAbsentValidators) &&
				tc.expectedAbsentValidators[ctr] == i {

				assert.False(t, v.SignedLastBlock)
				ctr++
			} else {
				assert.True(t, v.SignedLastBlock)
			}
		}
	}
}

// TestBeginBlockByzantineValidators ensures we send byzantine validators list.
func TestBeginBlockByzantineValidators(t *testing.T) {
	app := &testApp{}
	cc := proxy.NewLocalClientCreator(app)
	proxyApp := proxy.NewAppConns(cc)
	err := proxyApp.Start()
	require.Nil(t, err)
	defer proxyApp.Stop() //nolint:errcheck // ignore for tests

	state, stateDB, privVals := makeState(1, 1)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})

	defaultEvidenceTime := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)
	privVal := privVals[state.Validators.Validators[0].Address.String()]
	blockID := makeBlockID([]byte("headerhash"), 1000, []byte("partshash"))
	header := &types.Header{
		Version:            cmtversion.Consensus{Block: version.BlockProtocol, App: 1},
		ChainID:            state.ChainID,
		Height:             10,
		Time:               defaultEvidenceTime,
		LastBlockID:        blockID,
		LastCommitHash:     crypto.CRandBytes(tmhash.Size),
		DataHash:           crypto.CRandBytes(tmhash.Size),
		ValidatorsHash:     state.Validators.Hash(),
		NextValidatorsHash: state.Validators.Hash(),
		ConsensusHash:      crypto.CRandBytes(tmhash.Size),
		AppHash:            crypto.CRandBytes(tmhash.Size),
		LastResultsHash:    crypto.CRandBytes(tmhash.Size),
		EvidenceHash:       crypto.CRandBytes(tmhash.Size),
		ProposerAddress:    crypto.CRandBytes(crypto.AddressSize),
	}

	// we don't need to worry about validating the evidence as long as they pass validate basic
	dve := types.NewMockDuplicateVoteEvidenceWithValidator(3, defaultEvidenceTime, privVal, state.ChainID)
	dve.ValidatorPower = 1000
	lcae := &types.LightClientAttackEvidence{
		ConflictingBlock: &types.LightBlock{
			SignedHeader: &types.SignedHeader{
				Header: header,
				Commit: types.NewCommit(10, 0, makeBlockID(header.Hash(), 100, []byte("partshash")), []types.CommitSig{{
					BlockIDFlag:      types.BlockIDFlagNil,
					ValidatorAddress: crypto.AddressHash([]byte("validator_address")),
					Timestamp:        defaultEvidenceTime,
					Signature:        crypto.CRandBytes(types.MaxSignatureSize),
				}}),
			},
			ValidatorSet: state.Validators,
		},
		CommonHeight:        8,
		ByzantineValidators: []*types.Validator{state.Validators.Validators[0]},
		TotalVotingPower:    12,
		Timestamp:           defaultEvidenceTime,
	}

	ev := []types.Evidence{dve, lcae}

	abciEv := []abci.Evidence{
		{
			Type:             abci.EvidenceType_DUPLICATE_VOTE,
			Height:           3,
			Time:             defaultEvidenceTime,
			Validator:        types.TM2PB.Validator(state.Validators.Validators[0]),
			TotalVotingPower: 10,
		},
		{
			Type:             abci.EvidenceType_LIGHT_CLIENT_ATTACK,
			Height:           8,
			Time:             defaultEvidenceTime,
			Validator:        types.TM2PB.Validator(state.Validators.Validators[0]),
			TotalVotingPower: 12,
		},
	}

	evpool := &mocks.EvidencePool{}
	evpool.On("PendingEvidence", mock.AnythingOfType("int64")).Return(ev, int64(100))
	evpool.On("Update", mock.AnythingOfType("state.State"), mock.AnythingOfType("types.EvidenceList")).Return()
	evpool.On("CheckEvidence", mock.AnythingOfType("types.EvidenceList")).Return(nil)

	blockExec := sm.NewBlockExecutor(stateStore, log.TestingLogger(), proxyApp.Consensus(),
		mmock.Mempool{}, evpool)

	block := makeBlock(state, 1)
	block.Evidence = types.EvidenceData{Evidence: ev}
	block.Header.EvidenceHash = block.Evidence.Hash()
	blockID = types.BlockID{Hash: block.Hash(), PartSetHeader: block.MakePartSet(testPartSize).Header()}

	state, retainHeight, err := blockExec.ApplyBlock(state, blockID, block, nil)
	require.Nil(t, err)
	assert.EqualValues(t, retainHeight, 1)

	// TODO check state and mempool
	assert.Equal(t, abciEv, app.ByzantineValidators)
}

func TestProcessProposal(t *testing.T) {
	height := 1
	runTest := func(txs types.Txs, expectAccept bool) {
		app := &testApp{}
		cc := proxy.NewLocalClientCreator(app)
		proxyApp := proxy.NewAppConns(cc)
		err := proxyApp.Start()
		require.Nil(t, err)
		defer proxyApp.Stop() //nolint:errcheck // ignore for tests

		state, stateDB, _ := makeState(1, height)
		stateStore := sm.NewStore(stateDB, sm.StoreOptions{
			DiscardABCIResponses: false,
		})

		blockExec := sm.NewBlockExecutor(stateStore, log.TestingLogger(), proxyApp.Consensus(),
			mmock.Mempool{}, sm.EmptyEvidencePool{})

		block := sf.MakeBlock(state, int64(height), new(types.Commit))
		block.Txs = txs
		acceptBlock, err := blockExec.ProcessProposal(block)
		require.Nil(t, err)
		require.Equal(t, expectAccept, acceptBlock)
	}
	goodTxs := factory.MakeTenTxs(int64(height))
	runTest(goodTxs, true)
	// testApp has process proposal fail if any tx is 0-len
	badTxs := factory.MakeTenTxs(int64(height))
	badTxs[0] = types.Tx{}
	runTest(badTxs, false)
}

func TestProcessProposalRejectedMetric(t *testing.T) {
	server := httptest.NewServer(promhttp.Handler())
	defer server.Close()

	getPrometheusOutput := func() string {
		resp, err := http.Get(server.URL)
		require.NoError(t, err)
		defer resp.Body.Close()

		buf, _ := io.ReadAll(resp.Body)
		return string(buf)
	}
	metrics := sm.PrometheusMetrics(namespace)
	state, stateDB, _ := makeState(1, height)

	accptedBlock := makeAcceptedBlock(state, height)
	rejectedBlock := makeRejectedBlock(state, height)

	type testCase struct {
		name                             string
		block                            *types.Block
		wantProcessProposalRejectedCount int
	}
	tests := []testCase{
		// HACKHACK since Prometheus metrics are registered globally, these
		// tests cases are ordering dependent. In other words, since the counter
		// metric type is monotonically increasing, the expected metric count of
		// a test case is the cumulative sum of the metric count in previous
		// test cases.
		{"accepted block has a process proposal rejected count of 0", accptedBlock, 0},
		{"rejected block has a process proposal rejected count of 1", rejectedBlock, 1},
	}

	for _, test := range tests {
		blockExec := makeBlockExec(t, test.name, test.block, stateDB, metrics)

		_, err := blockExec.ProcessProposal(test.block)
		require.Nil(t, err, test.name)

		prometheusOutput := getPrometheusOutput()
		got := getProcessProposalRejectedCount(t, prometheusOutput)

		require.Equal(t, got, test.wantProcessProposalRejectedCount, test.name)
	}
}

func makeBlockExec(t *testing.T, testName string, block *types.Block, stateDB db.DB,
	metrics *sm.Metrics) (blockExec *sm.BlockExecutor) {
	app := &testApp{}
	clientCreator := proxy.NewLocalClientCreator(app)
	proxyApp := proxy.NewAppConns(clientCreator)

	err := proxyApp.Start()
	require.Nil(t, err, testName)

	defer func() {
		err := proxyApp.Stop()
		require.Nil(t, err, testName)
	}()

	return sm.NewBlockExecutor(
		sm.NewStore(stateDB, sm.StoreOptions{
			DiscardABCIResponses: false,
		}),
		log.TestingLogger(),
		proxyApp.Consensus(),
		mmock.Mempool{},
		sm.EmptyEvidencePool{},
		sm.BlockExecutorWithMetrics(metrics),
	)
}

func getProcessProposalRejectedCount(t *testing.T, prometheusOutput string) (count int) {
	metricName := strings.Join([]string{namespace, sm.MetricsSubsystem, "process_proposal_rejected"}, "_")
	lines := strings.Split(prometheusOutput, "\n")

	for _, line := range lines {
		if strings.HasPrefix(line, metricName) {
			parts := strings.Split(line, " ")
			count, err := strconv.Atoi(parts[1])
			require.Nil(t, err)
			return count
		}
	}

	return 0
}

func TestValidateValidatorUpdates(t *testing.T) {
	pubkey1 := ed25519.GenPrivKey().PubKey()
	pubkey2 := ed25519.GenPrivKey().PubKey()
	pk1, err := cryptoenc.PubKeyToProto(pubkey1)
	assert.NoError(t, err)
	pk2, err := cryptoenc.PubKeyToProto(pubkey2)
	assert.NoError(t, err)

	defaultValidatorParams := cmtproto.ValidatorParams{PubKeyTypes: []string{types.ABCIPubKeyTypeEd25519}}

	testCases := []struct {
		name string

		abciUpdates     []abci.ValidatorUpdate
		validatorParams cmtproto.ValidatorParams

		shouldErr bool
	}{
		{
			"adding a validator is OK",
			[]abci.ValidatorUpdate{{PubKey: pk2, Power: 20}},
			defaultValidatorParams,
			false,
		},
		{
			"updating a validator is OK",
			[]abci.ValidatorUpdate{{PubKey: pk1, Power: 20}},
			defaultValidatorParams,
			false,
		},
		{
			"removing a validator is OK",
			[]abci.ValidatorUpdate{{PubKey: pk2, Power: 0}},
			defaultValidatorParams,
			false,
		},
		{
			"adding a validator with negative power results in error",
			[]abci.ValidatorUpdate{{PubKey: pk2, Power: -100}},
			defaultValidatorParams,
			true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := sm.ValidateValidatorUpdates(tc.abciUpdates, tc.validatorParams)
			if tc.shouldErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestUpdateValidators(t *testing.T) {
	pubkey1 := ed25519.GenPrivKey().PubKey()
	val1 := types.NewValidator(pubkey1, 10)
	pubkey2 := ed25519.GenPrivKey().PubKey()
	val2 := types.NewValidator(pubkey2, 20)

	pk, err := cryptoenc.PubKeyToProto(pubkey1)
	require.NoError(t, err)
	pk2, err := cryptoenc.PubKeyToProto(pubkey2)
	require.NoError(t, err)

	testCases := []struct {
		name string

		currentSet  *types.ValidatorSet
		abciUpdates []abci.ValidatorUpdate

		resultingSet *types.ValidatorSet
		shouldErr    bool
	}{
		{
			"adding a validator is OK",
			types.NewValidatorSet([]*types.Validator{val1}),
			[]abci.ValidatorUpdate{{PubKey: pk2, Power: 20}},
			types.NewValidatorSet([]*types.Validator{val1, val2}),
			false,
		},
		{
			"updating a validator is OK",
			types.NewValidatorSet([]*types.Validator{val1}),
			[]abci.ValidatorUpdate{{PubKey: pk, Power: 20}},
			types.NewValidatorSet([]*types.Validator{types.NewValidator(pubkey1, 20)}),
			false,
		},
		{
			"removing a validator is OK",
			types.NewValidatorSet([]*types.Validator{val1, val2}),
			[]abci.ValidatorUpdate{{PubKey: pk2, Power: 0}},
			types.NewValidatorSet([]*types.Validator{val1}),
			false,
		},
		{
			"removing a non-existing validator results in error",
			types.NewValidatorSet([]*types.Validator{val1}),
			[]abci.ValidatorUpdate{{PubKey: pk2, Power: 0}},
			types.NewValidatorSet([]*types.Validator{val1}),
			true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			updates, err := types.PB2TM.ValidatorUpdates(tc.abciUpdates)
			assert.NoError(t, err)
			err = tc.currentSet.UpdateWithChangeSet(updates)
			if tc.shouldErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				require.Equal(t, tc.resultingSet.Size(), tc.currentSet.Size())

				assert.Equal(t, tc.resultingSet.TotalVotingPower(), tc.currentSet.TotalVotingPower())

				assert.Equal(t, tc.resultingSet.Validators[0].Address, tc.currentSet.Validators[0].Address)
				if tc.resultingSet.Size() > 1 {
					assert.Equal(t, tc.resultingSet.Validators[1].Address, tc.currentSet.Validators[1].Address)
				}
			}
		})
	}
}

// TestEndBlockValidatorUpdates ensures we update validator set and send an event.
func TestEndBlockValidatorUpdates(t *testing.T) {
	app := &testApp{}
	cc := proxy.NewLocalClientCreator(app)
	proxyApp := proxy.NewAppConns(cc)
	err := proxyApp.Start()
	require.Nil(t, err)
	defer proxyApp.Stop() //nolint:errcheck // ignore for tests

	state, stateDB, _ := makeState(1, 1)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})

	blockExec := sm.NewBlockExecutor(
		stateStore,
		log.TestingLogger(),
		proxyApp.Consensus(),
		mmock.Mempool{},
		sm.EmptyEvidencePool{},
	)

	eventBus := types.NewEventBus()
	err = eventBus.Start()
	require.NoError(t, err)
	defer eventBus.Stop() //nolint:errcheck // ignore for tests

	blockExec.SetEventBus(eventBus)

	updatesSub, err := eventBus.Subscribe(
		context.Background(),
		"TestEndBlockValidatorUpdates",
		types.EventQueryValidatorSetUpdates,
	)
	require.NoError(t, err)

	block := makeBlock(state, 1)
	blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: block.MakePartSet(testPartSize).Header()}

	pubkey := ed25519.GenPrivKey().PubKey()
	pk, err := cryptoenc.PubKeyToProto(pubkey)
	require.NoError(t, err)
	app.ValidatorUpdates = []abci.ValidatorUpdate{
		{PubKey: pk, Power: 10},
	}

	state, _, err = blockExec.ApplyBlock(state, blockID, block, nil)
	require.Nil(t, err)
	// test new validator was added to NextValidators
	if assert.Equal(t, state.Validators.Size()+1, state.NextValidators.Size()) {
		idx, _ := state.NextValidators.GetByAddress(pubkey.Address())
		if idx < 0 {
			t.Fatalf("can't find address %v in the set %v", pubkey.Address(), state.NextValidators)
		}
	}

	// test we threw an event
	select {
	case msg := <-updatesSub.Out():
		event, ok := msg.Data().(types.EventDataValidatorSetUpdates)
		require.True(t, ok, "Expected event of type EventDataValidatorSetUpdates, got %T", msg.Data())
		if assert.NotEmpty(t, event.ValidatorUpdates) {
			assert.Equal(t, pubkey, event.ValidatorUpdates[0].PubKey)
			assert.EqualValues(t, 10, event.ValidatorUpdates[0].VotingPower)
		}
	case <-updatesSub.Cancelled():
		t.Fatalf("updatesSub was cancelled (reason: %v)", updatesSub.Err())
	case <-time.After(1 * time.Second):
		t.Fatal("Did not receive EventValidatorSetUpdates within 1 sec.")
	}
}

// TestEndBlockValidatorUpdatesResultingInEmptySet checks that processing validator updates that
// would result in empty set causes no panic, an error is raised and NextValidators is not updated
func TestEndBlockValidatorUpdatesResultingInEmptySet(t *testing.T) {
	app := &testApp{}
	cc := proxy.NewLocalClientCreator(app)
	proxyApp := proxy.NewAppConns(cc)
	err := proxyApp.Start()
	require.Nil(t, err)
	defer proxyApp.Stop() //nolint:errcheck // ignore for tests

	state, stateDB, _ := makeState(1, 1)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockExec := sm.NewBlockExecutor(
		stateStore,
		log.TestingLogger(),
		proxyApp.Consensus(),
		mmock.Mempool{},
		sm.EmptyEvidencePool{},
	)

	block := makeBlock(state, 1)
	blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: block.MakePartSet(testPartSize).Header()}

	vp, err := cryptoenc.PubKeyToProto(state.Validators.Validators[0].PubKey)
	require.NoError(t, err)
	// Remove the only validator
	app.ValidatorUpdates = []abci.ValidatorUpdate{
		{PubKey: vp, Power: 0},
	}

	assert.NotPanics(t, func() { state, _, err = blockExec.ApplyBlock(state, blockID, block, nil) })
	assert.NotNil(t, err)
	assert.NotEmpty(t, state.NextValidators.Validators)
}

func TestFireEventSignedBlockEvent(t *testing.T) {
	app := &testApp{}
	cc := proxy.NewLocalClientCreator(app)
	proxyApp := proxy.NewAppConns(cc)
	err := proxyApp.Start()
	require.NoError(t, err)
	defer proxyApp.Stop() //nolint:errcheck // ignore for tests

	state, stateDB, _ := makeState(2, 1)
	// modify the current validators so it's different to the last validators
	state.Validators.Validators[0].VotingPower = 10
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockExec := sm.NewBlockExecutor(
		stateStore,
		log.TestingLogger(),
		proxyApp.Consensus(),
		mmock.Mempool{},
		sm.EmptyEvidencePool{},
	)
	eventBus := types.NewEventBus()
	err = eventBus.Start()
	require.NoError(t, err)
	defer eventBus.Stop() //nolint:errcheck

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, err := eventBus.Subscribe(ctx, "test-client", types.EventQueryNewSignedBlock)
	require.NoError(t, err)
	blockExec.SetEventBus(eventBus)

	block := makeBlock(state, 1)
	blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: block.MakePartSet(testPartSize).Header()}

	commit := &types.Commit{
		Height:  block.Height,
		Round:   0,
		BlockID: blockID,
		Signatures: []types.CommitSig{
			types.NewCommitSigAbsent(),
		},
	}

	state, _, err = blockExec.ApplyBlock(state, blockID, block, commit)
	require.NoError(t, err)

	select {
	case msg := <-sub.Out():
		signedBlock, ok := msg.Data().(types.EventDataSignedBlock)
		require.True(t, ok)

		// check that the published data are all from the same height
		if signedBlock.Header.Height != signedBlock.Commit.Height {
			t.Fatalf("expected commit height and header height to match")
		}

		if valHash := signedBlock.ValidatorSet.Hash(); !bytes.Equal(signedBlock.Header.ValidatorsHash, valHash) {
			t.Fatalf("expected validator hashes to match")
		}
	case <-sub.Cancelled():
		t.Fatalf("subscription was unexpectedly cancelled")
	case <-time.After(5 * time.Second):
		t.Fatalf("test timed out waiting for signed block")
	}
}

func makeBlockID(hash []byte, partSetSize uint32, partSetHash []byte) types.BlockID {
	var (
		h   = make([]byte, tmhash.Size)
		psH = make([]byte, tmhash.Size)
	)
	copy(h, hash)
	copy(psH, partSetHash)
	return types.BlockID{
		Hash: h,
		PartSetHeader: types.PartSetHeader{
			Total: partSetSize,
			Hash:  psH,
		},
	}
}

func makeAcceptedBlock(state sm.State, height int) (block *types.Block) {
	block = sf.MakeBlock(state, int64(height), new(types.Commit))
	goodTxs := factory.MakeTenTxs(int64(height))
	block.Txs = goodTxs
	return block
}

func makeRejectedBlock(state sm.State, height int) (block *types.Block) {
	block = makeAcceptedBlock(state, height)
	block.Txs[0] = types.Tx{}
	return block
}
