// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package ack provides the domain service that records user acknowledgments of
// policy versions. The service guarantees idempotency on (user, policy_version)
// and emits an audit-tier event the first time an ack is recorded so that the
// audit log captures evidence of compliance.
package ack

import (
	"context"
	"time"
)

// RecordInput is the caller-supplied data for a single ack write.
type RecordInput struct {
	UserID          string
	PolicyVersionID string
}

// RecordResult describes the outcome of a Record call. AlreadyExisted is true
// when the (user, policy_version) pair was already acknowledged before this
// call (in which case no audit event is emitted).
//
// UserID and PolicyVersionID identify whose acknowledgment this is. They are
// stamped by Record from the verified RecordInput rather than read back from
// the store, so the audit event can name the authenticated caller: an
// acknowledgment IS the attestation, and an ack.recorded event that names
// nobody is unusable as evidence.
type RecordResult struct {
	ID              string
	AlreadyExisted  bool
	UserID          string
	PolicyVersionID string
}

// TransferResolution is how a single policy_version's ack was reconciled during
// a Transfer. Values mirror store.Resolution* constants.
type TransferResolution = string

// TransferItem describes how one policy_version's ack was (or, for a dry-run,
// would be) reconciled during a Transfer. TargetAckedAt is the target user's
// pre-transfer ack time and is nil when only the source had acked.
type TransferItem struct {
	PolicyVersionID string
	SourceAckedAt   time.Time
	TargetAckedAt   *time.Time
	Resolution      TransferResolution
}

// TransferInput identifies the users and options for a Transfer. ActorUserID
// and MergeOperationId are recorded on the audit event; DryRun previews without
// mutating.
type TransferInput struct {
	SourceUserID     string
	TargetUserID     string
	ActorUserID      string
	DryRun           bool
	MergeOperationID string
	// Caller is the service the workload-auth check verified (identity for
	// an account merge), recorded next to ActorUserID, never instead of it.
	Caller string
}

// TransferResult is the outcome of a Transfer. Moved counts re-pointed
// source-only acks; Deduped counts collisions where both users had acked.
type TransferResult struct {
	Moved   int
	Deduped int
	Items   []TransferItem
}

// Store is the persistence dependency of Service. It is satisfied by
// store.AcknowledgmentStore (via AckStoreAdapter) in production; tests inject fakes.
type Store interface {
	Insert(ctx context.Context, userID, policyVersionID string) (RecordResult, error)
	HasAcked(ctx context.Context, userID, policyVersionID string) (bool, error)
	TransferAcks(ctx context.Context, sourceUserID, targetUserID string, dryRun bool) (moved int, deduped int, items []TransferItem, err error)
}

// AuditEmitter publishes audit-tier events: "ack.recorded" for first-time acks
// and "ack.transferred" for account-merge transfers. Failures bubble up to the
// caller so audit guarantees are preserved.
type AuditEmitter interface {
	EmitAck(ctx context.Context, r RecordResult) error
	EmitTransfer(ctx context.Context, in TransferInput, res TransferResult) error
}

// TxFunc runs fn as one unit of work: every write made with the context fn
// receives commits or rolls back together (store.InTx in production).
type TxFunc func(ctx context.Context, fn func(ctx context.Context) error) error

// Service orchestrates ack persistence and audit emission.
type Service struct {
	store   Store
	auditor AuditEmitter
	inTx    TxFunc
}

// NewService constructs a Service from its persistence and audit dependencies.
func NewService(s Store, a AuditEmitter) *Service {
	return &Service{store: s, auditor: a, inTx: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }}
}

// WithTx makes each write and its audit event one unit of work, so an
// acknowledgement never commits without the event that evidences it.
func (svc *Service) WithTx(tx TxFunc) *Service {
	svc.inTx = tx
	return svc
}

// Record records an acknowledgment. It is idempotent: when (user, policy
// version) was already acked, the call short-circuits without inserting and
// without emitting a duplicate audit event.
func (svc *Service) Record(ctx context.Context, in RecordInput) (RecordResult, error) {
	already, err := svc.store.HasAcked(ctx, in.UserID, in.PolicyVersionID)
	if err != nil {
		return RecordResult{}, err
	}
	if already {
		return RecordResult{AlreadyExisted: true}, nil
	}

	var result RecordResult
	err = svc.inTx(ctx, func(ctx context.Context) error {
		var err error
		result, err = svc.store.Insert(ctx, in.UserID, in.PolicyVersionID)
		if err != nil {
			return err
		}
		result.UserID = in.UserID
		result.PolicyVersionID = in.PolicyVersionID
		return svc.auditor.EmitAck(ctx, result)
	})
	return result, err
}

// Transfer moves sourceUserID's acknowledgments onto targetUserID with dedupe,
// idempotently. When DryRun is set nothing is mutated and the result is a
// preview. On a non-dry-run that actually reconciled at least one ack
// (Moved+Deduped > 0) a single "ack.transferred" audit event is emitted.
func (svc *Service) Transfer(ctx context.Context, in TransferInput) (TransferResult, error) {
	var res TransferResult
	err := svc.inTx(ctx, func(ctx context.Context) error {
		moved, deduped, items, err := svc.store.TransferAcks(ctx, in.SourceUserID, in.TargetUserID, in.DryRun)
		if err != nil {
			return err
		}
		res = TransferResult{Moved: moved, Deduped: deduped, Items: items}
		if !in.DryRun && (moved+deduped) > 0 {
			return svc.auditor.EmitTransfer(ctx, in, res)
		}
		return nil
	})
	if err != nil {
		return TransferResult{}, err
	}
	return res, nil
}

// GetStatus reports whether a user has acknowledged a given policy version.
// Satisfies grpcsvc.AckServiceBackend.
func (svc *Service) GetStatus(ctx context.Context, userID, policyVersionID string) (bool, error) {
	return svc.store.HasAcked(ctx, userID, policyVersionID)
}
