// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Heading, Text } from "@react-email/components";

import { EmailSection } from "./EmailSection.js";
import { DARK_CLASS, fontStack, palette } from "./palette.js";

export interface EmailHeroProperties {
  eyebrow?: string;
  subtitle?: string;
  title: string;
}

export const EmailHero = ({ eyebrow, subtitle, title }: EmailHeroProperties) => (
  <EmailSection padding="24px 24px 14px">
    {eyebrow !== undefined && (
      <Text
        className={DARK_CLASS.muted}
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
    <Heading
      as="h1"
      className={DARK_CLASS.text}
      style={{ color: palette.text, fontFamily: fontStack, fontSize: "22px", fontWeight: 700, lineHeight: "28px", margin: 0 }}
    >
      {title}
    </Heading>
    {subtitle !== undefined && (
      <Text className={DARK_CLASS.muted} style={{ color: palette.muted, fontSize: "14px", margin: "6px 0 0" }}>{subtitle}</Text>
    )}
  </EmailSection>
);
