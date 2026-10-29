import type { Directory } from "@/features/search/api";

export const SEARCH_RESULT_LIMIT = 20;

export type SearchResult =
  | { kind: "key"; id: string; title: string; detail: string; to: string }
  | { kind: "group"; id: string; title: string; detail: string; to: string }
  | { kind: "account"; id: string; title: string; detail: string; to: string; isEmail: boolean }
  | { kind: "request"; id: string; title: string; detail: string; to: string };

const REQUEST_ID_PATTERN = /^(req|resp)_[A-Za-z0-9-]{8,}$|^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function looksLikeRequestId(query: string): boolean {
  return REQUEST_ID_PATTERN.test(query.trim());
}

/** Routes of search results until the dedicated Keys and Groups pages exist. */
export const searchRoutes = {
  key: (id: string) => `/keys?selected=${encodeURIComponent(id)}`,
  group: (id: string) => `/groups?selected=${encodeURIComponent(id)}`,
  account: (id: string) => `/accounts?selected=${encodeURIComponent(id)}`,
  request: (id: string) => `/requests?request_id=${encodeURIComponent(id)}`,
};

/**
 * Case-insensitive matches of keys by name and prefix, groups by name and
 * accounts by alias, e-mail and id, prefix matches first, at most 20 in all,
 * in the order request, keys, groups, accounts.
 */
export function searchDirectory(directory: Directory | undefined, rawQuery: string): SearchResult[] {
  const query = rawQuery.trim().toLowerCase();
  if (!query) {
    return [];
  }
  const results: SearchResult[] = [];
  if (looksLikeRequestId(rawQuery)) {
    const id = rawQuery.trim();
    results.push({ kind: "request", id, title: id, detail: "", to: searchRoutes.request(id) });
  }
  if (!directory) {
    return results;
  }
  const rank = (...values: string[]) => {
    let best = Number.POSITIVE_INFINITY;
    for (const value of values) {
      const index = value.toLowerCase().indexOf(query);
      if (index === 0) {
        return 0;
      }
      if (index > 0) {
        best = Math.min(best, 1);
      }
    }
    return best;
  };
  const ranked = <T,>(items: T[], score: (item: T) => number, title: (item: T) => string) =>
    items
      .map((item) => ({ item, score: score(item) }))
      .filter((entry) => Number.isFinite(entry.score))
      .sort((a, b) => a.score - b.score || title(a.item).localeCompare(title(b.item)))
      .map((entry) => entry.item);

  for (const key of ranked(directory.keys, (k) => rank(k.name, k.prefix), (k) => k.name)) {
    results.push({ kind: "key", id: key.id, title: key.name, detail: key.prefix, to: searchRoutes.key(key.id) });
  }
  for (const group of ranked(directory.groups, (g) => rank(g.name), (g) => g.name)) {
    results.push({ kind: "group", id: group.id, title: group.name, detail: "", to: searchRoutes.group(group.id) });
  }
  for (const account of ranked(directory.accounts, (a) => rank(a.label, a.email, a.id), (a) => a.label)) {
    results.push({
      kind: "account",
      id: account.id,
      title: account.label,
      detail: account.label === account.email ? "" : account.email,
      to: searchRoutes.account(account.id),
      isEmail: account.label === account.email,
    });
  }
  return results.slice(0, SEARCH_RESULT_LIMIT);
}
