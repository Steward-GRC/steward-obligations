// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { EmailButton, EmailCallout, EmailHero, EmailSection, EmailText } from "../components/index.js";

import { type ChromeProperties, DEFAULT_PRODUCT_NAME, NotificationLayout } from "./layout.js";

// A just-in-time provisioned SSO user has no local password to fall back on,
// so the email must spell out how to sign in again; loginUrl is optional only
// so the guidance still renders if the sender omits it.
export interface SsoAccountWelcomeProperties extends ChromeProperties {
  email: string;
  loginUrl?: string;
  name?: string;
}

const SsoAccountWelcome = ({
  email,
  loginUrl,
  name,
  productName = DEFAULT_PRODUCT_NAME,
  ...chrome
}: SsoAccountWelcomeProperties) => (
  <NotificationLayout {...chrome} previewText="Welcome, your account is ready" productName={productName}>
    <EmailHero eyebrow="Welcome" title="Welcome, your account is ready" />
    <EmailText>Hi {name ?? email},</EmailText>
    <EmailText>
      Your account ({email}) was created automatically when you signed in through your
      organization&apos;s identity provider. You&apos;re all set, and you can sign in any time.
    </EmailText>
    {loginUrl !== undefined && (
      <EmailSection align="center" padding="8px 32px 28px">
        <EmailButton href={loginUrl}>Sign in with SSO</EmailButton>
      </EmailSection>
    )}
    <EmailCallout level="info" title="How to sign in next time">
      Go to the {productName} login page and choose &quot;Sign in with SSO&quot;. You&apos;ll be sent
      to your organization&apos;s login to finish signing in. There&apos;s no separate {productName}{" "}
      password: your organization login is all you need.
    </EmailCallout>
  </NotificationLayout>
);

export const sampleVars: SsoAccountWelcomeProperties = {
  email: "ivan@example.org",
  loginUrl: "https://policies.example.org",
  name: "Ivan",
};

export { SsoAccountWelcome };
