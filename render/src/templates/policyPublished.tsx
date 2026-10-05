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

export interface PolicyPublishedProperties extends ChromeProperties {
  ackUrl?: string;
  effectiveDate: string;
  policyRef: string;
  policyTitle: string;
  policyUrl: string;
  preferencesUrl: string;
  recipientName: string;
  requiresAck: boolean;
  summary: string;
}

const PolicyPublished = ({
  ackUrl,
  effectiveDate,
  policyRef,
  policyTitle,
  policyUrl,
  preferencesUrl,
  recipientName,
  requiresAck,
  summary,
  ...chrome
}: PolicyPublishedProperties) => {
  const showAck = requiresAck && ackUrl !== undefined;
  return (
    <NotificationLayout
      {...chrome}
      preferencesUrl={preferencesUrl}
      previewText={`New policy published: ${policyTitle}`}
    >
      <EmailHero eyebrow="Policy published" title={policyTitle} />
      <EmailText>Hi {recipientName},</EmailText>
      <EmailText>{summary}</EmailText>
      <EmailCard>
        <EmailRow meta={policyRef} title="Reference" />
        <EmailRow meta={effectiveDate} title="Effective" />
      </EmailCard>
      {requiresAck && (
        <EmailCallout level="info" title="Acknowledgement required">
          This policy requires your formal acknowledgement.
        </EmailCallout>
      )}
      <EmailSection align="center" padding={`8px 32px ${showAck ? "12px" : "28px"}`}>
        <EmailButton href={policyUrl}>Read policy</EmailButton>
      </EmailSection>
      {showAck && (
        <EmailSection align="center" padding="0 32px 28px">
          <EmailButton href={ackUrl} variant="secondary">
            Acknowledge
          </EmailButton>
        </EmailSection>
      )}
    </NotificationLayout>
  );
};

export const sampleVars: PolicyPublishedProperties = {
  ackUrl: "https://policies.example.org/ack/pol-facilities-000001",
  effectiveDate: "September 1, 2026",
  policyRef: "POL-FACILITIES-000001",
  policyTitle: "Desk Booking Policy",
  policyUrl: "https://policies.example.org/policies/pol-facilities-000001",
  preferencesUrl: "https://policies.example.org/portal/preferences",
  recipientName: "Carol",
  requiresAck: true,
  summary:
    "This new policy sets out how desks are booked and released in shared offices. Please read it and confirm your acknowledgement.",
};

export { PolicyPublished };
