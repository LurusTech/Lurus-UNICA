package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/kefu/unica/reporter/internal/config"
	"github.com/kefu/unica/reporter/internal/handler"
	_ "github.com/kefu/unica/reporter/internal/metrics" // register Prometheus metrics
	"github.com/kefu/unica/reporter/internal/repository"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	log.Println("[reporter] starting...")

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[reporter] failed to load config: %v", err)
	}

	// Connect to database
	if err := cfg.ConnectDB(); err != nil {
		log.Fatalf("[reporter] failed to connect to database: %v", err)
	}
	defer cfg.DB.Close()
	log.Println("[reporter] connected to database")

	// Create repository and handler
	repo := repository.NewRepository(cfg.DB)
	h := handler.NewHandler(repo)

	// Build HTTP mux
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Prometheus metrics endpoint
	mux.Handle("/metrics", promhttp.Handler())

	// Register report API routes
	h.RegisterRoutes(mux)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start HTTP server
	go func() {
		log.Printf("[reporter] HTTP server listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[reporter] HTTP server error: %v", err)
		}
	}()

	// Graceful shutdown
	ctx := context.Background()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("[reporter] received signal %s, shutting down...", sig)

	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[reporter] shutdown error: %v", err)
	}

	log.Println("[reporter] shutdown complete")
}
