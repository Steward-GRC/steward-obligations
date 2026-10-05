// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { test } from "node:test";

import { render } from "@react-email/components";
import { createElement } from "react";

import { EMAIL_COMPONENTS, EMAIL_KINDS, EMAIL_SAMPLE_VARS, type EmailKind } from "./templates/registry.js";

const renderKind = (kind: EmailKind, vars: Record<string, unknown> = EMAIL_SAMPLE_VARS[kind]) =>
  render(createElement(EMAIL_COMPONENTS[kind], vars));

for (const kind of EMAIL_KINDS) {
  test(`${kind} renders non-empty HTML from its sampleVars`, async () => {
    const html = await renderKind(kind);
    assert.ok(html.includes("<!DOCTYPE"));
    assert.ok(html.length > 0);
  });
}

test("kratos-recovery renders the recovery code and its expiry", async () => {
  const vars = EMAIL_SAMPLE_VARS["kratos-recovery"] as {
    recoveryCode: string;
    expiresInMinutes: number;
  };
  const html = await renderKind("kratos-recovery");
  // The code is rendered digit-by-digit (en-space separated), so assert on the
  // first digit and the expiry rather than the joined string.
  assert.ok(html.includes(vars.recoveryCode[0] ?? "missing"));
  assert.ok(html.includes(String(vars.expiresInMinutes)));
  assert.ok(html.includes("Reset your Steward password"));
});

test("policy-ack-reminder caps a large list and shows an '…and N more' row", async () => {
  const vars = EMAIL_SAMPLE_VARS["policy-ack-reminder"] as { policies: unknown[] };
  assert.equal(vars.policies.length, 7);
  const html = await renderKind("policy-ack-reminder");
  assert.ok(html.includes("…and 2 more"));
});

test("sso-account-welcome renders a 'Sign in with SSO' CTA linking loginUrl and how-to steps", async () => {
  const vars = EMAIL_SAMPLE_VARS["sso-account-welcome"] as { loginUrl: string };
  const html = await renderKind("sso-account-welcome");
  assert.ok(html.includes("Sign in with SSO"));
  assert.ok(html.includes(vars.loginUrl));
  assert.ok(html.includes("How to sign in next time"));
});

test("sso-account-welcome omits the CTA button when loginUrl is not supplied", async () => {
  const base = EMAIL_SAMPLE_VARS["sso-account-welcome"];
  const { loginUrl, ...withoutLoginUrl } = base;
  const html = await renderKind("sso-account-welcome", withoutLoginUrl);
  assert.ok(!html.includes(String(loginUrl)));
  assert.ok(html.includes("How to sign in next time"));
});

// Mandatory and security mail (onboarding, codes, alerts) carries no opt-out link.
const PREFERENCES_LINK_KINDS = new Set([
  "policy-ack-reminder",
  "policy-published",
  "policy-retired",
  "ack-required",
  "policy-escalation",
  "assigned-as-owner",
  "raci-permission-granted",
  "workflow-started",
  "workflow-awaiting-approval",
  "workflow-denied",
  "domain-verification-instructions",
  "domain-verified",
  "idp-test-failed",
  "sso-activated",
  "sp-cert-rotated",
  "sso-disabled",
  "access-granted",
  "digest",
]);

for (const kind of EMAIL_KINDS) {
  const expectsLink = PREFERENCES_LINK_KINDS.has(kind);
  test(`${kind} ${expectsLink ? "renders" : "omits"} the "Manage email preferences" footer link`, async () => {
    const html = await renderKind(kind);
    assert.equal(html.includes("Manage email preferences"), expectsLink);
  });
}

test("digest renders all four grouped sections with count headers", async () => {
  const html = await renderKind("digest");
  assert.ok(html.includes("<!DOCTYPE"));
  assert.ok(html.includes("Awaiting your acknowledgement (3)"));
  assert.ok(html.includes("New &amp; updated policies (1)"));
  assert.ok(html.includes("Approvals &amp; assignments (2)"));
  assert.ok(html.includes("Other (1)"));
});

test("digest renders per-item titles and per-item CTA links", async () => {
  const vars = EMAIL_SAMPLE_VARS.digest as {
    awaitingAck: { actionHref?: string; title: string }[];
  };
  const html = await renderKind("digest");
  const escapeAmp = (value: string): string => value.replaceAll("&", "&amp;");
  for (const item of vars.awaitingAck) {
    assert.ok(html.includes(escapeAmp(item.title)));
    if (item.actionHref !== undefined) {
      assert.ok(html.includes(item.actionHref));
    }
  }
  assert.ok(html.includes("Acknowledge"));
  assert.ok(html.includes("Approve"));
});

test("digest carries both opt-out affordances (manage link + one-click unsubscribe)", async () => {
  const html = await renderKind("digest");
  assert.ok(html.includes("Manage email preferences"));
  assert.ok(html.includes("Unsubscribe"));
});

test("digest omits an empty section (no zero-count header)", async () => {
  const html = await renderKind("digest", { ...EMAIL_SAMPLE_VARS.digest, other: [] });
  assert.ok(!html.includes("Other (0)"));
  assert.ok(!html.includes("Other ("));
});

const RENDER_LEAKS = ["undefined", "[object Object]", "NaN", "null"];
const LEGAL_MARKER = "data-email-legal";

for (const kind of EMAIL_KINDS) {
  test(`${kind} leaks no unset values into its HTML`, async () => {
    const html = await renderKind(kind);
    for (const leak of RENDER_LEAKS) {
      assert.ok(!html.includes(leak), `${kind} rendered "${leak}"`);
    }
  });

  test(`${kind} renders no legal or postal footer unless legalText is given`, async () => {
    const vars = EMAIL_SAMPLE_VARS[kind];
    assert.equal(vars.legalText, undefined);
    const bare = await renderKind(kind);
    assert.ok(!bare.includes(LEGAL_MARKER));
    const withLegal = await renderKind(kind, {
      ...vars,
      legalText: "Example Organisation, 1 Example Street",
    });
    assert.ok(withLegal.includes(LEGAL_MARKER));
    assert.ok(withLegal.includes("Example Organisation, 1 Example Street"));
  });

  test(`${kind} loads no remote image unless logoSrc is given`, async () => {
    const vars = EMAIL_SAMPLE_VARS[kind];
    assert.equal(vars.logoSrc, undefined);
    assert.ok(!(await renderKind(kind)).includes("<img"));
    const logoSrc = "https://policies.example.org/static/logo.png";
    const withLogo = await renderKind(kind, { ...vars, logoSrc });
    assert.ok(withLogo.includes(`src="${logoSrc}"`));
  });

  test(`${kind} names the default product and takes an adopter product name`, async () => {
    assert.ok((await renderKind(kind)).includes("Steward"));
    const branded = await renderKind(kind, {
      ...EMAIL_SAMPLE_VARS[kind],
      productName: "Example Portal",
    });
    assert.ok(branded.includes("Example Portal"));
    assert.ok(!branded.includes("Steward"));
  });

  if (kind !== "digest") {
    test(`${kind} renders no unsubscribe line unless unsubscribeHref is given`, async () => {
      assert.ok(!(await renderKind(kind)).includes("Unsubscribe"));
    });
  }
}
