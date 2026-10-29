import { lazy, Suspense, useState } from "react";
import { Navigate, Outlet, Route, Routes, useLocation, useParams, useSearchParams } from "react-router-dom";

import { AppHeader } from "@/components/layout/app-header";
import {
  NotFoundPage,
  RouteErrorBoundary,
  RouteLoading,
} from "@/components/layout/route-recovery";
import { RouteScrollRestoration } from "@/components/layout/route-scroll-restoration";
import {
  STATUS_BAR_DEFAULT_HEIGHT_PX,
  StatusBar,
} from "@/components/layout/status-bar";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiKeyAuthWarning } from "@/features/api-keys/components/api-key-auth-warning";
import { AuthGate } from "@/features/auth/components/auth-gate";
import { useAuthStore } from "@/features/auth/hooks/use-auth";
import { useTimeFormatStore } from "@/hooks/use-time-format";

// Route-level code splitting: only the visited page's chunk loads, instead
// of one entry bundle carrying all six pages' code.
const OverviewPage = lazy(() =>
  import("@/features/overview/components/overview-page").then((m) => ({ default: m.OverviewPage })),
);
const RequestsPage = lazy(() =>
  import("@/features/requests/components/requests-page").then((m) => ({ default: m.RequestsPage })),
);
const ReportsPage = lazy(() =>
  import("@/features/reports/components/reports-page").then((m) => ({ default: m.ReportsPage })),
);
const AccountsPage = lazy(() =>
  import("@/features/accounts/components/accounts-page").then((m) => ({ default: m.AccountsPage })),
);
const AutomationsPage = lazy(() =>
  import("@/features/automations/components/automations-page").then((m) => ({ default: m.AutomationsPage })),
);
const ApisPage = lazy(() => import("@/features/apis/components/apis-page").then((m) => ({ default: m.ApisPage })));
const SettingsPage = lazy(() =>
  import("@/features/settings/components/settings-page").then((m) => ({ default: m.SettingsPage })),
);
const KeyPage = lazy(() => import("@/features/key-page/components/key-page").then((m) => ({ default: m.KeyPage })));

/** `/keys/:id`: the administrator key page; a new key remounts the page. */
function AdminKeyPage() {
  const { id = "" } = useParams();
  return <KeyPage key={id} mode="admin" keyId={id} />;
}

/** Key links with `?selected=<id>` open the key page; the list keeps its other filters. */
function KeysRoute() {
  const [params] = useSearchParams();
  const selected = params.get("selected");
  return selected ? <Navigate to={`/keys/${encodeURIComponent(selected)}`} replace /> : <ApisPage />;
}

/** `/apis` became `/keys`; a selection opens the key page, filters in the query survive. */
function ApisRedirect() {
  const { search } = useLocation();
  const selected = new URLSearchParams(search).get("selected");
  return <Navigate to={selected ? `/keys/${encodeURIComponent(selected)}` : `/keys${search}`} replace />;
}

/** Groups are managed in Settings until the Groups page exists. */
function GroupsRedirect() {
  const [params] = useSearchParams();
  const selected = params.get("selected");
  const query = selected ? `?group=${encodeURIComponent(selected)}` : "";
  return <Navigate to={`/settings${query}#account-groups`} replace />;
}

function AppLayout() {
  const { hash, key: locationKey, pathname, search } = useLocation();
  const logout = useAuthStore((state) => state.logout);
  const passwordRequired = useAuthStore((state) => state.passwordRequired);
  const timeFormat = useTimeFormatStore((state) => state.timeFormat);
  const [statusBarHeight, setStatusBarHeight] = useState(STATUS_BAR_DEFAULT_HEIGHT_PX);

  return (
    <div
      className="flex min-h-screen flex-col bg-background"
      data-time-format={timeFormat}
      style={{ paddingBottom: statusBarHeight }}
    >
      <RouteScrollRestoration />
      <AppHeader
        onLogout={() => {
          void logout();
        }}
        showLogout={passwordRequired}
      />
      <main className="mx-auto flex w-full max-w-[1500px] flex-1 flex-col px-4 py-8 sm:px-6">
        <ApiKeyAuthWarning />
        <RouteErrorBoundary
          key={pathname}
          resetKey={`${locationKey}:${pathname}${search}${hash}`}
        >
          <Suspense fallback={<RouteLoading />}>
            <Outlet />
          </Suspense>
        </RouteErrorBoundary>
      </main>
      <StatusBar onHeightChange={setStatusBarHeight} />
    </div>
  );
}

export default function App() {
  return (
    <TooltipProvider>
      <Toaster richColors />
      <Routes>
        <Route path="/key-reports" element={<Navigate to="/" replace />} />
        <Route element={<AuthGate><Outlet /></AuthGate>}>
          <Route element={<AppLayout />}>
            <Route path="/" element={<Navigate to="/overview" replace />} />
            <Route path="/dashboard" element={<Navigate to="/overview" replace />} />
            <Route path="/overview" element={<OverviewPage />} />
            <Route path="/requests" element={<RequestsPage />} />
            <Route path="/groups" element={<GroupsRedirect />} />
            <Route path="/keys" element={<KeysRoute />} />
            <Route path="/keys/:id" element={<AdminKeyPage />} />
            <Route path="/reports" element={<ReportsPage />} />
            <Route path="/accounts" element={<AccountsPage />} />
            <Route path="/automations" element={<AutomationsPage />} />
            <Route path="/apis" element={<ApisRedirect />} />
            <Route path="/settings" element={<SettingsPage />} />
            <Route path="/firewall" element={<Navigate to="/settings?advanced=1#firewall" replace />} />
            <Route path="*" element={<NotFoundPage />} />
          </Route>
        </Route>
      </Routes>
    </TooltipProvider>
  );
}
