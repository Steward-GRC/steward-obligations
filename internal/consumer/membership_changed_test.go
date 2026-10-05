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

// fakeUserReconciler satisfies consumer.UserAckReconciler and records calls.
type fakeUserReconciler struct {
	calls   []string
	deleted int
	err     error
}

func (f *fakeUserReconciler) ReconcileUserAcks(ctx context.Context, userID string) (int, error) {
	f.calls = append(f.calls, userID)
	if f.err != nil {
		return 0, f.err
	}
	return f.deleted, nil
}

func TestMembershipChanged_ReconcilesNamedUser(t *testing.T) {
	rec := &fakeUserReconciler{deleted: 2}
	c := consumer.NewMembershipChangedConsumer(rec)

	body, _ := json.Marshal(map[string]string{
		"event_type": "membership.changed",
		"user_id":    "u1",
	})
	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(rec.calls) != 1 || rec.calls[0] != "u1" {
		t.Fatalf("want ReconcileUserAcks(u1) once, got %v", rec.calls)
	}
}

func TestMembershipChanged_MissingUserID_Errors(t *testing.T) {
	rec := &fakeUserReconciler{}
	c := consumer.NewMembershipChangedConsumer(rec)

	body, _ := json.Marshal(map[string]string{"event_type": "membership.changed"})
	if err := c.Handle(context.Background(), body); err == nil {
		t.Fatal("want error for missing user_id, got nil")
	}
	if len(rec.calls) != 0 {
		t.Fatalf("reconciler should not be called on invalid event, got %v", rec.calls)
	}
}

func TestMembershipChanged_BadJSON_Errors(t *testing.T) {
	c := consumer.NewMembershipChangedConsumer(&fakeUserReconciler{})
	if err := c.Handle(context.Background(), []byte("{nope")); err == nil {
		t.Fatal("want error for bad JSON, got nil")
	}
}

func TestMembershipChanged_ReconcileError_Propagates(t *testing.T) {
	rec := &fakeUserReconciler{err: errors.New("boom")}
	c := consumer.NewMembershipChangedConsumer(rec)

	body, _ := json.Marshal(map[string]string{
		"event_type": "membership.changed",
		"user_id":    "u1",
	})
	if err := c.Handle(context.Background(), body); err == nil {
		t.Fatal("want reconcile error to propagate, got nil")
	}
}
