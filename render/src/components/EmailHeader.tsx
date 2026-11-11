// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import { Column, Img, Row, Section, Text } from "@react-email/components";

import { DARK_CLASS, fontStack, headingFontStack, palette, RADIUS } from "./palette.js";

export interface EmailHeaderProperties {
  // The adopter's own logo, shown in the slot on the right.
  logoSrc?: string;
  // The Steward mark PNG for the paper tile; without it there is no tile.
  markSrc?: string;
  organisationName?: string;
  productName?: string;
}

const present = (value: string | undefined): value is string => value !== undefined && value.length > 0;

// A 4 px plum rule, then the lockup: the mark on a 28 px paper tile, the
// wordmark in Georgia, and the organisation slot on the right.
export const EmailHeader = ({ logoSrc, markSrc, organisationName, productName = "Steward" }: EmailHeaderProperties) => (
  <>
    <Section data-email-rule="" style={{ backgroundColor: palette.accent, fontSize: "0", height: "4px", lineHeight: "0" }}>
      <Row>
        <Column style={{ fontSize: "0", height: "4px", lineHeight: "0" }}>&nbsp;</Column>
      </Row>
    </Section>
    <Section
      className={DARK_CLASS.border}
      data-email-lockup=""
      style={{ borderBottom: `1px solid ${palette.border}`, padding: "16px 24px" }}
    >
      <Row>
        {present(markSrc) && (
          <Column style={{ verticalAlign: "middle", width: "28px" }}>
            <div
              data-email-tile=""
              style={{
                backgroundColor: "#F8F4EE",
                borderRadius: RADIUS,
                height: "28px",
                lineHeight: "28px",
                textAlign: "center",
                width: "28px",
              }}
            >
              <Img
                alt=""
                height="20"
                src={markSrc}
                style={{ display: "inline-block", margin: "4px", verticalAlign: "top" }}
                width="20"
              />
            </div>
          </Column>
        )}
        <Column style={{ paddingLeft: present(markSrc) ? "10px" : "0", verticalAlign: "middle" }}>
          <Text
            className={DARK_CLASS.text}
            data-email-wordmark=""
            style={{
              color: palette.text,
              fontFamily: headingFontStack,
              fontSize: "18px",
              fontWeight: 600,
              lineHeight: "28px",
              margin: 0,
            }}
          >
            {productName}
          </Text>
        </Column>
        {(present(organisationName) || present(logoSrc)) && (
          <Column
            align="right"
            data-email-org=""
            style={{ textAlign: "right", verticalAlign: "middle" }}
          >
            {present(logoSrc) && (
              <Img
                alt={present(organisationName) ? organisationName : ""}
                height="24"
                src={logoSrc}
                style={{ display: "inline-block", margin: "0 8px 0 0", verticalAlign: "middle" }}
              />
            )}
            {present(organisationName) && (
              <span
                className={DARK_CLASS.muted}
                style={{ color: palette.muted, fontFamily: fontStack, fontSize: "13px", verticalAlign: "middle" }}
              >
                {organisationName}
              </span>
            )}
          </Column>
        )}
      </Row>
    </Section>
  </>
);
