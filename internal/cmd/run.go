// Package cmd provides command-line interface functionality for the CLI Proxy API server.
// It includes authentication flows for various AI service providers, service startup,
// and other command-line operations.
package cmd

import (
	"context"
	"errors"
	"os/signal"
	"syscall"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/api"
	confighub "github.com/router-for-me/CLIProxyAPI/v6/internal/cfg"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy"
	log "github.com/sirupsen/logrus"
)

// StartService builds and runs the proxy service using the exported SDK.
// It creates a new proxy service instance, sets up signal handling for graceful shutdown,
// and starts the service with the provided configuration.
//
// Parameters:
//   - cfg: The application configuration
//   - configPath: The path to the configuration file
//   - localPassword: Optional password accepted for local management requests
func StartService(cfg *config.Config, configPath string, localPassword string) {
	builder := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		WithLocalManagementPassword(localPassword)

	ctxSignal, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	runCtx := ctxSignal
	if localPassword != "" {
		var keepAliveCancel context.CancelFunc
		runCtx, keepAliveCancel = context.WithCancel(ctxSignal)
		builder = builder.WithServerOptions(api.WithKeepAliveEndpoint(10*time.Second, func() {
			log.Warn("keep-alive endpoint idle for 10s, shutting down")
			keepAliveCancel()
		}))
	}

	service, err := builder.Build()
	if err != nil {
		log.Errorf("failed to build proxy service: %v", err)
		return
	}

	// Register the in-memory apply path so config_hub WS pushes drive
	// the same reload state machine as the file watcher. The callback
	// is a no-op when CONFIGHUB_URL isn't set (Bootstrap never installs
	// an OnChange handler in that case).
	confighub.SetApply(service.ApplyConfigBytes)
	defer confighub.SetApply(nil)

	err = service.Run(runCtx)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Errorf("proxy service exited with error: %v", err)
	}
}

// StartServiceBackground starts the proxy service in a background goroutine
// and returns a cancel function for shutdown and a done channel.
func StartServiceBackground(cfg *config.Config, configPath string, localPassword string) (cancel func(), done <-chan struct{}) {
	builder := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		WithLocalManagementPassword(localPassword)

	ctx, cancelFn := context.WithCancel(context.Background())
	doneCh := make(chan struct{})

	service, err := builder.Build()
	if err != nil {
		log.Errorf("failed to build proxy service: %v", err)
		close(doneCh)
		return cancelFn, doneCh
	}

	// Mirror StartService: wire config_hub in-memory apply to the
	// service. Cleared when the background goroutine exits.
	confighub.SetApply(service.ApplyConfigBytes)

	go func() {
		defer close(doneCh)
		defer confighub.SetApply(nil)
		if err := service.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Errorf("proxy service exited with error: %v", err)
		}
	}()

	return cancelFn, doneCh
}

// WaitForCloudDeploy waits indefinitely for shutdown signals in cloud deploy mode
// when no configuration file is available.
func WaitForCloudDeploy() {
	// Clarify that we are intentionally idle for configuration and not running the API server.
	log.Info("Cloud deploy mode: No config found; standing by for configuration. API server is not started. Press Ctrl+C to exit.")

	ctxSignal, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Block until shutdown signal is received
	<-ctxSignal.Done()
	log.Info("Cloud deploy mode: Shutdown signal received; exiting")
}
