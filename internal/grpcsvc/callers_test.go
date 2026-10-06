// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"testing"

	log "github.com/Bugs5382/go-log"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"github.com/Steward-GRC/steward-obligations/internal/audit"
	"github.com/Steward-GRC/steward-obligations/internal/workloadauth"
)

var obligationsServices = []grpc.ServiceDesc{
	obligationsv1.AckService_ServiceDesc, obligationsv1.ObligationService_ServiceDesc,
	obligationsv1.NotifPrefService_ServiceDesc, obligationsv1.ReportingService_ServiceDesc,
	obligationsv1.WelcomeService_ServiceDesc,
}

func allMethods() []string {
	var out []string
	for _, sd := range obligationsServices {
		for _, md := range sd.Methods {
			out = append(out, "/"+sd.ServiceName+"/"+md.MethodName)
		}
	}
	return out
}

func TestCallerPolicyCoversEveryMethodAndNothingElse(t *testing.T) {
	p := CallerPolicy()
	methods := map[string]bool{}
	for _, m := range allMethods() {
		methods[m] = true
		_, listed := p[m]
		require.True(t, listed, "%s is missing from the policy", m)
	}
	for m := range p {
		require.True(t, methods[m], "the policy lists %s, which obligations doesn't serve", m)
	}
}

// The gateway passes the signed-in user on every method it calls. Nobody
// calls TransferAcknowledgments yet (identity's merge isn't wired to it), so
// it is refused to every caller.
func TestCallerPolicyGatewayActsOnBehalfOnEveryMethodButTheMergeTransfer(t *testing.T) {
	p := CallerPolicy()
	for _, m := range allMethods() {
		a, ok := p.Lookup(m, CallerGateway)
		if m == obligationsv1.AckService_TransferAcknowledgments_FullMethodName {
			require.False(t, ok, "%s has no caller", m)
			continue
		}
		require.True(t, ok, "%s refuses the gateway", m)
		require.Equal(t, workloadauth.OnBehalf, a, m)
	}
}

func TestCallerPolicyListsNoOtherCaller(t *testing.T) {
	for m, callers := range CallerPolicy() {
		for c := range callers {
			require.Equal(t, CallerGateway, c, "%s lists caller %q", m, c)
		}
	}
	for _, other := range []string{"reporting", "identity", "core", "workflow", "collab", "ai", "delivery"} {
		for _, m := range allMethods() {
			_, ok := CallerPolicy().Lookup(m, other)
			require.False(t, ok, "%s lets %s call it", m, other)
		}
	}
}

type recordingEmitter struct{ evs []audit.Event }

func (r *recordingEmitter) Emit(_ context.Context, ev audit.Event) error {
	r.evs = append(r.evs, ev)
	return nil
}

func TestAuditDenialRecordsTheCallerNotAClaimedUser(t *testing.T) {
	rec := &recordingEmitter{}
	hook := AuditDenial(rec, log.Nop())
	hook(context.Background(), workloadauth.Denial{
		Method: obligationsv1.AckService_RecordAck_FullMethodName, Code: codes.PermissionDenied, Reason: workloadauth.ReasonMethodNotAllowed,
		Caller: workloadauth.Caller{Name: "reporting", ServiceAccount: "steward/steward-reporting"},
	})
	hook(context.Background(), workloadauth.Denial{Method: "/m", Code: codes.Unauthenticated, Reason: workloadauth.ReasonNoToken})
	require.Len(t, rec.evs, 2)
	require.Equal(t, audit.TierAudit, rec.evs[0].Tier)
	require.Equal(t, "rpc.denied", rec.evs[0].Action)
	require.Equal(t, "service:reporting", rec.evs[0].ActorUserID)
	require.Equal(t, obligationsv1.AckService_RecordAck_FullMethodName, rec.evs[0].Subject)
	require.Equal(t, map[string]string{
		"method": obligationsv1.AckService_RecordAck_FullMethodName, "caller": "reporting", "service_account": "steward/steward-reporting",
		"code": "PermissionDenied", "reason": workloadauth.ReasonMethodNotAllowed,
	}, rec.evs[0].Attributes)
	require.Equal(t, "service:unauthenticated", rec.evs[1].ActorUserID)
}
