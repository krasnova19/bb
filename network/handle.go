package network

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/MixinNetwork/mixin/common"
	"github.com/MixinNetwork/mixin/config"
	"github.com/MixinNetwork/mixin/crypto"
	"github.com/VictoriaMetrics/fastcache"
)

const (
	PeerMessageTypePing               = 1
	PeerMessageTypeAuthentication     = 3
	PeerMessageTypeGraph              = 4
	PeerMessageTypeSnapshotConfirm    = 5
	PeerMessageTypeTransactionRequest = 6
	PeerMessageTypeTransaction        = 7

	PeerMessageTypeSnapshotAnnoucement  = 10 // leader send snapshot to peer
	PeerMessageTypeSnapshotCommitment   = 11 // peer generate ri based, send Ri to leader
	PeerMessageTypeTransactionChallenge = 12 // leader send bitmask Z and aggragated R to peer
	PeerMessageTypeSnapshotResponse     = 13 // peer generate A from nodes and Z, send response si = ri + H(R || A || M)ai to leader
	PeerMessageTypeSnapshotFinalization = 14 // leader generate A, verify si B = ri B + H(R || A || M)ai B = Ri + H(R || A || M)Ai, then finaliz based on threshold
)

type PeerMessage struct {
	Type            uint8
	Snapshot        *common.Snapshot
	SnapshotHash    crypto.Hash
	Transaction     *common.VersionedTransaction
	TransactionHash crypto.Hash
	Cosi            crypto.CosiSignature
	Commitment      crypto.Key
	Response        [32]byte
	WantTx          bool
	FinalCache      []*SyncPoint
	Auth            []byte
}

type SyncHandle interface {
	GetCacheStore() *fastcache.Cache
	BuildAuthenticationMessage() []byte
	Authenticate(msg []byte) (crypto.Hash, string, error)
	BuildGraph() []*SyncPoint
	UpdateSyncPoint(peerId crypto.Hash, points []*SyncPoint)
	ReadAllNodes() []crypto.Hash
	ReadSnapshotsSinceTopology(offset, count uint64) ([]*common.SnapshotWithTopologicalOrder, error)
	ReadSnapshotsForNodeRound(nodeIdWithNetwork crypto.Hash, round uint64) ([]*common.SnapshotWithTopologicalOrder, error)
	SendTransactionToPeer(peerId, tx crypto.Hash) error
	CachePutTransaction(peerId crypto.Hash, ver *common.VersionedTransaction) error
	CosiQueueExternalAnnouncement(peerId crypto.Hash, s *common.Snapshot, R *crypto.Key) error
	CosiAggregateSelfCommitments(peerId crypto.Hash, snap crypto.Hash, commitment *crypto.Key, wantTx bool) error
	CosiQueueExternalChallenge(peerId crypto.Hash, snap crypto.Hash, cosi *crypto.CosiSignature, ver *common.VersionedTransaction) error
	CosiAggregateSelfResponses(peerId crypto.Hash, snap crypto.Hash, response *[32]byte) error
	VerifyAndQueueAppendSnapshotFinalization(peerId crypto.Hash, s *common.Snapshot) error
}

func (me *Peer) SendSnapshotAnnouncementMessage(idForNetwork crypto.Hash, s *common.Snapshot, R crypto.Key) error {
	data := buildSnapshotAnnouncementMessage(s, R)
	return me.sendSnapshotMessagetoPeer(idForNetwork, s.PayloadHash(), PeerMessageTypeSnapshotAnnoucement, data)
}

func (me *Peer) SendSnapshotCommitmentMessage(idForNetwork crypto.Hash, snap crypto.Hash, R crypto.Key, wantTx bool) error {
	data := buildSnapshotCommitmentMessage(snap, R, wantTx)
	return me.sendSnapshotMessagetoPeer(idForNetwork, snap, PeerMessageTypeSnapshotCommitment, data)
}

func (me *Peer) SendTransactionChallengeMessage(idForNetwork crypto.Hash, snap crypto.Hash, cosi *crypto.CosiSignature, tx *common.VersionedTransaction) error {
	data := buildTransactionChallengeMessage(snap, cosi, tx)
	return me.sendSnapshotMessagetoPeer(idForNetwork, snap, PeerMessageTypeTransactionChallenge, data)
}

func (me *Peer) SendSnapshotResponseMessage(idForNetwork crypto.Hash, snap crypto.Hash, si [32]byte) error {
	data := buildSnapshotResponseMessage(snap, si)
	return me.sendSnapshotMessagetoPeer(idForNetwork, snap, PeerMessageTypeSnapshotResponse, data)
}

func (me *Peer) SendSnapshotFinalizationMessage(idForNetwork crypto.Hash, s *common.Snapshot) error {
	if idForNetwork == me.IdForNetwork {
		return nil
	}

	hash := s.PayloadHash().ForNetwork(idForNetwork)
	key := crypto.NewHash(append(hash[:], 'S', 'C', 'O'))
	if me.snapshotsCaches.contains(key, config.Custom.CacheTTL*time.Second/2) {
		return nil
	}

	data := buildSnapshotFinalizationMessage(s)
	return me.sendSnapshotMessagetoPeer(idForNetwork, s.PayloadHash(), PeerMessageTypeSnapshotFinalization, data)
}

func (me *Peer) SendSnapshotConfirmMessage(idForNetwork crypto.Hash, snap crypto.Hash) error {
	key := snap.ForNetwork(idForNetwork)
	key = crypto.NewHash(append(key[:], 'S', 'N', 'A', 'P', PeerMessageTypeSnapshotConfirm))
	return me.sendHighToPeer(idForNetwork, key, buildSnapshotConfirmMessage(snap))
}

func (me *Peer) SendTransactionRequestMessage(idForNetwork crypto.Hash, tx crypto.Hash) error {
	key := tx.ForNetwork(idForNetwork)
	key = crypto.NewHash(append(key[:], 'T', 'X', PeerMessageTypeTransactionRequest))
	return me.sendHighToPeer(idForNetwork, key, buildTransactionRequestMessage(tx))
}

func (me *Peer) SendTransactionMessage(idForNetwork crypto.Hash, ver *common.VersionedTransaction) error {
	key := ver.PayloadHash().ForNetwork(idForNetwork)
	key = crypto.NewHash(append(key[:], 'T', 'X', PeerMessageTypeTransaction))
	return me.sendHighToPeer(idForNetwork, key, buildTransactionMessage(ver))
}

func (me *Peer) ConfirmSnapshotForPeer(idForNetwork, snap crypto.Hash) {
	hash := snap.ForNetwork(idForNetwork)
	key := crypto.NewHash(append(hash[:], 'S', 'C', 'O'))
	me.snapshotsCaches.store(key, time.Now())
}

func buildAuthenticationMessage(data []byte) []byte {
	header := []byte{PeerMessageTypeAuthentication}
	return append(header, data...)
}

func buildPingMessage() []byte {
	return []byte{PeerMessageTypePing}
}

func buildSnapshotAnnouncementMessage(s *common.Snapshot, R crypto.Key) []byte {
	data := common.MsgpackMarshalPanic(s)
	data = append(R[:], data...)
	return append([]byte{PeerMessageTypeSnapshotAnnoucement}, data...)
}

func buildSnapshotCommitmentMessage(snap crypto.Hash, R crypto.Key, wantTx bool) []byte {
	data := []byte{PeerMessageTypeSnapshotCommitment}
	data = append(data, snap[:]...)
	data = append(data, R[:]...)
	if wantTx {
		return append(data, byte(1))
	}
	return append(data, byte(0))
}

func buildTransactionChallengeMessage(snap crypto.Hash, cosi *crypto.CosiSignature, tx *common.VersionedTransaction) []byte {
	mask := make([]byte, 8)
	binary.BigEndian.PutUint64(mask, cosi.Mask)
	data := []byte{PeerMessageTypeTransactionChallenge}
	data = append(data, snap[:]...)
	data = append(data, cosi.Signature[:]...)
	data = append(data, mask...)
	if tx != nil {
		pl := tx.Marshal()
		return append(data, pl...)
	}
	return data
}

func buildSnapshotResponseMessage(snap crypto.Hash, si [32]byte) []byte {
	data := []byte{PeerMessageTypeSnapshotResponse}
	data = append(data, snap[:]...)
	return append(data, si[:]...)
}

func buildSnapshotFinalizationMessage(s *common.Snapshot) []byte {
	data := common.MsgpackMarshalPanic(s)
	return append([]byte{PeerMessageTypeSnapshotFinalization}, data...)
}

func buildSnapshotConfirmMessage(snap crypto.Hash) []byte {
	return append([]byte{PeerMessageTypeSnapshotConfirm}, snap[:]...)
}

func buildTransactionMessage(ver *common.VersionedTransaction) []byte {
	data := ver.Marshal()
	return append([]byte{PeerMessageTypeTransaction}, data...)
}

func buildTransactionRequestMessage(tx crypto.Hash) []byte {
	return append([]byte{PeerMessageTypeTransactionRequest}, tx[:]...)
}

func buildGraphMessage(points []*SyncPoint) []byte {
	data := common.MsgpackMarshalPanic(points)
	return append([]byte{PeerMessageTypeGraph}, data...)
}

func parseNetworkMessage(data []byte) (*PeerMessage, error) {
	if len(data) < 1 {
		return nil, errors.New("invalid message data")
	}
	msg := &PeerMessage{Type: data[0]}
	switch msg.Type {
	case PeerMessageTypeGraph:
		err := common.MsgpackUnmarshal(data[1:], &msg.FinalCache)
		if err != nil {
			return nil, err
		}
	case PeerMessageTypePing:
	case PeerMessageTypeAuthentication:
		msg.Auth = data[1:]
	case PeerMessageTypeSnapshotConfirm:
		copy(msg.SnapshotHash[:], data[1:])
	case PeerMessageTypeTransaction:
		ver, err := common.UnmarshalVersionedTransaction(data[1:])
		if err != nil {
			return nil, err
		}
		msg.Transaction = ver
	case PeerMessageTypeTransactionRequest:
		copy(msg.TransactionHash[:], data[1:])
	case PeerMessageTypeSnapshotAnnoucement:
		if len(data[1:]) <= 32 {
			return nil, fmt.Errorf("invalid announcement message size %d", len(data[1:]))
		}
		copy(msg.Commitment[:], data[1:])
		err := common.MsgpackUnmarshal(data[33:], &msg.Snapshot)
		if err != nil {
			return nil, err
		}
	case PeerMessageTypeSnapshotCommitment:
		if len(data[1:]) != 65 {
			return nil, fmt.Errorf("invalid commitment message size %d", len(data[1:]))
		}
		copy(msg.SnapshotHash[:], data[1:])
		copy(msg.Commitment[:], data[33:])
		msg.WantTx = data[65] == 1
	case PeerMessageTypeTransactionChallenge:
		if len(data[1:]) < 104 {
			return nil, fmt.Errorf("invalid challenge message size %d", len(data[1:]))
		}
		copy(msg.SnapshotHash[:], data[1:])
		copy(msg.Cosi.Signature[:], data[33:])
		msg.Cosi.Mask = binary.BigEndian.Uint64(data[97:105])
		if len(data[1:]) > 104 {
			ver, err := common.UnmarshalVersionedTransaction(data[105:])
			if err != nil {
				return nil, err
			}
			msg.Transaction = ver
		}
	case PeerMessageTypeSnapshotResponse:
		if len(data[1:]) != 64 {
			return nil, fmt.Errorf("invalid response message size %d", len(data[1:]))
		}
		copy(msg.SnapshotHash[:], data[1:])
		copy(msg.Response[:], data[33:])
	case PeerMessageTypeSnapshotFinalization:
		err := common.MsgpackUnmarshal(data[1:], &msg.Snapshot)
		if err != nil {
			return nil, err
		}
	}
	return msg, nil
}

func (me *Peer) handlePeerMessage(peer *Peer, receive chan *PeerMessage, done chan bool) {
	for {
		select {
		case <-done:
			return
		case msg := <-receive:
			switch msg.Type {
			case PeerMessageTypePing:
			case PeerMessageTypeGraph:
				me.handle.UpdateSyncPoint(peer.IdForNetwork, msg.FinalCache)
				peer.sync <- msg.FinalCache
			case PeerMessageTypeTransactionRequest:
				me.handle.SendTransactionToPeer(peer.IdForNetwork, msg.TransactionHash)
			case PeerMessageTypeTransaction:
				me.handle.CachePutTransaction(peer.IdForNetwork, msg.Transaction)
			case PeerMessageTypeSnapshotConfirm:
				me.ConfirmSnapshotForPeer(peer.IdForNetwork, msg.SnapshotHash)
			case PeerMessageTypeSnapshotAnnoucement:
				me.handle.CosiQueueExternalAnnouncement(peer.IdForNetwork, msg.Snapshot, &msg.Commitment)
			case PeerMessageTypeSnapshotCommitment:
				me.handle.CosiAggregateSelfCommitments(peer.IdForNetwork, msg.SnapshotHash, &msg.Commitment, msg.WantTx)
			case PeerMessageTypeTransactionChallenge:
				me.handle.CosiQueueExternalChallenge(peer.IdForNetwork, msg.SnapshotHash, &msg.Cosi, msg.Transaction)
			case PeerMessageTypeSnapshotResponse:
				me.handle.CosiAggregateSelfResponses(peer.IdForNetwork, msg.SnapshotHash, &msg.Response)
			case PeerMessageTypeSnapshotFinalization:
				me.handle.VerifyAndQueueAppendSnapshotFinalization(peer.IdForNetwork, msg.Snapshot)
			}
		}
	}
}
