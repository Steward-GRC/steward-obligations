// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Body, Container, Head, Html, Preview } from "@react-email/components";
import type { ReactNode } from "react";

import { fontStack, palette } from "./palette.js";

export interface EmailLayoutProperties {
  children: ReactNode;
  previewText?: string;
}

export const EmailLayout = ({ children, previewText }: EmailLayoutProperties) => (
  <Html lang="en">
    <Head>
      <meta content="light dark" name="color-scheme" />
      <meta content="light dark" name="supported-color-schemes" />
    </Head>
    {previewText !== undefined && previewText.length > 0 && <Preview>{previewText}</Preview>}
    <Body style={{ backgroundColor: palette.canvas, fontFamily: fontStack, margin: 0, padding: "24px 0" }}>
      <Container
        style={{
          backgroundColor: palette.surface,
          border: `1px solid ${palette.border}`,
          borderRadius: "8px",
          color: palette.text,
          maxWidth: "600px",
          width: "100%",
        }}
      >
        {children}
      </Container>
    </Body>
  </Html>
);
