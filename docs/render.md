# Render sidecar

`render/` is a small Node.js HTTP service that turns an email kind and its
variables into a subject line and HTML body. The Go sender calls it over
loopback; it holds no state and talks to nothing else. Templates are React
Email components built on the neutral primitives in `render/src/components/`.

## Contract

`POST /render` with a JSON body:

```json
{ "kind": "policy-ack-reminder", "vars": { "recipientName": "Alice", "...": "..." } }
```

returns `200` with `{ "subject": "...", "html": "<!DOCTYPE ..." }`.

- `vars.subject`, when set, replaces the kind's default subject.
- `vars.productName` (default `Steward`) is the Georgia wordmark in the header
  and is used in product-branded subjects and copy.
- `vars.organisationName` is shown on the right of the header. Without it the
  slot is left out.
- `vars.logoSrc` is the adopter's own logo, an absolute image URL shown beside
  the organisation name. Without it no remote image is loaded.
- `vars.markSrc` is an absolute URL of the Steward mark PNG for the 28 px paper
  tile in the header (`render/assets/email/steward-email-mark-2x.png`). Without
  it there is no tile, only the wordmark, and no image is loaded.
- `vars.legalText` is the organisation's postal address, the first line of the
  footer. It is empty by default.
- `vars.preferencesUrl` adds a "Manage email preferences" link to the footer.
  `vars.unsubscribeHref` adds "Unsubscribe from these emails" (the digest uses
  it). Mail with neither says it is a security email and can't be unsubscribed
  from. Security mail (sign-in codes, account and break-glass notices) never
  shows the preferences or unsubscribe links, even when the sender stamps them.
  Required compliance mail keeps the preferences link and says it can't be
  turned off.
- `vars.reviewUrl` on `break-glass-alert` adds the red "Review this immediately"
  button.
- An unknown kind, malformed JSON or a template that fails to render returns
  `400` with `{ "error": "..." }`.

Unknown paths return `404`; a known path with the wrong method returns `405`
with an `Allow` header.

## Kinds

| Kind | Default subject |
| --- | --- |
| `policy-ack-reminder` | Policy acknowledgements due |
| `policy-published` | New policy published |
| `policy-retired` | Policy retired |
| `ack-required` | Acknowledgement required |
| `policy-escalation` | Overdue: policy acknowledgement required |
| `welcome-account` | Your Steward account is ready |
| `email-verification` | Confirm your email address |
| `otp` | Your verification code |
| `kratos-recovery` | Reset your Steward password |
| `assigned-as-owner` | You've been assigned as category owner |
| `raci-permission-granted` | You've been granted a new permission |
| `review-due` | Policy review due |
| `workflow-started` | A workflow has started |
| `workflow-awaiting-approval` | Your approval is needed |
| `workflow-denied` | Your submission was returned |
| `domain-verification-instructions` | Verify your domain to enable SSO |
| `domain-verified` | Your domain has been verified |
| `idp-test-failed` | Your SSO connection test failed |
| `sso-activated` | Single sign-on is now active |
| `sp-cert-rotated` | Your SSO signing certificate was rotated |
| `sso-disabled` | Single sign-on has been disabled |
| `access-granted` | Your access has been granted |
| `sso-account-welcome` | Your Steward account is ready |
| `break-glass-alert` | Security alert: break-glass sign-in used |
| `break-glass-read-alert` | Security alert: a document was read under break-glass |
| `mfa-setup` | Set up multi-factor authentication |
| `digest` | Your Steward digest |

"Steward" in a subject follows `vars.productName`.

## Design

The layout follows the stored email design (the "email" tokens in the plan
repo's `design/tokens.json` and the frames in "Steward Emails"): a 600 px table
that is fluid on phones, a 4 px plum rule, the header lockup, the body with one
solid plum button, and a paper footer. Colours and type are in
`render/src/components/palette.ts`: Arial 16/24 body, a Georgia wordmark, a
22/28 bold title, radius 6. The dark client is a `prefers-color-scheme: dark`
block using the stored dark tokens; buttons stay solid plum with white text so a
client's forced inversion can't wash them out.

`render/assets/email/` holds `steward-email-header.svg` and
`steward-email-header-2x.png` from the design, and `steward-email-mark-2x.png`,
the mark cropped from that PNG. The sidecar does not serve them: host the mark
at a public URL and set `EMAIL_MARK_URL`.

## Health

`GET /livez` and `GET /readyz` return `200` with `{"status":"ok"}`. Neither has
dependencies. Every response carries `Steward-Version` and `Steward-Commit`
headers.

## Environment

| Variable | Default | Meaning |
| --- | --- | --- |
| `HOST` | `127.0.0.1` | Listen address |
| `PORT` | `8091` | Listen port |
| `VERSION` | `dev` | Value of the `Steward-Version` header |
| `COMMIT` | `unknown` | Value of the `Steward-Commit` header |

## Run and test

```sh
cd render
npm ci --ignore-scripts
npm run typecheck
npm test
npm run build
npm start
```

`npm run dev` runs the server from source with `tsx`. The image builds with
`docker build --build-arg VERSION=v0.1.0 --build-arg COMMIT=$(git rev-parse --short HEAD) render/`.
