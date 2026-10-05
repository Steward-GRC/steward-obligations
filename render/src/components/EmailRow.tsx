// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Column, Link, Row, Text } from "@react-email/components";

import { palette } from "./palette.js";

export interface EmailRowProperties {
  actionHref?: string;
  actionLabel?: string;
  meta?: string;
  title: string;
}

export const EmailRow = ({ actionHref, actionLabel = "View", meta, title }: EmailRowProperties) => (
  <Row style={{ borderBottom: `1px solid ${palette.tint}` }}>
    <Column style={{ padding: "10px 0", verticalAlign: "top" }}>
      <Text style={{ color: palette.text, fontSize: "14px", fontWeight: 600, lineHeight: "1.4", margin: 0 }}>
        {title}
      </Text>
      {meta !== undefined && (
        <Text style={{ color: palette.muted, fontSize: "13px", lineHeight: "1.4", margin: "2px 0 0" }}>{meta}</Text>
      )}
    </Column>
    {actionHref !== undefined && (
      <Column align="right" style={{ padding: "10px 0 10px 12px", verticalAlign: "top", whiteSpace: "nowrap" }}>
        <Link href={actionHref} style={{ color: palette.accent, fontSize: "14px", fontWeight: 600, textDecoration: "underline" }}>
          {actionLabel}
        </Link>
      </Column>
    )}
  </Row>
);
