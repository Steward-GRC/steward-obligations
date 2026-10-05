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

// Past the cap the email summarises and points at the portal rather than
// listing every pending acknowledgement.
const VISIBLE_CAP = 5;

export interface PolicyAckReminderPolicy {
  ackUrl: string;
  dueBy: string;
  ref: string;
  title: string;
}

export interface PolicyAckReminderProperties extends ChromeProperties {
  policies: PolicyAckReminderPolicy[];
  portalLabel?: string;
  portalUrl: string;
  preferencesUrl?: string;
  recipientName: string;
}

const nearestDueBy = (policies: PolicyAckReminderPolicy[]): string => {
  let nearest = policies[0];
  let nearestTime = nearest === undefined ? Number.NaN : Date.parse(nearest.dueBy);
  for (const policy of policies.slice(1)) {
    const time = Date.parse(policy.dueBy);
    if (!Number.isNaN(time) && (Number.isNaN(nearestTime) || time < nearestTime)) {
      nearest = policy;
      nearestTime = time;
    }
  }
  return nearest?.dueBy ?? "";
};

const PolicyAckReminder = ({
  policies,
  portalLabel,
  portalUrl,
  preferencesUrl,
  recipientName,
  ...chrome
}: PolicyAckReminderProperties) => {
  const isLarge = policies.length > VISIBLE_CAP;
  const visible = isLarge ? policies.slice(0, VISIBLE_CAP) : policies;
  const remaining = policies.length - visible.length;
  const ctaLabel = portalLabel ?? (isLarge ? "Review in the portal" : "Review & acknowledge");

  return (
    <NotificationLayout
      {...chrome}
      preferencesUrl={preferencesUrl}
      previewText={
        isLarge
          ? `You have ${policies.length} pending acknowledgements`
          : `Acknowledgement due: ${policies[0]?.title ?? ""}`
      }
    >
      <EmailHero eyebrow="Acknowledgement due" title="Policy acknowledgements pending" />
      <EmailText>Hi {recipientName},</EmailText>
      <EmailText>
        {isLarge
          ? `You have ${policies.length} pending acknowledgements.`
          : "The following policies need your acknowledgement."}
      </EmailText>
      <EmailCard>
        {visible.map((policy) => (
          <EmailRow key={policy.ref} meta={`Due ${policy.dueBy}`} title={policy.title} />
        ))}
        {isLarge && <EmailRow key="more" title={`…and ${remaining} more`} />}
      </EmailCard>
      {isLarge && (
        <EmailCallout level="warn" title="Nearest due date">
          Your earliest deadline is {nearestDueBy(policies)}.
        </EmailCallout>
      )}
      <EmailSection align="center" padding="8px 32px 28px">
        <EmailButton href={portalUrl}>{ctaLabel}</EmailButton>
      </EmailSection>
    </NotificationLayout>
  );
};

export const sampleVars: PolicyAckReminderProperties = {
  policies: [
    {
      ackUrl: "https://policies.example.org/ack/pol-facilities-000001",
      dueBy: "August 15, 2026",
      ref: "POL-FACILITIES-000001",
      title: "Desk Booking Policy",
    },
    {
      ackUrl: "https://policies.example.org/ack/pol-finance-000002",
      dueBy: "August 18, 2026",
      ref: "POL-FINANCE-000002",
      title: "Expense Claims Policy",
    },
    {
      ackUrl: "https://policies.example.org/ack/prc-travel-000003",
      dueBy: "August 20, 2026",
      ref: "PRC-TRAVEL-000003",
      title: "Travel Booking Procedure",
    },
    {
      ackUrl: "https://policies.example.org/ack/pol-facilities-000004",
      dueBy: "August 22, 2026",
      ref: "POL-FACILITIES-000004",
      title: "Meeting Room Policy",
    },
    {
      ackUrl: "https://policies.example.org/ack/prc-finance-000005",
      dueBy: "August 25, 2026",
      ref: "PRC-FINANCE-000005",
      title: "Purchase Approval Procedure",
    },
    {
      ackUrl: "https://policies.example.org/ack/pol-facilities-000006",
      dueBy: "August 27, 2026",
      ref: "POL-FACILITIES-000006",
      title: "Visitor Sign-in Policy",
    },
    {
      ackUrl: "https://policies.example.org/ack/prc-travel-000007",
      dueBy: "August 29, 2026",
      ref: "PRC-TRAVEL-000007",
      title: "Travel Expense Procedure",
    },
  ],
  portalUrl: "https://policies.example.org/portal/acknowledgements",
  preferencesUrl: "https://policies.example.org/portal/preferences",
  recipientName: "Alice",
};

export { PolicyAckReminder };
