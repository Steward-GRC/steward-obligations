// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"

	outbox "github.com/Bugs5382/go-outbox"
	postgres "github.com/Bugs5382/go-postgres"
)

// OutboxPublisher enqueues messages on a go-outbox table instead of sending
// them; the outbox relay delivers them to RabbitMQ after the commit. A
// Publish inside InTx joins that transaction, so the message exists only if
// the write it describes committed.
type OutboxPublisher struct {
	db          *postgres.DB
	ob          *outbox.Outbox
	contentType string
}

// NewOutboxPublisher returns a publisher writing to ob on db. Every message
// carries contentType.
func NewOutboxPublisher(db *postgres.DB, ob *outbox.Outbox, contentType string) *OutboxPublisher {
	return &OutboxPublisher{db: db, ob: ob, contentType: contentType}
}

// Publish enqueues body with routingKey as its topic.
func (p *OutboxPublisher) Publish(ctx context.Context, routingKey string, body []byte) error {
	return InTx(ctx, p.db, func(ctx context.Context) error {
		_, err := p.ob.Enqueue(ctx, querier(ctx, p.db), outbox.Message{Topic: routingKey, Payload: body, ContentType: p.contentType})
		return err
	})
}
