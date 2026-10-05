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

export interface PolicyEscalationProperties extends ChromeProperties {
  ackUrl: string;
  dueBy: string;
  escalationNote: string;
  policyRef?: string;
  policyTitle: string;
  preferencesUrl: string;
  recipientName: string;
}

const PolicyEscalation = ({
  ackUrl,
  dueBy,
  escalationNote,
  policyRef,
  policyTitle,
  preferencesUrl,
  recipientName,
  ...chrome
}: PolicyEscalationProperties) => (
  <NotificationLayout {...chrome} preferencesUrl={preferencesUrl} previewText={`Overdue: ${policyTitle}`}>
    <EmailHero eyebrow="Acknowledgement overdue" title={policyTitle} />
    <EmailText>Hi {recipientName},</EmailText>
    {policyRef !== undefined && (
      <EmailCard>
        <EmailRow meta={policyRef} title="Reference" />
      </EmailCard>
    )}
    <EmailCallout level="error" title="Overdue">
      This policy was due for acknowledgement by {dueBy} and is still outstanding.
    </EmailCallout>
    <EmailText>{escalationNote}</EmailText>
    <EmailSection align="center" padding="8px 32px 28px">
      <EmailButton href={ackUrl}>Acknowledge now</EmailButton>
    </EmailSection>
  </NotificationLayout>
);

export const sampleVars: PolicyEscalationProperties = {
  ackUrl: "https://policies.example.org/ack/pol-facilities-000001",
  dueBy: "August 15, 2026",
  escalationNote: "Your manager has been notified. Please acknowledge as soon as possible.",
  policyRef: "POL-FACILITIES-000001",
  policyTitle: "Desk Booking Policy",
  preferencesUrl: "https://policies.example.org/portal/preferences",
  recipientName: "Bob",
};

export { PolicyEscalation };
