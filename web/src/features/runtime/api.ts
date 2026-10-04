import { get, post } from "@/lib/api-client";

import { RuntimeUpdateStatusSchema, RuntimeVersionSchema } from "@/features/runtime/schemas";

const RUNTIME_VERSION_PATH = "/api/runtime/version";

export function getRuntimeVersion() {
  return get(RUNTIME_VERSION_PATH, RuntimeVersionSchema);
}

const RUNTIME_UPDATES_PATH = "/api/runtime/updates";

export function getRuntimeUpdateStatus() {
  return get(RUNTIME_UPDATES_PATH, RuntimeUpdateStatusSchema);
}

export function checkRuntimeUpdates() {
  return post(`${RUNTIME_UPDATES_PATH}/check`, RuntimeUpdateStatusSchema);
}

export function applyRuntimeUpdate(version: string) {
  return post(`${RUNTIME_UPDATES_PATH}/apply`, RuntimeUpdateStatusSchema, {
    body: { version },
  });
}

export function rollbackRuntimeUpdate(version: string) {
  return post(`${RUNTIME_UPDATES_PATH}/rollback`, RuntimeUpdateStatusSchema, {
    body: { version },
  });
}
