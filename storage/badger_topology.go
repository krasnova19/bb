package storage

import (
	"encoding/binary"

	"github.com/MixinNetwork/mixin/common"
	"github.com/MixinNetwork/mixin/crypto"
	"github.com/dgraph-io/badger"
)

func (s *BadgerStore) ReadSnapshot(hash crypto.Hash) (*common.SnapshotWithTopologicalOrder, error) {
	txn := s.snapshotsDB.NewTransaction(false)
	defer txn.Discard()

	return readSnapshotWithTopo(txn, hash)
}

func readSnapshotWithTopo(txn *badger.Txn, hash crypto.Hash) (*common.SnapshotWithTopologicalOrder, error) {
	item, err := txn.Get(graphSnapTopologyKey(hash))
	if err != nil {
		return nil, err
	}
	topo, err := item.ValueCopy(nil)
	if err != nil {
		return nil, err
	}

	item, err = txn.Get(topo)
	if err != nil {
		return nil, err
	}
	key, err := item.ValueCopy(nil)
	if err != nil {
		return nil, err
	}

	item, err = txn.Get(key)
	if err != nil {
		return nil, err
	}
	v, err := item.ValueCopy(nil)
	if err != nil {
		return nil, err
	}
	var snap common.SnapshotWithTopologicalOrder
	err = common.DecompressMsgpackUnmarshal(v, &snap)
	if err != nil {
		return nil, err
	}
	snap.Hash = hash
	snap.TopologicalOrder = graphTopologyOrder(topo)
	return &snap, nil
}

func (s *BadgerStore) ReadSnapshotWithTransactionsSinceTopology(topologyOffset, count uint64) ([]*common.SnapshotWithTopologicalOrder, []*common.VersionedTransaction, error) {
	snapshots, err := s.ReadSnapshotsSinceTopology(topologyOffset, count)
	if err != nil {
		return nil, nil, err
	}

	transactions := make([]*common.VersionedTransaction, len(snapshots))
	txn := s.snapshotsDB.NewTransaction(false)
	defer txn.Discard()

	for i, s := range snapshots {
		tx, err := readTransaction(txn, s.Transaction)
		if err != nil {
			return nil, nil, err
		}
		transactions[i] = tx
	}
	return snapshots, transactions, nil
}

func (s *BadgerStore) ReadSnapshotsSinceTopology(topologyOffset, count uint64) ([]*common.SnapshotWithTopologicalOrder, error) {
	snapshots := make([]*common.SnapshotWithTopologicalOrder, 0)
	txn := s.snapshotsDB.NewTransaction(false)
	defer txn.Discard()
	it := txn.NewIterator(badger.DefaultIteratorOptions)
	defer it.Close()

	prefix := []byte(graphPrefixTopology)
	it.Seek(graphTopologyKey(topologyOffset))
	for ; it.ValidForPrefix(prefix) && uint64(len(snapshots)) < count; it.Next() {
		item := it.Item()
		v, err := item.ValueCopy(nil)
		if err != nil {
			return snapshots, err
		}
		topology := graphTopologyOrder(item.Key())
		item, err = txn.Get(v)
		if err != nil {
			return snapshots, err
		}
		v, err = item.ValueCopy(nil)
		if err != nil {
			return snapshots, err
		}
		var snap common.SnapshotWithTopologicalOrder
		err = common.DecompressMsgpackUnmarshal(v, &snap)
		if err != nil {
			return snapshots, err
		}
		snap.Hash = snap.PayloadHash()
		snap.TopologicalOrder = topology
		snapshots = append(snapshots, &snap)
	}

	return snapshots, nil
}

func (s *BadgerStore) TopologySequence() uint64 {
	var sequence uint64

	txn := s.snapshotsDB.NewTransaction(false)
	defer txn.Discard()

	opts := badger.DefaultIteratorOptions
	opts.PrefetchValues = false
	opts.Reverse = true

	it := txn.NewIterator(opts)
	defer it.Close()

	it.Seek(graphTopologyKey(^uint64(0)))
	if it.ValidForPrefix([]byte(graphPrefixTopology)) {
		item := it.Item()
		sequence = graphTopologyOrder(item.Key()) + 1
	}
	return sequence
}

func writeTopology(txn *badger.Txn, snap *common.SnapshotWithTopologicalOrder) error {
	key := graphTopologyKey(snap.TopologicalOrder)
	val := graphSnapshotKey(snap.NodeId, snap.RoundNumber, snap.Transaction)
	err := txn.Set(key, val[:])
	if err != nil {
		return err
	}

	return txn.Set(graphSnapTopologyKey(snap.PayloadHash()), key)
}

func graphSnapTopologyKey(hash crypto.Hash) []byte {
	return append([]byte(graphPrefixSnapTopology), hash[:]...)
}

func graphTopologyKey(order uint64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, order)
	return append([]byte(graphPrefixTopology), buf...)
}

func graphTopologyOrder(key []byte) uint64 {
	order := key[len(graphPrefixTopology):]
	return binary.BigEndian.Uint64(order)
}
