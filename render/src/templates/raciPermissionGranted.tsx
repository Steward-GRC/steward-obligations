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

export interface RaciPermissionGrantedProperties extends ChromeProperties {
  actionLabel?: string;
  actionUrl: string;
  category: string;
  grantedBy: string;
  preferencesUrl: string;
  recipientName: string;
  role: string;
}

const RaciPermissionGranted = ({
  actionLabel,
  actionUrl,
  category,
  grantedBy,
  preferencesUrl,
  recipientName,
  role,
  ...chrome
}: RaciPermissionGrantedProperties) => (
  <NotificationLayout
    {...chrome}
    preferencesUrl={preferencesUrl}
    previewText={`You've been granted ${role} for ${category}`}
  >
    <EmailHero eyebrow="Permission granted" title={`You've been granted ${role}`} />
    <EmailText>Hi {recipientName},</EmailText>
    <EmailText>
      You&apos;ve been granted {role} for {category}.
    </EmailText>
    <EmailCard>
      <EmailRow meta={role} title="Role" />
      <EmailRow meta={category} title="Category" />
      <EmailRow meta={grantedBy} title="Granted by" />
    </EmailCard>
    <EmailCallout level="info" title="Scope">
      This {role} access applies to {category} and its policies and sub-categories.
    </EmailCallout>
    <EmailSection align="center" padding="8px 32px 28px">
      <EmailButton href={actionUrl}>{actionLabel ?? `Go to ${category}`}</EmailButton>
    </EmailSection>
  </NotificationLayout>
);

export const sampleVars: RaciPermissionGrantedProperties = {
  actionUrl: "https://policies.example.org/categories/finance",
  category: "Finance",
  grantedBy: "Bob",
  preferencesUrl: "https://policies.example.org/portal/preferences",
  recipientName: "Erin",
  role: "Author",
};

export { RaciPermissionGranted };
