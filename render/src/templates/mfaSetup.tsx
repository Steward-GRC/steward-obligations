// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { EmailButton, EmailCallout, EmailHero, EmailSection, EmailText } from "../components/index.js";

import { type ChromeProperties, NotificationLayout } from "./layout.js";

export interface MfaSetupProperties extends ChromeProperties {
  email: string;
  name?: string;
  setupUrl?: string;
}

const MfaSetup = ({ email, name, setupUrl, ...chrome }: MfaSetupProperties) => (
  <NotificationLayout {...chrome} previewText="Set up multi-factor authentication">
    <EmailHero eyebrow="Security" title="Set up multi-factor authentication" />
    <EmailText>Hi {name ?? email},</EmailText>
    <EmailText>
      Your organization requires multi-factor authentication on your account ({email}). Set it up
      now to keep signing in without interruption.
    </EmailText>
    <EmailCallout level="warn" title="Action needed">
      You&apos;ll be asked to set up MFA on your next sign-in if you haven&apos;t done so already.
    </EmailCallout>
    {setupUrl !== undefined && (
      <EmailSection align="center" padding="8px 32px 28px">
        <EmailButton href={setupUrl}>Set up MFA</EmailButton>
      </EmailSection>
    )}
  </NotificationLayout>
);

export const sampleVars: MfaSetupProperties = {
  email: "frank@example.org",
  name: "Frank",
  setupUrl: "https://policies.example.org/setup/mfa",
};

export { MfaSetup };
