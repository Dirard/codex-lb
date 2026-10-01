# Codex control route aliases

Original `app/modules/proxy/api.py` registers both methods for goal/get. The Go `validateControlRequest` already allows both, so only router registration is missing. Use explicit slash-equivalent patterns and trim that final slash before the existing allowlisted control path is forwarded. Realtime creation already fixes its provider path and only needs route registration. Test the actual registered handlers for each control method/path, body/query retention and unauthenticated rejection; no separate application policy or fallback is needed.

This is a local router repair, so the full layered specification lifecycle is unnecessary. Existing authorization and Realtime privacy tests remain authoritative for their unchanged use cases.

2026-09-28: Focused race tests passed every retained control method, its slash equivalent, unchanged provider body/query and unauthenticated rejection, plus Realtime call contracts. The unsupported DELETE goal-read method remained rejected before upstream dispatch.
