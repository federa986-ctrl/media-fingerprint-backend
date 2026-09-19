package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"media-fingerprint/internal/fingerprint"
)

func main() {
	port := getenv("PORT", "8080")
	service := fingerprint.NewService(fingerprint.Config{
		Workers:         getenvInt("FINGERPRINT_WORKERS", 0),
		QueueSize:       getenvInt("FINGERPRINT_QUEUE_SIZE", 16),
		MaxUploadBytes:  getenvInt64("FINGERPRINT_MAX_UPLOAD_BYTES", 512<<20),
		MaxTempBytes:    getenvInt64("FINGERPRINT_MAX_TEMP_BYTES", 2<<30),
		DefaultInterval: time.Duration(getenvInt("FINGERPRINT_INTERVAL_MS", 1000)) * time.Millisecond,
		MaxSamples:      getenvInt("FINGERPRINT_MAX_SAMPLES", 3600),
		TempDir:         os.TempDir(),
	})
	defer service.Close()

	maxConnections := getenvInt("FINGERPRINT_MAX_CONNECTIONS", 512)
	var activeConnections atomic.Int64
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           service.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Duration(getenvInt("FINGERPRINT_HTTP_TIMEOUT_SECONDS", 300)) * time.Second,
		WriteTimeout:      time.Duration(getenvInt("FINGERPRINT_HTTP_TIMEOUT_SECONDS", 300)) * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ConnState: func(conn net.Conn, state http.ConnState) {
			switch state {
			case http.StateNew:
				if maxConnections > 0 && activeConnections.Add(1) > int64(maxConnections) {
					_ = conn.Close()
				}
			case http.StateClosed:
				activeConnections.Add(-1)
			}
		},
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		panic(err)
	}
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	value := os.Getenv(key)
	var parsed int
	if value != "" {
		if _, err := fmt.Sscan(value, &parsed); err == nil {
			return parsed
		}
	}
	return fallback
}

func getenvInt64(key string, fallback int64) int64 {
	value := os.Getenv(key)
	var parsed int64
	if value != "" {
		if _, err := fmt.Sscan(value, &parsed); err == nil {
			return parsed
		}
	}
	return fallback
}
