// Package cmd implements the commands for the test-runner executable.
package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
	"github.com/spf13/cobra"
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/oasislabs/oasis-core/go/common/logging"
	"github.com/oasislabs/oasis-core/go/common/version"
	"github.com/oasislabs/oasis-core/go/oasis-node/cmd/common"
	cmdFlags "github.com/oasislabs/oasis-core/go/oasis-node/cmd/common/flags"
	"github.com/oasislabs/oasis-core/go/oasis-node/cmd/common/metrics"
	"github.com/oasislabs/oasis-core/go/oasis-test-runner/env"
	"github.com/oasislabs/oasis-core/go/oasis-test-runner/oasis"
	"github.com/oasislabs/oasis-core/go/oasis-test-runner/scenario"
)

const (
	cfgConfigFile       = "config"
	cfgLogFmt           = "log.format"
	cfgLogLevel         = "log.level"
	cfgLogNoStdout      = "log.no_stdout"
	cfgNumRuns          = "num_runs"
	cfgTest             = "test"
	cfgParallelJobCount = "parallel.job_count"
	cfgParallelJobIndex = "parallel.job_index"
)

var (
	rootCmd = &cobra.Command{
		Use:     "oasis-test-runner",
		Short:   "Oasis Test Runner",
		Version: version.SoftwareVersion,
		RunE:    runRoot,
	}

	listCmd = &cobra.Command{
		Use:   "list",
		Short: "List registered test cases",
		Run:   runList,
	}

	rootFlags = flag.NewFlagSet("", flag.ContinueOnError)

	cfgFile string
	numRuns int

	scenarioMap      = make(map[string]scenario.Scenario)
	defaultScenarios []scenario.Scenario
	scenarios        []scenario.Scenario

	// oasis-test-runner-specific metrics.
	upGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "up",
			Help: "Is oasis-test-runner test active",
		},
	)

	oasisTestRunnerCollectors = []prometheus.Collector{
		upGauge,
	}

	pusher              *push.Pusher
	oasisTestRunnerOnce sync.Once
)

// RootCmd returns the root command's structure that will be executed, so that
// it can be used to alter the configuration and flags of the command.
//
// Note: `Run` is pre-initialized to the main entry point of the test harness,
// and should likely be left un-altered.
func RootCmd() *cobra.Command {
	return rootCmd
}

// Execute spawns the main entry point after handing the config file.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		common.EarlyLogAndExit(err)
	}
}

// RegisterNondefault adds a scenario to the runner.
func RegisterNondefault(s scenario.Scenario) error {
	n := strings.ToLower(s.Name())
	if _, ok := scenarioMap[n]; ok {
		return errors.New("root: scenario already registered: " + n)
	}

	scenarioMap[n] = s
	scenarios = append(scenarios, s)

	params := s.Parameters()
	if len(params) > 0 {
		for k, v := range scenario.ParametersToStringMap(params) {
			// Re-register rootFlags for test parameters.
			rootFlags.StringSlice("params."+n+"."+k, []string{v}, "")
			rootCmd.PersistentFlags().AddFlagSet(rootFlags)
			_ = viper.BindPFlag("params."+n+"."+k, rootFlags.Lookup("params."+n+"."+k))
		}
	}

	return nil
}

// parseTestParams parses --params.<test_name>.<key1>=<val1>,<val2>... flags combinations, clones provided proto-
// scenarios, and populates them so that each scenario instance has unique paramater set. Returns mapping test name ->
// list of scenario instances.
func parseTestParams(toRun []scenario.Scenario) (map[string][]scenario.Scenario, error) {
	r := make(map[string][]scenario.Scenario)
	for _, s := range toRun {
		zippedParams := make(map[string][]string)
		for k := range s.Parameters() {
			userVal := viper.GetStringSlice("params." + s.Name() + "." + k)
			if userVal == nil {
				continue
			}
			zippedParams[k] = userVal
		}

		parameterSets := computeParamSets(zippedParams, map[string]string{})

		// For each parameter set combination, clone a scenario and apply user-provided parameter value.
		for _, ps := range parameterSets {
			sCloned := s.Clone()
			for k, userVal := range ps {
				v := sCloned.Parameters()[k]
				switch v := v.(type) {
				case *int:
					val, err := strconv.ParseInt(userVal, 10, 32)
					if err != nil {
						return nil, err
					}
					*v = int(val)
				case *int64:
					val, err := strconv.ParseInt(userVal, 10, 64)
					if err != nil {
						return nil, err
					}
					*v = val
				case *float64:
					val, err := strconv.ParseFloat(userVal, 64)
					if err != nil {
						return nil, err
					}
					*v = val
				case *bool:
					val, err := strconv.ParseBool(userVal)
					if err != nil {
						return nil, err
					}
					*v = val
				case *string:
					*v = userVal
				default:
					return nil, errors.New(fmt.Sprintf("cannot parse parameter. Unknown type %v", v))
				}
			}
			r[s.Name()] = append(r[s.Name()], sCloned)
		}

		// No parameters provided over CLI, keep a single copy.
		if len(parameterSets) == 0 {
			r[s.Name()] = []scenario.Scenario{s}
		}
	}

	return r, nil
}

// computeParamSets recursively combines a map of string slices into all possible key=>value parameter sets.
func computeParamSets(zp map[string][]string, ps map[string]string) []map[string]string {
	// Recursion stops when zp is empty.
	if len(zp) == 0 {
		// XXX: How do I clone a map in golang?
		psCloned := map[string]string{}
		for k, v := range ps {
			psCloned[k] = v
		}
		return []map[string]string{psCloned}
	}

	rps := []map[string]string{}

	// XXX: How do I clone a map in golang?
	zpCloned := map[string][]string{}
	for k, v := range zp {
		zpCloned[k] = v
	}
	// Take first element from cloned zp and do recursion.
	for k, vals := range zpCloned {
		delete(zpCloned, k)
		for _, v := range vals {
			ps[k] = v
			rps = append(rps, computeParamSets(zpCloned, ps)...)
		}
		break
	}

	return rps
}

// Register adds a scenario to the runner and the default scenarios list.
func Register(scenario scenario.Scenario) error {
	if err := RegisterNondefault(scenario); err != nil {
		return err
	}

	defaultScenarios = append(defaultScenarios, scenario)
	return nil
}

func initRootEnv(cmd *cobra.Command) (*env.Env, error) {
	// Initialize the root dir.
	rootDir := env.GetRootDir()
	if err := rootDir.Init(cmd); err != nil {
		return nil, err
	}
	env := env.New(rootDir)

	var ok bool
	defer func() {
		if !ok {
			env.Cleanup()
		}
	}()

	var logFmt logging.Format
	if err := logFmt.Set(viper.GetString(cfgLogFmt)); err != nil {
		return nil, errors.Wrap(err, "root: failed to set log format")
	}

	var logLevel logging.Level
	if err := logLevel.Set(viper.GetString(cfgLogLevel)); err != nil {
		return nil, errors.Wrap(err, "root: failed to set log level")
	}

	// Initialize logging.
	logFile := filepath.Join(env.Dir(), "test-runner.log")
	w, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, errors.Wrap(err, "root: failed to open log file")
	}

	var logWriter io.Writer = w
	if !viper.GetBool(cfgLogNoStdout) {
		logWriter = io.MultiWriter(os.Stdout, w)
	}
	if err := logging.Initialize(logWriter, logFmt, logLevel, nil); err != nil {
		return nil, errors.Wrap(err, "root: failed to initialize logging")
	}

	ok = true
	return env, nil
}

func runRoot(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true

	if viper.GetString(metrics.CfgMetricsAddr) != "" {
		oasisTestRunnerOnce.Do(func() {
			prometheus.MustRegister(oasisTestRunnerCollectors...)
		})
	}

	// Initialize the base dir, logging, etc.
	rootEnv, err := initRootEnv(cmd)
	if err != nil {
		return err
	}
	defer rootEnv.Cleanup()
	logger := logging.GetLogger("test-runner")

	// Enumerate the requested test cases.
	toRun := defaultScenarios // Run all default scenarios if not set.
	if vec := viper.GetStringSlice(cfgTest); len(vec) > 0 {
		toRun = nil
		for _, v := range vec {
			n := strings.ToLower(v)
			scenario, ok := scenarioMap[v]
			if !ok {
				logger.Error("unknown test case",
					"test", n,
				)
				return errors.New("root: unknown test case: " + n)
			}
			toRun = append(toRun, scenario)
		}
	}

	excludeMap := make(map[string]bool)
	if excludeEnv := os.Getenv("OASIS_EXCLUDE_E2E"); excludeEnv != "" {
		for _, v := range strings.Split(excludeEnv, ",") {
			excludeMap[strings.ToLower(v)] = true
		}
	}

	// Run the required test scenarios.
	parallelJobCount := viper.GetInt(cfgParallelJobCount)
	parallelJobIndex := viper.GetInt(cfgParallelJobIndex)

	// Parse test parameters passed by CLI.
	var toRunExploded map[string][]scenario.Scenario
	toRunExploded, err = parseTestParams(toRun)
	if err != nil {
		return errors.Wrap(err, "root: failed to parse test params")
	}

	// Run all test instances.
	index := 0
	for run := 0; run < numRuns; run++ {
		for name, sc := range toRunExploded {
			for i, v := range sc {
				// Maintain unique scenario datadir.
				n := fmt.Sprintf("%s/%d", name, run*len(sc)+i)

				if index%parallelJobCount != parallelJobIndex {
					logger.Info("skipping test case (assigned to different parallel job)",
						"test", n,
					)
					index++
					continue
				}

				if excludeMap[strings.ToLower(v.Name())] {
					logger.Info("skipping test case (excluded by environment)",
						"test", n,
					)
					index++
					continue
				}

				logger.Info("running test case",
					"test", n,
				)

				childEnv, err := rootEnv.NewChild(n, env.TestInstanceInfo{
					Test:         v.Name(),
					Instance:     filepath.Base(rootEnv.Dir()),
					ParameterSet: scenario.ParametersToStringMap(v.Parameters()),
					Run:          run,
				})
				if err != nil {
					logger.Error("failed to setup child environment",
						"err", err,
						"test", n,
					)
					return errors.Wrap(err, "root: failed to setup child environment")
				}

				// Dump current parameter set to file.
				if err = childEnv.WriteTestInstanceInfo(); err != nil {
					return err
				}

				// Init per-run prometheus pusher, if metrics are enabled.
				if viper.GetString(metrics.CfgMetricsAddr) != "" {
					pusher = push.New(viper.GetString(metrics.CfgMetricsAddr), "oasis-test-runner")
					pusher = pusher.
						Grouping("instance", childEnv.TestInfo().Instance).
						Grouping("run", strconv.Itoa(childEnv.TestInfo().Run)).
						Grouping("test", childEnv.TestInfo().Test).
						Grouping("software_version", version.SoftwareVersion).
						Grouping("git_branch", version.GitBranch).
						Gatherer(prometheus.DefaultGatherer)
				}

				if err = doScenario(childEnv, v); err != nil {
					logger.Error("failed to run test case",
						"err", err,
						"test", n,
					)
					err = errors.Wrap(err, "root: failed to run test case")
				}

				if cleanErr := doCleanup(childEnv); cleanErr != nil {
					logger.Error("failed to clean up child envionment",
						"err", cleanErr,
						"test", n,
					)
					if err == nil {
						err = errors.Wrap(cleanErr, "root: failed to clean up child enviroment")
					}
				}

				if err != nil {
					return err
				}

				logger.Info("passed test case",
					"test", n,
				)

				index++
			}
		}
	}

	return nil
}

func doScenario(childEnv *env.Env, scenario scenario.Scenario) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("root: panic caught running test case: %v", r)
		}
	}()

	var fixture *oasis.NetworkFixture
	if fixture, err = scenario.Fixture(); err != nil {
		err = errors.Wrap(err, "root: failed to initialize network fixture")
		return
	}

	// Instantiate fixture if it is non-nil. Otherwise assume Init will do
	// something on its own.
	var net *oasis.Network
	if fixture != nil {
		if net, err = fixture.Create(childEnv); err != nil {
			err = errors.Wrap(err, "root: failed to instantiate fixture")
			return
		}
	}

	if err = scenario.Init(childEnv, net); err != nil {
		err = errors.Wrap(err, "root: failed to initialize test case")
		return
	}

	if pusher != nil {
		upGauge.Set(1.0)
		if err = pusher.Push(); err != nil {
			err = errors.Wrap(err, "root: failed to push metrics")
			return
		}
	}

	if err = scenario.Run(childEnv); err != nil {
		err = errors.Wrap(err, "root: failed to run test case")
		return
	}

	if pusher != nil {
		upGauge.Set(0.0)
		if err = pusher.Push(); err != nil {
			err = errors.Wrap(err, "root: failed to push metrics")
			return
		}
	}

	return
}

func doCleanup(childEnv *env.Env) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("root: panic caught cleaning up test case: %v", r)
		}
	}()

	childEnv.Cleanup()

	return
}

func runList(cmd *cobra.Command, args []string) {
	switch len(scenarios) {
	case 0:
		fmt.Printf("No supported test cases!\n")
	default:
		fmt.Printf("Supported test cases:\n")

		// Sort scenarios alphabetically before printing.
		sort.Slice(scenarios, func(i, j int) bool {
			return scenarios[i].Name() < scenarios[j].Name()
		})

		for _, v := range scenarios {
			fmt.Printf("  * %v", v.Name())
			params := v.Parameters()
			if len(params) > 0 {
				fmt.Printf(" (parameters:")
				for p := range params {
					fmt.Printf(" %v", p)
				}
				fmt.Printf(")")
			}
			fmt.Printf("\n")
		}
	}
}

func init() {
	logFmt := logging.FmtLogfmt
	logLevel := logging.LevelWarn

	// Register flags.
	rootFlags.StringVar(&cfgFile, cfgConfigFile, "", "config file")
	rootFlags.Var(&logFmt, cfgLogFmt, "log format")
	rootFlags.Var(&logLevel, cfgLogLevel, "log level")
	rootFlags.Bool(cfgLogNoStdout, false, "do not mutiplex logs to stdout")
	rootFlags.StringSliceP(cfgTest, "t", nil, "test(s) to run")
	rootFlags.String(metrics.CfgMetricsAddr, "", "metrics push address")
	rootFlags.Duration(metrics.CfgMetricsPushInterval, 5*time.Second, "push interval for node exporter and oasis nodes")
	rootFlags.IntVarP(&numRuns, cfgNumRuns, "n", 1, "number of runs for given test(s)")
	rootFlags.Int(cfgParallelJobCount, 1, "(for CI) number of overall parallel jobs")
	rootFlags.Int(cfgParallelJobIndex, 0, "(for CI) index of this parallel job")
	_ = viper.BindPFlags(rootFlags)

	rootCmd.PersistentFlags().AddFlagSet(rootFlags)
	rootCmd.PersistentFlags().AddFlagSet(env.Flags)
	rootCmd.AddCommand(listCmd)

	cobra.OnInitialize(func() {
		if cfgFile != "" {
			viper.SetConfigFile(cfgFile)
			if err := viper.ReadInConfig(); err != nil {
				common.EarlyLogAndExit(err)
			}
		}

		viper.Set(cmdFlags.CfgDebugDontBlameOasis, true)
	})
}
