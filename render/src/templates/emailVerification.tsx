// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { EmailButton, EmailHero, EmailSection, EmailText } from "../components/index.js";

import { type ChromeProperties, DEFAULT_PRODUCT_NAME, NotificationLayout } from "./layout.js";

export interface EmailVerificationProperties extends ChromeProperties {
  recipientName?: string;
  ttlHours: number;
  verifyLabel?: string;
  verifyUrl: string;
}

const EmailVerification = ({
  productName = DEFAULT_PRODUCT_NAME,
  recipientName,
  ttlHours,
  verifyLabel = "Verify email address",
  verifyUrl,
  ...chrome
}: EmailVerificationProperties) => (
  <NotificationLayout
    {...chrome}
    previewText="Confirm your email address to finish setting up your account"
    productName={productName}
  >
    <EmailHero eyebrow="Verify your email" title="Confirm your email address" />
    <EmailText>{recipientName !== undefined ? `Hi ${recipientName},` : "Hi there,"}</EmailText>
    <EmailText>
      Confirm that this address belongs to you by clicking the button below. This link expires in{" "}
      {ttlHours} hours.
    </EmailText>
    <EmailSection align="center" padding="8px 32px 28px">
      <EmailButton href={verifyUrl}>{verifyLabel}</EmailButton>
    </EmailSection>
    <EmailText muted>
      If you didn&apos;t create a {productName} account, you can safely ignore this email.
    </EmailText>
  </NotificationLayout>
);

export const sampleVars: EmailVerificationProperties = {
  recipientName: "Erin",
  ttlHours: 48,
  verifyUrl: "https://policies.example.org/notify/verify-email?token=sample-token",
};

export { EmailVerification };
