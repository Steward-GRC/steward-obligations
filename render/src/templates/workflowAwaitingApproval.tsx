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

export interface WorkflowAwaitingApprovalProperties extends ChromeProperties {
  approveUrl: string;
  dueBy?: string;
  policyTitle: string;
  preferencesUrl: string;
  recipientName: string;
  requestedBy: string;
  reviewUrl?: string;
  workflowName: string;
}

const WorkflowAwaitingApproval = ({
  approveUrl,
  dueBy,
  policyTitle,
  preferencesUrl,
  recipientName,
  requestedBy,
  reviewUrl,
  workflowName,
  ...chrome
}: WorkflowAwaitingApprovalProperties) => (
  <NotificationLayout
    {...chrome}
    preferencesUrl={preferencesUrl}
    previewText={`Your approval is needed: ${workflowName}`}
  >
    <EmailHero eyebrow="Approval needed" title="Your approval is needed" />
    <EmailText>Hi {recipientName},</EmailText>
    <EmailCard>
      <EmailRow meta={workflowName} title="Workflow" />
      <EmailRow meta={policyTitle} title="Policy" />
      <EmailRow meta={requestedBy} title="Requested by" />
    </EmailCard>
    {dueBy !== undefined && (
      <EmailCallout level="warn" title="Decision needed by">
        Please respond by {dueBy}.
      </EmailCallout>
    )}
    <EmailSection align="center" padding={`8px 32px ${reviewUrl !== undefined ? "12px" : "28px"}`}>
      <EmailButton href={approveUrl}>Approve</EmailButton>
    </EmailSection>
    {reviewUrl !== undefined && (
      <EmailSection align="center" padding="0 32px 28px">
        <EmailButton href={reviewUrl} variant="secondary">
          Review
        </EmailButton>
      </EmailSection>
    )}
  </NotificationLayout>
);

export const sampleVars: WorkflowAwaitingApprovalProperties = {
  approveUrl: "https://policies.example.org/workflows/wf-2044/approve",
  dueBy: "August 4, 2026",
  policyTitle: "Desk Booking Policy",
  preferencesUrl: "https://policies.example.org/portal/preferences",
  recipientName: "Frank",
  requestedBy: "Grace",
  reviewUrl: "https://policies.example.org/workflows/wf-2044",
  workflowName: "Policy review & publish",
};

export { WorkflowAwaitingApproval };
