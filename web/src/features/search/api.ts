import { z } from "zod";

import { get } from "@/lib/api-client";

export const DirectoryKeySchema = z.object({
  id: z.string(),
  name: z.string(),
  prefix: z.string(),
  groupId: z.string().nullable(),
  status: z.enum(["active", "disabled", "expired"]),
});

export const DirectoryGroupSchema = z.object({
  id: z.string(),
  name: z.string(),
  accountCount: z.number().int(),
  keyCount: z.number().int(),
});

export const DirectoryAccountSchema = z.object({
  id: z.string(),
  label: z.string(),
  email: z.string(),
  status: z.string(),
  kind: z.enum(["chatgpt", "external"]),
});

export const DirectorySchema = z.object({
  keys: z.array(DirectoryKeySchema),
  groups: z.array(DirectoryGroupSchema),
  accounts: z.array(DirectoryAccountSchema),
});

export type Directory = z.infer<typeof DirectorySchema>;
export type DirectoryKey = z.infer<typeof DirectoryKeySchema>;
export type DirectoryGroup = z.infer<typeof DirectoryGroupSchema>;
export type DirectoryAccount = z.infer<typeof DirectoryAccountSchema>;

export const DIRECTORY_QUERY_KEY = ["directory"] as const;

export function getDirectory() {
  return get("/api/directory", DirectorySchema);
}
