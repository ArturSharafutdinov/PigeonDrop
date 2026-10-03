package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
)

// InTx returns a function that runs fn inside one database transaction: committed if fn returns nil,
// rolled back if it returns an error or panics. The queries fn receives all belong to that transaction.
func InTx(pool *pgxpool.Pool) func(ctx context.Context, fn func(q dbgen.Querier) error) error {
	return func(ctx context.Context, fn func(q dbgen.Querier) error) error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin transaction: %w", err)
		}
		// Rolling back after a commit is a no-op; this covers every early return and a panic in fn.
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

		if err := fn(dbgen.New(tx)); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit transaction: %w", err)
		}
		return nil
	}
}
