// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Heading, Text } from "@react-email/components";

import { EmailSection } from "./EmailSection.js";
import { palette } from "./palette.js";

export interface EmailHeroProperties {
  eyebrow?: string;
  subtitle?: string;
  title: string;
}

export const EmailHero = ({ eyebrow, subtitle, title }: EmailHeroProperties) => (
  <EmailSection padding="8px 32px 16px">
    {eyebrow !== undefined && (
      <Text
        style={{
          color: palette.muted,
          fontSize: "12px",
          fontWeight: 600,
          letterSpacing: "0.08em",
          margin: "0 0 6px",
          textTransform: "uppercase",
        }}
      >
        {eyebrow}
      </Text>
    )}
    <Heading as="h1" style={{ color: palette.text, fontSize: "24px", fontWeight: 600, lineHeight: "1.3", margin: 0 }}>
      {title}
    </Heading>
    {subtitle !== undefined && (
      <Text style={{ color: palette.muted, fontSize: "14px", margin: "6px 0 0" }}>{subtitle}</Text>
    )}
  </EmailSection>
);
