import { get } from "@/lib/api-client";
import { KeyUsageReportSchema, ReportsResponseSchema } from "./schemas";

export type ReportsParams = {
  startDate?: string;
  endDate?: string;
  accountId?: string[];
  apiKeyId?: string[];
  model?: string;
  useragent?: string;
  timezone?: string;
};

export function getReports(params: ReportsParams = {}, signal?: AbortSignal) {
  const query = new URLSearchParams();
  if (params.startDate) query.set("start_date", params.startDate);
  if (params.endDate) query.set("end_date", params.endDate);
  if (params.model) query.set("model", params.model);
  if (params.useragent) query.set("useragent_group", params.useragent);
  if (params.timezone) query.set("timezone", params.timezone);
  if (params.accountId) {
    for (const id of params.accountId) {
      query.append("account_id", id);
    }
  }
  if (params.apiKeyId) {
    for (const id of params.apiKeyId) {
      query.append("api_key_id", id);
    }
  }
  const suffix = query.size > 0 ? `?${query.toString()}` : "";
  return get(`/api/reports${suffix}`, ReportsResponseSchema, { signal });
}

/** The parameters of GET /api/reports/keys and its CSV export. */
export type KeyUsageReportParams = {
  groupIds: string[];
  keyIds: string[];
  ungrouped: boolean;
  startDate: string;
  endDate: string;
  timezone?: string;
};

export function keyUsageReportSearch(params: KeyUsageReportParams): URLSearchParams {
  const query = new URLSearchParams();
  for (const id of params.groupIds) {
    query.append("group_id", id);
  }
  for (const id of params.keyIds) {
    query.append("api_key_id", id);
  }
  if (params.ungrouped) {
    query.set("ungrouped", "1");
  }
  query.set("start_date", params.startDate);
  query.set("end_date", params.endDate);
  if (params.timezone) {
    query.set("timezone", params.timezone);
  }
  return query;
}

export function getKeyUsageReport(params: KeyUsageReportParams, signal?: AbortSignal) {
  return get(`/api/reports/keys?${keyUsageReportSearch(params).toString()}`, KeyUsageReportSchema, { signal });
}

/** The CSV export of the same report: one row per local day and key with requests. */
export function keyUsageReportExportUrl(params: KeyUsageReportParams): string {
  return `/api/reports/keys/export.csv?${keyUsageReportSearch(params).toString()}`;
}
