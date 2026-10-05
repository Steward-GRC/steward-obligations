// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/consumer"
)

// fakeReconciler satisfies consumer.AckReconciler and records its calls.
type fakeReconciler struct {
	calls   []string
	deleted int
	err     error
}

func (f *fakeReconciler) ReconcilePolicyAcks(ctx context.Context, policyID string) (int, error) {
	f.calls = append(f.calls, policyID)
	if f.err != nil {
		return 0, f.err
	}
	return f.deleted, nil
}

func TestObligationChanged_ReconcilesNamedPolicy(t *testing.T) {
	rec := &fakeReconciler{deleted: 3}
	c := consumer.NewObligationChangedConsumer(rec)

	body, _ := json.Marshal(map[string]string{
		"event_type": "policy.obligation_changed",
		"policy_id":  "p1",
	})
	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(rec.calls) != 1 || rec.calls[0] != "p1" {
		t.Fatalf("want ReconcilePolicyAcks(p1) once, got %v", rec.calls)
	}
}

func TestObligationChanged_MissingPolicyID_Errors(t *testing.T) {
	rec := &fakeReconciler{}
	c := consumer.NewObligationChangedConsumer(rec)

	body, _ := json.Marshal(map[string]string{"event_type": "policy.obligation_changed"})
	if err := c.Handle(context.Background(), body); err == nil {
		t.Fatal("want error for missing policy_id, got nil")
	}
	if len(rec.calls) != 0 {
		t.Fatalf("reconciler should not be called on invalid event, got %v", rec.calls)
	}
}

func TestObligationChanged_BadJSON_Errors(t *testing.T) {
	c := consumer.NewObligationChangedConsumer(&fakeReconciler{})
	if err := c.Handle(context.Background(), []byte("{not json")); err == nil {
		t.Fatal("want error for bad JSON, got nil")
	}
}

func TestObligationChanged_ReconcileError_Propagates(t *testing.T) {
	rec := &fakeReconciler{err: errors.New("boom")}
	c := consumer.NewObligationChangedConsumer(rec)

	body, _ := json.Marshal(map[string]string{
		"event_type": "policy.obligation_changed",
		"policy_id":  "p1",
	})
	if err := c.Handle(context.Background(), body); err == nil {
		t.Fatal("want reconcile error to propagate (nack+requeue), got nil")
	}
}
