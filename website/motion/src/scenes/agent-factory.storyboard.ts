// The editable part of the Maku overview clip: beats, timings and wording.
// Shift a beat's `start` and everything in it moves; the drawing code in
// agent-factory.ts only knows beat names. Wrap words in **…** to accent them.

import type { Beat } from './beat.js';

export const title = {
  text: 'Maku',
  subtitle: ['The open-source software factory', 'for Kubernetes.'],
  kicker: 'open stack · sandboxed · zero lock-in',
  url: 'maku.ogenki.io',
  licence: 'Apache-2.0',
};

export const labels = {
  cluster: 'Kubernetes',
  developer: { title: 'Developer', sub: 'coding agent · Slack' },
  planner: { title: 'Planner', sub: ['drafts the issue', 'asks ≤ 1 question'] },
  github: { title: 'GitHub', sub: 'issue · pull request' },
  readyLabel: 'factory/ready',
  room: { title: 'Room', sub: 'watch · steer' },
  factory: { title: 'Maku', sub: 'issue → task' },
  run: { title: 'Agent run', sandbox: 'gVisor sandbox', inside: 'OpenCode · OpenHands' },
  gateway: { title: 'agentgateway', sub: 'per-run JWT · USD caps' },
  models: { title: 'Any LLM', sub: 'SaaS · self-hosted' },
  tools: { title: 'MCP tools', sub: 'repo · cluster' },
  token: { title: 'GitHub token', sub: 'scoped per run' },
  edges: {
    ask: 'asks ·\nplans',
    file: 'files issue',
    review: 'reviews ·\nmerges',
    open: 'opens',
    start: 'starts run',
    steer: 'events · steering',
    watch: 'watches · steers',
    call: 'every call',
    pr: 'branch · PR',
  },
};

export const beats = {
  hook: { start: 0, end: 4 } as Beat,
  ask: {
    start: 4,
    end: 9,
    caption: 'One sentence from your **coding\u00a0agent** or **Slack**, or a labelled issue. A **planner** drafts the issue; you confirm it.',
  } as Beat,
  intake: {
    start: 9,
    end: 14,
    caption: 'The issue becomes a **sandboxed\u00a0agent\u00a0run** you can watch and steer in a room.',
  } as Beat,
  identity: {
    start: 14,
    end: 20,
    caption: 'Every call crosses **agentgateway** under a per-run **identity** and hard **USD\u00a0cap**.',
  } as Beat,
  pr: {
    start: 20,
    end: 25.5,
    caption: 'The agent opens a **pull\u00a0request**. A **merge\u00a0gate** or **human\u00a0review** decides the merge.',
  } as Beat,
  outro: {
    start: 25.5,
    end: 32,
    caption: 'Open stack, hard budgets, audited rooms, GitOps gates. **Own your software factory.**',
  } as Beat,
};

/** Total length. The last frame crossfades back to the hook, so the clip loops. */
export const duration = beats.outro.end;
