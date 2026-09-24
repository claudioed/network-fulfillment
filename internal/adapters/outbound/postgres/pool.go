// Package postgres provides pgxpool-backed implementations of the outbound
// ports, plus a golang-migrate runner for the SQL migrations in
// /migrations.
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool opens a connection pool against databaseURL.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	return pgxpool.NewWithConfig(ctx, config)
}
