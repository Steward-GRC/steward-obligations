// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from "react";

import { EmailFooter, EmailHeader, EmailLayout } from "../components/index.js";

export const DEFAULT_PRODUCT_NAME = "Steward";

export interface ChromeProperties {
  legalText?: string;
  logoSrc?: string;
  markSrc?: string;
  organisationName?: string;
  productName?: string;
}

export interface NotificationLayoutProperties extends ChromeProperties {
  children: ReactNode;
  preferencesUrl?: string;
  previewText?: string;
  // The footer's notice for mail with no opt-out; see EmailFooter.
  notice?: "required" | "security";
  // RFC 8058 one-click target; only optional, informational mail passes it.
  unsubscribeHref?: string;
}

export const NotificationLayout = ({
  children,
  legalText,
  logoSrc,
  markSrc,
  organisationName,
  preferencesUrl,
  previewText,
  notice,
  productName = DEFAULT_PRODUCT_NAME,
  unsubscribeHref,
}: NotificationLayoutProperties) => (
  <EmailLayout previewText={previewText}>
    <EmailHeader logoSrc={logoSrc} markSrc={markSrc} organisationName={organisationName} productName={productName} />
    {children}
    <EmailFooter
      legalText={legalText}
      preferencesUrl={preferencesUrl}
      notice={notice}
      unsubscribeHref={unsubscribeHref}
    />
  </EmailLayout>
);
