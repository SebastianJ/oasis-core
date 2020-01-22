// Package metrics implements a prometheus metrics service.
package metrics

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/push"
	"github.com/prometheus/procfs"
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/oasislabs/oasis-core/go/common/service"
	"github.com/oasislabs/oasis-core/go/oasis-node/cmd/common"
)

const (
	CfgMetricsMode         = "metrics.mode"
	CfgMetricsAddr         = "metrics.address"
	CfgMetricsPushJobName  = "metrics.push.job_name"
	CfgMetricsPushLabels   = "metrics.push.labels"
	CfgMetricsPushInterval = "metrics.push.interval"

	MetricsModeNone = "none"
	MetricsModePull = "pull"
	MetricsModePush = "push"
)

var (
	// Flags has the flags used by the metrics service.
	Flags = flag.NewFlagSet("", flag.ContinueOnError)

	diskUsageGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "oasis_worker_disk_usage_bytes",
			Help: "Size of datadir of the worker",
		},
	)

	diskIOReadBytesCounter = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "oasis_worker_disk_read_bytes",
			Help: "Read bytes by the worker",
		},
	)

	diskIOWrittenBytesCounter = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "oasis_worker_disk_written_bytes",
			Help: "Written bytes by the worker",
		},
	)

	diskCollectors = []prometheus.Collector{
		diskUsageGauge,
		diskIOReadBytesCounter,
		diskIOWrittenBytesCounter,
	}

	diskOnce sync.Once
)

// ParseMetricPushLabes is a drop-in replacement for viper.GetStringMapString due to https://github.com/spf13/viper/issues/608.
func ParseMetricPushLabels(val string) map[string]string {
	// viper.GetString() wraps the string inside [] parenthesis, unwrap it.
	val = val[1 : len(val)-1]

	labels := map[string]string{}
	for _, lPair := range strings.Split(val, ",") {
		kv := strings.Split(lPair, "=")
		if len(kv) != 2 || kv[0] == "" {
			continue
		}
		labels[kv[0]] = kv[1]
	}
	return labels
}

// ServiceConfig contains the configuration parameters for metrics.
type ServiceConfig struct {
	// Mode is the service mode ("none", "pull", "push").
	Mode string
	// Address is the address of the push server.
	Address string
	// JobName is the name of the job for which metrics are collected.
	JobName string
	// Labels are the key-value labels of the job being collected for.
	Labels map[string]string
	// Interval defined the push interval for metrics collection.
	Interval time.Duration
}

// GetServiceConfig gets the metrics configuration parameter struct.
func GetServiceConfig() *ServiceConfig {
	return &ServiceConfig{
		Mode:     viper.GetString(CfgMetricsMode),
		Address:  viper.GetString(CfgMetricsAddr),
		JobName:  viper.GetString(CfgMetricsPushJobName),
		Labels:   ParseMetricPushLabels(viper.GetString(CfgMetricsPushLabels)),
		Interval: viper.GetDuration(CfgMetricsPushInterval),
	}
}

type stubService struct {
	service.BaseBackgroundService

	dService *diskService
}

func (s *stubService) Start() error {
	if err := s.dService.Start(); err != nil {
		return err
	}

	return nil
}

func (s *stubService) Stop() {}

func (s *stubService) Cleanup() {}

func newStubService() (service.BackgroundService, error) {
	svc := *service.NewBaseBackgroundService("metrics")

	d, err := NewDiskUsageService()
	if err != nil {
		return nil, err
	}

	return &stubService{
		BaseBackgroundService: svc,
		dService:              d.(*diskService),
	}, nil
}

type pullService struct {
	service.BaseBackgroundService

	ln net.Listener
	s  *http.Server

	ctx   context.Context
	errCh chan error

	duService *diskService
}

func (s *pullService) Start() error {
	if err := s.duService.Start(); err != nil {
		return err
	}

	go func() {
		if err := s.s.Serve(s.ln); err != nil {
			s.BaseBackgroundService.Stop()
			s.errCh <- err
		}
	}()
	return nil
}

func (s *pullService) Stop() {
	if s.s != nil {
		select {
		case err := <-s.errCh:
			if err != nil {
				s.Logger.Error("metrics terminated uncleanly",
					"err", err,
				)
			}
		default:
			_ = s.s.Shutdown(s.ctx)
		}
		s.s = nil
	}
}

func (s *pullService) Cleanup() {
	if s.ln != nil {
		_ = s.ln.Close()
		s.ln = nil
	}
}

func newPullService(ctx context.Context) (service.BackgroundService, error) {
	addr := viper.GetString(CfgMetricsAddr)

	svc := *service.NewBaseBackgroundService("metrics")

	svc.Logger.Debug("Metrics Server Params",
		"mode", MetricsModePull,
		"addr", addr,
	)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	d, err := NewDiskUsageService()
	if err != nil {
		return nil, err
	}

	return &pullService{
		BaseBackgroundService: svc,
		ctx:                   ctx,
		ln:                    ln,
		s:                     &http.Server{Handler: promhttp.Handler()},
		errCh:                 make(chan error),
		duService:             d.(*diskService),
	}, nil
}

type pushService struct {
	service.BaseBackgroundService

	pusher   *push.Pusher
	interval time.Duration

	dService *diskService
}

func (s *pushService) Start() error {
	if err := s.dService.Start(); err != nil {
		return err
	}

	s.pusher = s.pusher.Gatherer(prometheus.DefaultGatherer)

	go s.worker()
	return nil
}

func (s *pushService) worker() {
	t := time.NewTicker(s.interval)
	defer t.Stop()

	for {
		select {
		case <-s.Quit():
			break
		case <-t.C:
		}

		if err := s.pusher.Push(); err != nil {
			s.Logger.Warn("Push: failed",
				"err", err,
			)
		}
	}
}

func newPushService() (service.BackgroundService, error) {
	addr := viper.GetString(CfgMetricsAddr)
	jobName := viper.GetString(CfgMetricsPushJobName)
	labels := ParseMetricPushLabels(viper.GetString(CfgMetricsPushLabels))
	interval := viper.GetDuration(CfgMetricsPushInterval)

	if jobName == "" {
		return nil, fmt.Errorf("metrics: metrics.push.job_name required for push mode")
	}
	if labels["instance"] == "" {
		return nil, fmt.Errorf("metrics: at least 'instance' key should be set for metrics.push.labels. Provided labels: %v, viper raw: %v", labels, viper.GetString(CfgMetricsPushLabels))
	}

	svc := *service.NewBaseBackgroundService("metrics")

	svc.Logger.Debug("Metrics Server Params",
		"mode", MetricsModePush,
		"addr", addr,
		"job_name", jobName,
		"labels", labels,
		"push_interval", interval,
	)

	pusher := push.New(addr, jobName)
	for k, v := range labels {
		pusher = pusher.Grouping(k, v)
	}

	d, err := NewDiskUsageService()
	if err != nil {
		return nil, err
	}

	return &pushService{
		BaseBackgroundService: svc,
		pusher:                pusher,
		interval:              interval,
		dService:              d.(*diskService),
	}, nil
}

// New constructs a new metrics service.
func New(ctx context.Context) (service.BackgroundService, error) {
	mode := viper.GetString(CfgMetricsMode)
	switch strings.ToLower(mode) {
	case MetricsModeNone:
		return newStubService()
	case MetricsModePull:
		return newPullService(ctx)
	case MetricsModePush:
		return newPushService()
	default:
		return nil, fmt.Errorf("metrics: unsupported mode: '%v'", mode)
	}
}

type diskService struct {
	service.BaseBackgroundService

	dataDir string
	// TODO: Should we monitor I/O of children PIDs as well?
	pid      int
	interval time.Duration
}

func (d *diskService) Start() error {
	go d.worker()
	return nil
}

func (d *diskService) worker() {
	t := time.NewTicker(d.interval)
	defer t.Stop()

	var prevRBytes, prevWBytes uint64
	for {
		select {
		case <-d.Quit():
			break
		case <-t.C:
		}

		{
			// Compute disk usage of datadir.
			var duBytes int64
			err := filepath.Walk(d.dataDir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					d.Logger.Warn("disk usage metric: failed to access file", "err", err, "path", path)
					return err
				}
				duBytes += info.Size()
				return nil
			})
			if err != nil {
				d.Logger.Warn("disk usage metric: failed to walk directory", "err", err, "dataDir", d.dataDir)
			}
			diskUsageGauge.Set(float64(duBytes))
		}

		{
			// Obtain process I/O info.
			proc, err := procfs.NewProc(d.pid)
			if err != nil {
				d.Logger.Warn("disk I/O metric: failed to obtain proc object for PID", "err", err, "pid", d.pid)
				continue
			}
			procIO, err := proc.IO()
			if err != nil {
				d.Logger.Warn("disk I/O metric: failed to obtain procIO object", "err", err, "pid", d.pid)
				continue
			}

			// Prometheus counter type doesn't allow setting absolute values.
			diskIOWrittenBytesCounter.Add(float64(procIO.ReadBytes - prevWBytes))
			diskIOReadBytesCounter.Add(float64(procIO.WriteBytes - prevRBytes))
			prevRBytes = procIO.ReadBytes
			prevWBytes = procIO.WriteBytes
		}
	}
}

// NewDiskUsageService constructs a new disk usage service.
//
// This service will compute the size of datadir folder by calling "du" command and read I/O info from /proc/<PID>/io
// file every --metric.push.interval seconds.
func NewDiskUsageService() (service.BackgroundService, error) {
	diskOnce.Do(func() {
		prometheus.MustRegister(diskCollectors...)
	})

	return &diskService{
		BaseBackgroundService: *service.NewBaseBackgroundService("disk"),
		dataDir:               viper.GetString(common.CfgDataDir),
		pid:                   os.Getpid(),
		interval:              viper.GetDuration(CfgMetricsPushInterval),
	}, nil
}

// EscapeLabelCharacters replaces invalid prometheus label name characters with "_".
func EscapeLabelCharacters(l string) string {
	return strings.Replace(l, ".", "_", -1)
}

func init() {
	Flags.String(CfgMetricsMode, MetricsModeNone, "metrics (prometheus) mode: none, pull, push")
	Flags.String(CfgMetricsAddr, "127.0.0.1:3000", "metrics pull/push address")
	Flags.String(CfgMetricsPushJobName, "", "metrics push job name")
	Flags.StringToString(CfgMetricsPushLabels, map[string]string{}, "metrics push instance label")
	Flags.Duration(CfgMetricsPushInterval, 5*time.Second, "metrics push interval")

	_ = viper.BindPFlags(Flags)
}
