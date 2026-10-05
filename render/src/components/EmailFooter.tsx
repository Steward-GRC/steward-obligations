// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Hr, Link, Text } from "@react-email/components";

import { EmailSection } from "./EmailSection.js";
import { palette } from "./palette.js";

export interface EmailFooterProperties {
  legalText?: string;
  preferencesUrl?: string;
  unsubscribeHref?: string;
}

const footerText = { color: palette.muted, fontSize: "12px", lineHeight: "1.5", margin: "0 0 6px" };
const footerLink = { color: palette.muted, textDecoration: "underline" };

export const EmailFooter = ({ legalText, preferencesUrl, unsubscribeHref }: EmailFooterProperties) => (
  <EmailSection align="center" padding="8px 32px 20px">
    <Hr style={{ borderColor: palette.border, margin: "8px 0 16px" }} />
    {preferencesUrl !== undefined && preferencesUrl.length > 0 && (
      <Text style={footerText}>
        <Link href={preferencesUrl} style={footerLink}>
          Manage email preferences
        </Link>
      </Text>
    )}
    {unsubscribeHref !== undefined && unsubscribeHref.length > 0 && (
      <Text style={footerText}>
        Don&apos;t want these emails?{" "}
        <Link href={unsubscribeHref} style={footerLink}>
          Unsubscribe
        </Link>
      </Text>
    )}
    {legalText !== undefined && legalText.length > 0 && (
      <Text data-email-legal="" style={footerText}>
        {legalText}
      </Text>
    )}
  </EmailSection>
);
