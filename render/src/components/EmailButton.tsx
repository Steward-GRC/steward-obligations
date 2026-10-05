// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Button } from "@react-email/components";
import type { ReactNode } from "react";

import { palette } from "./palette.js";

export interface EmailButtonProperties {
  children: ReactNode;
  href: string;
  variant?: "primary" | "secondary";
}

export const EmailButton = ({ children, href, variant = "primary" }: EmailButtonProperties) => {
  const primary = variant === "primary";
  return (
    <Button
      href={href}
      style={{
        backgroundColor: primary ? palette.accent : palette.surface,
        border: `1px solid ${palette.accent}`,
        borderRadius: "6px",
        color: primary ? palette.accentText : palette.accent,
        display: "inline-block",
        fontSize: "15px",
        fontWeight: 600,
        padding: "12px 24px",
        textDecoration: "none",
      }}
    >
      {children}
    </Button>
  );
};
