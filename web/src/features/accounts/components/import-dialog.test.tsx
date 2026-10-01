import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { importAccount } from "@/features/accounts/api";
import { renderWithProviders } from "@/test/utils";
import { ImportDialog, type ImportDialogProps } from "./import-dialog";

const syntheticJSON = '{\n  "tokens": {"access_token": "synthetic-only-token"}, "note": "тест"\n}\n';

function setup(overrides: Partial<ImportDialogProps> = {}) {
  const props: ImportDialogProps = {
    open: true, busy: false, error: null, onOpenChange: vi.fn(),
    onImport: vi.fn<(file: File) => Promise<void>>().mockResolvedValue(undefined),
    ...overrides,
  };
  return { ...renderWithProviders(<ImportDialog {...props} />), props, user: userEvent.setup() };
}

function readText(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsText(file);
  });
}

afterEach(() => vi.restoreAllMocks());

describe("ImportDialog", () => {
  it("keeps file import as the default and submits the selected file", async () => {
    const { user, props } = setup();
    expect(screen.getByRole("radio", { name: "Choose file" })).toBeChecked();
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
    const file = new File([syntheticJSON], "saved-auth.json", { type: "application/json" });
    await user.upload(screen.getByLabelText("auth.json file"), file);
    await user.click(screen.getByRole("button", { name: "Import" }));
    expect(props.onImport).toHaveBeenCalledExactlyOnceWith(file);
    expect(props.onOpenChange).toHaveBeenCalledWith(false);
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
  });

  it("sends pasted UTF-8 JSON through the existing multipart API and clears it on success", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({
      accountId: "synthetic-account", email: "synthetic@example.test", planType: "plus", status: "active",
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    const { user, props } = setup({ onImport: async (file) => { await importAccount(file); } });
    await user.click(screen.getByRole("radio", { name: "Paste JSON" }));
    const input = screen.getByRole("textbox", { name: "auth.json contents" });
    await user.click(input);
    await user.paste("   \n");
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
    await user.clear(input);
    await user.paste(syntheticJSON);
    expect(fetchMock).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(props.onOpenChange).toHaveBeenCalledWith(false));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, options] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/accounts/import");
    expect(options?.method).toBe("POST");
    expect(options?.credentials).toBe("same-origin");
    const form = options?.body as FormData;
    expect([...form.keys()]).toEqual(["auth_json"]);
    const file = form.get("auth_json") as File;
    expect(file.name).toBe("auth.json");
    expect(file.type).toBe("application/json");
    expect(await readText(file)).toBe(syntheticJSON);
    expect(input).toHaveValue("");
  });

  it("discards drafts when changing import mode", async () => {
    const { user, props } = setup();
    await user.upload(screen.getByLabelText("auth.json file"), new File([syntheticJSON], "auth.json", { type: "application/json" }));
    await user.click(screen.getByRole("radio", { name: "Paste JSON" }));
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
    await user.click(screen.getByRole("textbox", { name: "auth.json contents" }));
    await user.paste(syntheticJSON);
    await user.click(screen.getByRole("radio", { name: "Choose file" }));
    expect((screen.getByLabelText("auth.json file") as HTMLInputElement).files).toHaveLength(0);
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
    await user.click(screen.getByRole("radio", { name: "Paste JSON" }));
    expect(screen.getByRole("textbox", { name: "auth.json contents" })).toHaveValue("");
    expect(props.onImport).not.toHaveBeenCalled();
  });

  it("starts empty after an external close and reopen", async () => {
    const { user, props, rerender } = setup();
    await user.click(screen.getByRole("radio", { name: "Paste JSON" }));
    await user.click(screen.getByRole("textbox", { name: "auth.json contents" }));
    await user.paste(syntheticJSON);
    rerender(<ImportDialog {...props} open={false} />);
    rerender(<ImportDialog {...props} open />);
    expect(screen.getByRole("radio", { name: "Choose file" })).toBeChecked();
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
    await user.click(screen.getByRole("radio", { name: "Paste JSON" }));
    expect(screen.getByRole("textbox", { name: "auth.json contents" })).toHaveValue("");
  });

  it.each(["file", "paste"] as const)("rejects oversized %s content before dispatch", async (mode) => {
    const { user, props } = setup();
    const text = '"' + "é".repeat(512 * 1024) + '"';
    if (mode === "paste") {
      await user.click(screen.getByRole("radio", { name: "Paste JSON" }));
      fireEvent.change(screen.getByRole("textbox", { name: "auth.json contents" }), { target: { value: text } });
    } else {
      await user.upload(screen.getByLabelText("auth.json file"), new File([text], "auth.json", { type: "application/json" }));
    }
    await user.click(screen.getByRole("button", { name: "Import" }));
    expect(screen.getByRole("alert")).toHaveTextContent("auth.json must not exceed 1 MiB.");
    expect(props.onImport).not.toHaveBeenCalled();
  });

  it("keeps a failed draft open without an unhandled rejection or error payload echo", async () => {
    const onImport = vi.fn<(file: File) => Promise<void>>().mockRejectedValue(new Error("synthetic internal detail"));
    const { user, props } = setup({ onImport, error: "Invalid auth.json payload" });
    await user.click(screen.getByRole("radio", { name: "Paste JSON" }));
    const input = screen.getByRole("textbox", { name: "auth.json contents" });
    await user.click(input);
    await user.paste(syntheticJSON);
    await user.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(onImport).toHaveBeenCalledTimes(1));
    expect(input).toHaveValue(syntheticJSON);
    expect(screen.getByRole("alert")).toHaveTextContent("Invalid auth.json payload");
    expect(screen.getByRole("alert")).not.toHaveTextContent("synthetic");
    expect(props.onOpenChange).not.toHaveBeenCalled();
  });

  it("disables controls and ignores submissions while busy", async () => {
    const { user, props, rerender } = setup();
    await user.click(screen.getByRole("radio", { name: "Paste JSON" }));
    await user.click(screen.getByRole("textbox", { name: "auth.json contents" }));
    await user.paste(syntheticJSON);
    rerender(<ImportDialog {...props} busy />);
    expect(screen.getByRole("textbox", { name: "auth.json contents" })).toBeDisabled();
    expect(screen.getByRole("radio", { name: "Choose file" })).toBeDisabled();
    const submit = screen.getByRole("button", { name: "Import" });
    expect(submit).toBeDisabled();
    fireEvent.submit(submit.closest("form")!);
    expect(props.onImport).not.toHaveBeenCalled();
  });
});
