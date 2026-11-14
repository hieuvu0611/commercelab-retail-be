package migrate

import (
	"context"
	"testing"
	"testing/fstest"
	"time"
)

func TestEnabledOnStartOnlyForTrue(t *testing.T) {
	cases := map[string]bool{"": false, "false": false, "1": false, "true": true}
	for value, want := range cases {
		t.Setenv(StartEnvVar, value)
		if got := EnabledOnStart(); got != want {
			t.Fatalf("MIGRATE_ON_START=%q: got %v, want %v", value, got, want)
		}
	}
}

func TestUpFailsFastOnUnreachablePostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fsys := fstest.MapFS{
		"00001_init.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")},
	}
	if _, err := Up(ctx, "postgres://invalid:invalid@127.0.0.1:1/commerce_lab?sslmode=disable", fsys); err == nil {
		t.Fatal("expected an error when postgres is unreachable")
	}
}

func TestUpRejectsEmptyMigrationSet(t *testing.T) {
	if _, err := Up(context.Background(), "postgres://x:x@127.0.0.1:1/x", fstest.MapFS{}); err == nil {
		t.Fatal("expected an error when no migrations exist")
	}
}
