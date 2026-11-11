// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Section } from "@react-email/components";
import type { ReactNode } from "react";

import { DARK_CLASS, INSET, palette } from "./palette.js";

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
  <Section
    align={align}
    className={background === palette.tint ? DARK_CLASS.tint : undefined}
    style={{ backgroundColor: background, padding, textAlign: align }}
  >
    {children}
  </Section>
);
