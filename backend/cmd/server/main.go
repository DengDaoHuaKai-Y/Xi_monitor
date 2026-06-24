package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"xi_monitor/backend/internal/auth"
	"xi_monitor/backend/internal/config"
	"xi_monitor/backend/internal/crypto"
	"xi_monitor/backend/internal/httpapi"
	"xi_monitor/backend/internal/poller"
	"xi_monitor/backend/internal/store"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config.yaml")
	hashPassword := flag.String("hash-password", "", "print bcrypt hash for the provided password and exit")
	flag.Parse()

	if *hashPassword != "" {
		hash, err := auth.HashPassword(*hashPassword)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(hash)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatal(err)
	}

	box, err := crypto.NewSecretBox(cfg.Security.EncryptionKey)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.Database.DSN)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	if err := st.Migrate(ctx); err != nil {
		log.Fatal(err)
	}

	authSvc := auth.NewService(cfg.Admin.Username, cfg.Admin.PasswordHash, cfg.Security.SessionSecret)
	pl := poller.New(st, box, time.Duration(cfg.Poller.IntervalSeconds)*time.Second)
	go pl.Start(ctx)

	router := httpapi.NewRouter(authSvc, box, st, pl)
	httpapi.ServeFrontend(router, os.Getenv("MID_FRONTEND_DIST"))
	server := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("server listening on %s", cfg.Server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("server shutdown: %v", err)
	}
}
