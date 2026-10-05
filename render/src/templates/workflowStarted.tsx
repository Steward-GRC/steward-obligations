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

export interface WorkflowStartedProperties extends ChromeProperties {
  currentStep?: string;
  initiatedBy: string;
  policyTitle?: string;
  preferencesUrl: string;
  recipientName: string;
  workflowName: string;
  workflowUrl: string;
}

const WorkflowStarted = ({
  currentStep,
  initiatedBy,
  policyTitle,
  preferencesUrl,
  recipientName,
  workflowName,
  workflowUrl,
  ...chrome
}: WorkflowStartedProperties) => (
  <NotificationLayout
    {...chrome}
    preferencesUrl={preferencesUrl}
    previewText={`A workflow has started: ${workflowName}`}
  >
    <EmailHero eyebrow="Workflow started" title="A workflow has started" />
    <EmailText>Hi {recipientName},</EmailText>
    <EmailCard>
      <EmailRow meta={workflowName} title="Workflow" />
      {policyTitle !== undefined && <EmailRow meta={policyTitle} title="Policy" />}
      <EmailRow meta={initiatedBy} title="Initiated by" />
      {currentStep !== undefined && <EmailRow meta={currentStep} title="Current step" />}
    </EmailCard>
    <EmailCallout level="info" title="Stay informed">
      You&apos;ll be notified as this workflow progresses.
    </EmailCallout>
    <EmailSection align="center" padding="8px 32px 28px">
      <EmailButton href={workflowUrl}>View workflow</EmailButton>
    </EmailSection>
  </NotificationLayout>
);

export const sampleVars: WorkflowStartedProperties = {
  currentStep: "Awaiting review",
  initiatedBy: "Grace",
  policyTitle: "Desk Booking Policy",
  preferencesUrl: "https://policies.example.org/portal/preferences",
  recipientName: "Heidi",
  workflowName: "Policy review & publish",
  workflowUrl: "https://policies.example.org/workflows/wf-2044",
};

export { WorkflowStarted };
