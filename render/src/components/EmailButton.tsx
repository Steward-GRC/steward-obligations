// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Button } from "@react-email/components";
import type { ReactNode } from "react";

import { fontStack, palette, RADIUS } from "./palette.js";

export interface EmailButtonProperties {
  children: ReactNode;
  href: string;
  variant?: "danger" | "primary" | "secondary";
}

export const EmailButton = ({ children, href, variant = "primary" }: EmailButtonProperties) => {
  const primary = variant !== "secondary";
  const fill = variant === "danger" ? palette.error : palette.accent;
  return (
    <Button
      href={href}
      style={{
        backgroundColor: primary ? fill : palette.surface,
        border: `1px solid ${fill}`,
        borderRadius: RADIUS,
        color: primary ? palette.accentText : palette.accent,
        display: "inline-block",
        fontFamily: fontStack,
        fontSize: "15px",
        fontWeight: 700,
        padding: "12px 22px",
        textDecoration: "none",
      }}
    >
      {children}
    </Button>
  );
};
