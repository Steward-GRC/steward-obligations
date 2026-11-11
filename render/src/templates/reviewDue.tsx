// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { EmailButton, EmailCard, EmailHero, EmailRow, EmailSection, EmailText } from "../components/index.js";

import { type ChromeProperties, NotificationLayout } from "./layout.js";

export interface ReviewDueProperties extends ChromeProperties {
  category?: string;
  dueDate: string;
  policyRef?: string;
  policyTitle: string;
  preferencesUrl?: string;
  recipientName: string;
  reviewUrl: string;
}

// E6: the owner of a policy is told its periodic review is coming due.
const ReviewDue = ({
  category,
  dueDate,
  policyRef,
  policyTitle,
  preferencesUrl,
  recipientName,
  reviewUrl,
  ...chrome
}: ReviewDueProperties) => (
  <NotificationLayout {...chrome} preferencesUrl={preferencesUrl} previewText={`Review due: ${policyTitle}`}>
    <EmailHero title="A review is due" />
    <EmailText>Hi {recipientName},</EmailText>
    <EmailText>
      You own {policyTitle}. Its review is due on {dueDate}.
    </EmailText>
    <EmailCard>
      {policyRef !== undefined && <EmailRow meta={policyRef} title="Reference" />}
      {category !== undefined && <EmailRow meta={category} title="Category" />}
      <EmailRow meta={dueDate} title="Due" />
    </EmailCard>
    <EmailSection padding="8px 24px 24px">
      <EmailButton href={reviewUrl}>Open my reviews</EmailButton>
    </EmailSection>
  </NotificationLayout>
);

export const sampleVars: ReviewDueProperties = {
  category: "Workplace › Facilities",
  dueDate: "2 Oct 2027",
  policyRef: "POL-FACILITIES-000001",
  policyTitle: "Desk Booking Policy",
  preferencesUrl: "https://policies.example.org/portal/preferences",
  recipientName: "Dave",
  reviewUrl: "https://policies.example.org/reviews",
};

export { ReviewDue };
