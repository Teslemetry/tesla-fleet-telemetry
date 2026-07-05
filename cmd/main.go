package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "go.uber.org/automaxprocs"

	"github.com/airbrake/gobrake/v5"
	"github.com/teslamotors/fleet-telemetry/config"
	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/server/airbrake"
	"github.com/teslamotors/fleet-telemetry/server/monitoring"
	"github.com/teslamotors/fleet-telemetry/server/streaming"
)

// shutdownDrainTimeout bounds how long a SIGTERM/SIGINT drain waits for open
// sockets to finish tearing down (dispatching in-flight records and logging
// socket_disconnected) before the process proceeds to flush and exit anyway.
const shutdownDrainTimeout = 25 * time.Second

func main() {
	var err error

	// Trigger a graceful drain on SIGTERM/SIGINT so open sockets tear down cleanly
	// (dispatching in-flight telemetry and logging socket_disconnected) and the
	// deferred shutdownFuncs - including the OTel provider flush that exports any
	// buffered publish spans - actually run, instead of the process being
	// hard-killed with in-flight work dropped.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	config, logger, shutdownFuncs, err := config.LoadApplicationConfiguration()
	if err != nil {
		// logger is not available yet
		panic(fmt.Sprintf("error=load_service_config value=\"%s\"", err.Error()))
	}

	// Defer shutdown functions (e.g., OTel logging)
	defer func() {
		for _, shutdownFunc := range shutdownFuncs {
			if err := shutdownFunc(); err != nil {
				logger.ErrorLog("shutdown_func_error", err, nil)
			}
		}
	}()

	if config.Monitoring != nil && config.Monitoring.ProfilingPath != "" {
		if config.Monitoring.ProfilerFile, err = os.Create(config.Monitoring.ProfilingPath); err != nil {
			logger.ErrorLog("profiling_file_error", err, nil)
			config.Monitoring.ProfilingPath = ""
		}

		defer func() {
			config.MetricCollector.Shutdown()
			_ = config.Monitoring.ProfilerFile.Close()
		}()
	}

	airbrakeNotifier, _, err := config.CreateAirbrakeNotifier(logger)
	if err != nil {
		panic(err)
	}
	if airbrakeNotifier != nil {
		defer airbrakeNotifier.NotifyOnPanic()
		defer func() {
			if err := airbrakeNotifier.Close(); err != nil {
				logger.ErrorLog("airbrake_close_error", err, nil)
			}
		}()
	}

	// A signal-driven graceful shutdown returns nil; only genuine server faults
	// panic (so airbrake's NotifyOnPanic still fires for those). Either way the
	// deferred shutdownFuncs run and flush the OTel provider.
	if err := startServer(ctx, stop, config, airbrakeNotifier, logger); err != nil {
		panic(err)
	}
}

func startServer(ctx context.Context, stopSignal context.CancelFunc, config *config.Config, airbrakeNotifier *gobrake.Notifier, logger *logrus.Logger) (err error) {
	logger.ActivityLog("starting_server", nil)
	registry := streaming.NewSocketRegistry()

	airbrakeHandler := airbrake.NewAirbrakeHandler(airbrakeNotifier)

	if config.StatusPort > 0 {
		monitoring.StartStatusServer(config, logger, airbrakeHandler)
	}
	if config.Monitoring != nil {
		monitoring.StartServerMetrics(config, logger, registry)
	}

	dispatchers, producerRules, err := config.ConfigureProducers(airbrakeHandler, logger, false)
	if err != nil {
		return err
	}
	server, _, err := streaming.InitServer(config, airbrakeHandler, producerRules, logger, registry)
	if err != nil {
		return err
	}

	if server.TLSConfig, err = config.ExtractServiceTLSConfig(logger); err != nil {
		return err
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.ListenAndServeTLS(config.TLS.ServerCert, config.TLS.ServerKey)
	}()

	select {
	case err = <-serveErr:
		// The listener stopped on its own (bind failure, unexpected error). Surface
		// it to the caller, which panics so airbrake is notified.
	case <-ctx.Done():
		// Restore default signal handling so a second SIGTERM/SIGINT during the drain
		// hard-exits instead of being swallowed.
		stopSignal()
		// SIGTERM/SIGINT: stop accepting new connections, then drain the open ones
		// so each socket's ProcessTelemetry runs its normal teardown.
		err = gracefulShutdown(server, registry, logger)
	}

	for dispatcher, producer := range dispatchers {
		logger.ActivityLog("attempting_to_close", logrus.LogInfo{"dispatcher": dispatcher})
		// We don't care if this fails. If it does, we'll just continue on.
		if dispatcherCloseErr := producer.Close(); dispatcherCloseErr != nil {
			logger.ErrorLog("producer_close_error", dispatcherCloseErr, logrus.LogInfo{"dispatcher": dispatcher})
		}
	}
	logger.ActivityLog("stopped_server", nil)
	return err
}

// gracefulShutdown stops the listener, closes every open socket so its read-loop
// teardown runs, and waits (bounded by shutdownDrainTimeout) for them to finish.
func gracefulShutdown(server *http.Server, registry *streaming.SocketRegistry, logger *logrus.Logger) error {
	logger.ActivityLog("shutdown_signal_received", logrus.LogInfo{"open_sockets": registry.NumConnectedSockets()})

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownDrainTimeout)
	defer cancel()

	// Shutdown stops accepting new connections. Hijacked websocket connections are
	// not tracked by net/http, so we drain them explicitly below.
	if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil && !errors.Is(shutdownErr, context.DeadlineExceeded) {
		logger.ErrorLog("server_shutdown_error", shutdownErr, nil)
	}

	registry.CloseAllSockets()
	waitForSocketsDrain(shutdownCtx, registry, logger)

	logger.ActivityLog("stopped_server_graceful", nil)
	return nil
}

// waitForSocketsDrain blocks until every socket has deregistered or the context
// deadline is hit, polling the registry's connected-socket count.
func waitForSocketsDrain(ctx context.Context, registry *streaming.SocketRegistry, logger *logrus.Logger) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		if remaining := registry.NumConnectedSockets(); remaining == 0 {
			return
		}
		select {
		case <-ctx.Done():
			logger.ErrorLog("shutdown_drain_timeout", ctx.Err(), logrus.LogInfo{"remaining_sockets": registry.NumConnectedSockets()})
			return
		case <-ticker.C:
		}
	}
}
