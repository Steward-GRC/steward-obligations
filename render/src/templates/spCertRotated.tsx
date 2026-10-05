// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { EmailCallout, EmailCard, EmailHero, EmailRow, EmailText } from "../components/index.js";

import { type ChromeProperties, NotificationLayout } from "./layout.js";

export interface SpCertRotatedProperties extends ChromeProperties {
  notAfter?: string;
  preferencesUrl: string;
  serial: string;
}

const SpCertRotated = ({ notAfter, preferencesUrl, serial, ...chrome }: SpCertRotatedProperties) => (
  <NotificationLayout
    {...chrome}
    preferencesUrl={preferencesUrl}
    previewText="Your SSO signing certificate was rotated"
  >
    <EmailHero eyebrow="Certificate rotated" title="Your SSO signing certificate was rotated" />
    <EmailText>
      The service-provider certificate used to sign and verify SAML messages for your organization
      has been rotated.
    </EmailText>
    <EmailCard>
      <EmailRow meta={serial} title="New serial" />
      {notAfter !== undefined && <EmailRow meta={notAfter} title="Expires" />}
    </EmailCard>
    <EmailCallout level="warn" title="Action may be required">
      If your identity provider pins our service-provider metadata or certificate, update it to
      match the new certificate above.
    </EmailCallout>
  </NotificationLayout>
);

export const sampleVars: SpCertRotatedProperties = {
  notAfter: "August 5, 2027",
  preferencesUrl: "https://policies.example.org/portal/preferences",
  serial: "5A:3F:9C:12:E8:07:B4:66",
};

export { SpCertRotated };
