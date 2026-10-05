// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import {
  EmailButton,
  EmailCallout,
  EmailCard,
  EmailHeading,
  EmailHero,
  EmailRow,
  EmailSection,
  EmailText,
} from "../components/index.js";

import { type ChromeProperties, DEFAULT_PRODUCT_NAME, NotificationLayout } from "./layout.js";

export interface DigestItem {
  actionHref?: string;
  actionLabel?: string;
  meta?: string;
  title: string;
}

export interface DigestProperties extends ChromeProperties {
  approvals: DigestItem[];
  awaitingAck: DigestItem[];
  newAndUpdated: DigestItem[];
  other: DigestItem[];
  periodLabel: string;
  portalLabel?: string;
  portalUrl?: string;
  preferencesUrl: string;
  recipientName: string;
  unsubscribeHref?: string;
}

interface DigestSectionProperties {
  items: DigestItem[];
  title: string;
}

const DigestSection = ({ items, title }: DigestSectionProperties) => {
  if (items.length === 0) return null;
  return (
    <>
      <EmailSection padding="20px 32px 4px">
        <EmailHeading size="sm">{`${title} (${items.length})`}</EmailHeading>
      </EmailSection>
      <EmailCard>
        {items.map((item, index) => (
          <EmailRow
            actionHref={item.actionHref}
            actionLabel={item.actionLabel}
            key={`${item.title}-${index}`}
            meta={item.meta}
            title={item.title}
          />
        ))}
      </EmailCard>
    </>
  );
};

const Digest = ({
  approvals,
  awaitingAck,
  newAndUpdated,
  other,
  periodLabel,
  portalLabel = "Open the portal",
  portalUrl,
  preferencesUrl,
  productName = DEFAULT_PRODUCT_NAME,
  recipientName,
  unsubscribeHref,
  ...chrome
}: DigestProperties) => {
  const total = awaitingAck.length + newAndUpdated.length + approvals.length + other.length;
  return (
    <NotificationLayout
      {...chrome}
      preferencesUrl={preferencesUrl}
      previewText={`Your ${productName} digest: ${total} update${total === 1 ? "" : "s"}`}
      productName={productName}
      unsubscribeHref={unsubscribeHref}
    >
      <EmailHero eyebrow="Digest" subtitle={periodLabel} title={`Your ${productName} digest`} />
      <EmailText>Hi {recipientName},</EmailText>
      <EmailText>
        {total === 0
          ? "You're all caught up. There's nothing new to review right now."
          : `You have ${total} item${total === 1 ? " that needs" : "s that need"} your attention.`}
      </EmailText>
      {awaitingAck.length > 0 && (
        <EmailCallout level="warn" title="Action needed">
          You have {awaitingAck.length} outstanding acknowledgement
          {awaitingAck.length === 1 ? "" : "s"}.
        </EmailCallout>
      )}
      <DigestSection items={awaitingAck} title="Awaiting your acknowledgement" />
      <DigestSection items={newAndUpdated} title="New & updated policies" />
      <DigestSection items={approvals} title="Approvals & assignments" />
      <DigestSection items={other} title="Other" />
      {portalUrl !== undefined && (
        <EmailSection align="center" padding="16px 32px 28px">
          <EmailButton href={portalUrl}>{portalLabel}</EmailButton>
        </EmailSection>
      )}
    </NotificationLayout>
  );
};

export const sampleVars: DigestProperties = {
  approvals: [
    {
      actionHref: "https://policies.example.org/workflows/wf-2044/approve",
      actionLabel: "Approve",
      meta: "Requested by Grace · due August 28, 2026",
      title: "Approval needed: Expense Claims Policy",
    },
    {
      actionHref: "https://policies.example.org/categories/finance",
      actionLabel: "Open",
      meta: "You were assigned as Author",
      title: "New assignment: Finance",
    },
  ],
  awaitingAck: [
    {
      actionHref: "https://policies.example.org/ack/pol-facilities-000001",
      actionLabel: "Acknowledge",
      meta: "Due August 15, 2026",
      title: "Desk Booking Policy (POL-FACILITIES-000001)",
    },
    {
      actionHref: "https://policies.example.org/ack/prc-travel-000003",
      actionLabel: "Acknowledge",
      meta: "Due August 18, 2026",
      title: "Travel Booking Procedure (PRC-TRAVEL-000003)",
    },
    {
      actionHref: "https://policies.example.org/ack/pol-finance-000002",
      actionLabel: "Acknowledge",
      meta: "Due August 20, 2026",
      title: "Expense Claims & Refunds Policy (POL-FINANCE-000002)",
    },
  ],
  newAndUpdated: [
    {
      actionHref: "https://policies.example.org/policies/pol-facilities-000004",
      actionLabel: "Read",
      meta: "Effective September 1, 2026",
      title: "Meeting Room Policy (POL-FACILITIES-000004)",
    },
  ],
  other: [
    {
      actionHref: "https://policies.example.org/portal",
      meta: "Granted by Bob",
      title: "You were added to the Finance team group",
    },
  ],
  periodLabel: "Tuesday, August 25, 2026",
  portalUrl: "https://policies.example.org/portal",
  preferencesUrl: "https://policies.example.org/portal/preferences",
  recipientName: "Alice",
  unsubscribeHref: "https://policies.example.org/portal/preferences/unsubscribe?c=informational",
};

export { Digest };
