package main

import (
	"context"
	"errors"
	"flag"
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
	"github.com/10xdev4u-alt/princedotdev/internal/mcp"
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
		case "user:create", "create-user":
			os.Exit(runCreateUser(args[1:]))
		case "mcp":
			// MCP stdio server: `docker exec -i <c> draftdeck mcp` or a local
			// binary, wired to Claude Code via `claude mcp add draftdeck -- …`.
			apiURL := os.Getenv("DRAFTDECK_API_URL")
			if apiURL == "" {
				apiURL = config.Load().PublicBaseURL
			}
			srv := mcp.New(apiURL, os.Getenv("DRAFTDECK_API_KEY"))
			if err := srv.Run(os.Stdin, os.Stdout); err != nil {
				log.Printf("draftdeck mcp: %v", err)
				os.Exit(1)
			}
			return
		case "serve":
			// fall through to serve below
		case "help", "-h", "--help":
			fmt.Println("usage: draftdeck [serve | user:create --name X --email Y | backup <dest.db> | check]")
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command: %s (try: serve | user:create | backup <dest.db> | check)\n", args[0])
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

// runCreateUser bootstraps the first account: creates it and prints a fresh
// API key (shown once). This is how you onboard before any session exists.
func runCreateUser(args []string) int {
	fs := flag.NewFlagSet("user:create", flag.ContinueOnError)
	name := fs.String("name", "", "Account name")
	email := fs.String("email", "", "Account email")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "usage: draftdeck user:create --name X --email Y")
		return 1
	}
	if *name == "" {
		fmt.Fprintln(os.Stderr, "--name is required")
		return 1
	}
	cfg := config.Load()
	d, err := db.Open(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "user:create: %v\n", err)
		return 1
	}
	defer d.Close()
	id, err := d.CreateAccount(*name, *email)
	if err != nil {
		fmt.Fprintf(os.Stderr, "user:create: %v\n", err)
		return 1
	}
	_, token, err := d.CreateAPIKey(id, "CLI · "+time.Now().UTC().Format("2006-01-02"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "user:create: %v\n", err)
		return 1
	}
	fmt.Printf("account: %s (%s)\n", *name, id)
	fmt.Printf("api key: %s\n", token)
	fmt.Println("save it now — it is shown only once. Paste it into the dashboard sign-in.")
	return 0
}
