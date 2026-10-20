package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sbrw-evc/VLAN-Analizer/internal/app"
)

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	mode := "serve"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	dir := env("APP_DATA_DIR", "/var/lib/vlan-analizer")
	bundle := env("BUNDLE_DIR", "/bootstrap")
	switch mode {
	case "prepare":
		if e := app.PrepareBundle(bundle, env("CH_USERS_DIR", "/ch-users")); e != nil {
			log.Fatal(e)
		}
		return
	case "bootstrap":
		if e := app.RunBundle(ctx, bundle, env("BUNDLE_BAO_URL", "http://openbao:8200"), env("BUNDLE_CH_URL", "http://clickhouse:8123")); e != nil {
			log.Fatal(e)
		}
		return
	case "bundle-health":
		if !app.BundleReady(bundle) {
			os.Exit(1)
		}
		return
	case "health":
		if e := app.Health("http://127.0.0.1:8000/readyz"); e != nil {
			os.Exit(1)
		}
		return
	case "version":
		fmt.Println(app.Version)
		return
	}
	application, e := app.New(dir, env("MANAGED_CONFIG", filepath.Join(bundle, "managed.json")))
	if e != nil {
		log.Fatal("configuration could not be loaded: ", e)
	}
	application.Start(ctx)
	server := &http.Server{Addr: env("APP_LISTEN", ":8000"), Handler: application.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 120 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("VLAN Analizer %s listening on %s", app.Version, server.Addr)
	if e = server.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		log.Print("HTTP server stopped")
	}
	application.Close()
}
