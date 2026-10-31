import { z } from "zod";

import { del, get, patch, post, put } from "@/lib/api-client";

import {
  VideoAccountsResponseSchema,
  VideoImportedAccountSchema,
  VideoJobsResponseSchema,
  VideoKeysResponseSchema,
  VideoSummarySchema,
} from "@/features/video/schemas";

const VIDEO_PATH = "/api/video";
const NoContentSchema = z.unknown();

export function listVideoAccounts() {
  return get(`${VIDEO_PATH}/accounts`, VideoAccountsResponseSchema);
}

export function importVideoAccount(file: File) {
  const formData = new FormData();
  formData.append("auth_json", file);
  return post(`${VIDEO_PATH}/accounts/import`, VideoImportedAccountSchema, { body: formData });
}

export function updateVideoAccount(id: string, body: { label?: string; status?: "active" | "paused" }) {
  return patch(`${VIDEO_PATH}/accounts/${encodeURIComponent(id)}`, NoContentSchema, { body });
}

export function deleteVideoAccount(id: string) {
  return del(`${VIDEO_PATH}/accounts/${encodeURIComponent(id)}`);
}

export function listVideoKeys() {
  return get(`${VIDEO_PATH}/keys`, VideoKeysResponseSchema);
}

export function allowVideoKey(id: string) {
  return put(`${VIDEO_PATH}/keys/${encodeURIComponent(id)}`, NoContentSchema);
}

export function denyVideoKey(id: string) {
  return del(`${VIDEO_PATH}/keys/${encodeURIComponent(id)}`);
}

export function listVideoJobs(cursor?: string | null) {
  const query = cursor ? `?limit=50&cursor=${encodeURIComponent(cursor)}` : "?limit=50";
  return get(`${VIDEO_PATH}/jobs${query}`, VideoJobsResponseSchema);
}

export function getVideoSummary() {
  return get(`${VIDEO_PATH}/summary`, VideoSummarySchema);
}
