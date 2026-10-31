import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Clapperboard } from "lucide-react";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { AlertMessage } from "@/components/alert-message";
import { EmptyState } from "@/components/empty-state";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
  allowVideoKey,
  deleteVideoAccount,
  denyVideoKey,
  getVideoSummary,
  importVideoAccount,
  listVideoAccounts,
  listVideoJobs,
  listVideoKeys,
  updateVideoAccount,
} from "@/features/video/api";
import type { VideoAccount, VideoJob } from "@/features/video/schemas";
import { getErrorMessageOrNull } from "@/utils/errors";
import { formatCurrency, formatDateTimeInline } from "@/utils/formatters";

const POLL_MS = 30_000;

function coolingUntil(account: VideoAccount): string | null {
  return account.cooldownUntil && new Date(account.cooldownUntil).getTime() > Date.now() ? account.cooldownUntil : null;
}

/** The separate "Video generation" page: its own pool, allowed keys and jobs. Reading it never calls xAI. */
export function VideoPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const fileInput = useRef<HTMLInputElement>(null);
  const [selectedKey, setSelectedKey] = useState("");
  const [olderJobs, setOlderJobs] = useState<VideoJob[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);

  const accounts = useQuery({ queryKey: ["video", "accounts"], queryFn: listVideoAccounts, refetchInterval: POLL_MS });
  const keys = useQuery({ queryKey: ["video", "keys"], queryFn: listVideoKeys });
  const jobs = useQuery({ queryKey: ["video", "jobs"], queryFn: () => listVideoJobs(), refetchInterval: POLL_MS });
  const summary = useQuery({ queryKey: ["video", "summary"], queryFn: getVideoSummary, refetchInterval: POLL_MS });

  const refresh = () => queryClient.invalidateQueries({ queryKey: ["video"] });
  const onError = (error: unknown) => toast.error(getErrorMessageOrNull(error) ?? t("video.actionFailed"));
  const importAccount = useMutation({
    mutationFn: importVideoAccount,
    onSuccess: (account) => {
      toast.success(t("video.accounts.imported", { email: account.email }));
      void refresh();
    },
    onError,
  });
  const updateAccount = useMutation({
    mutationFn: ({ id, status }: { id: string; status: "active" | "paused" }) => updateVideoAccount(id, { status }),
    onSuccess: () => void refresh(),
    onError,
  });
  const removeAccount = useMutation({ mutationFn: deleteVideoAccount, onSuccess: () => void refresh(), onError });
  const allowKey = useMutation({
    mutationFn: allowVideoKey,
    onSuccess: () => {
      setSelectedKey("");
      void refresh();
    },
    onError,
  });
  const denyKey = useMutation({ mutationFn: denyVideoKey, onSuccess: () => void refresh(), onError });
  const loadMore = useMutation({
    mutationFn: (next: string) => listVideoJobs(next),
    onSuccess: (page) => {
      setOlderJobs((current) => [...current, ...page.jobs]);
      setCursor(page.nextCursor ?? "");
    },
    onError,
  });

  const error = getErrorMessageOrNull(accounts.error ?? keys.error ?? jobs.error ?? summary.error);
  const allowed = keys.data?.keys.filter((key) => key.allowed) ?? [];
  const available = keys.data?.keys.filter((key) => !key.allowed && key.isActive) ?? [];
  const visibleJobs = [...(jobs.data?.jobs ?? []), ...olderJobs];
  const nextCursor = cursor === null ? (jobs.data?.nextCursor ?? null) : cursor || null;
  const periods = summary.data
    ? ([
        ["today", summary.data.today],
        ["week", summary.data.week],
        ["month", summary.data.month],
      ] as const)
    : [];

  return (
    <div className="animate-fade-in-up space-y-8">
      <header>
        <h1 className="text-2xl font-semibold tracking-tight">{t("video.title")}</h1>
        <p className="mt-1 text-sm text-muted-foreground">{t("video.subtitle")}</p>
      </header>
      {error ? <AlertMessage variant="error">{error}</AlertMessage> : null}

      {periods.length ? (
        <section aria-label={t("video.totals.title")} className="grid gap-3 sm:grid-cols-3">
          {periods.map(([name, totals]) => (
            <div key={name} className="rounded-xl border p-4">
              <p className="text-xs text-muted-foreground">{t(`video.totals.${name}`)}</p>
              <p className="mt-1 text-lg font-semibold tabular-nums">{formatCurrency(totals.costUsd)}</p>
              <p className="text-xs text-muted-foreground">
                {t("video.totals.detail", { done: totals.done, jobs: totals.jobs, seconds: Math.round(totals.seconds) })}
              </p>
            </div>
          ))}
        </section>
      ) : null}

      <section className="space-y-3" aria-labelledby="video-accounts">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 id="video-accounts" className="text-lg font-semibold">{t("video.accounts.title")}</h2>
          <input
            ref={fileInput}
            type="file"
            accept="application/json,.json"
            className="hidden"
            aria-label={t("video.accounts.importFile")}
            onChange={(event) => {
              const file = event.target.files?.[0];
              event.target.value = "";
              if (file) importAccount.mutate(file);
            }}
          />
          <Button size="sm" disabled={importAccount.isPending} onClick={() => fileInput.current?.click()}>
            {t("video.accounts.import")}
          </Button>
        </div>
        <p className="text-sm text-muted-foreground">{t("video.accounts.howTo")}</p>
        <p className="text-sm text-muted-foreground">{t("video.accounts.poolNote")}</p>
        {accounts.data && accounts.data.accounts.length === 0 ? (
          <EmptyState icon={Clapperboard} title={t("video.accounts.emptyTitle")} description={t("video.accounts.emptyDescription")} />
        ) : (
          <div className="overflow-x-auto rounded-xl border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("video.accounts.account")}</TableHead>
                  <TableHead>{t("video.accounts.status")}</TableHead>
                  <TableHead className="text-right">{t("video.accounts.unfinished")}</TableHead>
                  <TableHead className="text-right">{t("video.accounts.done")}</TableHead>
                  <TableHead className="text-right">{t("video.accounts.cost")}</TableHead>
                  <TableHead>{t("video.accounts.lastUsed")}</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {accounts.data?.accounts.map((account) => {
                  const cooling = coolingUntil(account);
                  return (
                    <TableRow key={account.id}>
                      <TableCell>{account.label || account.email}</TableCell>
                      <TableCell>
                        <Badge variant={account.status === "active" && !cooling ? "secondary" : "outline"}>{t(`video.status.${account.status}`, account.status)}</Badge>
                        {cooling ? <span className="ml-2 text-xs text-muted-foreground">{t("video.accounts.coolingUntil", { time: formatDateTimeInline(cooling) })}</span> : null}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">{account.unfinishedJobs}</TableCell>
                      <TableCell className="text-right tabular-nums">{account.doneJobs}</TableCell>
                      <TableCell className="text-right tabular-nums">{formatCurrency(account.costUsd)}</TableCell>
                      <TableCell>{formatDateTimeInline(account.lastUsedAt)}</TableCell>
                      <TableCell className="space-x-2 whitespace-nowrap text-right">
                        {account.status === "paused" || account.status === "access_denied" ? (
                          <Button size="sm" variant="outline" onClick={() => updateAccount.mutate({ id: account.id, status: "active" })}>
                            {t("video.accounts.resume")}
                          </Button>
                        ) : account.status === "active" ? (
                          <Button size="sm" variant="outline" onClick={() => updateAccount.mutate({ id: account.id, status: "paused" })}>
                            {t("video.accounts.pause")}
                          </Button>
                        ) : null}
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() => {
                            if (window.confirm(t("video.accounts.deleteConfirm", { name: account.label || account.email }))) removeAccount.mutate(account.id);
                          }}
                        >
                          {t("video.accounts.delete")}
                        </Button>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </div>
        )}
      </section>

      <section className="space-y-3" aria-labelledby="video-keys">
        <h2 id="video-keys" className="text-lg font-semibold">{t("video.keys.title")}</h2>
        <p className="text-sm text-muted-foreground">{t("video.keys.description")}</p>
        <div className="flex flex-wrap items-center gap-2">
          <select
            aria-label={t("video.keys.select")}
            className="h-9 min-w-56 rounded-md border bg-background px-3 text-sm"
            value={selectedKey}
            onChange={(event) => setSelectedKey(event.target.value)}
          >
            <option value="">{t("video.keys.select")}</option>
            {available.map((key) => (
              <option key={key.id} value={key.id}>{`${key.name} (${key.keyPrefix})`}</option>
            ))}
          </select>
          <Button size="sm" disabled={!selectedKey || allowKey.isPending} onClick={() => allowKey.mutate(selectedKey)}>
            {t("video.keys.add")}
          </Button>
        </div>
        {allowed.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("video.keys.empty")}</p>
        ) : (
          <ul className="divide-y rounded-xl border">
            {allowed.map((key) => (
              <li key={key.id} className="flex items-center justify-between gap-2 px-4 py-2 text-sm">
                <span>
                  {key.name} <span className="text-muted-foreground">{key.keyPrefix}</span>
                  {key.isActive ? null : <span className="ml-2 text-xs text-muted-foreground">{t("video.keys.inactive")}</span>}
                </span>
                <Button size="sm" variant="outline" onClick={() => denyKey.mutate(key.id)}>
                  {t("video.keys.remove")}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="space-y-3" aria-labelledby="video-jobs">
        <h2 id="video-jobs" className="text-lg font-semibold">{t("video.jobs.title")}</h2>
        {jobs.data && visibleJobs.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("video.jobs.empty")}</p>
        ) : (
          <div className="overflow-x-auto rounded-xl border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("video.jobs.created")}</TableHead>
                  <TableHead>{t("video.jobs.key")}</TableHead>
                  <TableHead>{t("video.jobs.account")}</TableHead>
                  <TableHead>{t("video.jobs.model")}</TableHead>
                  <TableHead>{t("video.jobs.mode")}</TableHead>
                  <TableHead>{t("video.jobs.status")}</TableHead>
                  <TableHead className="text-right">{t("video.jobs.seconds")}</TableHead>
                  <TableHead className="text-right">{t("video.jobs.cost")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {visibleJobs.map((job) => (
                  <TableRow key={job.id}>
                    <TableCell className="whitespace-nowrap">{formatDateTimeInline(job.createdAt)}</TableCell>
                    <TableCell>{job.keyName}</TableCell>
                    <TableCell>{job.accountLabel}</TableCell>
                    <TableCell>{job.model}</TableCell>
                    <TableCell>{t(`video.mode.${job.mode}`, job.mode)}</TableCell>
                    <TableCell title={job.errorCode || undefined}>{t(`video.jobStatus.${job.status}`, job.status)}</TableCell>
                    <TableCell className="text-right tabular-nums">{job.durationSeconds ?? job.requestedSeconds}</TableCell>
                    <TableCell className="text-right tabular-nums">{job.costUsd === null ? "--" : formatCurrency(job.costUsd)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
        {nextCursor ? (
          <Button size="sm" variant="outline" disabled={loadMore.isPending} onClick={() => loadMore.mutate(nextCursor)}>
            {t("video.jobs.more")}
          </Button>
        ) : null}
      </section>
    </div>
  );
}
