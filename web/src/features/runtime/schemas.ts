import { z } from "zod";

export const RuntimeVersionSchema = z.object({
  currentVersion: z.string(),
  latestVersion: z.string().nullable().optional(),
  updateAvailable: z.boolean(),
  checkedAt: z.string().nullable(),
  source: z.string().nullable().optional(),
  releaseUrl: z.url(),
});

export type RuntimeVersion = z.infer<typeof RuntimeVersionSchema>;

export const RuntimeUpdateStatusSchema = z.object({
  currentVersion: z.string(),
  latestVersion: z.string().optional(),
  updateAvailable: z.boolean(),
  checkedAt: z.string().nullable(),
  source: z.string(),
  releaseUrl: z.url(),
  supported: z.boolean(),
  unavailableReason: z.string().optional(),
  previousVersion: z.string().optional(),
  canRollback: z.boolean(),
  phase: z.string(),
  targetVersion: z.string().optional(),
  lastError: z.string().optional(),
});

export type RuntimeUpdateStatus = z.infer<typeof RuntimeUpdateStatusSchema>;

export const RuntimeCapacitySchema = z.object({
  host: z.object({
    cpus: z.number().int(),
    memoryBytes: z.number(),
    memorySource: z.string(),
    fileDescriptors: z.number(),
  }),
  sizingMemoryBytes: z.number(),
  sizingMemorySource: z.string(),
  limits: z.object({
    goMemoryLimitBytes: z.number(),
    maxGenerations: z.number().int(),
    maxQueued: z.number().int(),
    bodyReaders: z.number().int(),
    clientWebSockets: z.number().int(),
    upstreamWebSockets: z.number().int(),
    upstreamConnectionsPerHost: z.number().int(),
    sqliteReaders: z.number().int(),
  }),
  overridden: z.array(z.string()),
  usage: z.object({
    activeGenerations: z.number().int(),
    queuedGenerations: z.number().int(),
    clientWebSockets: z.number().int(),
    goHeapBytes: z.number(),
    goTotalBytes: z.number(),
    residentBytes: z.number(),
    goroutines: z.number().int(),
  }),
});

export type RuntimeCapacity = z.infer<typeof RuntimeCapacitySchema>;
