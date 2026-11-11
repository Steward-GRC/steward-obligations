// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { EmailButton, EmailCallout, EmailCard, EmailHero, EmailRow, EmailSection, EmailText } from "../components/index.js";

import { type ChromeProperties, NotificationLayout } from "./layout.js";

export interface BreakGlassAlertProperties extends ChromeProperties {
  at?: string;
  email: string;
  reason?: string;
  reviewUrl?: string;
}

const BreakGlassAlert = ({ at, email, reason, reviewUrl, ...chrome }: BreakGlassAlertProperties) => (
  <NotificationLayout {...chrome} previewText={`Security alert: break-glass sign-in for ${email}`}>
    <EmailHero eyebrow="Security alert" title="A break-glass sign-in was used" />
    <EmailText>The break-glass credential for {email} was used to bypass single sign-on.</EmailText>
    {(at !== undefined || reason !== undefined) && (
      <EmailCard>
        {at !== undefined && <EmailRow meta={at} title="When" />}
        {reason !== undefined && <EmailRow meta={reason} title="Stated reason" />}
      </EmailCard>
    )}
    <EmailCallout level="error" title="Review this immediately">
      If this wasn&apos;t expected, rotate the break-glass credential and investigate right away.
    </EmailCallout>
    {reviewUrl !== undefined && (
      <EmailSection padding="8px 24px 24px">
        <EmailButton href={reviewUrl} variant="danger">
          Review this immediately
        </EmailButton>
      </EmailSection>
    )}
  </NotificationLayout>
);

export const sampleVars: BreakGlassAlertProperties = {
  at: "August 5, 2026 at 3:14 PM UTC",
  email: "dave@example.org",
  reason: "Identity provider outage, SSO sign-in unavailable.",
};

export { BreakGlassAlert };
