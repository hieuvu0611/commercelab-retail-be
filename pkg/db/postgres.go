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

	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(pingCtx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
