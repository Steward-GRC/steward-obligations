// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Section } from "@react-email/components";
import type { ReactNode } from "react";

import { EmailSection } from "./EmailSection.js";
import { palette } from "./palette.js";

export interface EmailCardProperties {
  children: ReactNode;
}

export const EmailCard = ({ children }: EmailCardProperties) => (
  <EmailSection padding="0 32px 16px">
    <Section style={{ border: `1px solid ${palette.border}`, borderRadius: "6px", padding: "4px 16px" }}>
      {children}
    </Section>
  </EmailSection>
);
