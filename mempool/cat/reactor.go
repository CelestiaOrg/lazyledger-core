package cat

import (
	"fmt"
	"time"

	"github.com/gogo/protobuf/proto"

	cfg "github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/crypto/tmhash"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/mempool"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace"
	"github.com/tendermint/tendermint/pkg/trace/schema"
	protomem "github.com/tendermint/tendermint/proto/tendermint/mempool"
	"github.com/tendermint/tendermint/types"
)

const (
	// default duration to wait before considering a peer non-responsive
	// and searching for the tx from a new peer
	DefaultGossipDelay = 200 * time.Millisecond

	// Content Addressable Tx Pool gossips state based messages (SeenTx and WantTx) on a separate channel
	// for cross compatibility
	MempoolStateChannel = byte(0x31)

	// MempoolRecoveryChannel is a channel for mempool recovery messages
	MempoolRecoveryChannel = byte(0x32)

	// peerHeightDiff signifies the tolerance in difference in height between the peer and the height
	// the node received the tx
	peerHeightDiff = 2
)

// Reactor handles mempool tx broadcasting logic amongst peers. For the main
// logic behind the protocol, refer to `ReceiveEnvelope` or to the english
// spec under /.spec.md
type Reactor struct {
	p2p.BaseReactor
	opts         *ReactorOptions
	mempool      *TxPool
	ids          *mempoolIDs
	requests     *requestScheduler
	blockFetcher *blockFetcher
	traceClient  trace.Tracer
}

type ReactorOptions struct {
	// ListenOnly means that the node will never broadcast any of the transactions that
	// it receives. This is useful for keeping transactions private
	ListenOnly bool

	// MaxTxSize is the maximum size of a transaction that can be received
	MaxTxSize int

	// MaxGossipDelay is the maximum allotted time that the reactor expects a transaction to
	// arrive before issuing a new request to a different peer
	MaxGossipDelay time.Duration

	// TraceClient is the trace client for collecting trace level events
	TraceClient trace.Tracer
}

func (opts *ReactorOptions) VerifyAndComplete() error {
	if opts.MaxTxSize == 0 {
		opts.MaxTxSize = cfg.DefaultMempoolConfig().MaxTxBytes
	}

	if opts.MaxGossipDelay == 0 {
		opts.MaxGossipDelay = DefaultGossipDelay
	}

	if opts.MaxTxSize < 0 {
		return fmt.Errorf("max tx size (%d) cannot be negative", opts.MaxTxSize)
	}

	if opts.MaxGossipDelay < 0 {
		return fmt.Errorf("max gossip delay (%d) cannot be negative", opts.MaxGossipDelay)
	}

	if opts.TraceClient == nil {
		opts.TraceClient = trace.NoOpTracer()
	}

	return nil
}

// NewReactor returns a new Reactor with the given config and mempool.
func NewReactor(mempool *TxPool, opts *ReactorOptions) (*Reactor, error) {
	err := opts.VerifyAndComplete()
	if err != nil {
		return nil, err
	}
	memR := &Reactor{
		opts:         opts,
		mempool:      mempool,
		ids:          newMempoolIDs(),
		requests:     newRequestScheduler(opts.MaxGossipDelay, defaultGlobalRequestTimeout),
		blockFetcher: newBlockFetcher(),
		traceClient:  opts.TraceClient,
	}
	memR.BaseReactor = *p2p.NewBaseReactor("Mempool", memR)
	return memR, nil
}

// SetLogger sets the Logger on the reactor and the underlying mempool.
func (memR *Reactor) SetLogger(l log.Logger) {
	memR.Logger = l
}

// OnStart implements Service.
func (memR *Reactor) OnStart() error {
	if !memR.opts.ListenOnly {
		go memR.broadcastTxsRoutine()
	} else {
		memR.Logger.Info("Tx broadcasting is disabled")
	}
	// run a separate go routine to check for time based TTLs
	if memR.mempool.config.TTLDuration > 0 {
		go func() {
			ticker := time.NewTicker(memR.mempool.config.TTLDuration)
			for {
				select {
				case <-ticker.C:
					memR.mempool.CheckToPurgeExpiredTxs()
				case <-memR.Quit():
					return
				}
			}
		}()
	}

	return nil
}

func (memR *Reactor) broadcastTxsRoutine() {
	for {
		select {
		case <-memR.Quit():
			return
		default:
		}

		// if there are no current transactions to broadcast, check if there are any new ones to add to the queue
		if memR.mempool.priorityBroadcastQueue.queue.Len() == 0 {
			select {
			case <-memR.Quit():
				return
			case <-memR.mempool.priorityBroadcastQueue.isReady():
				memR.mempool.priorityBroadcastQueue.processIncomingTxs()
			}
		}

		nextTxKey, peer := memR.mempool.priorityBroadcastQueue.Pop()
		if nextTxKey.IsEmpty() {
			continue
		}

		nextTx, has := memR.mempool.GetTxByKey(nextTxKey)
		if !has {
			// the tx was evicted, remove it from the priority broadcast queue
			continue
		}

		peers := map[uint16]p2p.Peer{}
		if peer != nil {
			id := memR.ids.GetIDForPeer(peer.ID())
			peers[id] = peer
		} else {
			peers = memR.ids.GetAll()
		}
		for peerID, peer := range peers {
			// we continually try to send to that peer until one of the
			// termination conditions are met
			for {
				if !peer.IsRunning() {
					// we disconnected from the peer
					break
				}

				if !memR.IsRunning() {
					// mempool reactor has stopped
					return
				}

				peerState, ok := peer.Get(types.PeerStateKey).(PeerState)
				if !ok {
					break
				}

				if peerState.GetHeight() < memR.mempool.Height()-peerHeightDiff {
					// peer is too far behind
					break
				}

				if memR.mempool.seenByPeersSet.Has(nextTxKey, memR.ids.GetIDForPeer(peer.ID())) {
					// peer already has the tx
					break
				}

				if p2p.TrySendEnvelopeShim(peer, p2p.Envelope{
					ChannelID: mempool.MempoolChannel,
					Message:   &protomem.Txs{Txs: [][]byte{nextTx}},
				}, memR.Logger) {
					memR.mempool.PeerHasTx(peerID, nextTxKey)
					break
				} else {
					// buffer is full. Wait a bit and try again with the same peer
					time.Sleep(10 * time.Millisecond)
					continue
				}
			}
		}
	}
}

// OnStop implements Service
func (memR *Reactor) OnStop() {
	// stop all the timers tracking outbound requests
	memR.requests.Close()
}

// GetChannels implements Reactor by returning the list of channels for this
// reactor.
func (memR *Reactor) GetChannels() []*p2p.ChannelDescriptor {
	largestTx := make([]byte, memR.opts.MaxTxSize)
	txMsg := protomem.Message{
		Sum: &protomem.Message_Txs{
			Txs: &protomem.Txs{Txs: [][]byte{largestTx}},
		},
	}

	stateMsg := protomem.Message{
		Sum: &protomem.Message_SeenTx{
			SeenTx: &protomem.SeenTx{
				TxKey: make([]byte, tmhash.Size),
			},
		},
	}

	return []*p2p.ChannelDescriptor{
		{
			ID:                  mempool.MempoolChannel,
			Priority:            3,
			SendQueueCapacity:   2,
			RecvMessageCapacity: txMsg.Size(),
			MessageType:         &protomem.Message{},
		},
		{
			ID:                  MempoolStateChannel,
			Priority:            6,
			SendQueueCapacity:   1000,
			RecvMessageCapacity: stateMsg.Size(),
			MessageType:         &protomem.Message{},
		},
		{
			ID:                  MempoolRecoveryChannel,
			Priority:            12,
			SendQueueCapacity:   100,
			RecvMessageCapacity: txMsg.Size(),
			MessageType:         &protomem.Message{},
		},
	}
}

// InitPeer implements Reactor by creating a state for the peer.
func (memR *Reactor) InitPeer(peer p2p.Peer) p2p.Peer {
	memR.ids.ReserveForPeer(peer)
	return peer
}

// AddPeer broadcasts all the transactions that this node has seen
func (memR *Reactor) AddPeer(peer p2p.Peer) {
	keys := memR.mempool.store.getAllKeys()
	for _, key := range keys {
		memR.broadcastSeenTx(key)
	}
}

// RemovePeer implements Reactor. For all current outbound requests to this
// peer it will find a new peer to rerequest the same transactions.
func (memR *Reactor) RemovePeer(peer p2p.Peer, reason interface{}) {
	peerID := memR.ids.Reclaim(peer.ID())
	// clear all memory of seen txs by that peer
	memR.mempool.seenByPeersSet.RemovePeer(peerID)

	// remove and rerequest all pending outbound requests to that peer since we know
	// we won't receive any responses from them.
	outboundRequests := memR.requests.ClearAllRequestsFrom(peerID)
	for key := range outboundRequests {
		memR.findNewPeerToRequestTx(key)
	}
}

func (memR *Reactor) Receive(chID byte, peer p2p.Peer, msgBytes []byte) {
	msg := &protomem.Message{}
	err := proto.Unmarshal(msgBytes, msg)
	if err != nil {
		panic(err)
	}
	uw, err := msg.Unwrap()
	if err != nil {
		panic(err)
	}
	memR.ReceiveEnvelope(p2p.Envelope{
		ChannelID: chID,
		Src:       peer,
		Message:   uw,
	})
}

// ReceiveEnvelope implements Reactor.
// It processes one of three messages: Txs, SeenTx, WantTx.
func (memR *Reactor) ReceiveEnvelope(e p2p.Envelope) {
	switch msg := e.Message.(type) {

	// A peer has sent us one or more transactions. This could be either because we requested them
	// or because the peer received a new transaction and is broadcasting it to us.
	// NOTE: This setup also means that we can support older mempool implementations that simply
	// flooded the network with transactions.
	case *protomem.Txs:
		protoTxs := msg.GetTxs()
		if len(protoTxs) == 0 {
			memR.Logger.Error("received empty txs from peer", "src", e.Src)
			return
		}
		peerID := memR.ids.GetIDForPeer(e.Src.ID())
		txInfo := mempool.TxInfo{SenderID: peerID}
		txInfo.SenderP2PID = e.Src.ID()

		var err error
		for _, tx := range protoTxs {
			ntx := types.Tx(tx)
			key := ntx.Key()
			schema.WriteMempoolTx(memR.traceClient, string(e.Src.ID()), key[:], len(tx), schema.Download)
			// If we requested the transaction we mark it as received.
			if memR.requests.Has(peerID, key) {
				memR.requests.MarkReceived(peerID, key)
				memR.Logger.Debug("received a response for a requested transaction", "peerID", peerID, "txKey", key)
			} else {
				// If we didn't request the transaction we simply mark the peer as having the
				// tx (we'd have already done it if we were requesting the tx).
				memR.mempool.PeerHasTx(peerID, key)
				memR.Logger.Debug("received new trasaction", "peerID", peerID, "txKey", key)
			}
			// If a block has been proposed with this transaction and
			// consensus was waiting for it, it will now be published.
			memR.blockFetcher.TryAddMissingTx(key, tx)

			// Now attempt to add the tx to the mempool.
			_, _, err = memR.mempool.tryAddNewTx(ntx, key, txInfo)
			if err != nil && err != ErrTxInMempool && err != ErrTxRecentlyCommitted {
				memR.Logger.Info("could not add tx from peer", "peerID", peerID, "txKey", key, "err", err)
				return
			}
			// if !memR.opts.ListenOnly {
			// We broadcast only transactions that we deem valid and actually have in our mempool.
			memR.broadcastSeenTx(key)
			// }
		}

	// A peer has indicated to us that it has a transaction. We first verify the txkey and
	// mark that peer as having the transaction. Then we proceed with the following logic:
	//
	// 1. If we have the transaction, we do nothing.
	// 2. If we don't yet have the tx but have an outgoing request for it, we do nothing.
	// 3. If we recently evicted the tx and still don't have space for it, we do nothing.
	// 4. Else, we request the transaction from that peer.
	case *protomem.SeenTx:
		txKey, err := types.TxKeyFromBytes(msg.TxKey)
		if err != nil {
			memR.Logger.Error("peer sent SeenTx with incorrect tx key", "err", err)
			memR.Switch.StopPeerForError(e.Src, err)
			return
		}
		schema.WriteMempoolPeerState(
			memR.traceClient,
			string(e.Src.ID()),
			schema.SeenTx,
			txKey[:],
			schema.Download,
		)
		peerID := memR.ids.GetIDForPeer(e.Src.ID())
		memR.mempool.PeerHasTx(peerID, txKey)
		// Check if we don't already have the transaction and that it was recently rejected
		if memR.mempool.Has(txKey) || memR.mempool.IsRejectedTx(txKey) || memR.mempool.store.hasCommitted(txKey) {
			memR.Logger.Debug("received a seen tx for a tx we already have", "txKey", txKey)
			return
		}

		// If we are already requesting that tx, then we don't need to go any further.
		if memR.requests.ForTx(txKey) != 0 {
			memR.Logger.Debug("received a SeenTx message for a transaction we are already requesting", "txKey", txKey)
			return
		}

		// We don't have the transaction, nor are we requesting it so we send the node
		// a want msg
		memR.requestTx(txKey, e.Src)

	// A peer is requesting a transaction that we have claimed to have. Find the specified
	// transaction and broadcast it to the peer. We may no longer have the transaction
	case *protomem.WantTx:
		txKey, err := types.TxKeyFromBytes(msg.TxKey)
		if err != nil {
			memR.Logger.Error("peer sent WantTx with incorrect tx key", "err", err)
			memR.Switch.StopPeerForError(e.Src, err)
			return
		}
		schema.WriteMempoolPeerState(
			memR.traceClient,
			string(e.Src.ID()),
			schema.WantTx,
			txKey[:],
			schema.Download,
		)
		tx := memR.mempool.store.get(txKey)
		if tx == nil {
			// see if the tx was recently committed
			tx = memR.mempool.store.getCommitted(txKey)
		}
		if tx == nil {
			memR.Logger.Debug("received a want tx for a transaction we don't have", "txKey", txKey)
			return
		}
		// If this transaction is in an active proposal, we send it with high priority
		peerID := memR.ids.GetIDForPeer(e.Src.ID())
		if tx.proposed {
			memR.Logger.Debug("sending a proposed transaction in response to a want msg", "peer", peerID, "txKey", txKey)
			if p2p.SendEnvelopeShim(e.Src, p2p.Envelope{ //nolint:staticcheck
				ChannelID: MempoolRecoveryChannel,
				Message:   &protomem.Txs{Txs: [][]byte{tx.tx}},
			}, memR.Logger) {
				memR.mempool.PeerHasTx(peerID, txKey)
				schema.WriteMempoolTx(
					memR.traceClient,
					string(e.Src.ID()),
					txKey[:],
					len(tx.tx),
					schema.Upload,
				)
			}
		} else if !memR.opts.ListenOnly {
			memR.Logger.Debug("sending a transaction in response to a want msg", "peer", peerID, "txKey", txKey)
			// else we add it to the queue to be sent to the peer based on
			// the priority of the transaction
			memR.mempool.priorityBroadcastQueue.BroadcastToPeer(tx.priority, txKey, e.Src)
		}

	default:
		memR.Logger.Error("unknown message type", "src", e.Src, "chId", e.ChannelID, "msg", fmt.Sprintf("%T", msg))
		memR.Switch.StopPeerForError(e.Src, fmt.Errorf("mempool cannot handle message of type: %T", msg))
		return
	}
}

// PeerState describes the state of a peer.
type PeerState interface {
	GetHeight() int64
}

// broadcastSeenTx broadcasts a SeenTx message to all peers unless we
// know they have already seen the transaction
func (memR *Reactor) broadcastSeenTx(txKey types.TxKey) {
	memR.Logger.Debug("broadcasting seen tx to all peers", "tx_key", txKey.String())
	msg := &protomem.Message{
		Sum: &protomem.Message_SeenTx{
			SeenTx: &protomem.SeenTx{
				TxKey: txKey[:],
			},
		},
	}
	bz, err := msg.Marshal()
	if err != nil {
		panic(err)
	}

	for id, peer := range memR.ids.GetAll() {
		if p, ok := peer.Get(types.PeerStateKey).(PeerState); ok {
			// make sure peer isn't too far behind. This can happen
			// if the peer is blocksyncing still and catching up
			// in which case we just skip sending the transaction
			if p.GetHeight() < memR.mempool.Height()-peerHeightDiff {
				memR.Logger.Debug("peer is too far behind us. Skipping broadcast of seen tx", "peerID", peer.ID(),
					"peerHeight", p.GetHeight(), "ourHeight", memR.mempool.Height())
				continue
			}
		}
		// no need to send a seen tx message to a peer that already
		// has that tx.
		if memR.mempool.seenByPeersSet.Has(txKey, id) {
			continue
		}

		if !peer.Send(MempoolStateChannel, bz) {
			memR.Logger.Error("failed to send seen tx to peer", "peerID", peer.ID(), "txKey", txKey)
		}
	}
	memR.Logger.Debug("broadcasted seen tx to all peers", "tx_key", txKey.String())
}

// broadcastProposedTx broadcast a transaction that's already proposed in a block to all peers unless we are already sure they have seen the tx.
func (memR *Reactor) broadcastProposedTx(wtx *wrappedTx) {
	msg := &protomem.Message{
		Sum: &protomem.Message_Txs{
			Txs: &protomem.Txs{
				Txs: [][]byte{wtx.tx},
			},
		},
	}
	bz, err := msg.Marshal()
	if err != nil {
		panic(err)
	}

	for id, peer := range memR.ids.GetAll() {
		if p, ok := peer.Get(types.PeerStateKey).(PeerState); ok {
			// make sure peer isn't too far behind. This can happen
			// if the peer is blocksyncing still and catching up
			// in which case we just skip sending the transaction
			if p.GetHeight() < wtx.height-peerHeightDiff {
				memR.Logger.Debug("peer is too far behind us. Skipping broadcast of seen tx")
				continue
			}
		}

		if memR.mempool.seenByPeersSet.Has(wtx.key, id) {
			continue
		}

		if peer.TrySend(MempoolRecoveryChannel, bz) { //nolint:staticcheck
			memR.mempool.PeerHasTx(id, wtx.key)
		} else {
			memR.Logger.Error("failed to send new tx to peer", "peerID", peer.ID(), "txKey", wtx.key)
		}
	}
}

// requestTx requests a transaction from a peer and tracks it,
// requesting it from another peer if the first peer does not respond.
func (memR *Reactor) requestTx(txKey types.TxKey, peer p2p.Peer) {
	if peer == nil {
		// we have disconnected from the peer
		return
	}
	msg := &protomem.Message{
		Sum: &protomem.Message_WantTx{
			WantTx: &protomem.WantTx{TxKey: txKey[:]},
		},
	}
	bz, err := msg.Marshal()
	if err != nil {
		panic(err)
	}

	success := peer.Send(MempoolStateChannel, bz) //nolint:staticcheck
	if success {
		memR.Logger.Debug("requested transaction", "txKey", txKey, "peerID", peer.ID())
		memR.mempool.metrics.RequestedTxs.Add(1)
		requested := memR.requests.Add(txKey, memR.ids.GetIDForPeer(peer.ID()), memR.findNewPeerToRequestTx)
		if !requested {
			memR.Logger.Debug("have already marked a tx as requested", "txKey", txKey, "peerID", peer.ID())
		}
	} else {
		memR.Logger.Error("failed to send message to request transaction", "txKey", txKey, "peerID", peer.ID())
	}
}

// findNewPeerToSendTx finds a new peer that has already seen the transaction to
// request a transaction from.
func (memR *Reactor) findNewPeerToRequestTx(txKey types.TxKey) {
	// ensure that we are connected to peers
	if memR.ids.Len() == 0 {
		return
	}

	// get the next peer in the list of remaining peers that have seen the tx
	// and does not already have an outbound request for that tx
	seenMap := memR.mempool.seenByPeersSet.Get(txKey)
	var peerID uint16
	for possiblePeer := range seenMap {
		if !memR.requests.Has(possiblePeer, txKey) {
			peerID = possiblePeer
			break
		}
	}

	if peerID == 0 {
		// No other free peer has the transaction we are looking for.
		// We give up 🤷‍♂️ and hope either a peer responds late or the tx
		// is gossiped again
		memR.Logger.Debug("no other peer has the tx we are looking for", "txKey", txKey)
		// TODO: should add a metric to see how common this is
		return
	}
	peer := memR.ids.GetPeer(peerID)
	if peer == nil {
		// we disconnected from that peer, retry again until we exhaust the list
		memR.mempool.seenByPeersSet.Remove(txKey, peerID)
		memR.findNewPeerToRequestTx(txKey)
	} else {
		memR.mempool.metrics.RerequestedTxs.Add(1)
		memR.requestTx(txKey, peer)
	}
}
