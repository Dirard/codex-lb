import { useId } from "react";
import { zodResolver } from "@hookform/resolvers/zod";
import { useQuery } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { Button } from "@/components/ui/button";
import { DialogFooter } from "@/components/ui/dialog";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { ModelSourceKindSelect } from "@/features/model-sources/components/model-source-form-fields";
import { grokAccountName, type ModelSourceDraft } from "@/features/model-sources/components/model-source-form";
import { listVideoAccounts } from "@/features/video/api";
import type { VideoAccount } from "@/features/video/schemas";

export type GrokSourceValues = { name: string; grokAccountId: string };

type GrokSourceCreateFormProps = {
  busy: boolean;
  draft: ModelSourceDraft;
  updateDraft: (patch: Partial<ModelSourceDraft>) => void;
  onSubmit: (values: GrokSourceValues) => Promise<void>;
};

// Only an account that can still lend its token serves text; a pause on the
// Video page stops video, not text.
function canServeText(account: VideoAccount): boolean {
  return account.status === "active" || account.status === "paused";
}

/**
 * Creates a Grok subscription source: the operator names it and picks a
 * signed-in Grok account; the server takes the models and prices from xAI.
 */
export function GrokSourceCreateForm({ busy, draft, updateDraft, onSubmit }: GrokSourceCreateFormProps) {
  const { t } = useTranslation();
  const fieldId = useId();
  const accounts = useQuery({ queryKey: ["video", "accounts"], queryFn: listVideoAccounts });
  const schema = z.object({
    name: z.string().trim().min(1, t("modelSources.validation.nameRequired")),
    grokAccountId: z.string().min(1, t("modelSources.grok.accountRequired")),
  });
  const form = useForm<GrokSourceValues>({
    resolver: zodResolver(schema),
    defaultValues: { name: "Grok", grokAccountId: "" },
  });
  const list = accounts.data?.accounts ?? [];

  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <ModelSourceKindSelect draft={draft} updateDraft={updateDraft} />
        <p className="text-xs text-muted-foreground">{t("modelSources.grok.description")}</p>
        <FormField
          control={form.control}
          name="name"
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t("apiKeys.table.name")}</FormLabel>
              <FormControl>
                <Input {...field} autoComplete="off" />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name="grokAccountId"
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t("modelSources.grok.account")}</FormLabel>
              <Select value={field.value} onValueChange={field.onChange} disabled={list.length === 0}>
                <FormControl>
                  <SelectTrigger className="w-full" aria-label={t("modelSources.grok.account")}>
                    <SelectValue placeholder={accounts.isLoading ? t("modelSources.selector.loading") : t("modelSources.grok.chooseAccount")} />
                  </SelectTrigger>
                </FormControl>
                <SelectContent>
                  {list.map((account) => (
                    <SelectItem key={account.id} value={account.id} disabled={!canServeText(account)}>
                      {grokAccountName(account)}
                      {canServeText(account) ? "" : ` (${t("modelSources.grok.needsSignIn")})`}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {!accounts.isLoading && list.length === 0 ? (
                <p className="text-xs text-muted-foreground">{t("modelSources.grok.noAccounts")}</p>
              ) : null}
              <FormMessage />
            </FormItem>
          )}
        />
        <div className="grid gap-3 sm:grid-cols-2">
          <label htmlFor={`${fieldId}-timeout`} className="space-y-1 text-sm font-medium">
            <span>{t("modelSources.fields.timeoutSeconds", { defaultValue: "Request timeout (seconds)" })}</span>
            <Input id={`${fieldId}-timeout`} type="number" min={1} max={86400} step={1} placeholder="600" value={draft.timeoutSeconds} onChange={(event) => updateDraft({ timeoutSeconds: event.target.value })} />
          </label>
          <label htmlFor={`${fieldId}-concurrency`} className="space-y-1 text-sm font-medium">
            <span>{t("modelSources.fields.maxConcurrency", { defaultValue: "Concurrent requests (optional)" })}</span>
            <Input id={`${fieldId}-concurrency`} type="number" min={1} max={100000} step={1} value={draft.maxConcurrency} onChange={(event) => updateDraft({ maxConcurrency: event.target.value })} />
          </label>
        </div>
        <DialogFooter>
          <Button type="submit" disabled={busy || form.formState.isSubmitting}>
            {t("common.actions.create")}
          </Button>
        </DialogFooter>
      </form>
    </Form>
  );
}
