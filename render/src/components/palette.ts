// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// The stored email design: the "email" tokens in the plan repo's
// design/tokens.json, light client, plus the dark client from the frames in
// "Steward Emails". Buttons are solid plum with white text in both clients so
// they survive a client's forced inversion.
export const palette = {
  accent: "#5D2A5B",
  accentText: "#FFFFFF",
  border: "#E3D9CE",
  canvas: "#F8F4EE",
  error: "#A3342C",
  footer: "#F8F4EE",
  muted: "#5E525F",
  surface: "#FFFFFF",
  text: "#261B25",
  tint: "#F8F4EE",
  warn: "#875400",
} as const;

export const darkPalette = {
  canvas: "#120C14",
  footer: "#171019",
  border: "#3A2D3D",
  muted: "#C9B8C7",
  surface: "#1E1620",
  text: "#F3ECF2",
  tint: "#171019",
} as const;

export const fontStack = "Arial, Helvetica, sans-serif";
export const headingFontStack = "Georgia, serif";

export const RADIUS = "6px";
export const INSET = "24px";
export const MAX_WIDTH = "600px";

// Class hooks for the dark client. Every element that paints one of the
// tokens above carries the matching class so the media query can swap it.
export const DARK_CLASS = {
  border: "em-line",
  canvas: "em-page",
  footer: "em-foot",
  muted: "em-muted",
  surface: "em-card",
  text: "em-ink",
  tint: "em-tint",
} as const;

export const darkModeCss = `@media (prefers-color-scheme: dark) {
  .${DARK_CLASS.canvas} { background-color: ${darkPalette.canvas} !important; }
  .${DARK_CLASS.surface} { background-color: ${darkPalette.surface} !important; }
  .${DARK_CLASS.footer} { background-color: ${darkPalette.footer} !important; }
  .${DARK_CLASS.tint} { background-color: ${darkPalette.tint} !important; }
  .${DARK_CLASS.text} { color: ${darkPalette.text} !important; }
  .${DARK_CLASS.muted} { color: ${darkPalette.muted} !important; }
  .${DARK_CLASS.border} { border-color: ${darkPalette.border} !important; }
}`;
