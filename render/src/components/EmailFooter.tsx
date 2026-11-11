// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Link, Section, Text } from "@react-email/components";

import { DARK_CLASS, palette } from "./palette.js";

export interface EmailFooterProperties {
  legalText?: string;
  preferencesUrl?: string;
  // What kind of mail this is. "security" mail has no opt-out at all and says
  // why; "required" compliance mail keeps the preferences link but says it
  // cannot be turned off. Unset is optional mail: links only, no notice.
  notice?: "required" | "security";
  unsubscribeHref?: string;
}

const footerText = { color: palette.muted, fontSize: "12px", lineHeight: "18px", margin: "0 0 6px" };
const footerLink = { color: palette.muted, textDecoration: "underline" };

const present = (value: string | undefined): value is string => value !== undefined && value.length > 0;

const SECURITY_NOTICE = "This is a security email, so you can't unsubscribe from it.";
const REQUIRED_NOTICE = "Required for auditability — you can't turn these off.";

// A paper block: the organisation's postal address first, then the opt-out
// line.
export const EmailFooter = ({ legalText, notice, preferencesUrl, unsubscribeHref }: EmailFooterProperties) => {
  const security = notice === "security";
  const hasPreferences = !security && present(preferencesUrl);
  const hasUnsubscribe = !security && present(unsubscribeHref);
  return (
    <Section
      className={DARK_CLASS.footer}
      data-email-footer=""
      style={{ backgroundColor: palette.footer, padding: "16px 24px 10px" }}
    >
      {present(legalText) && (
        <Text className={DARK_CLASS.muted} data-email-legal="" style={footerText}>
          {legalText}
        </Text>
      )}
      {security && (
        <Text className={DARK_CLASS.muted} style={footerText}>
          {SECURITY_NOTICE}
        </Text>
      )}
      {(hasPreferences || hasUnsubscribe || notice === "required") && (
        <Text className={DARK_CLASS.muted} style={footerText}>
          {notice === "required" && `${REQUIRED_NOTICE} `}
          {hasPreferences && (
            <Link className={DARK_CLASS.muted} href={preferencesUrl} style={footerLink}>
              Manage email preferences
            </Link>
          )}
          {hasPreferences && hasUnsubscribe && " · "}
          {hasUnsubscribe && (
            <Link className={DARK_CLASS.muted} href={unsubscribeHref} style={footerLink}>
              Unsubscribe from these emails
            </Link>
          )}
        </Text>
      )}
    </Section>
  );
};
