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

export interface WorkflowReminderProperties extends ChromeProperties {
  approveUrl: string;
  dueBy?: string;
  policyTitle: string;
  preferencesUrl: string;
  recipientName: string;
  stageName: string;
  workflowName: string;
}

const WorkflowReminder = ({
  approveUrl,
  dueBy,
  policyTitle,
  preferencesUrl,
  recipientName,
  stageName,
  workflowName,
  ...chrome
}: WorkflowReminderProperties) => (
  <NotificationLayout
    {...chrome}
    preferencesUrl={preferencesUrl}
    previewText={`An approval is waiting on you: ${policyTitle}`}
  >
    <EmailHero eyebrow="Approval reminder" title="An approval is waiting on you" />
    <EmailText>Hi {recipientName},</EmailText>
    <EmailText>
      A decision on the policy below is still waiting on you and is past its due date. Please review it
      and record your decision.
    </EmailText>
    <EmailCard>
      <EmailRow meta={policyTitle} title="Policy" />
      <EmailRow meta={workflowName} title="Workflow" />
      <EmailRow meta={stageName} title="Step" />
    </EmailCard>
    {dueBy !== undefined && (
      <EmailCallout level="warn" title="Was due">
        This decision was due on {dueBy}.
      </EmailCallout>
    )}
    <EmailSection padding="8px 24px 24px">
      <EmailButton href={approveUrl}>Review and decide</EmailButton>
    </EmailSection>
  </NotificationLayout>
);

export const sampleVars: WorkflowReminderProperties = {
  approveUrl: "https://policies.example.org/workflows/approvals",
  dueBy: "August 4, 2026",
  policyTitle: "Desk Booking Policy",
  preferencesUrl: "https://policies.example.org/portal/preferences",
  recipientName: "Frank",
  stageName: "Legal review",
  workflowName: "Policy review & publish",
};

export { WorkflowReminder };
