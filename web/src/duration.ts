// One duration rule, shared with internal/roomview (duration.go) through
// internal/roomview/testdata/durations.json.
//   span: the two largest units: "40 s", "12 min", "5 h 47 min", "37 d 17 h" (a zero second unit is left out).
//   ago:  the largest unit only, plus " ago"; "just now" under a minute.
// Each unit is floored; a negative input reads as 0.

const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

function parts(ms: number): [number, string, number, string] {
  const v = Math.max(0, ms);
  if (v < MIN) return [Math.floor(v / 1000), "s", 0, ""];
  if (v < HOUR) return [Math.floor(v / MIN), "min", 0, ""];
  if (v < DAY) return [Math.floor(v / HOUR), "h", Math.floor((v % HOUR) / MIN), "min"];
  return [Math.floor(v / DAY), "d", Math.floor((v % DAY) / HOUR), "h"];
}

export function span(ms: number): string {
  const [a, au, b, bu] = parts(ms);
  return b > 0 ? `${a} ${au} ${b} ${bu}` : `${a} ${au}`;
}

export function ago(ms: number): string {
  if (ms < MIN) return "just now";
  const [a, au] = parts(ms);
  return `${a} ${au} ago`;
}
