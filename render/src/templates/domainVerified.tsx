// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { EmailButton, EmailCallout, EmailHero, EmailSection, EmailText } from "../components/index.js";

import { type ChromeProperties, NotificationLayout } from "./layout.js";

export interface DomainVerifiedProperties extends ChromeProperties {
  domain: string;
  preferencesUrl: string;
  ssoSetupUrl?: string;
}

const DomainVerified = ({ domain, preferencesUrl, ssoSetupUrl, ...chrome }: DomainVerifiedProperties) => (
  <NotificationLayout {...chrome} preferencesUrl={preferencesUrl} previewText={`${domain} is verified`}>
    <EmailHero eyebrow="Domain verified" title={`${domain} is verified`} />
    <EmailText>
      We found your DNS TXT record and confirmed ownership of {domain}. You can now configure and
      test single sign-on for this domain.
    </EmailText>
    <EmailCallout level="info" title="Next step">
      Set up your identity provider connection to finish enabling SSO.
    </EmailCallout>
    {ssoSetupUrl !== undefined && (
      <EmailSection align="center" padding="8px 32px 28px">
        <EmailButton href={ssoSetupUrl}>Configure SSO</EmailButton>
      </EmailSection>
    )}
  </NotificationLayout>
);

export const sampleVars: DomainVerifiedProperties = {
  domain: "example.org",
  preferencesUrl: "https://policies.example.org/portal/preferences",
  ssoSetupUrl: "https://policies.example.org/admin/sso/example.org",
};

export { DomainVerified };
