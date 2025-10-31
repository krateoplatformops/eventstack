package etcd

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	xcontext "github.com/krateoplatformops/plumbing/context"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	serviceName = "etcd-usage-watcher"
)

// UsageWatcherConfig defines configuration parameters for the etcd usage watcher.
type UsageWatcherConfig struct {
	Client    *clientv3.Client
	Threshold float64       // Ratio threshold (e.g. 0.8 for 80%)
	Interval  time.Duration // Polling interval (e.g. 10 * time.Second)
	// Callback invoked when usage exceeds the threshold.
	OnThreshold func(status *clientv3.StatusResponse)
}

// UsageWatcher monitors etcd DB usage and triggers cleanup callbacks
// when usage exceeds a defined threshold. It can be paused/resumed externally.
type UsageWatcher struct {
	client      *clientv3.Client
	threshold   float64
	interval    time.Duration
	onThreshold func(status *clientv3.StatusResponse)

	mu        sync.Mutex
	suspended atomic.Bool
	log       *slog.Logger
	cancel    context.CancelFunc
}

// NewUsageWatcher creates a new watcher with sane defaults.
func NewUsageWatcher(cfg UsageWatcherConfig) *UsageWatcher {
	if cfg.Threshold <= 0 || cfg.Threshold > 1 {
		cfg.Threshold = 0.8
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 15 * time.Second
	}

	return &UsageWatcher{
		client:      cfg.Client,
		threshold:   cfg.Threshold,
		interval:    cfg.Interval,
		onThreshold: cfg.OnThreshold,
		log:         slog.Default(),
	}
}

// Start launches the watcher loop until the context is canceled or Stop is called.
func (w *UsageWatcher) Start(ctx context.Context) {
	w.log = xcontext.Logger(ctx).
		With(slog.String("service", serviceName))

	ctx, w.cancel = context.WithCancel(ctx)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.log.Info("stopping etcd usage watcher")
			return

		case <-ticker.C:
			if w.IsSuspended() {
				continue
			}

			w.checkUsage(ctx)
		}
	}
}

// Stop cancels the watcher’s loop gracefully.
func (w *UsageWatcher) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
}

// Suspend pauses threshold checks.
func (w *UsageWatcher) Suspend() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.suspended.Load() {
		w.log.Info("suspending etcd usage watcher")
		w.suspended.Store(true)
	}
}

// Resume resumes threshold checks.
func (w *UsageWatcher) Resume() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.suspended.Load() {
		w.log.Info("resuming etcd usage watcher")
		w.suspended.Store(false)
	}
}

// IsSuspended safely checks watcher suspension state.
func (w *UsageWatcher) IsSuspended() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.suspended.Load()
}

// checkUsage queries etcd for DB size and triggers callback if needed.
func (w *UsageWatcher) checkUsage(ctx context.Context) {
	ctxTimeout, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	for _, ep := range w.client.Endpoints() {
		status, err := w.client.Maintenance.Status(ctxTimeout, ep)
		if err != nil {
			w.log.Error("failed to get etcd status",
				slog.String("endpoint", ep),
				slog.Any("err", err))
			continue
		}

		ratio := float64(status.DbSizeInUse) / float64(status.DbSizeQuota)

		w.log.Info("etcd usage check",
			slog.String("endpoint", ep),
			slog.String("usage", fmt.Sprintf("%.2f%%", ratio*100)),
			slog.String("used", fmt.Sprintf("%.2f MB", float64(status.DbSizeInUse)/(1024*1024))),
			slog.String("limit", fmt.Sprintf("%.2f MB", float64(status.DbSizeQuota)/(1024*1024))),
		)

		if ratio >= w.threshold {
			w.log.Warn("threshold exceeded",
				slog.String("endpoint", ep),
				slog.Float64("ratio", ratio),
				slog.Float64("threshold", w.threshold),
			)
			if w.onThreshold != nil {
				w.onThreshold(status)
			}
			// optionally auto-suspend until cleanup finishes
			w.Suspend()
		}
	}
}
