import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { describe, expect, it } from "vitest";

import App from "@/App";
import { createAccountSummary } from "@/test/mocks/factories";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

describe("accounts flow integration", () => {
  it("tells accounts with the same e-mail apart by their account id", async () => {
    // Go sends no duplicate marker; the dashboard derives it from the list.
    const twins = ["acc-twin-0001-aaaaaaaaaaaa", "acc-twin-0002-bbbbbbbbbbbb"].map((accountId) =>
      createAccountSummary({ accountId, email: "twin@example.com", displayName: "twin@example.com" }),
    );
    const solo = createAccountSummary({ accountId: "acc-solo", email: "solo@example.com", displayName: "solo@example.com" });
    server.use(http.get("/api/accounts", () => HttpResponse.json({ accounts: [...twins, solo] })));

    window.history.pushState({}, "", "/accounts");
    renderWithProviders(<App />);

    for (const twin of twins) {
      expect((await screen.findAllByTitle(`Account ID ${twin.accountId}`)).length).toBeGreaterThan(0);
    }
    expect(screen.queryByTitle("Account ID acc-solo")).not.toBeInTheDocument();
  });

  it("supports account selection and pause/resume actions", async () => {
    const user = userEvent.setup({ delay: null });

    window.history.pushState({}, "", "/accounts");
    renderWithProviders(<App />);

    expect(await screen.findByRole("heading", { name: "Accounts" })).toBeInTheDocument();
    expect((await screen.findAllByText("primary@example.com")).length).toBeGreaterThan(0);
    expect(screen.getByText("secondary@example.com")).toBeInTheDocument();

    await user.click(screen.getByText("secondary@example.com"));
    expect(await screen.findByText("Token Status")).toBeInTheDocument();

    const resumeButton = screen.queryByRole("button", { name: "Resume" });
    if (resumeButton) {
      await user.click(resumeButton);
      await waitFor(() => {
        expect(screen.getByRole("button", { name: "Pause" })).toBeInTheDocument();
      });
    } else {
      await user.click(screen.getByRole("button", { name: "Pause" }));
      await waitFor(() => {
        expect(screen.getByRole("button", { name: "Resume" })).toBeInTheDocument();
      });
    }
  });

  it("lets operators set, search, and clear an account alias", async () => {
    const user = userEvent.setup({ delay: null });

    window.history.pushState({}, "", "/accounts");
    renderWithProviders(<App />);

    expect(await screen.findByRole("heading", { name: "Accounts" })).toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Edit alias" }));
    const aliasInput = await screen.findByLabelText("Account alias");
    await user.clear(aliasInput);
    await user.type(aliasInput, "Personal Plus");
    await user.click(screen.getByRole("button", { name: "Save alias" }));

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Personal Plus" })).toBeInTheDocument();
    });

    await user.type(screen.getByPlaceholderText("Search accounts..."), "personal");
    expect(screen.getAllByText("Personal Plus").length).toBeGreaterThan(0);
    expect(screen.queryByText("secondary@example.com")).not.toBeInTheDocument();

    await user.click(await screen.findByRole("button", { name: "Edit alias" }));
    const aliasInputToClear = await screen.findByLabelText("Account alias");
    await user.clear(aliasInputToClear);
    await user.click(screen.getByRole("button", { name: "Save alias" }));
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "primary@example.com" })).toBeInTheDocument();
    });
  });
});
