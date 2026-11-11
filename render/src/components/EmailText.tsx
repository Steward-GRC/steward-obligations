// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Text } from "@react-email/components";
import type { ReactNode } from "react";

import { DARK_CLASS, INSET, palette } from "./palette.js";

export interface EmailTextProperties {
  children: ReactNode;
  color?: string;
  inset?: boolean;
  muted?: boolean;
}

export const EmailText = ({ children, color, inset = true, muted = false }: EmailTextProperties) => (
  <Text
    className={color === undefined ? (muted ? DARK_CLASS.muted : DARK_CLASS.text) : undefined}
    style={{
      color: color ?? (muted ? palette.muted : palette.text),
      fontSize: muted ? "14px" : "16px",
      lineHeight: muted ? "20px" : "24px",
      margin: "0 0 16px",
      padding: inset ? `0 ${INSET}` : 0,
    }}
  >
    {children}
  </Text>
);
