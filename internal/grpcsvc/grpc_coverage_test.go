// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"reflect"
	"testing"

	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// knownDeferred allowlists RPCs that are intentionally left unimplemented
// (falling through to the embedded UnimplementedXxxServiceServer stub, which
// returns codes.Unimplemented at runtime). Keyed by "<ServiceName>.<Method>".
// Every entry here is a deliberate, reviewed decision -- never add one to
// silence a failure without confirming the RPC really is meant to be a
// not-yet-implemented stub.
var knownDeferred = map[string]string{}

// coverageCase pairs a proto-generated gRPC service interface with a
// zero-value instance of the concrete handler that implements it.
type coverageCase struct {
	name          string
	interfaceType reflect.Type
	zeroHandler   any
}

// coverageCases enumerates every handler in this package that embeds an
// UnimplementedXxxServiceServer stub for forward-compatibility. Each such
// embed is a trap: the concrete handler compiles whether or not it overrides
// a given proto RPC, and an RPC that is never overridden silently falls
// through to the stub and returns codes.Unimplemented at runtime instead of
// failing to build. Add new handlers here as they're introduced.
func coverageCases() []coverageCase {
	return []coverageCase{
		{
			name:          "ObligationService",
			interfaceType: reflect.TypeFor[obligationsv1.ObligationServiceServer](),
			zeroHandler:   &ObligationHandler{},
		},
		{
			name:          "ReportingService",
			interfaceType: reflect.TypeFor[obligationsv1.ReportingServiceServer](),
			zeroHandler:   &ReportingHandler{},
		},
		{
			name:          "AckService",
			interfaceType: reflect.TypeFor[obligationsv1.AckServiceServer](),
			zeroHandler:   &AckHandler{},
		},
		{
			name:          "NotifPrefService",
			interfaceType: reflect.TypeFor[obligationsv1.NotifPrefServiceServer](),
			zeroHandler:   &NotifPrefHandler{},
		},
		{
			name:          "WelcomeService",
			interfaceType: reflect.TypeFor[obligationsv1.WelcomeServiceServer](),
			zeroHandler:   &WelcomeHandler{},
		},
	}
}

// TestGRPCHandlers_NoSilentUnimplementedRPC drives every unary RPC declared
// on each service interface in coverageCases against a zero-value instance
// of the concrete handler that's supposed to implement it.
//
// A genuinely implemented method dereferences a nil zero-value dependency
// (e.g. a nil backend interface field) before it can return, which panics.
// That panic is recovered below and treated as a pass -- reaching the
// dereference proves the concrete override ran rather than the embedded
// Unimplemented*Server stub. A method left to the stub returns
// codes.Unimplemented without panicking, which fails the test unless the
// method is allowlisted in knownDeferred with a documented reason.
//
// This intentionally does not require real dependencies or a running
// server: the whole point is to catch a proto RPC that compiles clean
// because the embedded stub satisfies the interface, but is never actually
// wired up on the handler.
func TestGRPCHandlers_NoSilentUnimplementedRPC(t *testing.T) {
	totalInvoked := 0

	for _, tc := range coverageCases() {
		t.Run(tc.name, func(t *testing.T) {
			handlerVal := reflect.ValueOf(tc.zeroHandler)
			caseInvoked := 0

			for m := range tc.interfaceType.Methods() {

				// Skip interface members that aren't unary RPCs, e.g. the
				// generated mustEmbedUnimplementedXxxServiceServer marker
				// method, which has no request/response pair.
				if !isUnaryRPCShape(m.Type) {
					continue
				}

				caseInvoked++
				totalInvoked++

				method := tc.name + "." + m.Name
				t.Run(m.Name, func(t *testing.T) {
					invokeAndCheck(t, method, handlerVal, m)
				})
			}

			if caseInvoked == 0 {
				t.Fatalf("%s: discovered 0 unary RPC methods on %s -- the shape filter or interface reflection is broken for this case", tc.name, tc.interfaceType)
			}
		})
	}

	// Sanity check: if reflection silently found nothing across every case
	// (e.g. a refactor changed how interfaceType/zeroHandler are wired up),
	// this test would otherwise pass trivially having verified nothing.
	if totalInvoked == 0 {
		t.Fatal("sanity check failed: no unary RPC methods were invoked across any coverage case")
	}
	t.Logf("invoked %d unary RPC(s) across %d service interface(s)", totalInvoked, len(coverageCases()))
}

// invokeAndCheck calls the handler method named by m on handlerVal with a
// background context and a zero-value request, per the shape confirmed by
// isUnaryRPCShape. See TestGRPCHandlers_NoSilentUnimplementedRPC for why a
// panic here counts as a pass.
func invokeAndCheck(t *testing.T, method string, handlerVal reflect.Value, m reflect.Method) {
	t.Helper()

	defer func() {
		if r := recover(); r != nil {
			t.Logf("%s: recovered panic from a nil zero-value dependency (treated as implemented): %v", method, r)
		}
	}()

	reqType := m.Type.In(1)
	args := []reflect.Value{
		reflect.ValueOf(context.Background()),
		reflect.New(reqType.Elem()),
	}

	out := handlerVal.MethodByName(m.Name).Call(args)

	errVal := out[len(out)-1].Interface()
	err, _ := errVal.(error)
	if err == nil {
		return
	}
	if status.Code(err) != codes.Unimplemented {
		return
	}
	if reason, ok := knownDeferred[method]; ok {
		t.Logf("%s: allowlisted as not-yet-implemented: %s", method, reason)
		return
	}
	t.Errorf("%s: returned codes.Unimplemented -- this RPC is declared on the proto service interface but is never overridden on the concrete handler, so calls silently fall through to the embedded UnimplementedXxxServiceServer stub", method)
}

// isUnaryRPCShape reports whether m matches the grpc-go unary server-method
// shape func(context.Context, *Req) (*Resp, error). Interface methods that
// don't match (e.g. the generated mustEmbedUnimplementedXxxServiceServer
// marker, which takes no arguments and returns nothing) are skipped.
func isUnaryRPCShape(m reflect.Type) bool {
	ctxType := reflect.TypeFor[context.Context]()
	errType := reflect.TypeFor[error]()

	if m.NumIn() != 2 || m.NumOut() != 2 {
		return false
	}
	if m.In(0) != ctxType {
		return false
	}
	if m.In(1).Kind() != reflect.Pointer {
		return false
	}
	if m.Out(0).Kind() != reflect.Pointer {
		return false
	}
	if m.Out(1) != errType {
		return false
	}
	return true
}
