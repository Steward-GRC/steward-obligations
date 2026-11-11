// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Section } from "@react-email/components";
import type { ReactNode } from "react";

import { EmailSection } from "./EmailSection.js";
import { DARK_CLASS, palette, RADIUS } from "./palette.js";

export interface EmailCardProperties {
  children: ReactNode;
}

export const EmailCard = ({ children }: EmailCardProperties) => (
  <EmailSection padding="0 24px 16px">
    <Section
      className={DARK_CLASS.border}
      style={{ border: `1px solid ${palette.border}`, borderRadius: RADIUS, overflow: "hidden", padding: "0 14px" }}
    >
      {children}
    </Section>
  </EmailSection>
);
