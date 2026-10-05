// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import {
  EmailButton,
  EmailCallout,
  EmailCard,
  EmailHeading,
  EmailHero,
  EmailSection,
  EmailText,
} from "../components/index.js";

import { type ChromeProperties, NotificationLayout } from "./layout.js";

export interface AckRequiredProperties extends ChromeProperties {
  ackUrl: string;
  bodyText: string;
  dueBy?: string;
  itemTitle: string;
  preferencesUrl?: string;
  recipientName: string;
}

const AckRequired = ({
  ackUrl,
  bodyText,
  dueBy,
  itemTitle,
  preferencesUrl,
  recipientName,
  ...chrome
}: AckRequiredProperties) => (
  <NotificationLayout {...chrome} preferencesUrl={preferencesUrl} previewText={`Action required: ${itemTitle}`}>
    <EmailHero eyebrow="Action required" title="Your acknowledgement is needed" />
    <EmailText>Hi {recipientName},</EmailText>
    <EmailText>{bodyText}</EmailText>
    <EmailCard>
      <EmailHeading size="sm">{itemTitle}</EmailHeading>
      <EmailText inset={false} muted>
        Please review this item and confirm your acknowledgement.
      </EmailText>
    </EmailCard>
    {dueBy !== undefined && <EmailCallout title="Due date">Please acknowledge by {dueBy}.</EmailCallout>}
    <EmailSection align="center" padding="8px 32px 28px">
      <EmailButton href={ackUrl}>Review &amp; acknowledge</EmailButton>
    </EmailSection>
  </NotificationLayout>
);

export const sampleVars: AckRequiredProperties = {
  ackUrl: "https://policies.example.org/ack/prc-travel-000003",
  bodyText:
    "Please review the item below and confirm you've read and understood it before September 1.",
  dueBy: "September 1, 2026",
  itemTitle: "Travel Booking Procedure",
  preferencesUrl: "https://policies.example.org/portal/preferences",
  recipientName: "Alice",
};

export { AckRequired };
