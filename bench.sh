#/bin/bash

# bench.sh and parse_bench.py are used to benchmark specific e2e test using
# various execution parameters and parse the results.
# Accepted environmental variables are:
# - GS: executor group size,
# - RT: number of runtimes used,
# - W: number of compute workers used.
#
# Because of syncing and flushing caches, you need to run this script as root.
# For example:
# sudo GS=2 RT=4 W=2 ./bench.sh multiple-runtimes
#
# If no environmental variables are set, this script benchmarks all GS and RT
# combinations from 1..128 in power of 2. W will equal GS for each run.

set -x

if [ "$#" -ne 1 ]
then
	echo "Usage: $0 <TEST NAME>"
	exit 1
fi

if [ "$USER" != "root" ]
then
       echo "This script should be run as root."
       exit 2
fi

BENCH_RESULTS_DIR=bench-results
TEST="$1"

cleanup() {
    killall -q mpstat
    killall -q iostat
    killall -q sar
}

trap cleanup EXIT
trap "exit" INT

mkdir -p bench-results

# Environmental variables GS and RT.
GROUP_SIZES=$GS
RUNTIMES=$RT
if [ -z "$GROUP_SIZES" ]
then
        GROUP_SIZES="1 2 3 4 6 8"
fi
if [ -z "$RUNTIMES" ]
then
        RUNTIMES="1 2 3 4 5 6"
fi

for G in $GROUP_SIZES; do
  for R in $RUNTIMES; do
    if [ -z "$W" ]
    then
      WORKERS=$G
    else
      WORKERS=$W
    fi

    echo "BENCHMARK: Executing test $TEST with executor group size $G, runtime count $R, and compute workers $WORKERS..."
    BENCH_INFO=GS_$G.RT_$R.W_$WORKERS

    # Flush all to disk, clear any caches.
    sync; echo 3 > /proc/sys/vm/drop_caches

    mpstat -P ALL 1 >$BENCH_RESULTS_DIR/$TEST.$BENCH_INFO.mpstat &
    iostat -d /dev/sda -x 1 >$BENCH_RESULTS_DIR/$TEST.$BENCH_INFO.iostat &
    sar -r 1 >$BENCH_RESULTS_DIR/$TEST.$BENCH_INFO.sar &
    

    EXECUTOR_GROUP_SIZE=$G \
     COMPUTE_RUNTIMES=$R \
     COMPUTE_WORKERS=$WORKERS \
      /usr/bin/time -o $BENCH_RESULTS_DIR/$TEST.$BENCH_INFO.time \
      .buildkite/scripts/test_e2e.sh -t $TEST

    cleanup
  done
done
