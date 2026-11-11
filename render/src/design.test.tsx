// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// These tests pin the email layout to the stored design: the "email" tokens in
// design/tokens.json and the frames in "Steward Emails" (issue #28).

import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { render } from "@react-email/components";
import { createElement } from "react";

import { darkPalette, fontStack, headingFontStack, palette } from "./components/palette.js";
import { EMAIL_COMPONENTS, EMAIL_KINDS, EMAIL_SAMPLE_VARS, type EmailKind } from "./templates/registry.js";

const renderKind = (kind: EmailKind, vars: Record<string, unknown> = EMAIL_SAMPLE_VARS[kind]) =>
  render(createElement(EMAIL_COMPONENTS[kind], vars));

const indexOfOrFail = (html: string, needle: string): number => {
  const at = html.indexOf(needle);
  assert.ok(at >= 0, `missing ${needle}`);
  return at;
};

test("the palette is the stored email tokens, light client", () => {
  assert.equal(palette.canvas, "#F8F4EE");
  assert.equal(palette.text, "#261B25");
  assert.equal(palette.muted, "#5E525F");
  assert.equal(palette.border, "#E3D9CE");
  assert.equal(palette.accent, "#5D2A5B");
  assert.equal(palette.accentText, "#FFFFFF");
  assert.equal(palette.surface, "#FFFFFF");
  assert.equal(palette.footer, "#F8F4EE");
  assert.equal(palette.error, "#A3342C");
});

test("the palette carries the stored dark client tokens", () => {
  assert.equal(darkPalette.canvas, "#120C14");
  assert.equal(darkPalette.surface, "#1E1620");
  assert.equal(darkPalette.text, "#F3ECF2");
  assert.equal(darkPalette.muted, "#C9B8C7");
  assert.equal(darkPalette.border, "#3A2D3D");
  assert.equal(darkPalette.footer, "#171019");
});

test("type is Arial for the body and Georgia for the wordmark", () => {
  assert.equal(fontStack, "Arial, Helvetica, sans-serif");
  assert.equal(headingFontStack, "Georgia, serif");
});

test("the container is 600 wide, fluid on a 360 phone, with a 6 px radius", async () => {
  const html = await renderKind("otp");
  assert.ok(html.includes("max-width:600px"));
  assert.ok(html.includes("width:100%"));
  assert.ok(html.includes("border-radius:6px"));
  assert.ok(!html.includes("border-radius:8px"));
  assert.ok(html.includes('name="viewport"'));
  assert.ok(html.includes(`background-color:${palette.canvas}`));
});

test("body text is Arial 16/24 and the title is 22/28 bold", async () => {
  const html = await renderKind("ack-required");
  assert.ok(html.includes("font-family:Arial, Helvetica, sans-serif"));
  assert.ok(html.includes("font-size:16px;line-height:24px") || html.includes("line-height:24px;font-size:16px"));
  assert.ok(html.includes("font-size:22px"));
  assert.ok(html.includes("line-height:28px"));
  assert.ok(html.includes("font-weight:700"));
});

test("every email opens with the 4 px plum rule above the header lockup", async () => {
  for (const kind of EMAIL_KINDS) {
    const html = await renderKind(kind);
    const rule = indexOfOrFail(html, "data-email-rule");
    const lockup = indexOfOrFail(html, "data-email-lockup");
    assert.ok(rule < lockup, kind);
    const ruleTag = html.slice(html.lastIndexOf("<", rule), html.indexOf(">", rule));
    assert.ok(ruleTag.includes("height:4px"), kind);
    assert.ok(ruleTag.includes(`background-color:${palette.accent}`), kind);
  }
});

test("the lockup is a 28 px paper tile, a Georgia wordmark and the organisation name on the right", async () => {
  const html = await renderKind("otp", { ...EMAIL_SAMPLE_VARS.otp, organisationName: "Example Organisation" });
  const tile = indexOfOrFail(html, "data-email-tile");
  const tileTag = html.slice(html.lastIndexOf("<", tile), html.indexOf(">", tile));
  assert.ok(tileTag.includes("width:28px"));
  assert.ok(tileTag.includes("height:28px"));
  assert.ok(tileTag.includes("background-color:#F8F4EE"));
  assert.ok(tileTag.includes("border-radius:6px"));
  const wordmark = indexOfOrFail(html, "data-email-wordmark");
  const wordmarkTag = html.slice(html.lastIndexOf("<", wordmark), html.indexOf(">", wordmark));
  assert.ok(wordmarkTag.includes("font-family:Georgia, serif"));
  assert.ok(wordmarkTag.includes("font-size:18px"));
  assert.ok(html.slice(wordmark).includes("Steward"));
  const org = indexOfOrFail(html, "data-email-org");
  assert.ok(org > wordmark, "organisation name comes after the wordmark");
  const orgTag = html.slice(html.lastIndexOf("<", org), html.indexOf(">", org));
  assert.ok(orgTag.includes('align="right"') || orgTag.includes("text-align:right"));
  const orgText = html.slice(org, html.indexOf("</td>", org));
  assert.ok(orgText.includes("font-size:13px"));
  assert.ok(orgText.includes("Example Organisation"));
});

test("the organisation slot is absent when no organisation name is given", async () => {
  assert.ok(!(await renderKind("otp")).includes("data-email-org"));
});

test("the mark image sits in the tile only when markSrc is given", async () => {
  assert.ok(!(await renderKind("otp")).includes("<img"));
  const markSrc = "https://policies.example.org/static/steward-email-mark-2x.png";
  const html = await renderKind("otp", { ...EMAIL_SAMPLE_VARS.otp, markSrc });
  const img = indexOfOrFail(html, `src="${markSrc}"`);
  assert.ok(img > indexOfOrFail(html, "data-email-tile"));
  assert.ok(img < indexOfOrFail(html, "data-email-wordmark"));
  const tag = html.slice(html.lastIndexOf("<img", img), html.indexOf(">", img));
  assert.ok(tag.includes('width="20"'));
  assert.ok(tag.includes('height="20"'));
});

test("the footer is a paper block with the postal address first, then the opt-out line", async () => {
  const html = await renderKind("digest", {
    ...EMAIL_SAMPLE_VARS.digest,
    legalText: "Example Organisation, 1 Example Street, Example Town",
    unsubscribeHref: "https://policies.example.org/unsubscribe/abc",
  });
  const foot = indexOfOrFail(html, "data-email-footer");
  const footTag = html.slice(html.lastIndexOf("<", foot), html.indexOf(">", foot));
  assert.ok(footTag.includes(`background-color:${palette.footer}`));
  const address = indexOfOrFail(html, "Example Organisation, 1 Example Street, Example Town");
  const prefs = indexOfOrFail(html, "Manage email preferences");
  const unsub = indexOfOrFail(html, "Unsubscribe from these emails");
  assert.ok(foot < address && address < prefs && prefs < unsub);
  assert.ok(!html.includes("<hr"), "no rule above the footer; the block is its own band");
});

test("mandatory security mail says why it has no opt-out", async () => {
  for (const kind of ["otp", "kratos-recovery", "email-verification", "welcome-account", "mfa-setup"] as const) {
    const html = await renderKind(kind);
    assert.ok(html.includes("This is a security email, so you can&#x27;t unsubscribe from it."), kind);
  }
  assert.ok(!(await renderKind("digest")).includes("security email"));
});

test("required compliance mail says it cannot be turned off and still links preferences", async () => {
  for (const kind of ["ack-required", "policy-ack-reminder", "policy-escalation", "policy-published"] as const) {
    const html = await renderKind(kind);
    assert.ok(html.includes("Required for auditability"), kind);
    assert.ok(html.includes("Manage email preferences"), kind);
  }
});

test("dark client rules carry the stored dark tokens and leave the plum button alone", async () => {
  const html = await renderKind("ack-required");
  const media = indexOfOrFail(html, "prefers-color-scheme: dark");
  const css = html.slice(media, html.indexOf("</style>", media));
  for (const token of [
    darkPalette.canvas,
    darkPalette.surface,
    darkPalette.text,
    darkPalette.muted,
    darkPalette.border,
    darkPalette.footer,
  ]) {
    assert.ok(css.includes(token), token);
  }
  assert.ok(!css.includes(palette.accent), "the button stays solid plum with white text");
  assert.ok(html.includes(`background-color:${palette.accent}`));
  assert.ok(html.includes(`color:${palette.accentText}`));
  assert.ok(html.includes('content="light dark"'));
});

test("the break-glass alert button is the danger red", async () => {
  const html = await renderKind("break-glass-alert", {
    ...EMAIL_SAMPLE_VARS["break-glass-alert"],
    reviewUrl: "https://policies.example.org/admin/security",
  });
  assert.ok(html.includes(`background-color:${palette.error}`));
});

test("review-due (E6) names the policy, its category and the due date, with the preferences link", async () => {
  const html = await renderKind("review-due");
  for (const want of [
    "A review is due",
    "Desk Booking Policy",
    "Workplace › Facilities",
    "2 Oct 2027",
    "Open my reviews",
    "Manage email preferences",
  ]) {
    assert.ok(html.includes(want), want);
  }
});

test("the stored header assets ship with the sidecar", () => {
  const dir = fileURLToPath(new URL("../assets/email/", import.meta.url));
  const png = Buffer.from([0x89, 0x50, 0x4e, 0x47]);
  for (const name of ["steward-email-header-2x.png", "steward-email-mark-2x.png"]) {
    assert.ok(existsSync(dir + name), name);
    assert.deepEqual(readFileSync(dir + name).subarray(0, 4), png, name);
  }
  assert.ok(readFileSync(dir + "steward-email-header.svg", "utf8").startsWith("<svg"));
});
