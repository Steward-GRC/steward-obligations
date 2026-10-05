// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Section } from "@react-email/components";
import type { ReactNode } from "react";

import { INSET } from "./palette.js";

export interface EmailSectionProperties {
  align?: "left" | "center" | "right";
  background?: string;
  children: ReactNode;
  padding?: string;
}

export const EmailSection = ({
  align = "left",
  background,
  children,
  padding = `0 ${INSET}`,
}: EmailSectionProperties) => (
  <Section align={align} style={{ backgroundColor: background, padding, textAlign: align }}>
    {children}
  </Section>
);
