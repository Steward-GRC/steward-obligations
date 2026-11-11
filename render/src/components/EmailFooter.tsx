// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Link, Section, Text } from "@react-email/components";

import { DARK_CLASS, palette } from "./palette.js";

export interface EmailFooterProperties {
  legalText?: string;
  preferencesUrl?: string;
  // Compliance mail the recipient cannot turn off; says so before the link.
  required?: boolean;
  unsubscribeHref?: string;
}

const footerText = { color: palette.muted, fontSize: "12px", lineHeight: "18px", margin: "0 0 6px" };
const footerLink = { color: palette.muted, textDecoration: "underline" };

const present = (value: string | undefined): value is string => value !== undefined && value.length > 0;

const SECURITY_NOTICE = "This is a security email, so you can't unsubscribe from it.";
const REQUIRED_NOTICE = "Required for auditability — you can't turn these off.";

// A paper block: the organisation's postal address first, then the opt-out
// line. Mail with no preferences link and no unsubscribe target is security or
// transactional mail and says why it has neither.
export const EmailFooter = ({ legalText, preferencesUrl, required = false, unsubscribeHref }: EmailFooterProperties) => {
  const hasPreferences = present(preferencesUrl);
  const hasUnsubscribe = present(unsubscribeHref);
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
      {!hasPreferences && !hasUnsubscribe && (
        <Text className={DARK_CLASS.muted} style={footerText}>
          {SECURITY_NOTICE}
        </Text>
      )}
      {(hasPreferences || hasUnsubscribe) && (
        <Text className={DARK_CLASS.muted} style={footerText}>
          {required && `${REQUIRED_NOTICE} `}
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
