package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kcompton15/Todo-Board/internal/httpapi"
	"github.com/kcompton15/Todo-Board/internal/inbox"
	"github.com/kcompton15/Todo-Board/internal/store"
	"github.com/kcompton15/Todo-Board/webui"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		_, port, err := net.SplitHostPort(envOrDefault("TODO_ADDR", "127.0.0.1:7337"))
		if err != nil {
			os.Exit(1)
		}
		response, err := http.Get("http://" + net.JoinHostPort("127.0.0.1", port) + "/healthz")
		if err != nil || response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		_ = response.Body.Close()
		return
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()}))
	addr := envOrDefault("TODO_ADDR", "127.0.0.1:7337")
	dataDir := envOrDefault("TODO_DATA_DIR", "./data")
	interval, err := time.ParseDuration(envOrDefault("TODO_INBOX_INTERVAL", "1s"))
	if err != nil || interval <= 0 {
		logger.Error("invalid TODO_INBOX_INTERVAL", "value", os.Getenv("TODO_INBOX_INTERVAL"))
		os.Exit(1)
	}

	if err := store.SetJiraSite(os.Getenv("TODO_JIRA_SITE")); err != nil {
		logger.Error("invalid TODO_JIRA_SITE", "error", err)
		os.Exit(1)
	}

	taskStore, err := store.New(dataDir)
	if err != nil {
		logger.Error("load task store", "error", err)
		os.Exit(1)
	}
	hub := httpapi.NewHub()
	api := httpapi.New(taskStore, hub, logger, webui.BoardHTML)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	watcher := inbox.New(filepath.Join(dataDir, "inbox"), interval, taskStore, api.Broadcast, logger)
	go watcher.Run(ctx)

	server := &http.Server{
		Addr:              addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			logger.Error("shut down server", "error", err)
		}
	}()

	logger.Info("task board listening", "address", addr, "dataDir", dataDir)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serve task board", "error", err)
		os.Exit(1)
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func logLevel() slog.Level {
	if os.Getenv("TODO_LOG_LEVEL") == "debug" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}
