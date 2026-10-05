// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import {
  EmailCallout,
  EmailCard,
  EmailHeading,
  EmailHero,
  EmailRow,
  EmailSection,
  EmailText,
  palette,
} from "../components/index.js";

import { type ChromeProperties, NotificationLayout } from "./layout.js";

export interface DomainVerificationInstructionsProperties extends ChromeProperties {
  dnsRecordName: string;
  domain: string;
  instructions: string;
  preferencesUrl: string;
  token: string;
}

const DomainVerificationInstructions = ({
  dnsRecordName,
  domain,
  instructions,
  preferencesUrl,
  token,
  ...chrome
}: DomainVerificationInstructionsProperties) => (
  <NotificationLayout {...chrome} preferencesUrl={preferencesUrl} previewText={`Verify ${domain} to enable SSO`}>
    <EmailHero eyebrow="Domain verification" title={`Verify ownership of ${domain}`} />
    <EmailText>
      To enable single sign-on for {domain}, add the DNS TXT record below and we&apos;ll verify it
      automatically.
    </EmailText>
    <EmailCard>
      <EmailRow meta={dnsRecordName} title="Record name" />
    </EmailCard>
    <EmailSection align="center" background={palette.tint} padding="28px 32px">
      <EmailHeading size="sm">{token}</EmailHeading>
    </EmailSection>
    <EmailCallout level="info" title="How to add this record">
      {instructions}
    </EmailCallout>
  </NotificationLayout>
);

export const sampleVars: DomainVerificationInstructionsProperties = {
  dnsRecordName: "_steward-verify.example.org",
  domain: "example.org",
  instructions:
    "Sign in to your DNS provider, create a new TXT record with the name and value shown above, and save it. DNS changes can take up to an hour to propagate.",
  preferencesUrl: "https://policies.example.org/portal/preferences",
  token: "steward-verify=sample-value",
};

export { DomainVerificationInstructions };
