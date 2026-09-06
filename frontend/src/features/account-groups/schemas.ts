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

export type AccountGroup = z.infer<typeof AccountGroupSchema>;
export type AccountGroupCreateRequest = z.infer<typeof AccountGroupCreateRequestSchema>;
export type AccountGroupUpdateRequest = z.infer<typeof AccountGroupUpdateRequestSchema>;
