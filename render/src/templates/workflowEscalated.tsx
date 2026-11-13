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

export interface WorkflowEscalatedProperties extends ChromeProperties {
  dueBy?: string;
  pendingCount: number;
  policyTitle: string;
  preferencesUrl: string;
  recipientName: string;
  stageName: string;
  workflowName: string;
  workflowUrl: string;
}

const WorkflowEscalated = ({
  dueBy,
  pendingCount,
  policyTitle,
  preferencesUrl,
  recipientName,
  stageName,
  workflowName,
  workflowUrl,
  ...chrome
}: WorkflowEscalatedProperties) => (
  <NotificationLayout
    {...chrome}
    preferencesUrl={preferencesUrl}
    previewText={`An approval is overdue in your category: ${policyTitle}`}
  >
    <EmailHero eyebrow="Approval overdue" title="An approval is overdue in your category" />
    <EmailText>Hi {recipientName},</EmailText>
    <EmailText>
      A step in the approval workflow for a policy in a category you own is still undecided well after
      its due date. This is a notice only. Nothing has been approved or rejected on your behalf, and no
      action is required from you unless you want to follow up with the approvers.
    </EmailText>
    <EmailCard>
      <EmailRow meta={policyTitle} title="Policy" />
      <EmailRow meta={workflowName} title="Workflow" />
      <EmailRow meta={stageName} title="Step" />
      <EmailRow meta={String(pendingCount)} title="Approvers still to decide" />
    </EmailCard>
    {dueBy !== undefined && (
      <EmailCallout level="warn" title="Was due">
        This step was due on {dueBy}.
      </EmailCallout>
    )}
    <EmailSection padding="8px 24px 24px">
      <EmailButton href={workflowUrl}>View workflows</EmailButton>
    </EmailSection>
  </NotificationLayout>
);

export const sampleVars: WorkflowEscalatedProperties = {
  dueBy: "August 4, 2026",
  pendingCount: 2,
  policyTitle: "Desk Booking Policy",
  preferencesUrl: "https://policies.example.org/portal/preferences",
  recipientName: "Olive",
  stageName: "Legal review",
  workflowName: "Policy review & publish",
  workflowUrl: "https://policies.example.org/workflows",
};

export { WorkflowEscalated };
