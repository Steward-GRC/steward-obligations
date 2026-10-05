// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notifytoken

import (
	"testing"
	"time"
)

func mustSigner(t *testing.T, secret string) *Signer {
	t.Helper()
	s, err := NewSigner(secret)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

func TestNewSignerRejectsEmptySecret(t *testing.T) {
	if _, err := NewSigner(""); err != ErrNoSecret {
		t.Fatalf("want ErrNoSecret, got %v", err)
	}
}

func TestUnsubscribeRoundTrip(t *testing.T) {
	s := mustSigner(t, "top-secret")
	tok, err := s.MintUnsubscribe("user-1", "informational", time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	c, err := s.Verify(tok, PurposeUnsubscribe)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if c.UserID != "user-1" || c.Category != "informational" || c.Purpose != PurposeUnsubscribe {
		t.Fatalf("unexpected claims: %+v", c)
	}
}

func TestPreferencesRoundTrip(t *testing.T) {
	s := mustSigner(t, "top-secret")
	tok, err := s.MintPreferences("user-2", time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	c, err := s.Verify(tok, PurposePreferences)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if c.UserID != "user-2" || c.Category != "" || c.Purpose != PurposePreferences {
		t.Fatalf("unexpected claims: %+v", c)
	}
}

func TestEmailVerifyRoundTrip(t *testing.T) {
	s := mustSigner(t, "top-secret")
	tok, err := s.MintEmailVerify("user-3", "erin@example.org", time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	c, err := s.Verify(tok, PurposeEmailVerify)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if c.UserID != "user-3" || c.Email != "erin@example.org" || c.Purpose != PurposeEmailVerify {
		t.Fatalf("unexpected claims: %+v", c)
	}
}

// TestEmailVerifyRejectsWrongPurpose: an unsubscribe/preferences token must
// never be replayable as an email-verify token, or vice-versa.
func TestEmailVerifyRejectsWrongPurpose(t *testing.T) {
	s := mustSigner(t, "top-secret")
	tok, _ := s.MintPreferences("u", time.Hour)
	if _, err := s.Verify(tok, PurposeEmailVerify); err != ErrPurpose {
		t.Fatalf("want ErrPurpose, got %v", err)
	}
	verifyTok, _ := s.MintEmailVerify("u", "u@example.org", time.Hour)
	if _, err := s.Verify(verifyTok, PurposePreferences); err != ErrPurpose {
		t.Fatalf("want ErrPurpose, got %v", err)
	}
}

func TestEmailVerifyRejectsWrongSecret(t *testing.T) {
	minter := mustSigner(t, "secret-A")
	verifier := mustSigner(t, "secret-B")
	tok, _ := minter.MintEmailVerify("u", "u@example.org", time.Hour)
	if _, err := verifier.Verify(tok, PurposeEmailVerify); err != ErrBadSignature {
		t.Fatalf("want ErrBadSignature, got %v", err)
	}
}

// TestEmailVerifyRejectsTamperedEmail: flipping the payload (e.g. swapping in
// a different address post-signature) must fail the MAC check, not silently
// verify a different address than the one the token was minted for.
func TestEmailVerifyRejectsTamperedEmail(t *testing.T) {
	s := mustSigner(t, "top-secret")
	tok, _ := s.MintEmailVerify("u", "erin@example.org", time.Hour)
	b := []byte(tok)
	dot := 0
	for i, c := range b {
		if c == '.' {
			dot = i
			break
		}
	}
	if b[dot-1] == 'A' {
		b[dot-1] = 'B'
	} else {
		b[dot-1] = 'A'
	}
	if _, err := s.Verify(string(b), PurposeEmailVerify); err != ErrBadSignature {
		t.Fatalf("want ErrBadSignature, got %v", err)
	}
}

func TestEmailVerifyRejectsExpired(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := mustSigner(t, "top-secret")
	s.now = func() time.Time { return base }
	tok, _ := s.MintEmailVerify("u", "erin@example.org", time.Minute)
	s.now = func() time.Time { return base.Add(2 * time.Minute) }
	if _, err := s.Verify(tok, PurposeEmailVerify); err != ErrExpired {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

func TestVerifyRejectsWrongPurpose(t *testing.T) {
	s := mustSigner(t, "top-secret")
	tok, _ := s.MintUnsubscribe("u", "workflow", time.Hour)
	if _, err := s.Verify(tok, PurposePreferences); err != ErrPurpose {
		t.Fatalf("want ErrPurpose, got %v", err)
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	minter := mustSigner(t, "secret-A")
	verifier := mustSigner(t, "secret-B")
	tok, _ := minter.MintUnsubscribe("u", "workflow", time.Hour)
	if _, err := verifier.Verify(tok, PurposeUnsubscribe); err != ErrBadSignature {
		t.Fatalf("want ErrBadSignature, got %v", err)
	}
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	s := mustSigner(t, "top-secret")
	tok, _ := s.MintUnsubscribe("u", "workflow", time.Hour)
	// Flip the last character of the payload segment.
	b := []byte(tok)
	dot := 0
	for i, c := range b {
		if c == '.' {
			dot = i
			break
		}
	}
	if b[dot-1] == 'A' {
		b[dot-1] = 'B'
	} else {
		b[dot-1] = 'A'
	}
	if _, err := s.Verify(string(b), PurposeUnsubscribe); err != ErrBadSignature {
		t.Fatalf("want ErrBadSignature, got %v", err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := mustSigner(t, "top-secret")
	s.now = func() time.Time { return base }
	tok, _ := s.MintUnsubscribe("u", "workflow", time.Minute)
	// Advance the clock past expiry.
	s.now = func() time.Time { return base.Add(2 * time.Minute) }
	if _, err := s.Verify(tok, PurposeUnsubscribe); err != ErrExpired {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	s := mustSigner(t, "top-secret")
	for _, bad := range []string{"", "nodot", ".", "a.", ".b", "not_base64!!.sig"} {
		if _, err := s.Verify(bad, PurposeUnsubscribe); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}
