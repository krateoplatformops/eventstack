package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	xcontext "github.com/krateoplatformops/plumbing/context"
	"github.com/krateoplatformops/plumbing/env"
	"github.com/krateoplatformops/plumbing/slogs/pretty"
	"github.com/krateoplatformops/sweeper/internal/cleanup"
	"github.com/krateoplatformops/sweeper/internal/handlers"

	etcdutil "github.com/krateoplatformops/sweeper/internal/util/etcd"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	serviceName = "sweeper"
)

func main() {
	debugOn := flag.Bool("debug", env.Bool("DEBUG", false), "enable or disable debug logs")
	port := flag.Int("port", env.ServicePort("PORT", 8081), "port to listen on")
	etcdServers := flag.String("etcd-servers", env.String("ETCD_SERVERS", "localhost:2379"),
		"Comma-separated list of etcd endpoints used to store and retrieve logs.")
	cleanupThreshold := flag.Float64("cleanup-threshold", env.Float64("CLEANUP_THRESHOLD", 0.8),
		"Usage ratio (0.0–1.0) at which the cleanup callback is triggered")
	monitoringInterval := flag.Duration("monitoring-interval",
		env.Duration("MONITORING_INTERVAL", 15*time.Second), "Monitoring interval")

	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "Flags:")
		flag.PrintDefaults()
	}

	flag.Parse()

	logLevel := slog.LevelInfo
	if *debugOn {
		logLevel = slog.LevelDebug
	}

	lh := pretty.New(&slog.HandlerOptions{
		Level:     logLevel,
		AddSource: false,
	},
		pretty.WithDestinationWriter(os.Stderr),
		pretty.WithColor(),
		pretty.WithOutputEmptyAttrs(),
	)

	log := slog.New(lh).With(slog.String("service", serviceName))
	if *debugOn {
		log.Debug("environment variables", slog.Any("env", os.Environ()))
	}

	ctx := xcontext.BuildContext(context.Background(),
		xcontext.WithTraceId(serviceName),
		xcontext.WithLogger(log),
	)

	etcdClient, err := etcdutil.NewEtcdClient(strings.Split(*etcdServers, ","))
	if err != nil {
		log.Error("unable to create Etcd client", slog.Any("err", err))
		os.Exit(1)
	}
	defer etcdClient.Close()

	var (
		watcher *etcdutil.UsageWatcher
	)

	cleanupManager := cleanup.NewCleanupManager(etcdClient,
		cleanup.CleanupOptions{
			DesiredRatio: 0.6,
			BatchSize:    100,
			AutoCompact:  true,
			AutoDefrag:   true,
		})

	watcher = etcdutil.NewUsageWatcher(etcdutil.UsageWatcherConfig{
		Client:    etcdClient,
		Threshold: *cleanupThreshold,
		Interval:  *monitoringInterval,
		OnThreshold: func(status *clientv3.StatusResponse) {
			log.Warn("etcd usage high",
				slog.String("used", fmt.Sprintf("%.2f MB", float64(status.DbSizeInUse)/(1024*1024))))
			// run cleanup logic...
			watcher.Suspend()
			go func() {
				cleanupManager.RunCleanup(ctx, status)
				// resume watcher
				watcher.Resume()
			}()
		},
	})

	// Health endpoints
	health := handlers.Health()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", health.LivenessProbe) // liveness probe
	mux.HandleFunc("/readyz", health.ReadinessProbe) // readiness probe

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", *port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 50 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	var (
		wg   sync.WaitGroup
		stop context.CancelFunc
	)

	// Setup signal handling (SIGTERM/SIGINT)
	ctx, stop = signal.NotifyContext(ctx,
		os.Interrupt,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGHUP,
		syscall.SIGQUIT,
	)
	defer stop()

	// Start HTTP server for health checks
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Info("starting healthz endpoints", slog.Int("port", *port))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("healthz server error", slog.Any("err", err))
			os.Exit(1)
		}
	}()

	// Start etcd watcher
	wg.Add(1)
	go func() {
		defer wg.Done()

		// Mark ready after initialization
		health.SetReady(true)

		watcher.Start(ctx)
	}()

	// Wait for termination signal
	// Wait for context cancellation (triggered by signal)
	<-ctx.Done()
	log.Info("context cancelled — initiating graceful shutdown")

	// Mark as not ready, cancel ongoing operations
	health.SetReady(false)

	// Gracefully shutdown HTTP server
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	watcher.Stop()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Warn("error during HTTP shutdown", slog.Any("err", err))
	} else {
		log.Info("HTTP server stopped gracefully")
	}

	// Wait for all goroutines to complete
	wg.Wait()
	log.Info("All components stopped. Exiting cleanly.")
}
