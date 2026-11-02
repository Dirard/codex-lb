import { useReducer } from "react";
import { zodResolver } from "@hookform/resolvers/zod";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Form } from "@/components/ui/form";
import { ModelSourceFormFields } from "@/features/model-sources/components/model-source-form-fields";
import {
  GrokSourceCreateForm,
  type GrokSourceValues,
} from "@/features/model-sources/components/grok-source-fields";
import {
  GROK_TEXT_BASE_URL,
  initialModelSourceDraft,
  createModelSourceFormSchema,
  modelInputsFromForm,
  modelSourceDraftReducer,
  sourceRuntimeOptions,
  type ModelSourceFormValues,
} from "@/features/model-sources/components/model-source-form";
import type { ModelSourceCreateRequest } from "@/features/model-sources/schemas";

export type ModelSourceCreateDialogProps = {
  open: boolean;
  busy: boolean;
  onOpenChange: (open: boolean) => void;
  onSubmit: (payload: ModelSourceCreateRequest) => Promise<void>;
};

export function ModelSourceCreateDialog({
  open,
  busy,
  onOpenChange,
  onSubmit,
}: ModelSourceCreateDialogProps) {
  const { t } = useTranslation();
  const form = useForm<ModelSourceFormValues>({
    resolver: zodResolver(createModelSourceFormSchema(t)),
    defaultValues: {
      name: "",
      baseUrl: "",
      apiKey: "",
      models: "",
    },
  });
  const [draft, updateDraft] = useReducer(modelSourceDraftReducer, initialModelSourceDraft);

  const handleSubmit = async (values: ModelSourceFormValues) => {
    const payload: ModelSourceCreateRequest = {
      kind: draft.kind,
      ...sourceRuntimeOptions(draft),
      name: values.name,
      baseUrl: values.baseUrl,
      apiKey: values.apiKey.trim() ? values.apiKey.trim() : undefined,
      supportsChatCompletions: draft.supportsChatCompletions,
      supportsResponses: draft.supportsResponses,
      supportsAudioTranscriptions: draft.supportsAudioTranscriptions,
      supportsEmbeddings: draft.supportsEmbeddings,
      models: modelInputsFromForm(values, draft),
    };
    await onSubmit(payload);
    onOpenChange(false);
  };

  // The server fills a Grok source's models and prices from xAI.
  const handleGrokSubmit = async (values: GrokSourceValues) => {
    await onSubmit({
      kind: "grok_subscription",
      ...sourceRuntimeOptions(draft),
      name: values.name.trim(),
      baseUrl: GROK_TEXT_BASE_URL,
      supportsChatCompletions: false,
      supportsResponses: true,
      providerConfig: { grokAccountId: values.grokAccountId },
      models: [],
    });
    onOpenChange(false);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
	          <DialogTitle>{t("modelSources.createDialog.title")}</DialogTitle>
	          <DialogDescription>{t("modelSources.createDialog.description")}</DialogDescription>
        </DialogHeader>

        {draft.kind === "grok_subscription" ? (
          <GrokSourceCreateForm busy={busy} draft={draft} updateDraft={updateDraft} onSubmit={handleGrokSubmit} />
        ) : (
          <Form {...form}>
            <form onSubmit={form.handleSubmit(handleSubmit)} className="space-y-4">
              <ModelSourceFormFields
                control={form.control}
                draft={draft}
                updateDraft={updateDraft}
                apiKeyLabel={t("modelSources.fields.upstreamApiKey")}
              />
              <DialogFooter>
                <Button type="submit" disabled={busy || form.formState.isSubmitting}>
                  {t("common.actions.create")}
                </Button>
              </DialogFooter>
            </form>
          </Form>
        )}
      </DialogContent>
    </Dialog>
  );
}
