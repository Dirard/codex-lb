import { z } from "zod";

import { LimitRuleCreateSchema } from "@/features/api-keys/schemas";

export const AccountGroupSchema = z.object({
  id: z.string(),
  name: z.string().min(1).max(128),
  accountIds: z.array(z.string()),
  limits: z.array(LimitRuleCreateSchema),
  keyCount: z.number().int().nonnegative(),
});

export const AccountGroupListSchema = z.array(AccountGroupSchema);

export const AccountGroupCreateRequestSchema = z.object({
  name: z.string().min(1).max(128),
  accountIds: z.array(z.string()),
  limits: z.array(LimitRuleCreateSchema),
});

export const AccountGroupUpdateRequestSchema = AccountGroupCreateRequestSchema;

const AccountGroupIDsSchema = z.array(z.string().min(1)).refine(
  (groupIds) => new Set(groupIds).size === groupIds.length,
  { message: "Group IDs must be unique" },
);

export const AccountGroupMembershipUpdateRequestSchema = z.object({
  groupIds: AccountGroupIDsSchema,
});

export const AccountGroupMembershipUpdateResponseSchema = z.object({
  accountId: z.string(),
  groupIds: z.array(z.string()),
});

export type AccountGroup = z.infer<typeof AccountGroupSchema>;
export type AccountGroupCreateRequest = z.infer<typeof AccountGroupCreateRequestSchema>;
export type AccountGroupUpdateRequest = z.infer<typeof AccountGroupUpdateRequestSchema>;
export type AccountGroupMembershipUpdateRequest = z.infer<typeof AccountGroupMembershipUpdateRequestSchema>;
