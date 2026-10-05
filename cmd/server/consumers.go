// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"

	"github.com/Bugs5382/go-rabbitmq"
	"github.com/rs/zerolog"
)

// jobsExchange carries the domain events the service consumes.
const jobsExchange = "jobs"

// handler is what each consumer runs on a delivery's body.
type handler interface {
	Handle(ctx context.Context, body []byte) error
}

// binding is one durable queue on the jobs exchange. Two queues may bind the
// same routing key: a topic exchange gives each its own copy.
type binding struct {
	queue, routingKey, tag string
	h                      handler
}

// consume runs one go-rabbitmq consumer per binding until ctx ends. The
// connection re-declares the topology and resumes after any drop, and a failed
// Handle is requeued.
func consume(ctx context.Context, conn *rabbitmq.Conn, logger zerolog.Logger, bs []binding) {
	for _, b := range bs {
		cfg := rabbitmq.ConsumerConfig{
			Exchange:    rabbitmq.ExchangeConfig{Name: jobsExchange, Kind: "topic", Durable: true},
			Queue:       rabbitmq.QueueConfig{Name: b.queue, Durable: true},
			Bindings:    []rabbitmq.BindingConfig{{Queue: b.queue, Exchange: jobsExchange, RoutingKey: b.routingKey}},
			ConsumerTag: b.tag,
		}
		go func(b binding) {
			handle := func(ctx context.Context, d rabbitmq.Delivery) error { return b.h.Handle(ctx, d.Body) }
			if err := conn.Consume(ctx, cfg, handle); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error().Err(err).Str("queue", b.queue).Msg("consumer stopped")
			}
		}(b)
	}
}
