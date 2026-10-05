// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Heading } from "@react-email/components";
import type { ReactNode } from "react";

import { palette } from "./palette.js";

const SIZES = {
  lg: { as: "h1", fontSize: "28px", letterSpacing: "2px" },
  md: { as: "h2", fontSize: "20px", letterSpacing: "0" },
  sm: { as: "h3", fontSize: "16px", letterSpacing: "0" },
} as const;

export interface EmailHeadingProperties {
  children: ReactNode;
  size?: keyof typeof SIZES;
}

export const EmailHeading = ({ children, size = "md" }: EmailHeadingProperties) => {
  const { as, fontSize, letterSpacing } = SIZES[size];
  return (
    <Heading
      as={as}
      style={{ color: palette.text, fontSize, fontWeight: 600, letterSpacing, lineHeight: "1.3", margin: "0 0 8px", wordBreak: "break-word" }}
    >
      {children}
    </Heading>
  );
};
