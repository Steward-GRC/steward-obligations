// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Img, Text } from "@react-email/components";

import { EmailSection } from "./EmailSection.js";
import { palette } from "./palette.js";

export interface EmailHeaderProperties {
  logoSrc?: string;
  productName?: string;
}

export const EmailHeader = ({ logoSrc, productName }: EmailHeaderProperties) => {
  const hasLogo = logoSrc !== undefined && logoSrc.length > 0;
  const hasName = productName !== undefined && productName.length > 0;
  if (!hasLogo && !hasName) return null;
  return (
    <EmailSection padding="24px 32px 8px">
      {hasLogo && <Img alt={hasName ? productName : ""} height="32" src={logoSrc} style={{ display: "block" }} />}
      {hasName && (
        <Text style={{ color: palette.text, fontSize: "16px", fontWeight: 700, margin: hasLogo ? "8px 0 0" : 0 }}>
          {productName}
        </Text>
      )}
    </EmailSection>
  );
};
