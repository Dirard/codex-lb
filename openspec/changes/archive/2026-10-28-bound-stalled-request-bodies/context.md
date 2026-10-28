# Bounding stalled request bodies: context

## What the review found

- **Reservation without data.** `budget-request-body-memory` charged a declared Content-Length in full at the headers and held it until the 2-minute ReadTimeout. Four authenticated connections that sent `Content-Length: 33554432` and no body held a 1 GB host's 128 MiB budget (about 16 on the 4 GB production host); every body-carrying route then answered 503 until the timeout, and reconnecting kept it there. A client stuck mid-upload on a bad link did the same by accident.
- **503 instead of 413.** The charged stream ran one byte past the route maximum, so a gzip or chunked body larger than 32 MiB was refused as budget pressure (retryable 503) whenever the key's free share was at most 32 MiB — always on the 64 MiB minimum plan.

## How admission works now

- A declared length is checked, not reserved: above the route maximum → 413, larger than what the key may still charge → 503, both before reading. Every body is then charged as its bytes arrive (at most 64 KiB ahead of a blocked read), so headers without a body hold nothing and a client must actually send the bytes it is charged for.
- A body must keep arriving: each read that returns data moves the connection read deadline 10 s ahead, never past 2 minutes from the start of the body (`httpapi.RequestBodyTimeout`, also the server ReadTimeout). A stalled upload ends with 408 `request_body_timeout` and releases its charge. Once the body has ended the deadline is not moved again; the server clears it for its own background read, so long streamed responses are unaffected (covered by a route test whose response outlives the deadline).
- An idle-only deadline was not enough on its own: with full reservation, a client reconnecting every 10 s would still hold the budget. Charging on arrival removes the free reservation; the deadline bounds how long received bytes can be held by a client that stops.
- The charged stream stops at the route maximum; one more byte is read without charge and yields 413 (`audio_too_large` stays the answer for oversized transcription audio). An oversized body is therefore never counted as a budget refusal.
- Declared bodies are read with `io.ReadAll` (growing chunks, one exact final copy) instead of an exactly sized buffer allocated at the headers, because that allocation would be uncharged memory for a body that may never arrive.

## Corrections to the first change

- **The 1 GB example.** The budget is one eighth of the sizing memory, so 128 MiB holds only at exactly 1 GiB. A real 1 GB VPS reports 960–990 MiB, which gives 120–123 MiB: about 480–491 of the 512 concurrent 256 KiB contexts of the earlier capacity measurement fit at once, and the rest get 503 with Retry-After. The burst measurements used `--memory-limit 768MiB`, i.e. exactly 1 GiB of sizing memory.
- **Seamless updates.** The budget belongs to one process. If an update hands traffic from one worker to another while both run (the planned seamless-update overlap of up to 30 minutes), each worker has its own full budget, so the host's worst case for retained bodies doubles during the overlap. That needs a shared tally or a halved budget when the overlap mechanism lands; nothing changes now.

## Measurements after this change

Same synthetic harness and binaries as the first change (loopback Z.AI source holding each body 5 s, 4 keys, burst at once, peak = VmHWM): 1 GiB profile 200 × 4 MiB — 31 admitted, 169 × 503, 799 MiB (before the budget: 2624 MiB); 4 GiB profile 400 × 4 MiB — 128 admitted, 272 × 503, 3104 MiB (before: 5244 MiB); 512 × 256 KiB at exactly 1 GiB — all 512 admitted, 768 MiB.

## Not changed

- The WebSocket path already charged bytes as they arrived; a message's buffer can have up to 1.25× its length in capacity, which is part of the per-byte multiplier, not charged separately.
- Handlers built without `ConfigureBodyBudget` still get their own 1 GB-host budget; production wires one shared budget.
