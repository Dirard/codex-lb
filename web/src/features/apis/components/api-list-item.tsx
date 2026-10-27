import { cn } from "@/lib/utils";
import type { ApiKey } from "@/features/api-keys/schemas";
import { ApiKeyLimitSummary, ApiKeyStatusBadge } from "./api-key-limit-usage";

export type ApiListItemProps = {
  apiKey: ApiKey;
  selected: boolean;
  onSelect: (keyId: string) => void;
};

export function ApiListItem({ apiKey, selected, onSelect }: ApiListItemProps) {
  return (
    <button
      type="button"
      onClick={() => onSelect(apiKey.id)}
      className={cn(
        "w-full rounded-lg px-3 py-2.5 text-left transition-colors",
        selected
          ? "bg-primary/8 ring-1 ring-primary/25"
          : "hover:bg-muted/50",
      )}
    >
      <div className="flex items-center gap-2.5">
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium">{apiKey.name}</p>
        </div>
        <ApiKeyStatusBadge apiKey={apiKey} />
      </div>
      <ApiKeyLimitSummary limits={apiKey.limits} />
    </button>
  );
}
