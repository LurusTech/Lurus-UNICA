package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/admin/internal/config"
	_ "github.com/kefu/unica/admin/internal/metrics"
	"github.com/kefu/unica/admin/internal/opsdigest"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	log.Println("[admin] starting...")

	cfg := config.Load()

	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("[admin] failed to open database: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("[admin] failed to connect to database: %v", err)
	}
	log.Println("[admin] connected to PostgreSQL")

	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatalf("[admin] invalid REDIS_URL: %v", err)
	}
	rdb := redis.NewClient(redisOpts)

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("[admin] failed to connect to Redis: %v", err)
	}
	log.Println("[admin] connected to Redis")

	stores := initStores(ctx, cfg, db, rdb)
	defer stores.auditLogger.Close()

	services := buildServices(cfg, stores)
	mux := buildMux(cfg, stores, services)

	if services.digestCfg.WebhookURL != "" {
		digest := opsdigest.New(services.digestBuilder, services.digestCfg, nil)
		digest.Start(ctx)
		defer digest.Stop()
	}

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("[admin] HTTP server listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[admin] HTTP server error: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("[admin] received signal %s, shutting down...", sig)

	shutdownCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[admin] shutdown error: %v", err)
	}

	if err := rdb.Close(); err != nil {
		log.Printf("[admin] Redis close error: %v", err)
	}

	log.Println("[admin] shutdown complete")
}
