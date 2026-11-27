// SPDX-License-Identifier: Apache-2.0

// New task form (Task G4): plans a request over the broker's /api/handoffs routes, shows the
// workflow card, the planner's one question when it has one, the issue's title and body, a
// described workflow selector for maintainers, and Plan first. Start files the issue as the
// viewer through their GitHub link; an unlinked viewer sees "Link your GitHub account" and no
// Start button. Every string from the broker enters the DOM as text (T10).

import { api, SignInRequired } from "./api";
import { button, el, labelled, named } from "./controls";
import { safeLink } from "./summary";

interface HandoffQuestion {
  text: string;
  recommended?: string;
}

interface HandoffCreateResponse {
  id: string;
  text?: string;
  question?: HandoffQuestion | null;
  ready: boolean;
  workflow?: string;
  budget?: number;
  repository?: string;
  title?: string;
  body?: string;
}

interface WorkflowChoice {
  name: string;
  description?: string;
  ceilingUSD?: number;
  stepUpCeilingUSD?: number;
  path?: string[];
}

interface PlanIssue {
  title?: string;
  body?: string;
  profile?: string;
  outcome?: string;
  outcomeInferred?: boolean;
  outcomeNote?: string;
  roles?: string[];
  suggestion?: { profile?: string; roles?: string[] };
}

interface HandoffCheckResponse {
  id: string;
  hash: string;
  canStart: boolean;
  canStartWhy?: string;
  maintainer?: boolean;
  workflow?: string;
  linked?: boolean;
  workflowChoices?: WorkflowChoice[];
  defaultChoice?: WorkflowChoice;
  activeChoice?: WorkflowChoice;
  title?: string;
  body?: string;
  repository?: string;
  plan?: {
    repository?: string;
    budget?: { ceilingUSD?: number; stepUpCeilingUSD?: number };
    issues?: PlanIssue[];
  };
}

interface HandoffStartResponse {
  repository: string;
  issue: number;
  url: string;
  labels?: string[];
  viewer?: string;
}

interface RefusalBody {
  error?: string;
  message?: string;
}

function fmtUSD(usd: number): string {
  return `$${usd.toFixed(2)}`;
}

function outcomeWords(outcome?: string): string {
  switch (outcome) {
    case "plan-first":
      return "a plan to approve, then a pull request";
    case "review":
      return "a review of a pull request";
    case "report":
      return "findings on the issue";
    default:
      return "a pull request";
  }
}

function cardRow(label: string, value: string): HTMLElement {
  const row = el("div", { className: "workflow-row" });
  row.append(
    el("span", { className: "workflow-label", textContent: label }),
    el("span", { className: "workflow-value", textContent: value }),
  );
  return row;
}

function choiceLabel(c: WorkflowChoice, isDefault: boolean): string {
  const parts: string[] = [];
  if (isDefault) parts.push("default");
  if (c.description) parts.push(c.description);
  if (c.ceilingUSD && c.ceilingUSD > 0) parts.push(`up to ${fmtUSD(c.ceilingUSD)}`);
  return parts.length > 0 ? `${c.name} (${parts.join(", ")})` : c.name;
}

function renderPlanCard(checked: HandoffCheckResponse): HTMLElement {
  const section = el("section", { className: "workflow-card" });
  const head = el("div", { className: "workflow-head" });
  const issue = checked.plan?.issues?.[0];
  const wf = issue?.profile || checked.workflow || checked.activeChoice?.name || checked.defaultChoice?.name || "default";
  const pinned = !!issue?.profile;
  const suggested = issue?.suggestion?.profile;
  let reason = pinned ? "pinned by a maintainer" : "this repository's default";
  if (suggested && suggested !== wf) {
    reason += `; the planner suggested ${suggested}`;
  }

  let outcomeVal = outcomeWords(issue?.outcome);
  if (issue?.outcomeInferred && issue.outcome && issue.outcome !== "change") {
    outcomeVal += " (inferred)";
  }
  head.append(cardRow("Outcome", outcomeVal));
  head.append(cardRow("Workflow", `${wf} \u00b7 ${reason}`));

  const choice = checked.activeChoice?.name === wf
    ? checked.activeChoice
    : checked.defaultChoice?.name === wf
      ? checked.defaultChoice
      : checked.workflowChoices?.find((c) => c.name === wf);
  const path = choice?.path?.length
    ? choice.path
    : issue?.roles?.length
      ? [...issue.roles, "CI", "merge gate"]
      : [];
  if (path.length > 0) {
    head.append(cardRow("Will run", path.join(" \u2192 ")));
  }

  const ceiling = checked.plan?.budget?.ceilingUSD || choice?.ceilingUSD || 0;
  const stepUp = checked.plan?.budget?.stepUpCeilingUSD || choice?.stepUpCeilingUSD || 0;
  if (ceiling > 0) {
    let cost = `up to ${fmtUSD(ceiling)}`;
    if (stepUp > ceiling) cost += ` \u00b7 up to ${fmtUSD(stepUp)} if it steps up`;
    head.append(cardRow("Cost", cost));
  }
  if (issue?.outcomeNote) {
    head.append(el("p", { className: "hint", textContent: issue.outcomeNote }));
  }

  section.append(head);
  return section;
}

function linkGitHubNotice(): HTMLElement {
  const p = el("p", { className: "new-task-link-notice" });
  const a = el("a", { href: "/link/github", textContent: "Link your GitHub account" });
  p.append(a, " to start a task from here.");
  return p;
}

function startRefusalCopy(why?: string): string {
  switch (why) {
    case "no_write":
      return "You do not have write access to this repository on GitHub.";
    case "repository_not_offered":
      return "Starting tasks from here is not offered on this repository.";
    case "not_maintainer":
      return "Only a maintainer of this repository can start a task. File the issue on GitHub and ask a maintainer to label it factory/ready.";
    case "access_unwired":
    case "unverified":
      return "GitHub access could not be verified right now. Retry.";
    default:
      return "This task cannot be started from here.";
  }
}

async function readRefusal(r: Response): Promise<RefusalBody> {
  try {
    return (await r.json()) as RefusalBody;
  } catch {
    return {};
  }
}

// offers is each New task form's way to add repositories to its picker.
const offers = new WeakMap<HTMLElement, (names: string[]) => void>();

// offerServedRepositories adds every repository the planner serves to form's picker, not only the
// rooms' ones: a developer sees none of those on their first day (developer review gap 9). A list
// that cannot be read leaves the picker as it is.
export async function offerServedRepositories(form: HTMLElement, get: typeof fetch = fetch): Promise<void> {
  const offer = offers.get(form);
  if (!offer) return;
  try {
    const r = await api("/api/handoffs/repositories", {}, get);
    if (r.ok) offer(((await r.json()) as { repositories?: string[] }).repositories ?? []);
  } catch {
    // the rooms' repositories stay offered
  }
}

// newTaskForm builds the New task planner and confirmation form over POST /api/handoffs,
// POST /api/handoffs/{id}/check and POST /api/handoffs/{id}/start.
export function newTaskForm(get: typeof fetch = fetch, repos: string[] = []): HTMLElement {
  const root = el("section", { className: "new-task" });
  root.setAttribute("aria-label", "New task");

  const form = el("form", { className: "new-task-form" });
  const text = named(
    el("textarea", { name: "text", rows: 3, maxLength: 16384, placeholder: "What should change?", required: true }),
    "What should change?",
  );
  const repository = named(el("select", { name: "repository" }), "repository");
  const offer = (names: string[]): void => {
    // GitHub names are case-insensitive: a room's spelling wins over the served list's lower case.
    const byKey = new Map<string, string>();
    for (const o of [...repository.options].slice(1)) byKey.set(o.value.toLowerCase(), o.value);
    for (const r of names) if (r && !byKey.has(r.toLowerCase())) byKey.set(r.toLowerCase(), r);
    const keep = repository.value;
    repository.replaceChildren(el("option", { value: "", textContent: "Auto (let Maku choose)" }));
    for (const key of [...byKey.keys()].sort()) {
      const r = byKey.get(key) as string;
      repository.append(el("option", { value: r, textContent: r }));
    }
    repository.value = keep;
  };
  offer(repos);
  offers.set(root, offer);
  const planBtn = el("button", { type: "submit", className: "primary", textContent: "Plan" });
  planBtn.dataset.act = "plan-task";

  const controlsRow = el("div", { className: "new-task-row" });
  controlsRow.append(labelled("Repository", repository), planBtn);
  form.append(labelled("What should change?", text, "field new-task-text"), controlsRow);

  const notice = el("p", { className: "notice" });
  notice.setAttribute("role", "status");
  const preview = el("div", { className: "new-task-preview", hidden: true });
  root.append(form, notice, preview);

  let currentId = "";
  let currentChecked: HandoffCheckResponse | undefined;
  let busy = false;

  const setBusy = (on: boolean) => {
    busy = on;
    planBtn.disabled = on;
  };

  const showNotice = (msg: string) => {
    notice.textContent = msg;
  };

  const renderNotLinked = () => {
    preview.hidden = false;
    preview.replaceChildren(linkGitHubNotice());
  };

  const renderQuestion = (h: HandoffCreateResponse) => {
    preview.hidden = false;
    const box = el("div", { className: "new-task-question" });
    const q = h.question!;
    box.append(el("p", { className: "new-task-q-text", textContent: q.text }));
    if (q.recommended) {
      box.append(el("p", { className: "hint", textContent: `Recommended: ${q.recommended}` }));
    }
    const answerInput = named(el("input", { name: "answer", type: "text", placeholder: "Your answer" }), "Answer");
    const answerBtn = button("Answer", "answer-handoff", () => {
      const val = answerInput.value.trim();
      if (!val || busy) return;
      void continueHandoff({ id: h.id, answer: val });
    });
    answerBtn.classList.add("primary");
    const acceptBtn = button("Accept recommendation", "accept-rest", () => {
      if (busy) return;
      void continueHandoff({ id: h.id, acceptRest: true });
    });
    const row = el("div", { className: "new-task-q-actions" });
    row.append(answerInput, answerBtn, acceptBtn);
    box.append(row);

    const kids: HTMLElement[] = [box];
    if (h.title || h.body) {
      kids.push(renderDraft(h.repository, h.title, h.body));
    }
    preview.replaceChildren(...kids);
  };

  const renderDraft = (repo?: string, title?: string, body?: string): HTMLElement => {
    const draft = el("div", { className: "new-task-draft" });
    if (repo) draft.append(el("p", { className: "hint new-task-repo", textContent: repo }));
    if (title) draft.append(el("h3", { className: "new-task-title", textContent: title }));
    if (body) draft.append(el("pre", { className: "new-task-body", textContent: body }));
    return draft;
  };

  const renderChecked = (checked: HandoffCheckResponse) => {
    currentChecked = checked;
    preview.hidden = false;
    const kids: HTMLElement[] = [renderPlanCard(checked)];
    const issue = checked.plan?.issues?.[0];
    const repoName = checked.repository || checked.plan?.repository;
    const title = checked.title || issue?.title;
    const body = checked.body || issue?.body;
    kids.push(renderDraft(repoName, title, body));

    const opts = el("div", { className: "new-task-options" });
    const choices = checked.workflowChoices ?? [];
    const defaultWF = checked.defaultChoice?.name || checked.workflow || "";
    const activeWF = issue?.profile || checked.workflow || defaultWF;
    if (checked.maintainer && choices.length > 1) {
      const wfSelect = named(el("select", { name: "workflow" }), "Workflow");
      for (const c of choices) {
        const opt = el("option", { value: c.name, textContent: choiceLabel(c, c.name === defaultWF) });
        if (c.name === activeWF) opt.selected = true;
        wfSelect.append(opt);
      }
      wfSelect.onchange = () => {
        if (busy) return;
        void checkHandoff(checked.id, { workflow: wfSelect.value });
      };
      opts.append(labelled("Workflow", wfSelect));
    }

    const planFirstInput = named(
      el("input", { type: "checkbox", name: "planFirst", checked: issue?.outcome === "plan-first" }),
      "Plan first",
    );
    planFirstInput.onchange = () => {
      if (busy) return;
      void checkHandoff(checked.id, { outcome: planFirstInput.checked ? "plan-first" : "change" });
    };
    const planFirstLabel = el("label", { className: "new-task-check" });
    planFirstLabel.append(planFirstInput, " Plan first");
    opts.append(planFirstLabel);
    kids.push(opts);

    const actions = el("div", { className: "new-task-actions" });
    const unlinked = checked.linked === false || checked.canStartWhy === "not_linked";
    if (unlinked) {
      actions.append(linkGitHubNotice());
    } else if (!checked.canStart) {
      actions.append(el("p", { className: "hint", textContent: startRefusalCopy(checked.canStartWhy) }));
    } else {
      const startBtn = button("Start", "start-task", () => {
        if (busy || !currentChecked) return;
        void startHandoff(currentChecked.id, currentChecked.hash);
      });
      startBtn.classList.add("primary");
      actions.append(startBtn);
    }
    kids.push(actions);
    preview.replaceChildren(...kids);
  };

  const checkHandoff = async (id: string, body: { workflow?: string; outcome?: string } = {}) => {
    setBusy(true);
    showNotice("");
    try {
      const r = await api(`/api/handoffs/${encodeURIComponent(id)}/check`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      }, get);
      if (!r.ok) {
        const ref = await readRefusal(r);
        if (ref.error === "not_linked") {
          renderNotLinked();
          return;
        }
        showNotice(ref.message || `Could not check the plan (${r.status}).`);
        return;
      }
      const checked = (await r.json()) as HandoffCheckResponse;
      renderChecked(checked);
    } catch (err) {
      if (!(err instanceof SignInRequired)) {
        showNotice(`Could not check the plan: ${err instanceof Error ? err.message : String(err)}`);
      }
    } finally {
      setBusy(false);
    }
  };

  const continueHandoff = async (payload: Record<string, unknown>) => {
    setBusy(true);
    showNotice("");
    try {
      const r = await api("/api/handoffs", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      }, get);
      if (!r.ok) {
        const ref = await readRefusal(r);
        if (ref.error === "not_linked") {
          renderNotLinked();
          return;
        }
        showNotice(ref.message || `Could not plan the task (${r.status}).`);
        return;
      }
      const h = (await r.json()) as HandoffCreateResponse;
      currentId = h.id;
      if (!h.ready && h.question) {
        renderQuestion(h);
        return;
      }
      await checkHandoff(h.id);
    } catch (err) {
      if (!(err instanceof SignInRequired)) {
        showNotice(`Could not plan the task: ${err instanceof Error ? err.message : String(err)}`);
      }
    } finally {
      setBusy(false);
    }
  };

  const startHandoff = async (id: string, hash: string) => {
    setBusy(true);
    showNotice("");
    try {
      const r = await api(`/api/handoffs/${encodeURIComponent(id)}/start`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ hash }),
      }, get);
      if (!r.ok) {
        const ref = await readRefusal(r);
        if (ref.error === "not_linked") {
          renderNotLinked();
          return;
        }
        showNotice(ref.message || `Could not start the task (${r.status}).`);
        return;
      }
      const started = (await r.json()) as HandoffStartResponse;
      const done = el("p", { className: "new-task-filed" });
      done.append("Filed and started ", safeLink(`#${started.issue}`, started.url), ` on ${started.repository}.`);
      preview.replaceChildren(done);
      text.value = "";
      currentId = "";
      currentChecked = undefined;
    } catch (err) {
      if (!(err instanceof SignInRequired)) {
        showNotice(`Could not start the task: ${err instanceof Error ? err.message : String(err)}`);
      }
    } finally {
      setBusy(false);
    }
  };

  form.onsubmit = (e) => {
    e.preventDefault();
    if (busy) return;
    const prompt = text.value.trim();
    if (!prompt) {
      showNotice("Describe what should change first.");
      return;
    }
    const payload: Record<string, string> = { text: prompt };
    if (repository.value) payload.repository = repository.value;
    currentId = "";
    currentChecked = undefined;
    void continueHandoff(payload);
  };

  // Expose the active handoff id for debugging if needed.
  void currentId;
  return root;
}
