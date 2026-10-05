// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { EmailCallout, EmailHero, EmailText } from "../components/index.js";

import { type ChromeProperties, NotificationLayout } from "./layout.js";

export interface SsoActivatedProperties extends ChromeProperties {
  domain: string;
  orgName?: string;
  preferencesUrl: string;
}

const SsoActivated = ({ domain, orgName, preferencesUrl, ...chrome }: SsoActivatedProperties) => (
  <NotificationLayout
    {...chrome}
    preferencesUrl={preferencesUrl}
    previewText={`Single sign-on is now active for ${domain}`}
  >
    <EmailHero eyebrow="SSO activated" title="Single sign-on is now active" />
    <EmailText>
      Single sign-on has been activated for {domain}
      {orgName !== undefined ? ` (${orgName})` : ""}. Members signing in with an address on this
      domain will now be routed to your identity provider.
    </EmailText>
    <EmailCallout level="info" title="What changes">
      New and existing members with a {domain} address will authenticate through your identity
      provider going forward.
    </EmailCallout>
  </NotificationLayout>
);

export const sampleVars: SsoActivatedProperties = {
  domain: "example.org",
  orgName: "Example Organisation",
  preferencesUrl: "https://policies.example.org/portal/preferences",
};

export { SsoActivated };
