export type OwnerLinkPeriod = {
  /** First local day, YYYY-MM-DD. */
  from: string;
  /** Last local day, YYYY-MM-DD. */
  to: string;
};

/**
 * Link to a key owner's page for a period (R29). It opens the key sign-in
 * page, which keeps `from` and `to` through sign-in; the link carries no
 * credential, so the owner still signs in with the key (Q4).
 */
export function ownerKeyPageLink(period: OwnerLinkPeriod, origin: string = window.location.origin): string {
  const params = new URLSearchParams({ from: period.from, to: period.to });
  return `${origin}/?${params.toString()}`;
}
