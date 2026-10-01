import { describe, expect, it } from "vitest";
import { initialModelSourceDraft, mergeReasoningMetadata, modelSourceDraftReducer, parseReasoningEffortsInput } from "./model-source-form";

describe("Z.AI protocol selection", () => {
  it("keeps Responses selection and clears only unsupported protocols", () => {
    const state = { ...initialModelSourceDraft, supportsResponses: true, supportsChatCompletions: false, supportsAudioTranscriptions: true, supportsEmbeddings: true };
    expect(modelSourceDraftReducer(state, { kind: "zai" })).toMatchObject({
      kind: "zai", supportsResponses: true, supportsChatCompletions: false,
      supportsAudioTranscriptions: false, supportsEmbeddings: false,
    });
    expect(state.supportsAudioTranscriptions).toBe(true);
  });

  it("retains the existing Chat default for new sources", () => {
    expect(modelSourceDraftReducer(initialModelSourceDraft, { kind: "zai" })).toMatchObject({
      supportsChatCompletions: true, supportsResponses: false,
    });
  });
});

describe("model-source-form reasoning effort normalization", () => {
  it("trims whitespace and preserves casing for effort values", () => {
    expect(parseReasoningEffortsInput("  Ultra,   xhigh , low  ")).toEqual([
      "Ultra",
      "xhigh",
      "low",
    ]);
  });

  it("preserves casing for declared default reasoning effort", () => {
    const metadata = mergeReasoningMetadata(
      null,
      true,
      ["Ultra", "provider-specific", "xhigh"],
      "  provider-specific  ",
    );
    const parsed = JSON.parse(metadata ?? "{}");

    expect(parsed.supported_reasoning_levels).toEqual(["Ultra", "provider-specific", "xhigh"]);
    expect(parsed.default_reasoning_level).toBe("provider-specific");
  });
});
