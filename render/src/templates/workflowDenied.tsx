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

export interface WorkflowDeniedProperties extends ChromeProperties {
  authorName: string;
  policyTitle: string;
  preferencesUrl: string;
  reason: string;
  reviewedBy: string;
  reviseUrl: string;
  workflowName: string;
}

const WorkflowDenied = ({
  authorName,
  policyTitle,
  preferencesUrl,
  reason,
  reviewedBy,
  reviseUrl,
  workflowName,
  ...chrome
}: WorkflowDeniedProperties) => (
  <NotificationLayout
    {...chrome}
    preferencesUrl={preferencesUrl}
    previewText={`Your submission was returned: ${workflowName}`}
  >
    <EmailHero eyebrow="Submission returned" title="Your submission was returned" />
    <EmailText>Hi {authorName},</EmailText>
    <EmailCard>
      <EmailRow meta={workflowName} title="Workflow" />
      <EmailRow meta={policyTitle} title="Policy" />
      <EmailRow meta={reviewedBy} title="Reviewed by" />
    </EmailCard>
    <EmailCallout level="error" title="Reason">
      {reason}
    </EmailCallout>
    <EmailSection align="center" padding="8px 32px 28px">
      <EmailButton href={reviseUrl}>Revise &amp; resubmit</EmailButton>
    </EmailSection>
  </NotificationLayout>
);

export const sampleVars: WorkflowDeniedProperties = {
  authorName: "Grace",
  policyTitle: "Travel Booking Procedure",
  preferencesUrl: "https://policies.example.org/portal/preferences",
  reason: "Section 4 conflicts with the current expense limits. Please align the two before resubmitting.",
  reviewedBy: "Heidi",
  reviseUrl: "https://policies.example.org/workflows/wf-2045/revise",
  workflowName: "Policy review & publish",
};

export { WorkflowDenied };
