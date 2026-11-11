// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Section, Text } from "@react-email/components";
import type { ReactNode } from "react";

import { EmailSection } from "./EmailSection.js";
import { DARK_CLASS, palette, RADIUS } from "./palette.js";

const LEVEL_COLOURS = {
  error: palette.error,
  info: palette.accent,
  warn: palette.warn,
} as const;

export interface EmailCalloutProperties {
  children: ReactNode;
  level?: keyof typeof LEVEL_COLOURS;
  title?: string;
}

export const EmailCallout = ({ children, level = "info", title }: EmailCalloutProperties) => (
  <EmailSection padding="0 24px 16px">
    <Section
      className={DARK_CLASS.tint}
      style={{
        backgroundColor: palette.tint,
        borderLeft: `4px solid ${LEVEL_COLOURS[level]}`,
        borderRadius: RADIUS,
        padding: "12px 16px",
      }}
    >
      {title !== undefined && (
        <Text
          className={level === "info" ? DARK_CLASS.text : undefined}
          style={{ color: LEVEL_COLOURS[level], fontSize: "14px", fontWeight: 600, margin: "0 0 4px" }}
        >
          {title}
        </Text>
      )}
      <Text className={DARK_CLASS.text} style={{ color: palette.text, fontSize: "14px", lineHeight: "20px", margin: 0 }}>
        {children}
      </Text>
    </Section>
  </EmailSection>
);
