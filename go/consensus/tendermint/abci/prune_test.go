package abci

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oasislabs/oasis-core/go/common"
	"github.com/oasislabs/oasis-core/go/common/crypto/hash"
	mkvs "github.com/oasislabs/oasis-core/go/storage/mkvs/urkel"
	mkvsDB "github.com/oasislabs/oasis-core/go/storage/mkvs/urkel/db/api"
	mkvsBadgerDB "github.com/oasislabs/oasis-core/go/storage/mkvs/urkel/db/badger"
)

func TestPruneKeepN(t *testing.T) {
	require := require.New(t)

	// Create a new random temporary directory under /tmp.
	dir, err := ioutil.TempDir("", "abci-prune.test.badger")
	require.NoError(err, "TempDir")
	defer os.RemoveAll(dir)

	// Create a Badger-backed Node DB.
	ndb, err := mkvsBadgerDB.New(&mkvsDB.Config{
		DB:           dir,
		NoFsync:      true,
		MaxCacheSize: 16 * 1024 * 1024,
	})
	require.NoError(err, "New")
	tree := mkvs.New(nil, ndb)

	ctx := context.Background()
	for i := uint64(1); i <= 11; i++ {
		err = tree.Insert(ctx, []byte(fmt.Sprintf("key:%d", i)), []byte(fmt.Sprintf("value:%d", i)))
		require.NoError(err, "Insert")

		var rootHash hash.Hash
		_, rootHash, err = tree.Commit(ctx, common.Namespace{}, i)
		require.NoError(err, "Commit")
		err = ndb.Finalize(ctx, i, []hash.Hash{rootHash})
		require.NoError(err, "Finalize")
	}

	earliestVersion, err := ndb.GetEarliestRound(ctx)
	require.NoError(err, "GetEarliestRound")
	require.EqualValues(1, earliestVersion, "earliest version should be correct")
	latestVersion, err := ndb.GetLatestRound(ctx)
	require.NoError(err, "GetLatestRound")
	require.EqualValues(11, latestVersion, "latest version should be correct")

	pruner, err := newStatePruner(&PruneConfig{
		Strategy: PruneKeepN,
		NumKept:  2,
	}, ndb, 10)
	require.NoError(err, "newStatePruner failed")

	earliestVersion, err = ndb.GetEarliestRound(ctx)
	require.NoError(err, "GetEarliestRound")
	require.EqualValues(8, earliestVersion, "earliest version should be correct")
	latestVersion, err = ndb.GetLatestRound(ctx)
	require.NoError(err, "GetLatestRound")
	require.EqualValues(11, latestVersion, "latest version should be correct")

	err = pruner.Prune(ctx, 11)
	require.NoError(err, "Prune")

	earliestVersion, err = ndb.GetEarliestRound(ctx)
	require.NoError(err, "GetEarliestRound")
	require.EqualValues(9, earliestVersion, "earliest version should be correct")
	latestVersion, err = ndb.GetLatestRound(ctx)
	require.NoError(err, "GetLatestRound")
	require.EqualValues(11, latestVersion, "latest version should be correct")
}
