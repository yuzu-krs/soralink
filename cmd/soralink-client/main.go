package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/yuzut/soralink/internal/client"
)

func main() {
	configPath := flag.String("config", "configs/client.yaml", "config file path")
	serverAddr := flag.String("server", "", "server address (overrides config)")
	token := flag.String("token", "", "auth token (overrides config)")
	localPort := flag.Int("local", 0, "local port (quick mode)")
	remotePort := flag.Int("remote", 0, "requested remote port (quick mode)")
	flag.Parse()

	var cfg *client.Config

	if *serverAddr != "" && *localPort > 0 {
		// クイックモード: CLI フラグから直接設定を構築
		authToken := *token
		if authToken == "" {
			fmt.Fprintf(os.Stderr, "error: --token is required in quick mode\n")
			os.Exit(1)
		}
		cfg = &client.Config{
			ServerAddr: *serverAddr,
			AuthToken:  authToken,
			Tunnels: []client.TunnelConfig{
				{
					LocalPort:  *localPort,
					RemotePort: *remotePort,
					Protocol:   "tcp",
				},
			},
		}
	} else {
		// 設定ファイルモード
		var err error
		cfg, err = client.LoadConfig(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "config error: %v\n", err)
			os.Exit(1)
		}
		// CLI フラグで上書き
		if *serverAddr != "" {
			cfg.ServerAddr = *serverAddr
		}
		if *token != "" {
			cfg.AuthToken = *token
		}
	}

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "invalid config: %v\n", err)
		os.Exit(1)
	}

	logger := initLogger(cfg.LogLevel)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	c := client.NewClient(cfg, logger)
	if err := c.RunWithRetry(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("client stopped with error", "err", err)
		os.Exit(1)
	}
	logger.Info("client disconnected")
}

func initLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
