// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { EmailCallout, EmailCard, EmailHero, EmailRow, EmailText } from "../components/index.js";

import { type ChromeProperties, NotificationLayout } from "./layout.js";

export interface AccessGrantedProperties extends ChromeProperties {
  email: string;
  groups: string[];
  preferencesUrl: string;
}

const AccessGranted = ({ email, groups, preferencesUrl, ...chrome }: AccessGrantedProperties) => (
  <NotificationLayout {...chrome} preferencesUrl={preferencesUrl} previewText="Your access has been granted">
    <EmailHero eyebrow="Access granted" title="Your access has been granted" />
    <EmailText>
      An administrator has reviewed your account ({email}) and granted you access to the group(s)
      below.
    </EmailText>
    <EmailCard>
      {groups.map((group) => (
        <EmailRow key={group} title={group} />
      ))}
    </EmailCard>
    <EmailCallout level="info" title="What's next">
      You can now sign in and use everything your granted groups unlock.
    </EmailCallout>
  </NotificationLayout>
);

export const sampleVars: AccessGrantedProperties = {
  email: "carol@example.org",
  groups: ["Finance team", "All staff"],
  preferencesUrl: "https://policies.example.org/portal/preferences",
};

export { AccessGranted };
