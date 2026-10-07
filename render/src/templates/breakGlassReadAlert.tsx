// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { EmailCallout, EmailCard, EmailHero, EmailRow, EmailText } from "../components/index.js";

import { type ChromeProperties, NotificationLayout } from "./layout.js";

export interface BreakGlassReadAlertProperties extends ChromeProperties {
  actAsAdmin?: string;
  at?: string;
  number: string;
  reader?: string;
  title: string;
}

const BreakGlassReadAlert = ({
  actAsAdmin,
  at,
  number,
  reader,
  title,
  ...chrome
}: BreakGlassReadAlertProperties) => (
  <NotificationLayout {...chrome} previewText={`Security alert: break-glass read of ${number}`}>
    <EmailHero eyebrow="Security alert" title="A document was read under break-glass" />
    <EmailText>
      {number} {title} was read with a time-boxed break-glass grant, outside the normal access rules.
    </EmailText>
    <EmailCard>
      {reader !== undefined && <EmailRow meta={reader} title="Read by" />}
      {actAsAdmin !== undefined && <EmailRow meta={actAsAdmin} title="Acting as that user" />}
      {at !== undefined && <EmailRow meta={at} title="When" />}
    </EmailCard>
    <EmailCallout level="error" title="Review this read">
      Every break-glass read is recorded in the audit log. If it wasn&apos;t expected, investigate right away.
    </EmailCallout>
  </NotificationLayout>
);

export const sampleVars: BreakGlassReadAlertProperties = {
  actAsAdmin: "alice@example.org",
  at: "August 5, 2026 at 3:14 PM UTC",
  number: "POL-HR-000042",
  reader: "dave@example.org",
  title: "Sensitive Matter",
};

export { BreakGlassReadAlert };
