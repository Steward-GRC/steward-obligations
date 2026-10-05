// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { EmailCallout, EmailHero, EmailText } from "../components/index.js";

import { type ChromeProperties, NotificationLayout } from "./layout.js";

export interface SsoDisabledProperties extends ChromeProperties {
  domain: string;
  orgName?: string;
  preferencesUrl: string;
}

const SsoDisabled = ({ domain, orgName, preferencesUrl, ...chrome }: SsoDisabledProperties) => (
  <NotificationLayout
    {...chrome}
    preferencesUrl={preferencesUrl}
    previewText={`Single sign-on has been disabled for ${domain}`}
  >
    <EmailHero eyebrow="SSO disabled" title="Single sign-on has been disabled" />
    <EmailText>
      Single sign-on has been disabled for {domain}
      {orgName !== undefined ? ` (${orgName})` : ""}.
    </EmailText>
    <EmailCallout level="warn" title="What changes">
      Members with a {domain} address will no longer be routed to your identity provider and will
      fall back to their prior sign-in method.
    </EmailCallout>
  </NotificationLayout>
);

export const sampleVars: SsoDisabledProperties = {
  domain: "example.org",
  orgName: "Example Organisation",
  preferencesUrl: "https://policies.example.org/portal/preferences",
};

export { SsoDisabled };
