import { z } from "zod";

import { del, get, post, put } from "@/lib/api-client";

import {
  AccountGroupCreateRequestSchema,
  AccountGroupListSchema,
  AccountGroupMembershipUpdateRequestSchema,
  AccountGroupMembershipUpdateResponseSchema,
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

export function resetAccountGroupUsage(groupId: string) {
  return post(`${ACCOUNT_GROUPS_BASE_PATH}/${encodeURIComponent(groupId)}/reset-usage`, z.void());
}

export function updateAccountGroups(accountId: string, payload: unknown) {
  const validated = AccountGroupMembershipUpdateRequestSchema.parse(payload);
  return put(`/api/accounts/${encodeURIComponent(accountId)}/groups`, AccountGroupMembershipUpdateResponseSchema, {
    body: validated,
  });
}
