package load

import (
	"crypto/rand"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/p2p/conn"
	"github.com/tendermint/tendermint/pkg/trace"
	protomem "github.com/tendermint/tendermint/proto/tendermint/mempool"
)

const (
	FirstChannel   = byte(0x01)
	SecondChannel  = byte(0x02)
	ThirdChannel   = byte(0x03)
	FourthChannel  = byte(0x04)
	FifthChannel   = byte(0x05)
	SixthChannel   = byte(0x06)
	SeventhChannel = byte(0x07)
	EighthChannel  = byte(0x08)
	NinthChannel   = byte(0x09)
	TenthChannel   = byte(0x10)
)

var priorities = make(map[byte]int)

func init() {
	for _, ch := range defaultTestChannels {
		priorities[ch.ID] = ch.Priority
	}
}

var defaultTestChannels = []*p2p.ChannelDescriptor{
	{
		ID:                  FirstChannel,
		Priority:            1,
		SendQueueCapacity:   1,
		RecvBufferCapacity:  100,
		RecvMessageCapacity: 2000000,
		MessageType:         &protomem.TestTx{},
	},
	{
		ID:                  SecondChannel,
		Priority:            3,
		SendQueueCapacity:   1,
		RecvBufferCapacity:  1000,
		RecvMessageCapacity: 2000000,
		MessageType:         &protomem.TestTx{},
	},
	{
		ID:                  ThirdChannel,
		Priority:            5,
		SendQueueCapacity:   1,
		RecvBufferCapacity:  100,
		RecvMessageCapacity: 2000000,
		MessageType:         &protomem.TestTx{},
	},
	{
		ID:                  FourthChannel,
		Priority:            7,
		SendQueueCapacity:   1,
		RecvBufferCapacity:  100,
		RecvMessageCapacity: 2000000,
		MessageType:         &protomem.TestTx{},
	},
	{
		ID:                  FifthChannel,
		Priority:            9,
		SendQueueCapacity:   1,
		RecvBufferCapacity:  100,
		RecvMessageCapacity: 2000000,
		MessageType:         &protomem.TestTx{},
	},
	{
		ID:                  SixthChannel,
		Priority:            11,
		SendQueueCapacity:   1,
		RecvBufferCapacity:  100,
		RecvMessageCapacity: 2000000,
		MessageType:         &protomem.TestTx{},
	},
	{
		ID:                  SeventhChannel,
		Priority:            13,
		SendQueueCapacity:   100,
		RecvBufferCapacity:  100,
		RecvMessageCapacity: 2000000,
		MessageType:         &protomem.TestTx{},
	},
	{
		ID:                  EighthChannel,
		Priority:            15,
		SendQueueCapacity:   100,
		RecvBufferCapacity:  100,
		RecvMessageCapacity: 200000,
		MessageType:         &protomem.TestTx{},
	},
	{
		ID:                  NinthChannel,
		Priority:            13,
		SendQueueCapacity:   1,
		RecvBufferCapacity:  100,
		RecvMessageCapacity: 2000000,
		MessageType:         &protomem.TestTx{},
	},
	{
		ID:                  TenthChannel,
		Priority:            15,
		SendQueueCapacity:   1,
		RecvBufferCapacity:  100,
		RecvMessageCapacity: 2000000,
		MessageType:         &protomem.TestTx{},
	},
}

var defaultMsgSizes = []int{
	300,
	1000,
	1000,
	100,
	1000,
	1000,
	100,
	100000,
	300,
	1000,
}

// MockReactor represents a mock implementation of the Reactor interface.
type MockReactor struct {
	p2p.BaseReactor
	channels []*conn.ChannelDescriptor
	sizes    map[byte]int

	mtx                     sync.Mutex
	peers                   map[p2p.ID]p2p.Peer
	received                atomic.Int64
	startTime               map[string]time.Time
	cumulativeReceivedBytes map[string]int
	speed                   map[string]float64

	tracer trace.Tracer
}

// NewMockReactor creates a new mock reactor.
func NewMockReactor(channels []*conn.ChannelDescriptor, msgSizes []int) *MockReactor {
	mr := &MockReactor{
		channels:                channels,
		peers:                   make(map[p2p.ID]p2p.Peer),
		sizes:                   make(map[byte]int),
		startTime:               map[string]time.Time{},
		speed:                   map[string]float64{},
		cumulativeReceivedBytes: map[string]int{},
	}
	for i, ch := range channels {
		mr.sizes[ch.ID] = msgSizes[i]
	}
	mr.BaseReactor = *p2p.NewBaseReactor("MockReactor", mr)
	return mr
}

func (mr *MockReactor) SetTracer(tracer trace.Tracer) {
	mr.tracer = tracer
}

// GetChannels implements Reactor.
func (mr *MockReactor) GetChannels() []*conn.ChannelDescriptor {
	return mr.channels
}

// InitPeer implements Reactor.
func (mr *MockReactor) InitPeer(peer p2p.Peer) p2p.Peer {
	// Initialize any data structures related to the peer here.
	// This is a mock implementation, so we'll keep it simple.
	return peer
}

// AddPeer implements Reactor.
func (mr *MockReactor) AddPeer(peer p2p.Peer) {
	mr.mtx.Lock()
	defer mr.mtx.Unlock()
	mr.peers[peer.ID()] = peer
}

// RemovePeer implements Reactor.
func (mr *MockReactor) RemovePeer(peer p2p.Peer, reason interface{}) {
	// Handle the removal of a peer.
	// In this mock implementation, we'll simply log the event.
	mr.Logger.Info("MockReactor removed a peer", "peer", peer.ID(), "reason", reason)
}

func (mr *MockReactor) PrintReceiveSpeed() {
	for _, peer := range mr.peers {
		mr.mtx.Lock()
		cumul := mr.cumulativeReceivedBytes[string(peer.ID())]
		speed := mr.speed[string(peer.ID())]
		mr.mtx.Unlock()
		fmt.Printf("%s: %d bytes received in speed %.2f bytes/s\n", peer.ID(), cumul, speed)
	}
}

// Receive implements Reactor.
func (mr *MockReactor) Receive(chID byte, peer p2p.Peer, msgBytes []byte) {
	fmt.Println("received something")
	mr.mtx.Lock()
	if _, ok := mr.startTime[string(peer.ID())]; !ok {
		mr.startTime[string(peer.ID())] = time.Now()
	}
	mr.cumulativeReceivedBytes[string(peer.ID())] += len(msgBytes)
	mr.speed[string(peer.ID())] = float64(mr.cumulativeReceivedBytes[string(peer.ID())]) / time.Now().Sub(mr.startTime[string(peer.ID())]).Seconds()
	mr.mtx.Unlock()

	msg := &protomem.Message{}
	err := proto.Unmarshal(msgBytes, msg)
	if err != nil {
		fmt.Println("failure to unmarshal")
		// panic(err)
	}
	uw, err := msg.Unwrap()
	if err != nil {
		fmt.Println("failure to unwrap")
		// panic(err)
	}
	mr.ReceiveEnvelope(p2p.Envelope{
		ChannelID: chID,
		Src:       peer,
		Message:   uw,
	})
}

type Payload struct {
	Time time.Time `json:"time"`
	Data string    `json:"data"`
}

// ReceiveEnvelope implements Reactor.
// It processes one of three messages: Txs, SeenTx, WantTx.
func (mr *MockReactor) ReceiveEnvelope(e p2p.Envelope) {
	size := 0
	// Decode JSON bytes back to time.Time
	var (
		start time.Time
		err   error
	)
	switch msg := e.Message.(type) {
	case *protomem.TestTx:
		size = len(msg.Tx)
		start, err = time.Parse(time.RFC3339Nano, msg.StartTime)
	default:
		fmt.Printf("Unexpected message type %T\n", e.Message)
		return
	}
	if err != nil {
		fmt.Println("failure to parse time", err)
		return
	}

	t := time.Now()

	if t.Sub(start) > 1*time.Second {
		fmt.Println("time difference is too large")
	}

	mr.received.Add(int64(size))

	// transit := Transit{
	// 	SendTime:    start,
	// 	ReceiveTime: t,
	// 	Size:        size,
	// 	Channel:     e.ChannelID,
	// }

	// mr.tracer.Write(transit)
}

func (mr *MockReactor) SendBytes(id p2p.ID, chID byte, count int) bool {
	peer, has := mr.peers[id]
	if !has {
		mr.Logger.Error("Peer not found")
		return false
	}

	b := make([]byte, count)
	_, err := rand.Read(b)
	if err != nil {
		mr.Logger.Error("Failed to generate random bytes")
		return false
	}

	txs := &protomem.TestTx{StartTime: time.Now().Format(time.RFC3339Nano), Tx: b}
	return p2p.SendEnvelopeShim(peer, p2p.Envelope{
		Message:   txs,
		ChannelID: chID,
		Src:       peer,
	}, mr.Logger)
}

func (mr *MockReactor) FillChannel(id p2p.ID, chID byte, count, msgSize int) (bool, int, time.Duration) {
	start := time.Now()
	for i := 0; i < count; i++ {
		success := mr.SendBytes(id, chID, msgSize)
		if !success {
			end := time.Now()
			return success, i, end.Sub(start)
		}
	}
	end := time.Now()
	return true, count, end.Sub(start)
}

func (mr *MockReactor) DumpFloodChannel(wg *sync.WaitGroup, id p2p.ID, d, t time.Duration, chIDs ...byte) {
	for _, chID := range chIDs {
		wg.Add(1)
		size := mr.sizes[chID]
		go func(d time.Duration, chID byte, size int) {
			start := time.Now()
			defer wg.Done()
			for time.Since(start) < t {
				subStart := time.Now()
				for time.Since(subStart) < d {
					mr.SendBytes(id, chID, size)
				}
				time.Sleep(d)
			}

		}(d, chID, size)
	}
}

func (mr *MockReactor) FloodChannel(wg *sync.WaitGroup, id p2p.ID, d time.Duration, chIDs ...byte) {
	for _, chID := range chIDs {
		wg.Add(1)
		size := mr.sizes[chID]
		go func(d time.Duration, chID byte, size int) {
			start := time.Now()
			defer wg.Done()
			for time.Since(start) < d {
				mr.SendBytes(id, chID, size)
			}

		}(d, chID, size)
	}
}

func (mr *MockReactor) FloodAllPeers(wg *sync.WaitGroup, d time.Duration, chIDs ...byte) {
	for _, peer := range mr.peers {
		mr.FloodChannel(wg, peer.ID(), d, chIDs...)
	}
}

func (mr *MockReactor) DumpFloodAllPeers(wg *sync.WaitGroup, d, t time.Duration, chIDs ...byte) {
	counter := 0
	for _, peer := range mr.peers {
		mr.DumpFloodChannel(wg, peer.ID(), d, t, chIDs...)
		counter++
	}
}
