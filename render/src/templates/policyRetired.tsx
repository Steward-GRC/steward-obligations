// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import {
  EmailButton,
  EmailCallout,
  EmailCard,
  EmailHero,
  EmailRow,
  EmailSection,
  EmailText,
} from "../components/index.js";

import { type ChromeProperties, NotificationLayout } from "./layout.js";

export interface PolicyRetiredProperties extends ChromeProperties {
  policyRef: string;
  policyTitle: string;
  policyUrl: string;
  preferencesUrl: string;
  recipientName: string;
  retiredDate: string;
  summary: string;
}

const PolicyRetired = ({
  policyRef,
  policyTitle,
  policyUrl,
  preferencesUrl,
  recipientName,
  retiredDate,
  summary,
  ...chrome
}: PolicyRetiredProperties) => (
  <NotificationLayout {...chrome} preferencesUrl={preferencesUrl} previewText={`Policy retired: ${policyTitle}`}>
    <EmailHero eyebrow="Policy retired" title={policyTitle} />
    <EmailText>Hi {recipientName},</EmailText>
    <EmailText>{summary}</EmailText>
    <EmailCard>
      <EmailRow meta={policyRef} title="Reference" />
      <EmailRow meta={retiredDate} title="Retired" />
    </EmailCard>
    <EmailCallout level="info" title="No acknowledgement required">
      This policy has been retired. Any outstanding acknowledgement for it has been cancelled, so
      you do not need to take any action.
    </EmailCallout>
    <EmailSection align="center" padding="8px 32px 28px">
      <EmailButton href={policyUrl}>View policies</EmailButton>
    </EmailSection>
  </NotificationLayout>
);

export const sampleVars: PolicyRetiredProperties = {
  policyRef: "PRC-TRAVEL-000003",
  policyTitle: "Travel Booking Procedure",
  policyUrl: "https://policies.example.org/policies",
  preferencesUrl: "https://policies.example.org/portal/preferences",
  recipientName: "Dave",
  retiredDate: "August 25, 2026",
  summary:
    "This procedure has been retired and is no longer in effect. You no longer need to acknowledge it.",
};

export { PolicyRetired };
