// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { EmailButton, EmailCallout, EmailHero, EmailSection, EmailText } from "../components/index.js";

import { type ChromeProperties, DEFAULT_PRODUCT_NAME, NotificationLayout } from "./layout.js";

// New users get a count of pending acknowledgements, never the list itself.
export interface WelcomeAccountProperties extends ChromeProperties {
  accountLabel?: string;
  accountUrl: string;
  introCopy?: string;
  pendingAckCount?: number;
  recipientName: string;
}

const WelcomeAccount = ({
  accountLabel = "Set up your account",
  accountUrl,
  introCopy,
  pendingAckCount,
  productName = DEFAULT_PRODUCT_NAME,
  recipientName,
  ...chrome
}: WelcomeAccountProperties) => (
  <NotificationLayout
    {...chrome}
    previewText={`Welcome, your account is ready, ${recipientName}.`}
    productName={productName}
  >
    <EmailHero eyebrow="Welcome" title="Welcome, your account is ready" />
    <EmailText>Hi {recipientName},</EmailText>
    <EmailText>
      {introCopy ?? `Your ${productName} account has been created. Set a password and sign in to get started.`}
    </EmailText>
    <EmailSection align="center" padding="8px 32px 28px">
      <EmailButton href={accountUrl}>{accountLabel}</EmailButton>
    </EmailSection>
    <EmailCallout level="info" title="What your account gives you">
      Your assigned policies and any pending acknowledgements are waiting in your portal. Sign in to
      review.
      {pendingAckCount !== undefined && pendingAckCount > 0 && (
        <> You have {pendingAckCount} items to review in your portal.</>
      )}
    </EmailCallout>
  </NotificationLayout>
);

export const sampleVars: WelcomeAccountProperties = {
  accountLabel: "Set up your account",
  accountUrl: "https://policies.example.org/setup/alice",
  pendingAckCount: 12,
  recipientName: "Alice",
};

export { WelcomeAccount };
