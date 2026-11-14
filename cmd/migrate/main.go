// Command migrate applies the versioned SQL migrations of every service (or one service) to Postgres.
//
// Usage: go run ./cmd/migrate [service ...]
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/migrate"
)

const migrationTimeout = 2 * time.Minute

func main() {
	selected := os.Args[1:]
	targets, err := selectTargets(selected)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), migrationTimeout)
	defer cancel()

	for _, target := range targets {
		dsn := config.EnvOrDefault(target.dsnEnv, target.defaultDSN)
		results, err := migrate.Up(ctx, dsn, target.fs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] migration failed: %v\n", target.name, err)
			os.Exit(1)
		}
		fmt.Printf("[%s] applied %d migration(s)\n", target.name, len(results))
		for _, result := range results {
			fmt.Printf("[%s]   %s\n", target.name, result)
		}
	}
}
