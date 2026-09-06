import { useReducer } from "react";
import { zodResolver } from "@hookform/resolvers/zod";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { AccountMultiSelect } from "@/features/api-keys/components/account-multi-select";
import { LimitRulesEditor } from "@/features/api-keys/components/limit-rules-editor";
import { normalizeLimitRules } from "@/features/api-keys/components/limit-rules-utils";
import type { LimitRuleCreate } from "@/features/api-keys/schemas";
import type { AccountGroup, AccountGroupCreateRequest } from "@/features/account-groups/schemas";

export type AccountGroupDialogProps = {
  open: boolean;
  busy: boolean;
  group: AccountGroup | null;
  onOpenChange: (open: boolean) => void;
  onSubmit: (payload: AccountGroupCreateRequest) => Promise<void>;
};

type AccountGroupFormProps = {
  group: AccountGroup | null;
  busy: boolean;
  onSubmit: (payload: AccountGroupCreateRequest) => Promise<void>;
  onClose: () => void;
};

type AccountGroupDraft = {
  selectedAccountIds: string[];
  limitRules: LimitRuleCreate[];
};

function createAccountGroupDraft(group: AccountGroup | null): AccountGroupDraft {
  return {
    selectedAccountIds: group?.accountIds ?? [],
    limitRules: group?.limits ?? [],
  };
}

function accountGroupDraftReducer(
  state: AccountGroupDraft,
  patch: Partial<AccountGroupDraft>,
): AccountGroupDraft {
  return { ...state, ...patch };
}

function AccountGroupForm({ group, busy, onSubmit, onClose }: AccountGroupFormProps) {
  const { t } = useTranslation();
  const formSchema = z.object({
    name: z.string().min(1, t("apiKeys.validation.nameRequired")),
  });
  const form = useForm<{ name: string }>({
    resolver: zodResolver(formSchema),
    defaultValues: { name: group?.name ?? "" },
  });
  const [draft, updateDraft] = useReducer(
    accountGroupDraftReducer,
    group,
    createAccountGroupDraft,
  );

  const handleSubmit = async (values: { name: string }) => {
    const payload: AccountGroupCreateRequest = {
      name: values.name,
      accountIds: draft.selectedAccountIds,
      limits: normalizeLimitRules(draft.limitRules),
    };
    try {
      await onSubmit(payload);
    } catch {
      return;
    }
    onClose();
  };

  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(handleSubmit)} className="space-y-4">
        <FormField
          control={form.control}
          name="name"
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t("accountGroups.fields.name")}</FormLabel>
              <FormControl>
                <Input {...field} autoComplete="off" />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />

        <div className="space-y-1">
          <p className="text-sm font-medium">{t("apiKeys.form.assignedAccounts")}</p>
          <AccountMultiSelect
            value={draft.selectedAccountIds}
            onChange={(selectedAccountIds) => updateDraft({ selectedAccountIds })}
          />
        </div>

        <div className="space-y-1">
          <p className="text-sm font-medium">{t("apiKeys.form.limits")}</p>
          <LimitRulesEditor
            rules={draft.limitRules}
            onChange={(limitRules) => updateDraft({ limitRules })}
          />
        </div>

        <DialogFooter>
          <Button type="submit" disabled={busy || form.formState.isSubmitting}>
            {group ? t("common.actions.save") : t("common.actions.create")}
          </Button>
        </DialogFooter>
      </form>
    </Form>
  );
}

export function AccountGroupDialog({
  open,
  busy,
  group,
  onOpenChange,
  onSubmit,
}: AccountGroupDialogProps) {
  const { t } = useTranslation();
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>
            {group ? t("accountGroups.editDialog.title") : t("accountGroups.createDialog.title")}
          </DialogTitle>
          <DialogDescription>
            {group
              ? t("accountGroups.editDialog.description")
              : t("accountGroups.createDialog.description")}
          </DialogDescription>
        </DialogHeader>
        <AccountGroupForm
          key={`${group?.id ?? "new"}:${open ? "open" : "closed"}`}
          group={group}
          busy={busy}
          onSubmit={onSubmit}
          onClose={() => onOpenChange(false)}
        />
      </DialogContent>
    </Dialog>
  );
}
