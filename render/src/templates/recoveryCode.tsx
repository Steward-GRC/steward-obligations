// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { EmailHeading, EmailHero, EmailSection, EmailText, palette } from "../components/index.js";

import { type ChromeProperties, DEFAULT_PRODUCT_NAME, NotificationLayout } from "./layout.js";

const DIGIT_GAP = " ";

export interface RecoveryCodeProperties extends ChromeProperties {
  email?: string;
  expiresInMinutes: number;
  recipientName?: string;
  recoveryCode: string;
}

const RecoveryCode = ({
  email: _email,
  expiresInMinutes,
  productName = DEFAULT_PRODUCT_NAME,
  recipientName,
  recoveryCode,
  ...chrome
}: RecoveryCodeProperties) => (
  <NotificationLayout {...chrome} previewText={`Reset your ${productName} password`} productName={productName}>
    <EmailHero eyebrow="Password reset" title={`Reset your ${productName} password`} />
    <EmailText>
      {recipientName !== undefined ? `Hi ${recipientName}, use` : "Use"} the code below to reset your{" "}
      {productName} password.
    </EmailText>
    <EmailSection align="center" background={palette.tint} padding="28px 32px">
      <EmailHeading size="lg">{recoveryCode.split("").join(DIGIT_GAP)}</EmailHeading>
    </EmailSection>
    <EmailText muted>
      This code expires in {expiresInMinutes} minutes. If you didn&apos;t request a password reset,
      you can safely ignore this email. Your password will not change.
    </EmailText>
  </NotificationLayout>
);

export const sampleVars: RecoveryCodeProperties = {
  email: "heidi@example.org",
  expiresInMinutes: 60,
  recipientName: "Heidi",
  recoveryCode: "483920",
};

export { RecoveryCode };
