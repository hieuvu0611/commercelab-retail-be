package main

import (
	"database/sql"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestEveryServiceHasValidEmbeddedMigrations(t *testing.T) {
	// sql.Open does not connect, so the provider only parses the embedded files here.
	lazyDB, err := sql.Open("pgx", "postgres://unused:unused@127.0.0.1:1/unused")
	if err != nil {
		t.Fatal(err)
	}
	defer lazyDB.Close()

	for _, target := range allTargets() {
		provider, err := goose.NewProvider(goose.DialectPostgres, lazyDB, target.fs)
		if err != nil {
			t.Fatalf("%s: invalid migrations: %v", target.name, err)
		}
		sources := provider.ListSources()
		if len(sources) == 0 {
			t.Fatalf("%s: no migrations embedded", target.name)
		}
		if sources[0].Version != 1 {
			t.Fatalf("%s: first migration must be version 1, got %d", target.name, sources[0].Version)
		}
	}
}

func TestDefaultDSNsPinSearchPathToOwnSchema(t *testing.T) {
	for _, target := range allTargets() {
		if !strings.Contains(target.defaultDSN, "search_path=") {
			t.Fatalf("%s: default DSN has no search_path: %s", target.name, target.defaultDSN)
		}
	}
}

func TestSelectTargetsRejectsUnknownService(t *testing.T) {
	if _, err := selectTargets([]string{"nope"}); err == nil {
		t.Fatal("expected an error for an unknown service")
	}
	got, err := selectTargets([]string{"iam"})
	if err != nil || len(got) != 1 || got[0].name != "iam" {
		t.Fatalf("selectTargets(iam) = %v, %v", got, err)
	}
}
