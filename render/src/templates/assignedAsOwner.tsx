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

export interface AssignedAsOwnerProperties extends ChromeProperties {
  actionLabel?: string;
  actionUrl: string;
  assignedBy: string;
  category: string;
  preferencesUrl: string;
  recipientName: string;
}

const AssignedAsOwner = ({
  actionLabel,
  actionUrl,
  assignedBy,
  category,
  preferencesUrl,
  recipientName,
  ...chrome
}: AssignedAsOwnerProperties) => (
  <NotificationLayout
    {...chrome}
    preferencesUrl={preferencesUrl}
    previewText={`You've been assigned as owner of ${category}`}
  >
    <EmailHero eyebrow="Ownership assigned" title={`You've been assigned as owner of ${category}`} />
    <EmailText>Hi {recipientName},</EmailText>
    <EmailText>
      As owner of {category}, you have override authority and overarching review responsibility for{" "}
      {category} and all of its sub-categories.
    </EmailText>
    <EmailCard>
      <EmailRow meta={category} title="Category" />
      <EmailRow meta={assignedBy} title="Assigned by" />
    </EmailCard>
    <EmailCallout level="info" title="What this means">
      Owners can override and are accountable for review across {category} and everything beneath
      it.
    </EmailCallout>
    <EmailSection align="center" padding="8px 32px 28px">
      <EmailButton href={actionUrl}>{actionLabel ?? `Go to ${category}`}</EmailButton>
    </EmailSection>
  </NotificationLayout>
);

export const sampleVars: AssignedAsOwnerProperties = {
  actionUrl: "https://policies.example.org/categories/facilities",
  assignedBy: "Bob",
  category: "Facilities",
  preferencesUrl: "https://policies.example.org/portal/preferences",
  recipientName: "Alice",
};

export { AssignedAsOwner };
