import { Skeleton } from "@/components/ui/skeleton";

/** The accounts table while the first list is loading. */
export function AccountsSkeleton() {
  return (
    <div className="space-y-3" data-testid="accounts-skeleton">
      <div className="flex flex-wrap gap-2">
        <Skeleton className="h-8 min-w-[12rem] flex-1 rounded-md" />
        <Skeleton className="h-8 w-40 rounded-md" />
        <Skeleton className="h-8 w-40 rounded-md" />
        <Skeleton className="h-8 w-44 rounded-md" />
        <Skeleton className="h-8 w-28 rounded-md" />
      </div>
      <div className="space-y-2 rounded-xl border bg-card p-3">
        <Skeleton className="h-4 w-full" />
        {Array.from({ length: 6 }).map((_, i) => (
          <div key={i} className="flex items-center gap-3 py-1.5">
            <div className="w-48 space-y-1.5">
              <Skeleton className="h-4 w-36" />
              <Skeleton className="h-3 w-24" />
            </div>
            <Skeleton className="h-5 w-20 rounded-full" />
            <Skeleton className="h-3 flex-1" />
            <Skeleton className="h-3 flex-1" />
            <Skeleton className="h-7 w-28 rounded-md" />
          </div>
        ))}
      </div>
    </div>
  );
}
