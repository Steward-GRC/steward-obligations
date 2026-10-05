// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"

	postgres "github.com/Bugs5382/go-postgres"
	"github.com/jackc/pgx/v5"
)

type txKey struct{}

// InTx runs fn in one transaction. Store calls made with the context fn
// receives join it, so an acknowledgement and the audit event enqueued for it
// commit or roll back together. Inside a transaction already, fn joins that
// one. fn may run more than once: go-postgres retries serialization failures.
func InTx(ctx context.Context, db *postgres.DB, fn func(ctx context.Context) error) error {
	if _, ok := TxFrom(ctx); ok {
		return fn(ctx)
	}
	return db.RunInTx(ctx, func(tx pgx.Tx) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
}

// TxFrom returns the transaction InTx put in ctx.
func TxFrom(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	return tx, ok
}

func querier(ctx context.Context, db *postgres.DB) postgres.Querier {
	if tx, ok := TxFrom(ctx); ok {
		return tx
	}
	return db.Querier()
}
