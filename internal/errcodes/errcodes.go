// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package errcodes holds the obligations service's coded errors (band 7) and
// turns them into gRPC statuses through go-apperr.
package errcodes

import (
	"context"
	"errors"
	"sync"

	apperr "github.com/Bugs5382/go-apperr"
	"github.com/Bugs5382/go-apperr/apperrgrpc"
	log "github.com/Bugs5382/go-log"
)

// Domain is the ErrorInfo domain every obligations error carries.
const Domain = "obligations"

// The obligations service's codes.
const (
	CodeInternal                 = 7000
	CodeStoreUnavailable         = 7001
	CodeWelcomeSendUnavailable   = 7002
	CodeAckAuthRequired          = 7003
	CodePrefMandatoryOff         = 7004
	CodePrefInvalid              = 7005
	CodeAckTransferActorRequired = 7006
)

// Entries returns the registry entries.
func Entries() []apperr.Entry {
	return []apperr.Entry{
		{Code: CodeInternal, Symbol: "INTERNAL", Category: apperr.CategoryInternal,
			Title: "obligations", Cause: "an uncoded failure inside the obligations service"},
		{Code: CodeStoreUnavailable, Symbol: "NOTIFY_STORE_UNAVAILABLE", Category: apperr.CategoryInternal,
			Title: "obligations store", Cause: "an obligations Postgres read or write failed; the op metadata names it, the cause is only logged"},
		{Code: CodeWelcomeSendUnavailable, Symbol: "WELCOME_SEND_UNAVAILABLE", Category: apperr.CategoryInternal,
			Title: "resend welcome", Cause: "the welcome email could not be handed to the mail sender (a paused transport holds it instead and reports it sent); user_id names the recipient"},
		{Code: CodeAckAuthRequired, Symbol: "ACK_AUTH_REQUIRED", Category: apperr.CategoryUnauthenticated,
			Title: "acknowledge", Cause: "RecordAck or RecordView ran without a forwarded actor to attribute it to",
			UserSafe: true, Message: "You need to be signed in to acknowledge or view a policy."},
		{Code: CodePrefMandatoryOff, Symbol: "NOTIFY_PREF_MANDATORY_OFF", Category: apperr.CategoryInvalid,
			Title: "notification preferences", Cause: "a cadence write tried to turn off a mandatory category or type; category names it",
			UserSafe: true, Message: "This category is required for compliance and can't be turned off — choose a digest instead."},
		{Code: CodePrefInvalid, Symbol: "NOTIFY_PREF_INVALID", Category: apperr.CategoryInvalid,
			Title: "notification preferences", Cause: "a cadence write carried an unknown category, cadence or kind, or a digest window out of range; field names it",
			UserSafe: true, Message: "That notification preference isn't valid — check the value and try again."},
		{Code: CodeAckTransferActorRequired, Symbol: "ACK_TRANSFER_ACTOR_REQUIRED", Category: apperr.CategoryInvalid,
			Title: "transfer acknowledgements", Cause: "a real (not dry-run) transfer named no actor_user_id, so its audit event would name nobody"},
	}
}

var (
	regOnce sync.Once
	reg     *apperr.Registry
)

// Registry returns the service registry. Coded errors are logged through
// go-log with the trace of the request they failed.
func Registry() *apperr.Registry {
	regOnce.Do(func() {
		r, err := apperr.NewRegistry(Entries(), apperr.WithService(7), apperr.WithCodeDigits(4),
			apperr.WithLogger(logSink{log.NewLogger("obligations")}))
		if err != nil {
			panic(err)
		}
		reg = r
	})
	return reg
}

// Error turns err into the gRPC error a handler returns.
func Error(ctx context.Context, err error) error {
	return apperrgrpc.Error(ctx, Registry(), err, CodeInternal, Domain)
}

// Doc is the Markdown body of docs/error-codes.md.
func Doc() string {
	return "# Error codes\n\nEvery coded gRPC error from the obligations service carries an `ErrorInfo` with the symbol as\n" +
		"its reason, the domain `" + Domain + "` and the code in `codeNum`. Only user-safe messages reach\n" +
		"the caller; every other code is sent as `Code N: Internal Error`.\n\n" + Registry().Markdown()
}

// StoreUnavailable codes a failed store call; op names it.
func StoreUnavailable(op string, cause error) error {
	return apperr.WithMeta(apperr.Coded(CodeStoreUnavailable, cause), apperr.Meta("op", op))
}

// WelcomeSendUnavailable codes a failed welcome send to userID.
func WelcomeSendUnavailable(userID string, cause error) error {
	return apperr.WithMeta(apperr.Coded(CodeWelcomeSendUnavailable, cause), apperr.Meta("user_id", userID))
}

var (
	errNoActor         = errors.New("obligations: no forwarded actor")
	errMandatoryOff    = errors.New("obligations: a mandatory notification can't be turned off")
	errInvalidPref     = errors.New("obligations: invalid notification preference")
	errNoTransferActor = errors.New("obligations: the transfer names no actor")
)

// AckAuthRequired codes an acknowledgement or view with no actor.
func AckAuthRequired() error { return apperr.Coded(CodeAckAuthRequired, errNoActor) }

// PrefMandatoryOff codes a refused OFF for a mandatory category.
func PrefMandatoryOff(category string, cause error) error {
	if cause == nil {
		cause = errMandatoryOff
	}
	return apperr.WithMeta(apperr.Coded(CodePrefMandatoryOff, cause), apperr.Meta("category", category))
}

// PrefInvalid codes an invalid preference write; field names the input.
func PrefInvalid(field string, cause error) error {
	if cause == nil {
		cause = errInvalidPref
	}
	return apperr.WithMeta(apperr.Coded(CodePrefInvalid, cause), apperr.Meta("field", field))
}

// AckTransferActorRequired codes a transfer that names no actor.
func AckTransferActorRequired() error {
	return apperr.Coded(CodeAckTransferActorRequired, errNoTransferActor)
}

type logSink struct{ l log.Logger }

func (s logSink) LogCoded(ctx context.Context, code int, err error) {
	s.l.Ctx(ctx).Debug("coded error", log.F("code", code), log.F("error", err.Error()))
}
