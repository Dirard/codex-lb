import type { RequestPool } from "@/features/api-keys/schemas";

export const DEFAULT_REQUEST_POOL_WAIT_SECONDS = 30;

/** Text fields of the request pool editor; parsed on submit. */
export type RequestPoolDraft = {
  enabled: boolean;
  requestsPerMinute: string;
  maxWaitSeconds: string;
};

export function requestPoolDraft(pool: RequestPool | null): RequestPoolDraft {
  return {
    enabled: pool !== null,
    requestsPerMinute: pool ? String(pool.requestsPerMinute) : "",
    maxWaitSeconds: String(pool?.maxWaitSeconds ?? DEFAULT_REQUEST_POOL_WAIT_SECONDS),
  };
}

function wholeNumber(raw: string): number | null {
  return /^\d+$/.test(raw.trim()) ? Number(raw.trim()) : null;
}

/**
 * The pool the draft describes: null when disabled, undefined when a field is
 * out of range (1-10000 requests per minute, 0-60 seconds of waiting).
 */
export function parseRequestPool(draft: RequestPoolDraft): RequestPool | null | undefined {
  if (!draft.enabled) return null;
  const requestsPerMinute = wholeNumber(draft.requestsPerMinute);
  const maxWaitSeconds = wholeNumber(draft.maxWaitSeconds);
  if (requestsPerMinute === null || requestsPerMinute < 1 || requestsPerMinute > 10000) return undefined;
  if (maxWaitSeconds === null || maxWaitSeconds > 60) return undefined;
  return { requestsPerMinute, maxWaitSeconds };
}

export function sameRequestPool(a: RequestPool | null, b: RequestPool | null): boolean {
  return a === b || (a !== null && b !== null && a.requestsPerMinute === b.requestsPerMinute && a.maxWaitSeconds === b.maxWaitSeconds);
}
