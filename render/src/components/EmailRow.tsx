// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Column, Link, Row, Text } from "@react-email/components";

import { DARK_CLASS, palette } from "./palette.js";

export interface EmailRowProperties {
  actionHref?: string;
  actionLabel?: string;
  meta?: string;
  title: string;
}

export const EmailRow = ({ actionHref, actionLabel = "View", meta, title }: EmailRowProperties) => (
  <Row className={DARK_CLASS.border} style={{ borderBottom: `1px solid ${palette.border}` }}>
    <Column style={{ padding: "9px 0", verticalAlign: "top" }}>
      <Text className={DARK_CLASS.text} style={{ color: palette.text, fontSize: "14px", fontWeight: 700, lineHeight: "20px", margin: 0 }}>
        {title}
      </Text>
      {meta !== undefined && (
        <Text className={DARK_CLASS.muted} style={{ color: palette.muted, fontSize: "14px", lineHeight: "20px", margin: "2px 0 0" }}>{meta}</Text>
      )}
    </Column>
    {actionHref !== undefined && (
      <Column align="right" style={{ padding: "9px 0 9px 12px", verticalAlign: "top", whiteSpace: "nowrap" }}>
        <Link href={actionHref} style={{ color: palette.accent, fontSize: "14px", fontWeight: 700, textDecoration: "underline" }}>
          {actionLabel}
        </Link>
      </Column>
    )}
  </Row>
);
