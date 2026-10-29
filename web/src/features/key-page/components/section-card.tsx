import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

/** One key page section in the overview's dense card style. */
export function SectionCard({
  id,
  title,
  badge,
  actions,
  className,
  children,
}: {
  id: string;
  title: string;
  badge?: ReactNode;
  actions?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <section aria-labelledby={`${id}-title`} className={cn("min-w-0 rounded-xl border bg-card", className)}>
      <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2">
        <div className="flex items-center gap-2">
          <h2 id={`${id}-title`} className="text-[13px] font-medium uppercase tracking-wider text-muted-foreground">
            {title}
          </h2>
          {badge}
        </div>
        {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
      </div>
      {children}
    </section>
  );
}

/** Marks sections that show the key's current state, not the period. */
export function CurrentBadge() {
  const { t } = useTranslation();
  return (
    <span className="rounded border px-1.5 py-px text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
      {t("keyPage.current")}
    </span>
  );
}

/** Column header cell of the dense tables. */
export function Th({ children, align = "left" }: { children?: ReactNode; align?: "left" | "right" }) {
  return <th className={cn("px-3 py-2 font-medium", align === "right" && "text-right")}>{children}</th>;
}

export function TableHeadRow({ children }: { children: ReactNode }) {
  return (
    <tr className="border-b text-left text-[11px] uppercase tracking-wider text-muted-foreground">{children}</tr>
  );
}

/** Section placeholders while the page loads. */
export function KeyPageSkeleton() {
  return (
    <div className="space-y-4" data-testid="key-page-skeleton" aria-hidden="true">
      <Skeleton className="h-36 w-full rounded-xl" />
      <Skeleton className="h-20 w-full rounded-xl" />
      <Skeleton className="h-72 w-full rounded-xl" />
      <div className="grid gap-4 lg:grid-cols-2">
        <Skeleton className="h-48 w-full rounded-xl" />
        <Skeleton className="h-48 w-full rounded-xl" />
      </div>
      <div className="space-y-2 rounded-xl border p-4">
        {Array.from({ length: 6 }, (_, index) => (
          <Skeleton key={index} className="h-7 w-full" />
        ))}
      </div>
    </div>
  );
}
