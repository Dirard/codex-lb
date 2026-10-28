import { get, post } from "@/lib/api-client";

import { RuntimeCapacitySchema, RuntimeUpdateStatusSchema, RuntimeVersionSchema } from "@/features/runtime/schemas";

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

// currentPassword re-confirms the dashboard password; installations that
// authenticate administrators outside dashboard passwords omit it.
export function applyRuntimeUpdate(version: string, currentPassword?: string) {
  return post(`${RUNTIME_UPDATES_PATH}/apply`, RuntimeUpdateStatusSchema, {
    body: { version, currentPassword },
  });
}

export function rollbackRuntimeUpdate(version: string, currentPassword?: string) {
  return post(`${RUNTIME_UPDATES_PATH}/rollback`, RuntimeUpdateStatusSchema, {
    body: { version, currentPassword },
  });
}

const RUNTIME_CAPACITY_PATH = "/api/runtime/capacity";

export function getRuntimeCapacity() {
  return get(RUNTIME_CAPACITY_PATH, RuntimeCapacitySchema);
}
