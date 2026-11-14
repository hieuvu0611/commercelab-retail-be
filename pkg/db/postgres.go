package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ConnectPostgres khởi tạo connection pool tối ưu cho trần 250MB RAM (MaxConns = 6)
// để 9 Go Microservices * 6 = 54 connections < max_connections=60 của Postgres container.
func ConnectPostgres(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 6
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pingErr := retryStartup(ctx, startupAttempts, startupRetryDelay, func() error {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return pool.Ping(pingCtx)
	})
	if pingErr != nil {
		pool.Close()
		return nil, pingErr
	}
	return pool, nil
}
