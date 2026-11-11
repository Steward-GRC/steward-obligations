// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Body, Container, Head, Html, Preview, Section } from "@react-email/components";
import type { ReactNode } from "react";

import { DARK_CLASS, darkModeCss, fontStack, MAX_WIDTH, palette, RADIUS } from "./palette.js";

export interface EmailLayoutProperties {
  children: ReactNode;
  previewText?: string;
}

export const EmailLayout = ({ children, previewText }: EmailLayoutProperties) => (
  <Html lang="en">
    <Head>
      <meta content="width=device-width, initial-scale=1" name="viewport" />
      <meta content="light dark" name="color-scheme" />
      <meta content="light dark" name="supported-color-schemes" />
      <style>{darkModeCss}</style>
    </Head>
    {previewText !== undefined && previewText.length > 0 && <Preview>{previewText}</Preview>}
    <Body
      className={DARK_CLASS.canvas}
      style={{ backgroundColor: palette.canvas, fontFamily: fontStack, margin: 0, padding: 0 }}
    >
      <Section className={DARK_CLASS.canvas} style={{ backgroundColor: palette.canvas, padding: "20px 0" }}>
        <Container
          className={`${DARK_CLASS.surface} ${DARK_CLASS.border} ${DARK_CLASS.text}`}
          style={{
            backgroundColor: palette.surface,
            border: `1px solid ${palette.border}`,
            borderRadius: RADIUS,
            color: palette.text,
            fontSize: "16px",
            lineHeight: "24px",
            maxWidth: MAX_WIDTH,
            overflow: "hidden",
            width: "100%",
          }}
        >
          {children}
        </Container>
      </Section>
    </Body>
  </Html>
);
