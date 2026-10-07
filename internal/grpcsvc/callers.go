// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"

	log "github.com/Bugs5382/go-log"
	"google.golang.org/grpc"

	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"github.com/Steward-GRC/steward-obligations/internal/audit"
	"github.com/Steward-GRC/steward-obligations/internal/workloadauth"
)

// CallerGateway is the gateway's caller name, from its service account
// steward-gateway.
const CallerGateway = "gateway"

// CallerIdentity is identity's caller name, from its service account
// steward-identity.
const CallerIdentity = "identity"

var services = []grpc.ServiceDesc{
	obligationsv1.AckService_ServiceDesc, obligationsv1.ObligationService_ServiceDesc,
	obligationsv1.NotifPrefService_ServiceDesc, obligationsv1.ReportingService_ServiceDesc,
	obligationsv1.WelcomeService_ServiceDesc,
}

// CallerPolicy is obligations' per-method allow-list. The gateway passes the
// signed-in user's actor on every method but TransferAcknowledgments, which
// only identity's account merge calls, passing the admin who runs it. Anything
// else is refused.
func CallerPolicy() workloadauth.Policy {
	p := workloadauth.Policy{}
	for _, sd := range services {
		for _, md := range sd.Methods {
			p["/"+sd.ServiceName+"/"+md.MethodName] = map[string]workloadauth.Access{CallerGateway: workloadauth.OnBehalf}
		}
	}
	p[obligationsv1.AckService_TransferAcknowledgments_FullMethodName] = map[string]workloadauth.Access{CallerIdentity: workloadauth.OnBehalf}
	return p
}

type auditEmitter interface {
	Emit(ctx context.Context, ev audit.Event) error
}

// AuditDenial records a call the workload-auth interceptor refused, as
// rpc.denied in the audit tier. The actor is the authenticated caller (or
// "unauthenticated"), never a user the call claimed.
func AuditDenial(emitter auditEmitter, lg log.Logger) workloadauth.DenyHook {
	return func(ctx context.Context, d workloadauth.Denial) {
		caller := d.Caller.Name
		if caller == "" {
			caller = "unauthenticated"
		}
		err := emitter.Emit(ctx, audit.Event{
			Tier: audit.TierAudit, Action: "rpc.denied", ActorUserID: "service:" + caller, Subject: d.Method,
			Attributes: map[string]string{
				"method": d.Method, "caller": d.Caller.Name, "service_account": d.Caller.ServiceAccount,
				"code": d.Code.String(), "reason": d.Reason,
			},
		})
		if err != nil {
			lg.Ctx(ctx).Error(err, "audit of a refused call failed", log.F("method", d.Method), log.F("caller", caller))
		}
	}
}
