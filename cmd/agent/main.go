package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/enriquemanuel/eth-node-operator/internal/agent"
)

func main() {
	var (
		nodeName   = flag.String("node-name", envOrDefault("NODE_NAME", ""), "node name (required)")
		specPath   = flag.String("spec", envOrDefault("SPEC_PATH", "/etc/ethagent/node.yaml"), "path to node spec YAML")
		listenAddr = flag.String("listen", envOrDefault("LISTEN_ADDR", ":9000"), "HTTP listen address")
		elEndpoint = flag.String("el-endpoint", envOrDefault("EL_ENDPOINT", "http://localhost:8545"), "execution layer JSON-RPC URL")
		clEndpoint = flag.String("cl-endpoint", envOrDefault("CL_ENDPOINT", "http://localhost:5052"), "consensus layer REST API URL")
		logLevel   = flag.String("log-level", envOrDefault("LOG_LEVEL", "info"), "log level (debug|info|warn|error)")
	)
	flag.Parse()

	if *nodeName == "" {
		hostname, _ := os.Hostname()
		nodeName = &hostname
	}

	level := slog.LevelInfo
	switch *logLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	cfg := agent.Config{
		NodeName:   *nodeName,
		SpecPath:   *specPath,
		ListenAddr: *listenAddr,
		ELEndpoint: *elEndpoint,
		CLEndpoint: *clEndpoint,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("ethagent starting",
		"node", cfg.NodeName,
		"spec", cfg.SpecPath,
		"listen", cfg.ListenAddr,
		"el", cfg.ELEndpoint,
		"cl", cfg.CLEndpoint,
	)

	if err := agent.New(cfg, log).Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
