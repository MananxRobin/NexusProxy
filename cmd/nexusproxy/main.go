package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"nexusproxy/internal/config"
	"nexusproxy/internal/gateway"
	"nexusproxy/internal/server"
	"nexusproxy/internal/setup"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		slog.Error("nexusproxy failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		switch args[0] {
		case "setup":
			return runSetup(args[1:])
		case "serve":
			return runServer(args[1:])
		}
		if !strings.HasPrefix(args[0], "-") {
			return fmt.Errorf("unknown command %q", args[0])
		}
	}
	return runServer(args)
}

func runServer(args []string) error {
	flags := flag.NewFlagSet("nexusproxy", flag.ContinueOnError)
	configPath := flags.String("config", getenv("NEXUS_CONFIG", "config.example.json"), "path to config JSON")
	envPathFlag := flags.String("env-file", "", "path to provider key env file")
	versionFlag := flags.Bool("version", false, "print version and exit")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if *versionFlag {
		fmt.Println("nexusproxy " + version)
		return nil
	}

	envPath := *envPathFlag
	if envPath == "" {
		envPath = config.EnvFilePath(*configPath)
	}
	return serve(*configPath, envPath)
}

func runSetup(args []string) error {
	flags := flag.NewFlagSet("nexusproxy setup", flag.ContinueOnError)
	configPath := flags.String("config", getenv("NEXUS_CONFIG", "config.example.json"), "path to config JSON")
	envPathFlag := flags.String("env-file", "", "path to provider key env file")
	provider := flags.String("provider", "all", "provider type to configure, or all")
	testKeys := flags.Bool("test", false, "test provider keys after saving")
	noTest := flags.Bool("no-test", false, "skip provider key testing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *testKeys && *noTest {
		return errors.New("use only one of --test or --no-test")
	}

	testMode := setup.TestPrompt
	if *testKeys {
		testMode = setup.TestAlways
	}
	if *noTest {
		testMode = setup.TestNever
	}

	envPath := *envPathFlag
	if envPath == "" {
		envPath = config.EnvFilePath(*configPath)
	}

	return setup.Run(context.Background(), setup.Options{
		ConfigPath: *configPath,
		EnvPath:    envPath,
		Provider:   *provider,
		TestMode:   testMode,
	})
}

func serve(configPath string, envPath string) error {
	if err := config.LoadEnvFile(envPath); err != nil {
		return fmt.Errorf("load env file %s: %w", envPath, err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	searchGateway := gateway.New(cfg, gateway.Options{})
	handler := server.New(searchGateway, cfg.Server.APIKey, envPath, cfg.Server.MaxConcurrentRequests)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("nexusproxy listening", "url", "http://"+addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown failed: %w", err)
	}

	slog.Info("nexusproxy stopped")
	return nil
}

func getenv(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
