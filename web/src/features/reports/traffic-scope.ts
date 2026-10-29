/** The most keys whose traffic details are requested by id. */
export const TRAFFIC_DETAILS_KEY_LIMIT = 50;

/**
 * What the traffic details may request: every key unfiltered, the resolved
 * keys by id when there are at most 50, or nothing (wait for the report,
 * no keys, or too many keys).
 */
export type TrafficScope =
  | { kind: "all" }
  | { kind: "keys"; keyIds: string[] }
  | { kind: "pending" }
  | { kind: "none" }
  | { kind: "too_many"; count: number };

export function trafficScope(scopeEmpty: boolean, resolvedKeyIds: string[] | null): TrafficScope {
  if (scopeEmpty) {
    return { kind: "all" };
  }
  if (resolvedKeyIds === null) {
    return { kind: "pending" };
  }
  if (resolvedKeyIds.length === 0) {
    return { kind: "none" };
  }
  if (resolvedKeyIds.length > TRAFFIC_DETAILS_KEY_LIMIT) {
    return { kind: "too_many", count: resolvedKeyIds.length };
  }
  return { kind: "keys", keyIds: resolvedKeyIds };
}
