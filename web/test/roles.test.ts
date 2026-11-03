import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { mountControls } from "../src/controls";
import { mountFork } from "../src/fork";
import { RoomState } from "../src/room-state";

// The roles the broker starts, read from internal/humanapi/startrun.go's runRoles, so the page
// can never offer a role start_run refuses (U-1).
function serverRunRoles(): string[] {
  const src = readFileSync(join(__dirname, "../../internal/humanapi/startrun.go"), "utf8");
  const m = /runRoles\s*=\s*map\[string\]bool\{([^}]*)\}/.exec(src);
  if (!m) throw new Error("runRoles not found in internal/humanapi/startrun.go");
  return [...m[1].matchAll(/"([a-z]+)":\s*true/g)].map((r) => r[1]).sort();
}

// values are a select's option values, the fork's "no run" ("") left out.
const values = (root: HTMLElement, name: string) =>
  [...root.querySelectorAll<HTMLOptionElement>(`select[name="${name}"] option`)].map((o) => o.value).filter(Boolean).sort();

describe("the run role selects", () => {
  const conn = { send: () => true };
  it("offer exactly the roles the broker starts", () => {
    const want = serverRunRoles();
    expect(want.length).toBeGreaterThan(0);
    const controls = document.createElement("section");
    mountControls(controls, conn, new RoomState("human:a", 1),
      { principal: "human:a", role: "owner", approver: false, driver: true, webUI: true }, () => {});
    expect(values(controls, "role")).toEqual(want);
    const fork = document.createElement("section");
    mountFork(fork, conn, new RoomState("human:a", 1));
    expect(values(fork, "forkRole")).toEqual(want);
  });
});
