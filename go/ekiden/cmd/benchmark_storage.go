package cmd

import (
	"crypto/rand"
	"io"
	"io/ioutil"
	"os"
	"testing"

	"github.com/spf13/cobra"
	"golang.org/x/net/context"

	"github.com/oasislabs/ekiden/go/common/logging"
	"github.com/oasislabs/ekiden/go/epochtime"
	"github.com/oasislabs/ekiden/go/storage"
	storageAPI "github.com/oasislabs/ekiden/go/storage/api"
)

var (
	benchmarkStorageCmd = &cobra.Command{
		Use:   "benchmark-storage",
		Short: "benchmark storage backend",
		Run:   benchmarkStorage,
	}
)

func benchmarkStorage(cmd *cobra.Command, args []string) {
	initCommon()

	logger := logging.GetLogger("benchmark-storage")

	// Initialize the data directory.
	if dataDir == "" {
		var err error
		dataDir, err = ioutil.TempDir("", "benchmark-storage")
		if err != nil {
			logger.Error("failed to initialize data directory",
				"err", err,
			)
			return
		}

		logger.Debug("using temporary data directory",
			"data_dir", dataDir,
		)

		defer os.RemoveAll(dataDir)
	}

	// Initialize the various backends.
	timeSource, err := epochtime.New(cmd, nil)
	if err != nil {
		logger.Error("failed to initialize time source",
			"err", err,
		)
		return
	}
	storage, err := storage.New(cmd, timeSource, dataDir)
	if err != nil {
		logger.Error("failed to initialize storage",
			"err", err,
		)
		return
	}
	defer storage.Cleanup()

	// Wait for storage initialization.
	<-storage.Initialized()

	for _, sz := range []int{
		256, 512, 1024, 4096, 8192, 16384, 32768,
	} {
		buf := make([]byte, sz)

		// Insert.
		res := testing.Benchmark(func(b *testing.B) {
			b.SetBytes(int64(sz))
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				_, _ = io.ReadFull(rand.Reader, buf)
				b.StartTimer()

				if err := storage.Insert(context.Background(), buf, 9001); err != nil {
					b.Fatalf("failed to Insert(): %v", err)
				}
			}
		})
		logger.Info("Insert",
			"sz", sz,
			"ns_per_op", res.NsPerOp(),
		)

		// Get.
		key := storageAPI.HashStorageKey(buf)
		res = testing.Benchmark(func(b *testing.B) {
			b.SetBytes(int64(sz))
			for i := 0; i < b.N; i++ {
				if _, err := storage.Get(context.Background(), key); err != nil {
					b.Fatalf("failed to Get(): %v", err)
				}
			}
		})
		logger.Info("Get",
			"sz", sz,
			"ns_per_op", res.NsPerOp(),
		)
	}

	// PurgeExpired.
	sweeper, ok := storage.(storageAPI.SweepableBackend)
	if !ok {
		return
	}
	res := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sweeper.PurgeExpired(0)
		}
	})
	logger.Info("PurgeExpired (none purged)",
		"ns_per_op", res.NsPerOp(),
	)
}

func init() {
	// XXX: Flags

	for _, v := range []func(*cobra.Command){
		epochtime.RegisterFlags,
		storage.RegisterFlags,
	} {
		v(benchmarkStorageCmd)
	}

	rootCmd.AddCommand(benchmarkStorageCmd)
}
