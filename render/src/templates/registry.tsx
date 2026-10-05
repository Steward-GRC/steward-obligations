// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

import type { ComponentType } from "react";

import { AccessGranted, sampleVars as accessGrantedSampleVars } from "./accessGranted.js";
import { AckRequired, sampleVars as ackRequiredSampleVars } from "./ackRequired.js";
import { AssignedAsOwner, sampleVars as assignedAsOwnerSampleVars } from "./assignedAsOwner.js";
import { BreakGlassAlert, sampleVars as breakGlassAlertSampleVars } from "./breakGlassAlert.js";
import { Digest, sampleVars as digestSampleVars } from "./digest.js";
import {
  DomainVerificationInstructions,
  sampleVars as domainVerificationInstructionsSampleVars,
} from "./domainVerificationInstructions.js";
import { DomainVerified, sampleVars as domainVerifiedSampleVars } from "./domainVerified.js";
import { EmailVerification, sampleVars as emailVerificationSampleVars } from "./emailVerification.js";
import { IdpTestFailed, sampleVars as idpTestFailedSampleVars } from "./idpTestFailed.js";
import { DEFAULT_PRODUCT_NAME } from "./layout.js";
import { MfaSetup, sampleVars as mfaSetupSampleVars } from "./mfaSetup.js";
import { Otp, sampleVars as otpSampleVars } from "./otp.js";
import { PolicyAckReminder, sampleVars as policyAckReminderSampleVars } from "./policyAckReminder.js";
import { PolicyEscalation, sampleVars as policyEscalationSampleVars } from "./policyEscalation.js";
import { PolicyPublished, sampleVars as policyPublishedSampleVars } from "./policyPublished.js";
import { PolicyRetired, sampleVars as policyRetiredSampleVars } from "./policyRetired.js";
import {
  RaciPermissionGranted,
  sampleVars as raciPermissionGrantedSampleVars,
} from "./raciPermissionGranted.js";
import { RecoveryCode, sampleVars as recoveryCodeSampleVars } from "./recoveryCode.js";
import { SpCertRotated, sampleVars as spCertRotatedSampleVars } from "./spCertRotated.js";
import { SsoAccountWelcome, sampleVars as ssoAccountWelcomeSampleVars } from "./ssoAccountWelcome.js";
import { SsoActivated, sampleVars as ssoActivatedSampleVars } from "./ssoActivated.js";
import { SsoDisabled, sampleVars as ssoDisabledSampleVars } from "./ssoDisabled.js";
import { WelcomeAccount, sampleVars as welcomeAccountSampleVars } from "./welcomeAccount.js";
import {
  WorkflowAwaitingApproval,
  sampleVars as workflowAwaitingApprovalSampleVars,
} from "./workflowAwaitingApproval.js";
import { WorkflowDenied, sampleVars as workflowDeniedSampleVars } from "./workflowDenied.js";
import { WorkflowStarted, sampleVars as workflowStartedSampleVars } from "./workflowStarted.js";

// The kind strings are the wire contract with the Go sender; renaming one
// breaks delivery for that notification.
export const EMAIL_KINDS = [
  "policy-ack-reminder",
  "policy-published",
  "policy-retired",
  "ack-required",
  "policy-escalation",
  "welcome-account",
  "email-verification",
  "otp",
  "kratos-recovery",
  "assigned-as-owner",
  "raci-permission-granted",
  "workflow-started",
  "workflow-awaiting-approval",
  "workflow-denied",
  "domain-verification-instructions",
  "domain-verified",
  "idp-test-failed",
  "sso-activated",
  "sp-cert-rotated",
  "sso-disabled",
  "access-granted",
  "sso-account-welcome",
  "break-glass-alert",
  "mfa-setup",
  "digest",
] as const;

export type EmailKind = (typeof EMAIL_KINDS)[number];

export const isEmailKind = (kind: unknown): kind is EmailKind =>
  typeof kind === "string" && (EMAIL_KINDS as readonly string[]).includes(kind);

export const EMAIL_COMPONENTS: Record<EmailKind, ComponentType<any>> = {
  "access-granted": AccessGranted,
  "ack-required": AckRequired,
  "assigned-as-owner": AssignedAsOwner,
  "break-glass-alert": BreakGlassAlert,
  digest: Digest,
  "domain-verification-instructions": DomainVerificationInstructions,
  "domain-verified": DomainVerified,
  "email-verification": EmailVerification,
  "idp-test-failed": IdpTestFailed,
  "kratos-recovery": RecoveryCode,
  "mfa-setup": MfaSetup,
  otp: Otp,
  "policy-ack-reminder": PolicyAckReminder,
  "policy-escalation": PolicyEscalation,
  "policy-published": PolicyPublished,
  "policy-retired": PolicyRetired,
  "raci-permission-granted": RaciPermissionGranted,
  "sp-cert-rotated": SpCertRotated,
  "sso-account-welcome": SsoAccountWelcome,
  "sso-activated": SsoActivated,
  "sso-disabled": SsoDisabled,
  "welcome-account": WelcomeAccount,
  "workflow-awaiting-approval": WorkflowAwaitingApproval,
  "workflow-denied": WorkflowDenied,
  "workflow-started": WorkflowStarted,
};

export const EMAIL_SUBJECTS: Record<EmailKind, (productName: string) => string> = {
  "access-granted": () => "Your access has been granted",
  "ack-required": () => "Acknowledgement required",
  "assigned-as-owner": () => "You've been assigned as category owner",
  "break-glass-alert": () => "Security alert: break-glass sign-in used",
  digest: (product) => `Your ${product} digest`,
  "domain-verification-instructions": () => "Verify your domain to enable SSO",
  "domain-verified": () => "Your domain has been verified",
  "email-verification": () => "Confirm your email address",
  "idp-test-failed": () => "Your SSO connection test failed",
  "kratos-recovery": (product) => `Reset your ${product} password`,
  "mfa-setup": () => "Set up multi-factor authentication",
  otp: () => "Your verification code",
  "policy-ack-reminder": () => "Policy acknowledgements due",
  "policy-escalation": () => "Overdue: policy acknowledgement required",
  "policy-published": () => "New policy published",
  "policy-retired": () => "Policy retired",
  "raci-permission-granted": () => "You've been granted a new permission",
  "sp-cert-rotated": () => "Your SSO signing certificate was rotated",
  "sso-account-welcome": (product) => `Your ${product} account is ready`,
  "sso-activated": () => "Single sign-on is now active",
  "sso-disabled": () => "Single sign-on has been disabled",
  "welcome-account": (product) => `Your ${product} account is ready`,
  "workflow-awaiting-approval": () => "Your approval is needed",
  "workflow-denied": () => "Your submission was returned",
  "workflow-started": () => "A workflow has started",
};

export const defaultSubject = (kind: EmailKind, productName: string = DEFAULT_PRODUCT_NAME): string =>
  EMAIL_SUBJECTS[kind](productName.length > 0 ? productName : DEFAULT_PRODUCT_NAME);

const asVars = (vars: object): Record<string, unknown> => vars as Record<string, unknown>;

export const EMAIL_SAMPLE_VARS: Record<EmailKind, Record<string, unknown>> = {
  "access-granted": asVars(accessGrantedSampleVars),
  "ack-required": asVars(ackRequiredSampleVars),
  "assigned-as-owner": asVars(assignedAsOwnerSampleVars),
  "break-glass-alert": asVars(breakGlassAlertSampleVars),
  digest: asVars(digestSampleVars),
  "domain-verification-instructions": asVars(domainVerificationInstructionsSampleVars),
  "domain-verified": asVars(domainVerifiedSampleVars),
  "email-verification": asVars(emailVerificationSampleVars),
  "idp-test-failed": asVars(idpTestFailedSampleVars),
  "kratos-recovery": asVars(recoveryCodeSampleVars),
  "mfa-setup": asVars(mfaSetupSampleVars),
  otp: asVars(otpSampleVars),
  "policy-ack-reminder": asVars(policyAckReminderSampleVars),
  "policy-escalation": asVars(policyEscalationSampleVars),
  "policy-published": asVars(policyPublishedSampleVars),
  "policy-retired": asVars(policyRetiredSampleVars),
  "raci-permission-granted": asVars(raciPermissionGrantedSampleVars),
  "sp-cert-rotated": asVars(spCertRotatedSampleVars),
  "sso-account-welcome": asVars(ssoAccountWelcomeSampleVars),
  "sso-activated": asVars(ssoActivatedSampleVars),
  "sso-disabled": asVars(ssoDisabledSampleVars),
  "welcome-account": asVars(welcomeAccountSampleVars),
  "workflow-awaiting-approval": asVars(workflowAwaitingApprovalSampleVars),
  "workflow-denied": asVars(workflowDeniedSampleVars),
  "workflow-started": asVars(workflowStartedSampleVars),
};
