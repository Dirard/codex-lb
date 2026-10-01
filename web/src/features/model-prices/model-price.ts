import { z } from "zod";

const RateSchema = z.object({
  inputMicrodollarsPerMillion: z.number().int().min(0).max(1e12),
  cachedMicrodollarsPerMillion: z.number().int().min(0).max(1e12),
  outputMicrodollarsPerMillion: z.number().int().min(0).max(1e12),
});
export const ModelPriceSchema = z.object({
  model: z.string(), source: z.enum(["builtin", "custom"]), hasBuiltin: z.boolean(),
  price: z.object({
    standard: RateSchema, priority: RateSchema.optional(), flex: RateSchema.optional(), longContext: RateSchema.optional(),
    longContextThreshold: z.number().int().positive().optional(),
    priorityMultiplierMilli: z.number().int().min(0).max(100000).optional(),
  }),
});
export const ModelPricesSchema = z.object({ prices: z.array(ModelPriceSchema) });
export const ModelRepriceSummarySchema = z.object({
  applied: z.boolean(),
  recomputedRequests: z.number().int().nonnegative(),
  costDeltaMicrodollars: z.number().int(),
  partialRawRequests: z.number().int().nonnegative(),
  partialFoldedRequests: z.number().int().nonnegative(),
  partialFoldedBuckets: z.number().int().nonnegative(),
  undimensionedHistory: z.boolean(),
});
export const ModelPriceSaveSchema = ModelPriceSchema.extend({ reprice: ModelRepriceSummarySchema });
export const ModelPriceDeleteSchema = z.object({ reprice: ModelRepriceSummarySchema });
export type ModelRepriceSummary = z.infer<typeof ModelRepriceSummarySchema>;
export type ModelPrice = z.infer<typeof ModelPriceSchema>;
export type Rates = z.infer<typeof RateSchema>;
export const emptyRates: Rates = { inputMicrodollarsPerMillion: 0, cachedMicrodollarsPerMillion: 0, outputMicrodollarsPerMillion: 0 };
export const rateFields = [
  ["inputMicrodollarsPerMillion", "Input"], ["cachedMicrodollarsPerMillion", "Cached input"], ["outputMicrodollarsPerMillion", "Output"],
] as const;

// Convert display dollars once at the boundary. Persistence and accounting use
// integer microdollars; an empty/invalid field must not become a free rate.
export function priceFromForm(data: FormData): ModelPrice["price"] {
  const rates = (name: string): Rates => {
    const result = { ...emptyRates };
    for (const [field] of rateFields) {
      const value = String(data.get(`${name}.${field}`) ?? "");
      if (!/^\d+(?:\.\d{1,6})?$/.test(value) || Number(value) > 1000000) throw new Error("Enter non-negative USD rates with at most six decimal places.");
      result[field] = Math.round(Number(value) * 1e6);
    }
    return result;
  };
  const price: ModelPrice["price"] = { standard: rates("standard") };
  if (data.has("usePriority")) price.priority = rates("priority");
  if (data.has("useFlex")) price.flex = rates("flex");
  if (data.has("useLong")) {
    price.longContext = rates("longContext");
    const threshold = Number(data.get("longContextThreshold"));
    if (!Number.isSafeInteger(threshold) || threshold < 1) throw new Error("Long-context threshold must be a positive token count.");
    price.longContextThreshold = threshold;
  }
  const multiplier = String(data.get("priorityMultiplier") ?? "");
  if (!price.priority && multiplier !== "") {
    if (!/^\d+(?:\.\d{1,3})?$/.test(multiplier) || Number(multiplier) > 100) throw new Error("Priority multiplier must be between 0 and 100, with at most three decimals.");
    price.priorityMultiplierMilli = Math.round(Number(multiplier) * 1000);
  }
  return price;
}
