// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { EmailCallout, EmailCard, EmailHero, EmailRow, EmailText } from "../components/index.js";

import { type ChromeProperties, NotificationLayout } from "./layout.js";

export interface IdpTestFailedProperties extends ChromeProperties {
  connectionId?: string;
  detail: string;
  domain?: string;
  preferencesUrl: string;
}

const IdpTestFailed = ({ connectionId, detail, domain, preferencesUrl, ...chrome }: IdpTestFailedProperties) => (
  <NotificationLayout {...chrome} preferencesUrl={preferencesUrl} previewText="Your SSO connection test failed">
    <EmailHero eyebrow="Connection test failed" title="Your SSO connection test failed" />
    <EmailText>
      We ran a test sign-in against your identity provider connection and it didn&apos;t succeed.
    </EmailText>
    {(domain !== undefined || connectionId !== undefined) && (
      <EmailCard>
        {domain !== undefined && <EmailRow meta={domain} title="Domain" />}
        {connectionId !== undefined && <EmailRow meta={connectionId} title="Connection" />}
      </EmailCard>
    )}
    <EmailCallout level="error" title="Failure detail">
      {detail}
    </EmailCallout>
    <EmailText muted>
      Review your identity provider&apos;s metadata and certificate configuration, then run the test
      again from the admin console.
    </EmailText>
  </NotificationLayout>
);

export const sampleVars: IdpTestFailedProperties = {
  connectionId: "conn-4471",
  detail: "The IdP's SAML response signature could not be verified against the configured certificate.",
  domain: "example.org",
  preferencesUrl: "https://policies.example.org/portal/preferences",
};

export { IdpTestFailed };
