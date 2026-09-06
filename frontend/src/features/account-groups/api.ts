import { del, get, post, put } from "@/lib/api-client";

import {
  AccountGroupCreateRequestSchema,
  AccountGroupListSchema,
  AccountGroupSchema,
  AccountGroupUpdateRequestSchema,
} from "@/features/account-groups/schemas";

export const ACCOUNT_GROUPS_BASE_PATH = "/api/account-groups";

export function listAccountGroups() {
  return get(`${ACCOUNT_GROUPS_BASE_PATH}/`, AccountGroupListSchema);
}

export function createAccountGroup(payload: unknown) {
  const validated = AccountGroupCreateRequestSchema.parse(payload);
  return post(`${ACCOUNT_GROUPS_BASE_PATH}/`, AccountGroupSchema, {
    body: validated,
  });
}

export function updateAccountGroup(groupId: string, payload: unknown) {
  const validated = AccountGroupUpdateRequestSchema.parse(payload);
  return put(`${ACCOUNT_GROUPS_BASE_PATH}/${encodeURIComponent(groupId)}`, AccountGroupSchema, {
    body: validated,
  });
}

export function deleteAccountGroup(groupId: string) {
  return del(`${ACCOUNT_GROUPS_BASE_PATH}/${encodeURIComponent(groupId)}`);
}
