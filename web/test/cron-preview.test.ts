// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import {
  CRON_PRESETS,
  formatLocalDateTimeDate,
  formatUTCDateTime,
  nextRuns,
  parseCron,
  presetForCron,
  validateCron,
} from "../src/cron-preview";

describe("cron presets", () => {
  it("maps standard presets and detects custom expressions", () => {
    expect(CRON_PRESETS.length).toBeGreaterThanOrEqual(5);
    for (const p of CRON_PRESETS) {
      if (p.cron) {
        expect(validateCron(p.cron)).toBeNull();
        expect(presetForCron(p.cron)).toBe(p.id);
      }
    }
    expect(presetForCron("0 3 * * 1,4")).toBe("mon_thu");
    expect(presetForCron("  0   3  *  *  * ")).toBe("daily");
    expect(presetForCron("15 */6 * * *")).toBe("custom");
  });
});

describe("parseCron and validateCron", () => {
  it("supports ranges, lists, steps, month/day names, and Sunday=0/sun (0..6 matching robfig/cron)", () => {
    expect(validateCron("0 3 * * 1,4")).toBeNull();
    expect(validateCron("*/15 0-12/2 1,15 jan-mar mon,wed,fri")).toBeNull();
    expect(validateCron("0 3 * * 0")).toBeNull();
    expect(validateCron("0 3 * * sun")).toBeNull();
    const sun0 = parseCron("0 3 * * 0");
    expect(sun0.dow.has(0)).toBe(true);
    const sunName = parseCron("0 3 * * sun");
    expect(sunName.dow.has(0)).toBe(true);
    const rangeFriSat = parseCron("0 3 * * 5-6");
    expect([...rangeFriSat.dow].sort((a, b) => a - b)).toEqual([5, 6]);
  });

  it("rejects Sunday=7 and 7-bounded DOW ranges to match robfig/cron ParseStandard", () => {
    for (const bad of ["0 3 * * 7", "0 3 * * 5-7", "0 3 * * 0-7"]) {
      expect(validateCron(bad), bad).not.toBeNull();
      expect(() => parseCron(bad), bad).toThrow();
      expect(() => nextRuns(bad, new Date("2026-11-27T00:00:00Z"), 3), bad).toThrow();
    }
  });

  it("rejects malformed, out-of-range, inverted, or zero-step crons", () => {
    for (const bad of [
      "",
      "0 3 * *",
      "0 3 * * * *",
      "@daily",
      "60 3 * * *",
      "0 24 * * *",
      "0 3 0 * *",
      "0 3 32 * *",
      "0 3 * 13 *",
      "0 3 * * 8",
      "0 5-2 * * *",
      "*/0 * * * *",
      "0 3 * * foo",
    ]) {
      expect(validateCron(bad), bad).not.toBeNull();
      expect(() => nextRuns(bad, new Date("2026-11-27T00:00:00Z"), 3), bad).toThrow();
    }
  });
});

describe("nextRuns", () => {
  it("computes exact next 3 UTC timestamps for Mon & Thu 03:00 UTC", () => {
    // 2026-11-27 is Friday. Next Monday is Nov 30, then Thu Dec 3, then Mon Dec 7.
    const now = new Date("2026-11-27T04:00:00Z");
    const runs = nextRuns("0 3 * * 1,4", now, 3);
    expect(runs.map((d) => d.toISOString())).toEqual([
      "2026-11-30T03:00:00.000Z",
      "2026-12-03T03:00:00.000Z",
      "2026-12-07T03:00:00.000Z",
    ]);
  });

  it("starts strictly after the current minute", () => {
    const exact = new Date("2026-11-30T03:00:00.000Z");
    const runs = nextRuns("0 3 * * 1,4", exact, 2);
    expect(runs.map((d) => d.toISOString())).toEqual([
      "2026-12-03T03:00:00.000Z",
      "2026-12-07T03:00:00.000Z",
    ]);
  });

  it("uses standard cron OR semantics when both DOM and DOW are non-wildcard", () => {
    // 15th of month OR Monday at 03:00 UTC from 2026-11-01 (Sunday).
    // Nov 2 (Mon), Nov 9 (Mon), Nov 15 (Sun 15th), Nov 16 (Mon).
    const runs = nextRuns("0 3 15 * 1", new Date("2026-11-01T00:00:00Z"), 4);
    expect(runs.map((d) => d.toISOString())).toEqual([
      "2026-11-02T03:00:00.000Z",
      "2026-11-09T03:00:00.000Z",
      "2026-11-15T03:00:00.000Z",
      "2026-11-16T03:00:00.000Z",
    ]);
  });

  it("handles leap year Feb 29 and fails cleanly on impossible Feb 31", () => {
    const leap = nextRuns("0 0 29 2 *", new Date("2026-11-27T00:00:00Z"), 2);
    expect(leap.map((d) => d.toISOString())).toEqual([
      "2028-02-29T00:00:00.000Z",
      "2032-02-29T00:00:00.000Z",
    ]);
    expect(() => nextRuns("0 0 31 2 *", new Date("2026-11-27T00:00:00Z"), 3)).toThrow();
  });

  it("formats UTC and local date times non-empty", () => {
    const d = new Date("2026-11-30T03:00:00.000Z");
    expect(formatUTCDateTime(d)).toContain("UTC");
    expect(formatLocalDateTimeDate(d).length).toBeGreaterThan(4);
  });
});
