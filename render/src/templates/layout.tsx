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
  // Compliance mail the recipient cannot turn off; the footer says so.
  required?: boolean;
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
  productName = DEFAULT_PRODUCT_NAME,
  required,
  unsubscribeHref,
}: NotificationLayoutProperties) => (
  <EmailLayout previewText={previewText}>
    <EmailHeader logoSrc={logoSrc} markSrc={markSrc} organisationName={organisationName} productName={productName} />
    {children}
    <EmailFooter
      legalText={legalText}
      preferencesUrl={preferencesUrl}
      required={required}
      unsubscribeHref={unsubscribeHref}
    />
  </EmailLayout>
);
