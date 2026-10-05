// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Text } from "@react-email/components";
import type { ReactNode } from "react";

import { INSET, palette } from "./palette.js";

export interface EmailTextProperties {
  children: ReactNode;
  color?: string;
  inset?: boolean;
  muted?: boolean;
}

export const EmailText = ({ children, color, inset = true, muted = false }: EmailTextProperties) => (
  <Text
    style={{
      color: color ?? (muted ? palette.muted : palette.text),
      fontSize: muted ? "13px" : "15px",
      lineHeight: "1.6",
      margin: "0 0 16px",
      padding: inset ? `0 ${INSET}` : 0,
    }}
  >
    {children}
  </Text>
);
