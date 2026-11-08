// SPDX-License-Identifier: Apache-2.0

import type { NeedItem } from "../src/api";

// Every needs/v1 kind as the route serves it (internal/summary's needs.go and coordination_needs.go),
// and the one action each card must end in: the control here when the room has one, else a link out
// with the line saying the room has no button for it. The preview's ?needs page and needs.test.ts both read this table.
export const NEED_FIXTURES: NeedItem[] = [
  { kind: "plan_confirmation", id: "2238", what: "a maintainer comments `/factory split` on issue #2238 to accept the parts, or `/factory retry` to run the task whole",
    url: "https://github.com/o/r/issues/2238", action: { kind: "comment", label: "comment on the issue", url: "https://github.com/o/r/issues/2238" } },
  { kind: "plan_confirmation", id: "p/parent/v2", what: "a maintainer of o/r confirms coordination plan v2",
    action: { kind: "decide", label: "confirm in the parent room", url: "https://rooms.example/r/3kq7x2ma#coordination" } },
  { kind: "question", id: "3", what: "@alice answers the scoper's question by editing issue #3", url: "https://github.com/o/r/issues/3",
    action: { kind: "answer", label: "answer on issue #3", url: "https://github.com/o/r/issues/3" } },
  { kind: "merge", id: "2760", approve: true, pr: 2760, what: "a maintainer approves PR #2760: approve the head you read on the PR page",
    url: "https://github.com/o/r/pull/2760", action: { kind: "merge", label: "approve the head on the pull request", url: "https://github.com/o/r/pull/2760" } },
  { kind: "review", id: "77", pr: 77, what: "a maintainer reviews PR #77", url: "https://github.com/o/r/pull/77",
    action: { kind: "review", label: "review PR #77", url: "https://github.com/o/r/pull/77" } },
  { kind: "failed_check", id: "ci_wait_expired", what: "a maintainer checks the failure and re-runs it, or retries the task", why: "CI never settled",
    url: "https://github.com/o/r/pull/77", action: { kind: "retry", label: "re-run the check or retry the task", url: "https://github.com/o/r/pull/77" } },
  { kind: "recovery", id: "budget_exhausted", issue: 3, what: "a maintainer comments `/factory retry` on issue #3 to run it again, or closes the room",
    url: "https://github.com/o/r/issues/3", action: { kind: "retry", label: "retry the task or close the room", url: "https://rooms.example/r/3kq7x2ma" } },
  { kind: "hold", id: "waiting_human_driver", what: "a person drives the room: hand the driver back to let the task run",
    url: "https://rooms.example/r/3kq7x2ma", action: { kind: "release", label: "hand the driver back to the factory", url: "https://rooms.example/r/3kq7x2ma" } },
  { kind: "hold", id: "waiting_room_busy", what: "another run is on in the room", url: "https://rooms.example/r/3kq7x2ma",
    action: { kind: "release", label: "wait for the other run in the room to end", url: "https://rooms.example/r/3kq7x2ma" } },
];
