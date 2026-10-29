import { useState } from "react";
import { useTranslation } from "react-i18next";

import { AlertMessage } from "@/components/alert-message";
import { Button } from "@/components/ui/button";
import { SpinnerBlock } from "@/components/ui/spinner";
import { ConversationDetailsDialog } from "@/features/requests/components/conversation-details-dialog";
import { ConversationTable } from "@/features/requests/components/conversation-table";
import { useConversations, type UseConversationsResult } from "@/features/requests/hooks/use-conversations";
import type { AccountLabelSource } from "@/features/requests/account-labels";

type ConversationsViewProps = {
  state?: UseConversationsResult;
  accounts?: AccountLabelSource[];
};

export function ConversationsView({ state, accounts = [] }: ConversationsViewProps) {
  if (state) {
    return <ConversationsViewContent state={state} accounts={accounts} />;
  }
  return <StandaloneConversationsView accounts={accounts} />;
}

function StandaloneConversationsView({ accounts }: { accounts: AccountLabelSource[] }) {
  const state = useConversations({ enabled: true });
  return <ConversationsViewContent state={state} accounts={accounts} />;
}

function ConversationsViewContent({ state, accounts }: { state: UseConversationsResult; accounts: AccountLabelSource[] }) {
  const { t } = useTranslation();
  const { filters, conversationsQuery, updateFilters } = state;
  const [selectedConversationId, setSelectedConversationId] = useState<string | null>(null);

  const data = conversationsQuery.data;
  const errorMessage = conversationsQuery.error instanceof Error
    ? conversationsQuery.error.message
    : t("dashboard.conversations.errorDescription");

  return (
    <div className="space-y-3">
      {conversationsQuery.error ? (
        <div className="space-y-3 rounded-xl border bg-card p-4">
          <div role="alert">
            <AlertMessage variant="error">{errorMessage}</AlertMessage>
          </div>
          <Button type="button" variant="outline" size="sm" onClick={() => void conversationsQuery.refetch()} disabled={conversationsQuery.isFetching}>
            {t("common.actions.retry")}
          </Button>
        </div>
      ) : null}

      {!conversationsQuery.error && conversationsQuery.isPending && !data ? (
        <div className="rounded-xl border bg-card py-8">
          <SpinnerBlock />
        </div>
      ) : null}

      {data ? (
        <ConversationTable
          conversations={data.conversations}
          accounts={accounts}
          total={data.total}
          limit={filters.limit}
          offset={filters.offset}
          hasMore={data.hasMore}
          onLimitChange={(limit) => updateFilters({ limit, offset: 0 })}
          onOffsetChange={(offset) => updateFilters({ offset })}
          onSelect={setSelectedConversationId}
        />
      ) : null}

      <ConversationDetailsDialog
        open={selectedConversationId !== null}
        conversationId={selectedConversationId}
        onOpenChange={(open) => {
          if (!open) {
            setSelectedConversationId(null);
          }
        }}
      />
    </div>
  );
}
