import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { AlertMessage } from "@/components/alert-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { del, get, put } from "@/lib/api-client";
import { getErrorMessageOrNull } from "@/utils/errors";
import { ModelPriceSaveSchema, ModelPriceDeleteSchema, ModelPricesSchema, emptyRates, rateFields, priceFromForm, type ModelPrice, type ModelRepriceSummary, type Rates } from "./model-price";
const priceQueryKey = ["model-prices", "list"];

function RateFields({ name, value }: { name: string; value: Rates }) {
  return <div className="grid gap-3 sm:grid-cols-3">{rateFields.map(([field, label]) => (
    <label key={field} className="space-y-1 text-xs font-medium">
      <span>{label} (USD / 1M tokens)</span>
      <Input name={`${name}.${field}`} aria-label={`${name} ${label}`} type="number" required min={0} max={1000000} step="0.000001" defaultValue={value[field] / 1e6} />
    </label>
  ))}</div>;
}

function PriceEditor({ entry, busy, onSave, onClose }: {
  entry: ModelPrice; busy: boolean; onSave: (model: string, price: ModelPrice["price"]) => Promise<void>; onClose: () => void;
}) {
  const [priority, setPriority] = useState(Boolean(entry.price.priority));
  const [flex, setFlex] = useState(Boolean(entry.price.flex));
  const [long, setLong] = useState(Boolean(entry.price.longContext));
  const [error, setError] = useState<string | null>(null);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy) return;
    setError(null);
    try {
      const data = new FormData(event.currentTarget);
      const model = String(data.get("model") ?? "").trim();
      if (!model || model.length > 128) throw new Error("Enter a model ID of 1–128 characters.");
      await onSave(model, priceFromForm(data));
      onClose();
    } catch (error) { setError(getErrorMessageOrNull(error) ?? "The price could not be saved."); }
  };
  return <form onSubmit={(event) => void submit(event)} className="space-y-4">
    {error ? <AlertMessage variant="error">{error}</AlertMessage> : null}
    <label className="block space-y-1 text-sm font-medium"><span>Model ID</span>
      <Input name="model" required maxLength={128} defaultValue={entry.model} readOnly={Boolean(entry.model)} disabled={busy} placeholder="gpt-new-model" autoComplete="off" />
    </label>
    <fieldset className="space-y-2" disabled={busy}><legend className="mb-2 text-sm font-semibold">Standard</legend><RateFields name="standard" value={entry.price.standard} /></fieldset>
    <details className="space-y-4 rounded-lg border p-3">
      <summary className="cursor-pointer text-sm font-medium">Priority, Flex and long context</summary>
      <label className="flex items-center gap-2 text-sm"><input type="checkbox" name="usePriority" checked={priority} onChange={(event) => setPriority(event.target.checked)} />Explicit priority rates</label>
      {priority ? <RateFields name="priority" value={entry.price.priority ?? entry.price.standard} /> : (
        <label className="block space-y-1 text-sm"><span>Priority multiplier (optional)</span>
          <Input name="priorityMultiplier" type="number" min={0} max={100} step="0.001" defaultValue={entry.price.priorityMultiplierMilli ? entry.price.priorityMultiplierMilli / 1000 : ""} />
        </label>
      )}
      <label className="flex items-center gap-2 text-sm"><input type="checkbox" name="useFlex" checked={flex} onChange={(event) => setFlex(event.target.checked)} />Explicit Flex rates</label>
      {flex ? <RateFields name="flex" value={entry.price.flex ?? entry.price.standard} /> : null}
      <label className="flex items-center gap-2 text-sm"><input type="checkbox" name="useLong" checked={long} onChange={(event) => setLong(event.target.checked)} />Long-context rates</label>
      {long ? <div className="space-y-3">
        <label className="block space-y-1 text-sm"><span>Input-token threshold</span><Input name="longContextThreshold" type="number" required min={1} step={1} defaultValue={entry.price.longContextThreshold ?? 272000} /></label>
        <RateFields name="longContext" value={entry.price.longContext ?? entry.price.standard} />
      </div> : null}
    </details>
    <DialogFooter><Button type="button" variant="outline" onClick={onClose} disabled={busy}>Cancel</Button><Button type="submit" disabled={busy}>{busy ? "Saving and recalculating…" : "Save and recalculate history"}</Button></DialogFooter>
  </form>;
}

function RepriceResult({ model, summary }: { model: string; summary: ModelRepriceSummary }) {
  const partial = summary.partialRawRequests > 0 || summary.partialFoldedBuckets > 0 || summary.undimensionedHistory;
  return <div role="status" aria-live="polite"><AlertMessage variant={partial ? "warning" : "success"}>
    {summary.applied ? <>{model}: recalculated {summary.recomputedRequests} requests. Cost adjustment: {summary.costDeltaMicrodollars < 0 ? "−" : "+"}${(Math.abs(summary.costDeltaMicrodollars) / 1e6).toFixed(6)}.</> : <>Price removed for {model}. Historical costs were preserved.</>}
    {partial ? <> Recalculation is partial: {summary.partialRawRequests} individual requests and {summary.partialFoldedRequests} requests in {summary.partialFoldedBuckets} summary buckets lack sufficient details.</> : null}
    {summary.undimensionedHistory ? <> Older totals also contain history without model-level details and could not be fully recalculated.</> : null}
  </AlertMessage></div>;
}

export function ModelPricesSettings({ disabled = false }: { disabled?: boolean }) {
  const client = useQueryClient();
  const query = useQuery({ queryKey: priceQueryKey, queryFn: () => get("/api/model-prices", ModelPricesSchema) });
  const [editor, setEditor] = useState<ModelPrice | null>(null);
  const [removing, setRemoving] = useState<ModelPrice | null>(null);
  const [result, setResult] = useState<{ model: string; summary: ModelRepriceSummary } | null>(null);
  const [filter, setFilter] = useState("");
  const invalidate = () => Promise.all(["model-prices", "dashboard", "reports", "accounts", "api-keys"].map((key) => client.invalidateQueries({ queryKey: [key] })));
  const save = useMutation({
    mutationFn: ({ model, price }: { model: string; price: ModelPrice["price"] }) => put(`/api/model-prices/${encodeURIComponent(model)}`, ModelPriceSaveSchema, { body: { price } }),
    onSuccess: async (saved) => { setResult({ model: saved.model, summary: saved.reprice }); await invalidate(); },
  });
  const remove = useMutation({
    mutationFn: (model: string) => del(`/api/model-prices/${encodeURIComponent(model)}`, ModelPriceDeleteSchema),
    onSuccess: async (removed, model) => { setResult({ model, summary: removed.reprice }); setRemoving(null); await invalidate(); },
  });
  const error = getErrorMessageOrNull(query.error) || getErrorMessageOrNull(remove.error);
  const busy = disabled || save.isPending || remove.isPending;
  const prices = (query.data?.prices ?? []).filter((entry) => entry.model.toLowerCase().includes(filter.toLowerCase()));

  return <section id="model-prices" className="space-y-4 rounded-xl border bg-card p-5">
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div><h3 className="text-sm font-semibold">Model prices</h3><p className="max-w-2xl text-xs text-muted-foreground">Codex rates in USD per million tokens. Saving a tariff recalculates this model's recorded historical costs, including previously unpriced requests, without restarting or contacting the provider. External sources retain their own prices.</p></div>
      <Button type="button" size="sm" disabled={busy} onClick={() => setEditor({ model: "", source: "custom", hasBuiltin: false, price: { standard: { ...emptyRates } } })}>Add model price</Button>
    </div>
    {error ? <AlertMessage variant="error">{error}</AlertMessage> : null}
    {result ? <RepriceResult {...result} /> : null}
    <Input aria-label="Filter model prices" placeholder="Filter models" value={filter} onChange={(event) => setFilter(event.target.value)} />
    {query.isPending ? <p role="status" className="text-sm text-muted-foreground">Loading prices…</p> : null}
    <div className="max-h-80 space-y-2 overflow-y-auto">
      {prices.map((entry) => <div key={entry.model} className="flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3">
        <div className="min-w-0"><div className="break-words text-sm font-medium">{entry.model} <span className="text-xs font-normal text-muted-foreground">({entry.source})</span></div>
          <div className="text-xs text-muted-foreground">Input ${entry.price.standard.inputMicrodollarsPerMillion / 1e6} · Cached ${entry.price.standard.cachedMicrodollarsPerMillion / 1e6} · Output ${entry.price.standard.outputMicrodollarsPerMillion / 1e6}</div>
        </div>
        <div className="flex gap-2"><Button type="button" variant="outline" size="sm" disabled={busy} onClick={() => setEditor(entry)}>Edit {entry.model}</Button>
          {entry.source === "custom" ? <Button type="button" variant="outline" size="sm" disabled={busy} onClick={() => setRemoving(entry)}>{entry.hasBuiltin ? "Restore bundled price" : "Remove price"}</Button> : null}
        </div>
      </div>)}
    </div>
    <Dialog open={editor !== null} onOpenChange={(open) => { if (!open && !save.isPending) setEditor(null); }}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl"><DialogHeader><DialogTitle>{editor?.model ? "Edit model price" : "Add model price"}</DialogTitle><DialogDescription>Use the provider's model ID. Setting a price does not grant model access or change provider capabilities.</DialogDescription></DialogHeader>
        {editor ? <PriceEditor key={editor.model} entry={editor} busy={busy} onSave={async (model, price) => { await save.mutateAsync({ model, price }); }} onClose={() => setEditor(null)} /> : null}
      </DialogContent>
    </Dialog>
    <Dialog open={removing !== null} onOpenChange={(open) => { if (!open && !remove.isPending) setRemoving(null); }}>
      <DialogContent><DialogHeader><DialogTitle>{removing?.hasBuiltin ? "Restore bundled price?" : "Remove model price?"}</DialogTitle><DialogDescription>{removing?.hasBuiltin ? "The bundled tariff will replace this override and historical costs will be recalculated where usage details are available." : "This model will become unpriced, not free. Existing costs will not be erased; new requests cannot be costed until a price is added again."}</DialogDescription></DialogHeader>
        <DialogFooter><Button type="button" variant="outline" disabled={remove.isPending} onClick={() => setRemoving(null)}>Cancel</Button><Button type="button" disabled={busy} onClick={() => { if (removing) remove.mutate(removing.model); }}>Confirm</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  </section>;
}
