// Package badger provides a Badger-backed node database.
package badger

import (
	"github.com/dgraph-io/badger"
	"github.com/pkg/errors"

	"github.com/oasislabs/ekiden/go/common/crypto/hash"
	"github.com/oasislabs/ekiden/go/common/logging"
	"github.com/oasislabs/ekiden/go/storage/mkvs/urkel/db/api"
	"github.com/oasislabs/ekiden/go/storage/mkvs/urkel/node"
)

var (
	nodeKeyPrefix = []byte{'N'}
)

// New creates a new BadgerDB-backed node database.
func New(opts badger.Options) (api.NodeDB, error) {
	db := &badgerNodeDB{
		logger: logging.GetLogger("urkel/db/badger"),
	}

	var err error
	if db.db, err = badger.Open(opts); err != nil {
		return nil, errors.Wrap(err, "urkel/db/badger: failed to open database")
	}

	return db, nil
}

type badgerNodeDB struct {
	logger *logging.Logger

	db *badger.DB
}

func (d *badgerNodeDB) GetNode(root hash.Hash, ptr *node.Pointer) (node.Node, error) {
	if ptr == nil || !ptr.IsClean() {
		panic("urkel/db/badger: attempted to get invalid pointer from node database")
	}

	tx := d.db.NewTransaction(false)
	defer tx.Discard()
	item, err := tx.Get(append(nodeKeyPrefix, ptr.Hash[:]...))
	switch err {
	case nil:
	case badger.ErrKeyNotFound:
		return nil, api.ErrNodeNotFound
	default:
		d.logger.Error("failed to Get node from backing store",
			"err", err,
		)
		return nil, errors.Wrap(err, "urkel/db/badger: failed to Get node from backing store")
	}

	var n node.Node
	if err = item.Value(func(val []byte) error {
		var vErr error
		n, vErr = node.UnmarshalBinary(val)
		return vErr
	}); err != nil {
		d.logger.Error("failed to unmarshal node",
			"err", err,
		)
		return nil, errors.Wrap(err, "urkel/db/badger: failed to unmarshal node")
	}

	return n, nil
}

func (d *badgerNodeDB) NewBatch() api.Batch {
	// WARNING: There is a maximum batch size and maximum batch entry count.
	// Both of these things are derived from the MaxTableSize option.
	//
	// The size limit also applies to normal transactions, so the "right"
	// thing to do would be to either crank up MaxTableSize or maybe split
	// the transaction out.

	return &badgerBatch{
		bat: d.db.NewWriteBatch(),
	}
}

func (d *badgerNodeDB) Close() {
	if err := d.db.Close(); err != nil {
		d.logger.Error("close returned error",
			"err", err,
		)
	}
}

type badgerBatch struct {
	api.BaseBatch

	bat *badger.WriteBatch
}

func (ba *badgerBatch) MaybeStartSubtree(subtree api.Subtree, depth uint8, subtreeRoot *node.Pointer) api.Subtree {
	if subtree == nil {
		return &badgerSubtree{batch: ba}
	}
	return subtree
}

func (ba *badgerBatch) Commit(root hash.Hash) error {
	if err := ba.bat.Flush(); err != nil {
		return err
	}

	return ba.BaseBatch.Commit(root)
}

func (ba *badgerBatch) Reset() {
	ba.bat.Cancel()
}

type badgerSubtree struct {
	batch *badgerBatch
}

func (s *badgerSubtree) PutNode(depth uint8, ptr *node.Pointer) error {
	data, err := ptr.Node.MarshalBinary()
	if err != nil {
		return err
	}

	h := ptr.Node.GetHash()
	if err = s.batch.bat.Set(append(nodeKeyPrefix, h[:]...), data); err != nil {
		return err
	}
	return nil
}

func (s *badgerSubtree) VisitCleanNode(depth uint8, ptr *node.Pointer) error {
	return nil
}

func (s *badgerSubtree) Commit() error {
	return nil
}