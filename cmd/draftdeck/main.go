package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/10xdev4u-alt/princedotdev/internal/config"
	"github.com/10xdev4u-alt/princedotdev/internal/db"
	"github.com/10xdev4u-alt/princedotdev/internal/server"
)

func main() {
	args := os.Args[1:]

	// Subcommands keep the single-binary story: `draftdeck serve` (default)
	// runs the server; `draftdeck backup <dest>` takes a consistent snapshot
	// of the database (safe to run while the server is live — WAL + VACUUM
	// INTO) and `draftdeck check` verifies the DB opens and the schema is
	// intact (a startup integrity check for scripts/health probes).
	if len(args) > 0 {
		switch args[0] {
		case "backup":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "usage: draftdeck backup <dest.db>")
				os.Exit(1)
			}
			os.Exit(runBackup(args[1]))
		case "check":
			os.Exit(runCheck())
		case "serve":
			// fall through to serve below
		case "help", "-h", "--help":
			fmt.Println("usage: draftdeck [serve | backup <dest.db> | check]")
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command: %s (try: serve | backup <dest.db> | check)\n", args[0])
			os.Exit(1)
		}
	}

	cfg := config.Load()

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("draftdeck: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("draftdeck listening on :%s (data: %s)", cfg.Port, cfg.DataDir)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("draftdeck: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	// Flush the WAL into the main .db file so the volume is a tidy, complete
	// snapshot for backups / volume snapshots.
	if err := srv.Checkpoint(); err != nil {
		log.Printf("draftdeck: checkpoint: %v", err)
	}
	log.Println("draftdeck stopped")
}

func runBackup(dest string) int {
	cfg := config.Load()
	d, err := db.Open(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "backup: open: %v\n", err)
		return 1
	}
	defer d.Close()
	if err := d.Backup(dest); err != nil {
		fmt.Fprintf(os.Stderr, "backup: %v\n", err)
		return 1
	}
	abs, _ := filepath.Abs(dest)
	log.Printf("database backed up to %s", abs)
	return 0
}

func runCheck() int {
	cfg := config.Load()
	d, err := db.Open(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "check: FAIL: %v\n", err)
		return 1
	}
	defer d.Close()
	if err := d.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "check: FAIL: %v\n", err)
		return 1
	}
	log.Printf("database OK at %s", cfg.DataDir)
	return 0
}
