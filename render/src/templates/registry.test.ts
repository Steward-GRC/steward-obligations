// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { test } from "node:test";

import {
  defaultSubject,
  EMAIL_COMPONENTS,
  EMAIL_KINDS,
  EMAIL_SAMPLE_VARS,
  EMAIL_SUBJECTS,
  isEmailKind,
} from "./registry.js";

test("registers every SSO lifecycle kind with a subject and component", () => {
  for (const kind of [
    "domain-verification-instructions",
    "domain-verified",
    "idp-test-failed",
    "sso-activated",
    "sp-cert-rotated",
    "sso-disabled",
    "access-granted",
    "sso-account-welcome",
    "break-glass-alert",
    "mfa-setup",
  ]) {
    assert.equal(isEmailKind(kind), true);
    assert.ok(EMAIL_SUBJECTS[kind as keyof typeof EMAIL_SUBJECTS]);
    assert.ok(EMAIL_COMPONENTS[kind as keyof typeof EMAIL_COMPONENTS]);
    assert.ok(EMAIL_SAMPLE_VARS[kind as keyof typeof EMAIL_SAMPLE_VARS]);
  }
});

test("keeps every kind name and default subject the sender relies on", () => {
  const expected: Record<string, string> = {
    "access-granted": "Your access has been granted",
    "ack-required": "Acknowledgement required",
    "assigned-as-owner": "You've been assigned as category owner",
    "break-glass-alert": "Security alert: break-glass sign-in used",
    digest: "Your Steward digest",
    "domain-verification-instructions": "Verify your domain to enable SSO",
    "domain-verified": "Your domain has been verified",
    "email-verification": "Confirm your email address",
    "idp-test-failed": "Your SSO connection test failed",
    "kratos-recovery": "Reset your Steward password",
    "mfa-setup": "Set up multi-factor authentication",
    otp: "Your verification code",
    "policy-ack-reminder": "Policy acknowledgements due",
    "policy-escalation": "Overdue: policy acknowledgement required",
    "policy-published": "New policy published",
    "policy-retired": "Policy retired",
    "raci-permission-granted": "You've been granted a new permission",
    "sp-cert-rotated": "Your SSO signing certificate was rotated",
    "sso-account-welcome": "Your Steward account is ready",
    "sso-activated": "Single sign-on is now active",
    "sso-disabled": "Single sign-on has been disabled",
    "welcome-account": "Your Steward account is ready",
    "workflow-awaiting-approval": "Your approval is needed",
    "workflow-denied": "Your submission was returned",
    "workflow-started": "A workflow has started",
  };
  assert.deepEqual([...EMAIL_KINDS].sort(), Object.keys(expected).sort());
  for (const kind of EMAIL_KINDS) {
    assert.equal(defaultSubject(kind), expected[kind], kind);
  }
});

test("defaultSubject substitutes a supplied product name", () => {
  assert.equal(defaultSubject("digest", "Example Portal"), "Your Example Portal digest");
  assert.equal(defaultSubject("otp", "Example Portal"), "Your verification code");
});

test("isEmailKind rejects non-strings and unknown kinds", () => {
  assert.equal(isEmailKind(undefined), false);
  assert.equal(isEmailKind(42), false);
  assert.equal(isEmailKind("not-a-real-kind"), false);
});
