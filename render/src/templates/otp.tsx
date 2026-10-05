// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { EmailHeading, EmailHero, EmailSection, EmailText, palette } from "../components/index.js";

import { type ChromeProperties, NotificationLayout } from "./layout.js";

const DIGIT_GAP = " ";

export interface OtpProperties extends ChromeProperties {
  code: string;
  expiresInMinutes: number;
  recipientName?: string;
  requestContext?: string;
}

const Otp = ({ code, expiresInMinutes, recipientName, requestContext, ...chrome }: OtpProperties) => (
  <NotificationLayout {...chrome} previewText="Your verification code">
    <EmailHero eyebrow="Verification" title="Your verification code" />
    <EmailText>
      {recipientName !== undefined ? `Hi ${recipientName}, use` : "Use"} the code below
      {requestContext !== undefined ? ` to ${requestContext}` : ""}.
    </EmailText>
    <EmailSection align="center" background={palette.tint} padding="28px 32px">
      <EmailHeading size="lg">{code.split("").join(DIGIT_GAP)}</EmailHeading>
    </EmailSection>
    <EmailText muted>
      This code expires in {expiresInMinutes} minutes. If you didn&apos;t request this, you can
      safely ignore this email.
    </EmailText>
  </NotificationLayout>
);

export const sampleVars: OtpProperties = {
  code: "482913",
  expiresInMinutes: 10,
  recipientName: "Grace",
  requestContext: "sign in to your account",
};

export { Otp };
