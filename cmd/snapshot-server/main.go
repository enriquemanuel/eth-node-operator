package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/enriquemanuel/eth-node-operator/internal/snapshot"
)

func main() {
	listenAddr := flag.String("listen", envOrDefault("LISTEN_ADDR", ":8888"), "HTTP listen address")
	rootDir := flag.String("root", envOrDefault("SNAPSHOT_ROOT", "/data/snapshots"), "snapshot storage root dir")
	logLevel := flag.String("log-level", envOrDefault("LOG_LEVEL", "info"), "log level")

	// Snapshot maker config
	makeEnabled := flag.Bool("make", false, "enable periodic snapshot creation")
	elContainer := flag.String("el-container", envOrDefault("EL_CONTAINER", "execution"), "EL docker container name")
	clContainer := flag.String("cl-container", envOrDefault("CL_CONTAINER", "consensus"), "CL docker container name")
	elDataDir := flag.String("el-datadir", envOrDefault("EL_DATADIR", "/data/execution"), "EL datadir to snapshot")
	clDataDir := flag.String("cl-datadir", envOrDefault("CL_DATADIR", "/data/consensus"), "CL datadir to snapshot")
	elClient := flag.String("el-client", envOrDefault("EL_CLIENT", "geth"), "EL client name (geth|nethermind|besu|reth)")
	clClient := flag.String("cl-client", envOrDefault("CL_CLIENT", "lighthouse"), "CL client name (lighthouse|teku|prysm)")
	elRPC := flag.String("el-rpc", envOrDefault("EL_RPC_URL", "http://127.0.0.1:8545"), "EL JSON-RPC URL")
	clRPC := flag.String("cl-rpc", envOrDefault("CL_REST_URL", "http://127.0.0.1:5052"), "CL REST API URL")
	network := flag.String("network", envOrDefault("NETWORK", "mainnet"), "Ethereum network")
	schedule := flag.Duration("schedule", 24*time.Hour, "how often to take a new snapshot")
	keepCount := flag.Int("keep", 3, "number of snapshots to retain per client")
	flag.Parse()

	level := slog.LevelInfo
	if *logLevel == "debug" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	srv := snapshot.NewServer(*rootDir, log)

	httpSrv := &http.Server{
		Addr:         *listenAddr,
		Handler:      srv.Handler(),
		ReadTimeout:  30 * time.Second,
		// WriteTimeout must be very long — serving multi-GB files
		WriteTimeout: 6 * time.Hour,
		IdleTimeout:  120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start snapshot maker loop if enabled
	if *makeEnabled {
		go func() {
			log.Info("snapshot maker enabled", "schedule", schedule, "network", *network)
			ticker := time.NewTicker(*schedule)
			defer ticker.Stop()

			// Take one immediately on start
			runSnapshots(ctx, log, *network, *elClient, *clClient,
				*elContainer, *clContainer, *elDataDir, *clDataDir,
				*elRPC, *clRPC, *rootDir, *keepCount)

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					runSnapshots(ctx, log, *network, *elClient, *clClient,
						*elContainer, *clContainer, *elDataDir, *clDataDir,
						*elRPC, *clRPC, *rootDir, *keepCount)
				}
			}
		}()
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutCtx) //nolint:errcheck
	}()

	log.Info("snapshot server starting", "listen", *listenAddr, "root", *rootDir)
	if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func runSnapshots(ctx context.Context, log *slog.Logger, network, elClient, clClient,
	elContainer, clContainer, elDataDir, clDataDir, elRPC, clRPC, rootDir string, keepCount int) {

	for _, s := range []struct {
		client, container, dataDir, rpcURL string
	}{
		{elClient, elContainer, elDataDir, elRPC},
		{clClient, clContainer, clDataDir, clRPC},
	} {
		outDir := rootDir + "/" + network + "/" + s.client
		cfg := snapshot.MakerConfig{
			Client:        s.client,
			DataDir:       s.dataDir,
			OutputDir:     outDir,
			ContainerName: s.container,
			StopTimeout:   3 * time.Minute,
			ELRPCURL:      elRPC,
			CLRestURL:     clRPC,
		}
		if s.client == clClient {
			cfg.ELRPCURL = ""
			cfg.CLRestURL = s.rpcURL
		} else {
			cfg.CLRestURL = ""
			cfg.ELRPCURL = s.rpcURL
		}

		maker := snapshot.NewMaker(cfg)
		log.Info("creating snapshot", "client", s.client, "datadir", s.dataDir)

		result, err := maker.CreateSnapshot(ctx)
		if err != nil {
			log.Error("snapshot failed", "client", s.client, "err", err)
			continue
		}
		log.Info("snapshot complete",
			"client", s.client,
			"block", result.BlockNumber,
			"size_gb", fmt.Sprintf("%.1f", float64(result.CompressedSize)/(1<<30)),
			"duration", result.Duration,
		)

		if err := maker.PruneOld(keepCount); err != nil {
			log.Warn("prune failed", "client", s.client, "err", err)
		}
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
