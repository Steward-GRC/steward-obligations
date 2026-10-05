// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { pathToFileURL } from "node:url";

import { render } from "@react-email/components";
import { createElement } from "react";

import { defaultSubject, EMAIL_COMPONENTS, isEmailKind } from "./templates/registry.js";

export interface BuildInfo {
  commit: string;
  version: string;
}

const nonEmpty = (value: string | undefined, fallback: string): string =>
  value !== undefined && value.length > 0 ? value : fallback;

export const buildInfoFromEnv = (env: NodeJS.ProcessEnv): BuildInfo => ({
  commit: nonEmpty(env.COMMIT, "unknown"),
  version: nonEmpty(env.VERSION, "dev"),
});

const ROUTES: Record<string, string> = {
  "/livez": "GET",
  "/readyz": "GET",
  "/render": "POST",
};

const readBody = async (req: IncomingMessage): Promise<string> => {
  const chunks: Buffer[] = [];
  for await (const chunk of req) {
    chunks.push(chunk as Buffer);
  }
  return Buffer.concat(chunks).toString("utf8");
};

const sendJson = (
  res: ServerResponse,
  status: number,
  body: unknown,
  headers: Record<string, string> = {},
): void => {
  res.writeHead(status, { ...headers, "Content-Type": "application/json; charset=utf-8" });
  res.end(JSON.stringify(body));
};

const handleRender = async (req: IncomingMessage, res: ServerResponse): Promise<void> => {
  let raw: string;
  try {
    raw = await readBody(req);
  } catch {
    sendJson(res, 400, { error: "failed to read request body" });
    return;
  }

  let parsed: unknown;
  try {
    parsed = raw.length > 0 ? JSON.parse(raw) : {};
  } catch {
    sendJson(res, 400, { error: "malformed JSON body" });
    return;
  }

  if (typeof parsed !== "object" || parsed === null) {
    sendJson(res, 400, { error: "request body must be a JSON object" });
    return;
  }

  const { kind, vars } = parsed as { kind?: unknown; vars?: unknown };

  if (!isEmailKind(kind)) {
    sendJson(res, 400, { error: `unknown email kind: ${String(kind)}` });
    return;
  }

  const templateVars =
    typeof vars === "object" && vars !== null ? (vars as Record<string, unknown>) : {};

  const subject =
    typeof templateVars.subject === "string" && templateVars.subject.length > 0
      ? templateVars.subject
      : defaultSubject(kind, typeof templateVars.productName === "string" ? templateVars.productName : undefined);

  let html: string;
  try {
    html = await render(createElement(EMAIL_COMPONENTS[kind], templateVars));
  } catch (err) {
    sendJson(res, 400, { error: err instanceof Error ? err.message : "render failed" });
    return;
  }

  sendJson(res, 200, { html, subject });
};

export const createRenderServer = (build: BuildInfo = buildInfoFromEnv(process.env)): Server =>
  createServer((req: IncomingMessage, res: ServerResponse) => {
    res.setHeader("Steward-Version", build.version);
    res.setHeader("Steward-Commit", build.commit);

    const method = req.method ?? "GET";
    const path = (req.url ?? "/").split("?")[0] ?? "/";
    const allowed = ROUTES[path];

    if (allowed === undefined) {
      sendJson(res, 404, { error: "not found" });
      return;
    }
    if (method !== allowed) {
      sendJson(res, 405, { error: "method not allowed" }, { Allow: allowed });
      return;
    }
    if (path === "/render") {
      void handleRender(req, res);
      return;
    }
    sendJson(res, 200, { status: "ok" });
  });

const isMain = (): boolean => {
  const entry = process.argv[1];
  return typeof entry === "string" && import.meta.url === pathToFileURL(entry).href;
};

if (isMain()) {
  const port = Number.parseInt(process.env.PORT ?? "8091", 10);
  const host = process.env.HOST ?? "127.0.0.1";
  const build = buildInfoFromEnv(process.env);
  const server = createRenderServer(build);
  server.listen(port, host, () => {
    console.log(
      JSON.stringify({ commit: build.commit, host, msg: "render sidecar listening", port, version: build.version }),
    );
  });
  const shutdown = (signal: string) => {
    console.log(JSON.stringify({ msg: "render sidecar shutting down", signal }));
    server.close(() => process.exit(0));
  };
  process.on("SIGTERM", () => shutdown("SIGTERM"));
  process.on("SIGINT", () => shutdown("SIGINT"));
}
