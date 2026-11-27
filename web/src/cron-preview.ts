// SPDX-License-Identifier: Apache-2.0

export interface CronPreset {
  id: string;
  label: string;
  cron: string;
}

export const CRON_PRESETS: CronPreset[] = [
  { id: "daily", label: "Daily 03:00 UTC", cron: "0 3 * * *" },
  { id: "weekdays", label: "Weekdays 03:00 UTC", cron: "0 3 * * 1-5" },
  { id: "mon_thu", label: "Mon & Thu 03:00 UTC", cron: "0 3 * * 1,4" },
  { id: "weekly_mon", label: "Weekly Monday 04:00 UTC", cron: "0 4 * * 1" },
  { id: "custom", label: "Custom cron", cron: "" },
];

export interface ParsedCron {
  minute: Set<number>;
  hour: Set<number>;
  dom: Set<number>;
  domWildcard: boolean;
  month: Set<number>;
  dow: Set<number>;
  dowWildcard: boolean;
}

const MONTH_NAMES: Record<string, number> = {
  jan: 1, feb: 2, mar: 3, apr: 4, may: 5, jun: 6,
  jul: 7, aug: 8, sep: 9, oct: 10, nov: 11, dec: 12,
};

const DOW_NAMES: Record<string, number> = {
  sun: 0, mon: 1, tue: 2, wed: 3, thu: 4, fri: 5, sat: 6,
};

export function presetForCron(expr: string): string {
  const norm = expr.trim().replace(/\s+/g, " ");
  const found = CRON_PRESETS.find((p) => p.cron !== "" && p.cron === norm);
  return found ? found.id : "custom";
}

function parseAtom(raw: string, min: number, max: number, names?: Record<string, number>): number {
  const lower = raw.toLowerCase();
  if (names && lower in names) return names[lower];
  if (!/^\d+$/.test(raw)) {
    throw new Error(`Invalid cron token ${JSON.stringify(raw)}`);
  }
  const n = Number(raw);
  if (n < min || n > max) {
    throw new Error(`Cron value ${n} out of range (${min}..${max})`);
  }
  return n;
}

function parseField(
  field: string,
  min: number,
  max: number,
  names?: Record<string, number>,
): Set<number> {
  if (!field) throw new Error("Empty cron field");
  const out = new Set<number>();
  for (const part of field.split(",")) {
    if (!part) throw new Error(`Invalid cron list in ${JSON.stringify(field)}`);
    const slashParts = part.split("/");
    if (slashParts.length > 2) throw new Error(`Invalid step syntax in ${JSON.stringify(part)}`);
    const [rangePart, stepStr] = slashParts;
    let step = 1;
    if (stepStr !== undefined) {
      if (!/^\d+$/.test(stepStr)) throw new Error(`Invalid cron step ${JSON.stringify(stepStr)}`);
      step = Number(stepStr);
      if (step <= 0) throw new Error(`Cron step must be positive: ${step}`);
    }
    let start: number;
    let end: number;
    if (rangePart === "*") {
      start = min;
      end = max;
    } else if (rangePart.includes("-")) {
      const dashParts = rangePart.split("-");
      if (dashParts.length !== 2 || !dashParts[0] || !dashParts[1]) {
        throw new Error(`Invalid cron range ${JSON.stringify(rangePart)}`);
      }
      start = parseAtom(dashParts[0], min, max, names);
      end = parseAtom(dashParts[1], min, max, names);
      if (start > end) {
        throw new Error(`Inverted cron range ${start}-${end}`);
      }
    } else {
      start = parseAtom(rangePart, min, max, names);
      end = stepStr !== undefined ? max : start;
    }
    for (let v = start; v <= end; v += step) {
      out.add(v);
    }
  }
  if (out.size === 0) throw new Error(`Cron field ${JSON.stringify(field)} matched nothing`);
  return out;
}

export function parseCron(expr: string): ParsedCron {
  const trimmed = expr.trim();
  if (!trimmed) throw new Error("Cron expression is required");
  if (trimmed.startsWith("@")) {
    throw new Error("Use 5-field UTC cron syntax (minute hour day month weekday)");
  }
  const fields = trimmed.split(/\s+/);
  if (fields.length !== 5) {
    throw new Error(`Expected 5 cron fields (minute hour day month weekday), got ${fields.length}`);
  }
  return {
    minute: parseField(fields[0], 0, 59),
    hour: parseField(fields[1], 0, 23),
    dom: parseField(fields[2], 1, 31),
    domWildcard: fields[2] === "*",
    month: parseField(fields[3], 1, 12, MONTH_NAMES),
    dow: parseField(fields[4], 0, 6, DOW_NAMES),
    dowWildcard: fields[4] === "*",
  };
}

export function validateCron(expr: string): string | null {
  try {
    parseCron(expr);
    return null;
  } catch (e) {
    return e instanceof Error ? e.message : String(e);
  }
}

function dayMatches(p: ParsedCron, dom: number, dow: number): boolean {
  if (!p.domWildcard && !p.dowWildcard) {
    return p.dom.has(dom) || p.dow.has(dow);
  }
  return p.dom.has(dom) && p.dow.has(dow);
}

export function nextRuns(expr: string, now: Date = new Date(), count = 3): Date[] {
  if (!Number.isInteger(count) || count <= 0) {
    throw new Error("count must be a positive integer");
  }
  if (Number.isNaN(now.getTime())) {
    throw new Error("Invalid reference date");
  }
  const p = parseCron(expr);
  let cursorMs = Math.floor(now.getTime() / 60_000) * 60_000 + 60_000;
  // 20-year horizon handles leap-year schedules (e.g. Feb 29) while terminating quickly on Feb 31.
  const maxEndMs = cursorMs + 20 * 366 * 86_400_000;
  const out: Date[] = [];

  while (out.length < count && cursorMs <= maxEndMs) {
    const d = new Date(cursorMs);
    const month = d.getUTCMonth() + 1;
    const dom = d.getUTCDate();
    const dow = d.getUTCDay();
    if (!p.month.has(month) || !dayMatches(p, dom, dow)) {
      cursorMs = Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), dom + 1, 0, 0, 0, 0);
      continue;
    }
    const hour = d.getUTCHours();
    if (!p.hour.has(hour)) {
      cursorMs = Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), dom, hour + 1, 0, 0, 0);
      continue;
    }
    const minute = d.getUTCMinutes();
    if (p.minute.has(minute)) {
      out.push(d);
    }
    cursorMs += 60_000;
  }

  if (out.length < count) {
    throw new Error("Cron schedule has no upcoming run within 20 years");
  }
  return out;
}

export function formatUTCDateTime(d: Date): string {
  return `${new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short", timeZone: "UTC" }).format(d)} UTC`;
}

export function formatLocalDateTimeDate(d: Date): string {
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(d);
}
