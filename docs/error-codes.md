# Error codes

Every coded gRPC error from the obligations service carries an `ErrorInfo` with the symbol as
its reason, the domain `obligations` and the code in `codeNum`. Only user-safe messages reach
the caller; every other code is sent as `Code N: Internal Error`.

| Code | Symbol | Area | Cause | User-safe |
| --- | --- | --- | --- | --- |
| 7000 | `INTERNAL` | obligations | an uncoded failure inside the obligations service | no |
| 7001 | `NOTIFY_STORE_UNAVAILABLE` | obligations store | an obligations Postgres read or write failed; the op metadata names it, the cause is only logged | no |
| 7002 | `WELCOME_SEND_UNAVAILABLE` | resend welcome | the welcome email could not be handed to the mail sender (a paused transport holds it instead and reports it sent); user_id names the recipient | no |
| 7003 | `ACK_AUTH_REQUIRED` | acknowledge | RecordAck or RecordView ran without a forwarded actor to attribute it to | yes |
| 7004 | `NOTIFY_PREF_MANDATORY_OFF` | notification preferences | a cadence write tried to turn off a mandatory category or type; category names it | yes |
| 7005 | `NOTIFY_PREF_INVALID` | notification preferences | a cadence write carried an unknown category, cadence or kind, or a digest window out of range; field names it | yes |
| 7006 | `ACK_TRANSFER_ACTOR_REQUIRED` | transfer acknowledgements | a real (not dry-run) transfer named no actor_user_id, so its audit event would name nobody | no |
