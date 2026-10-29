import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";

import type { Directory } from "@/features/search/api";

import { EMPTY_SCOPE, type ReportScope } from "../report-url";
import { SCOPE_PICKER_ROW_LIMIT, ScopePicker } from "./scope-picker";

function directory(keyCount = 3, groupCount = 2): Directory {
  const groups = Array.from({ length: groupCount }, (_, index) => ({
    id: `grp_${index + 1}`,
    name: `Team ${String.fromCharCode(65 + index)}`,
    accountCount: 1,
    keyCount: 0,
  }));
  const keys = Array.from({ length: keyCount }, (_, index) => ({
    id: `key_${index + 1}`,
    name: `Key ${String(index + 1).padStart(3, "0")}`,
    prefix: `sk-${index + 1}`,
    groupId: index === 0 ? null : groups[index % groupCount]?.id ?? null,
    status: "active" as const,
  }));
  for (const group of groups) {
    group.keyCount = keys.filter((key) => key.groupId === group.id).length;
  }
  return { keys, groups, accounts: [] };
}

function Harness({ dir, initial = EMPTY_SCOPE, onChange }: { dir: Directory; initial?: ReportScope; onChange?: (scope: ReportScope) => void }) {
  const [scope, setScope] = useState(initial);
  return (
    <ScopePicker
      directory={dir}
      scope={scope}
      onChange={(next) => {
        onChange?.(next);
        setScope(next);
      }}
    />
  );
}

async function openPicker(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: /Keys and groups/ }));
  return screen.findByRole("listbox", { name: "Keys and groups" });
}

describe("ScopePicker", () => {
  it("offers All keys, Ungrouped keys, groups with key counts and keys with prefix and group", async () => {
    const user = userEvent.setup();
    render(<Harness dir={directory()} />);
    expect(screen.getByRole("button", { name: /Keys and groups/ })).toHaveTextContent("All keys");
    const listbox = await openPicker(user);
    expect(within(listbox).getByRole("option", { name: /All keys/ })).toHaveAttribute("aria-selected", "true");
    expect(within(listbox).getByRole("option", { name: /Ungrouped keys\s*1 key/ })).toBeInTheDocument();
    expect(within(listbox).getByRole("option", { name: /Team A\s*1 key/ })).toBeInTheDocument();
    expect(within(listbox).getByRole("option", { name: /Key 002 · sk-2\s*Team B/ })).toBeInTheDocument();
    expect(within(listbox).getByRole("option", { name: /Key 001 · sk-1\s*Ungrouped keys/ })).toBeInTheDocument();
  });

  it("selects groups, keys and the ungrouped keys, shows removable chips and clears with All keys", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness dir={directory()} onChange={onChange} />);
    const listbox = await openPicker(user);
    await user.click(within(listbox).getByRole("option", { name: /^Team A/ }));
    await user.click(within(listbox).getByRole("option", { name: /^Key 002/ }));
    await user.click(within(listbox).getByRole("option", { name: /^Ungrouped keys/ }));
    expect(onChange).toHaveBeenLastCalledWith({ groupIds: ["grp_1"], keyIds: ["key_2"], ungrouped: true });
    expect(within(listbox).getByRole("option", { name: /^Team A/ })).toHaveAttribute("aria-selected", "true");
    expect(within(listbox).getByRole("option", { name: /All keys/ })).toHaveAttribute("aria-selected", "false");

    const chips = screen.getAllByTestId("scope-chip");
    expect(chips.map((chip) => chip.textContent)).toEqual(["Team A1 key", "Ungrouped keys1 key", "Key 002sk-2"]);
    await user.click(screen.getByRole("button", { name: "Remove Team A" }));
    expect(onChange).toHaveBeenLastCalledWith({ groupIds: [], keyIds: ["key_2"], ungrouped: true });

    await user.click(within(await openPicker(user)).getByRole("option", { name: /All keys/ }));
    expect(onChange).toHaveBeenLastCalledWith(EMPTY_SCOPE);
    expect(screen.queryAllByTestId("scope-chip")).toHaveLength(0);
  });

  it("narrows by key name, prefix or group name and toggles the active option with the keyboard", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness dir={directory()} onChange={onChange} />);
    const listbox = await openPicker(user);
    const search = screen.getByRole("combobox", { name: "Search keys and groups" });
    await user.type(search, "sk-3");
    expect(within(listbox).getAllByRole("option").map((option) => option.textContent)).toEqual(["Key 003 · sk-3Team A"]);
    await user.keyboard("{Enter}");
    expect(onChange).toHaveBeenLastCalledWith({ groupIds: [], keyIds: ["key_3"], ungrouped: false });

    await user.clear(search);
    await user.type(search, "team b");
    expect(within(listbox).getAllByRole("option").map((option) => option.textContent)).toEqual([
      "Team B1 key",
      "Key 002 · sk-2Team B",
    ]);
    await user.keyboard("{ArrowDown}{Enter}");
    expect(onChange).toHaveBeenLastCalledWith({ groupIds: [], keyIds: ["key_3", "key_2"], ungrouped: false });
    expect(search).toHaveAttribute("aria-activedescendant", within(listbox).getAllByRole("option")[1].id);
  });

  it("renders at most 100 rows of 300 keys and 50 groups and asks to type to narrow", async () => {
    const user = userEvent.setup();
    render(<Harness dir={directory(300, 50)} />);
    const listbox = await openPicker(user);
    const rows = within(listbox).getAllByRole("option").filter((option) => !/All keys|^Ungrouped keys/.test(option.textContent ?? ""));
    expect(rows).toHaveLength(SCOPE_PICKER_ROW_LIMIT);
    expect(screen.getByText("Showing 100 of 350 matches. Type to narrow the list.")).toBeInTheDocument();
    await user.type(screen.getByRole("combobox"), "Key 29");
    expect(within(listbox).getAllByRole("option")).toHaveLength(10);
    expect(screen.queryByText(/Type to narrow the list/)).not.toBeInTheDocument();
  });

  it("collapses chips after six into +n more", async () => {
    const user = userEvent.setup();
    const dir = directory(10, 2);
    render(<Harness dir={dir} initial={{ groupIds: [], keyIds: dir.keys.slice(0, 9).map((key) => key.id), ungrouped: false }} />);
    expect(screen.getAllByTestId("scope-chip")).toHaveLength(6);
    expect(screen.getByRole("button", { name: /Keys and groups/ })).toHaveTextContent("9 selected");
    await user.click(screen.getByRole("button", { name: "+3 more" }));
    expect(screen.getAllByTestId("scope-chip")).toHaveLength(9);
    await user.click(screen.getByRole("button", { name: "Show less" }));
    expect(screen.getAllByTestId("scope-chip")).toHaveLength(6);
  });

  it("keeps chips for ids the directory does not know yet", () => {
    render(<ScopePicker directory={undefined} loading scope={{ groupIds: ["grp_x"], keyIds: [], ungrouped: false }} onChange={() => {}} />);
    expect(screen.getByTestId("scope-chip")).toHaveTextContent("grp_x");
  });
});
