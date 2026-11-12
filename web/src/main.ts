// SPDX-License-Identifier: Apache-2.0

import { mountApprovalMemory } from "./approval-memory";
import { fetchDashboards } from "./dashboards";
import { mountInstallBanner } from "./install-banner";
import { mountRoom } from "./room";
import { isSettings, mountSettings, settingsFrame } from "./settings";
import { initTheme } from "./theme";
import { listRooms } from "./view";

const app = document.getElementById("app")!;

// The platform's theme, before anything paints: light by default, dark when the
// user chose it or follows a dark OS (theme.ts, the app-wizard's contract).
const theme = initTheme();

const m = location.pathname.match(/^\/r\/([a-z2-7]{8})$/);
// The install's unsafe settings, on the rooms list and every settings page (the room has its own
// per-run "Not sandboxed" banner).
if (!m) void mountInstallBanner(app, fetch);
if (m) mountRoom(app, m[1], theme, { dashboards: () => fetchDashboards(fetch) });
// Approval memory is a settings section: the same header and tabs, its own page in main.
else if (location.pathname === "/settings/approval-memory") mountApprovalMemory(settingsFrame(app, "Approval memory", "/settings/approval-memory", theme), fetch);
else if (isSettings(location.pathname)) void mountSettings(app, location.pathname, fetch, theme);
else void listRooms(app, fetch, {}, theme);
