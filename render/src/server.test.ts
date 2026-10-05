// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { after, before, test } from "node:test";

import { buildInfoFromEnv, createRenderServer } from "./server.js";

let baseUrl: string;
const server = createRenderServer({ commit: "0123abc", version: "v0.1.0" });

before(async () => {
  await new Promise<void>((resolve) => {
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();
  if (address === null || typeof address === "string") {
    throw new Error("expected server to bind a TCP port");
  }
  baseUrl = `http://127.0.0.1:${address.port}`;
});

after(async () => {
  await new Promise<void>((resolve, reject) => {
    server.close((err) => (err ? reject(err) : resolve()));
  });
});

for (const path of ["/livez", "/readyz"]) {
  test(`GET ${path} returns 200 with a status body and the build headers`, async () => {
    const res = await fetch(`${baseUrl}${path}`);
    assert.equal(res.status, 200);
    assert.deepEqual(await res.json(), { status: "ok" });
    assert.equal(res.headers.get("steward-version"), "v0.1.0");
    assert.equal(res.headers.get("steward-commit"), "0123abc");
  });

  test(`POST ${path} returns 405 and allows GET`, async () => {
    const res = await fetch(`${baseUrl}${path}`, { method: "POST" });
    assert.equal(res.status, 405);
    assert.equal(res.headers.get("allow"), "GET");
  });
}

for (const path of ["/healthz", "/health"]) {
  test(`GET ${path} is not a route`, async () => {
    const res = await fetch(`${baseUrl}${path}`);
    assert.equal(res.status, 404);
  });
}

test("GET /render returns 405 and allows POST", async () => {
  const res = await fetch(`${baseUrl}/render`);
  assert.equal(res.status, 405);
  assert.equal(res.headers.get("allow"), "POST");
});

test("an unknown route returns 404", async () => {
  const res = await fetch(`${baseUrl}/nope`);
  assert.equal(res.status, 404);
});

test("buildInfoFromEnv falls back to dev and unknown", () => {
  assert.deepEqual(buildInfoFromEnv({}), { commit: "unknown", version: "dev" });
  assert.deepEqual(buildInfoFromEnv({ COMMIT: "", VERSION: "" }), {
    commit: "unknown",
    version: "dev",
  });
});

test("buildInfoFromEnv reads VERSION and COMMIT", () => {
  assert.deepEqual(buildInfoFromEnv({ COMMIT: "fedcba9", VERSION: "v1.2.3" }), {
    commit: "fedcba9",
    version: "v1.2.3",
  });
});

test("POST /render with a valid policy kind returns subject and email-safe html", async () => {
  const res = await fetch(`${baseUrl}/render`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      kind: "policy-ack-reminder",
      vars: {
        policies: [
          {
            ackUrl: "https://policies.example.org/ack/pol-facilities-000001",
            dueBy: "August 15, 2026",
            ref: "POL-FACILITIES-000001",
            title: "Desk Booking Policy",
          },
        ],
        portalUrl: "https://policies.example.org/portal/acknowledgements",
        recipientName: "Alice",
      },
    }),
  });
  assert.equal(res.status, 200);
  assert.equal(res.headers.get("steward-version"), "v0.1.0");
  const body = (await res.json()) as { subject: string; html: string };
  assert.ok(body.subject.length > 0);
  assert.ok(body.html.includes("<!DOCTYPE"));
});

test("POST /render with vars.subject overrides the default subject", async () => {
  const res = await fetch(`${baseUrl}/render`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      kind: "ack-required",
      vars: {
        ackUrl: "https://policies.example.org/ack/item-1",
        bodyText: "Please review.",
        itemTitle: "Travel Booking Procedure",
        recipientName: "Alice",
        subject: "Custom subject line",
      },
    }),
  });
  assert.equal(res.status, 200);
  const body = (await res.json()) as { subject: string; html: string };
  assert.equal(body.subject, "Custom subject line");
});

test("POST /render uses vars.productName in a product-branded default subject", async () => {
  const res = await fetch(`${baseUrl}/render`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      kind: "kratos-recovery",
      vars: { expiresInMinutes: 60, productName: "Example Portal", recoveryCode: "483920" },
    }),
  });
  assert.equal(res.status, 200);
  const body = (await res.json()) as { subject: string; html: string };
  assert.equal(body.subject, "Reset your Example Portal password");
  assert.ok(body.html.includes("Example Portal"));
});

test("POST /render with an unknown kind returns 400", async () => {
  const res = await fetch(`${baseUrl}/render`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ kind: "not-a-real-kind", vars: {} }),
  });
  assert.equal(res.status, 400);
});

test("POST /render with malformed JSON returns 400", async () => {
  const res = await fetch(`${baseUrl}/render`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: "{not json",
  });
  assert.equal(res.status, 400);
});
