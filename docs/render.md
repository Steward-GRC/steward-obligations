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
- `vars.productName` (default `Steward`) is shown in the header and used in
  product-branded subjects and copy.
- `vars.logoSrc` is an absolute image URL for the header. Without it no remote
  image is loaded.
- `vars.legalText` is the adopter's legal or postal footer line. It is empty by
  default.
- `vars.preferencesUrl` adds a "Manage email preferences" link to the footer.
  `vars.unsubscribeHref` adds an unsubscribe line (the digest uses it).
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
| `mfa-setup` | Set up multi-factor authentication |
| `digest` | Your Steward digest |

"Steward" in a subject follows `vars.productName`.

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
