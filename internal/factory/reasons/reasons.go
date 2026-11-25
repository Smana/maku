// SPDX-License-Identifier: Apache-2.0

// Package reasons is the one table of human-readable task and merge-policy reason wording.
package reasons

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/factory/config"
)

// Words are the short label, long explanation and next step for one reason.
type Words struct {
	Short, Long, Next string
}

var table = map[string]Words{
	"Disrupted":                     {Short: "the sandbox's node was reclaimed or dra…", Long: "the sandbox's node was reclaimed or drained", Next: ""},
	"PodFailed":                     {Short: "the sandbox failed on its own", Long: "the sandbox failed on its own", Next: ""},
	"PodLost":                       {Short: "the sandbox was lost (deleted, or its n…", Long: "the sandbox was lost (deleted, or its node disappeared)", Next: ""},
	"adopt_ref_invalid":             {Short: "the adopted branch name is invalid", Long: "the adopted pull request's reference could not be read, so it was not adopted", Next: ""},
	"adopted_head_moved":            {Short: "the adopted branch moved", Long: "its author pushed a new head after the shadow decision, so it is decided again", Next: ""},
	"adoption_unread":               {Short: "the adopted pull request is unreadable", Long: "who authored its commits and pushed its branch could not be read", Next: ""},
	"agent_error":                   {Short: "the agent stopped on an error", Long: "the agent stopped on an error", Next: ""},
	"agent_finished":                {Short: "the agent finished its work", Long: "the agent finished its work", Next: ""},
	"agent_stuck":                   {Short: "the agent said it was stuck", Long: "the agent reported that it was stuck", Next: ""},
	"approval_required":             {Short: "a maintainer approves this change", Long: "it changes a file its class asks a maintainer to approve", Next: ""},
	"approval_stale":                {Short: "the approval is of an older head", Long: "a maintainer's approval is of an older head: approve the current one", Next: ""},
	"auto_merge_cap":                {Short: "today's auto-merge cap is reached", Long: "today's auto-merge cap is reached", Next: ""},
	"base_not_main":                 {Short: "the pull request does not target main, …", Long: "the pull request does not target main, so the factory does not gate or merge it", Next: ""},
	"branch_not_allowed":            {Short: "the branch is not allowed", Long: "its branch is not one the class adopts", Next: ""},
	"budget-family":                 {Short: "the task family is out of budget", Long: "the cap of the task and its parts has no room left for another part's run", Next: ""},
	"budget-fleet":                  {Short: "the agent fleet's daily token budget is…", Long: "the agent fleet's daily token budget is spent", Next: ""},
	"budget-person":                 {Short: "the daily budget in dollars of the pers…", Long: "the daily budget in dollars of the person its next run is charged to is spent", Next: ""},
	"budget-principal":              {Short: "the factory's daily budget in dollars i…", Long: "the factory's daily budget in dollars is spent", Next: ""},
	"budget-run":                    {Short: "the run spent its token budget", Long: "the run spent its token budget", Next: ""},
	"budget-task":                   {Short: "the task is out of budget", Long: "the task's token budget has no room left for another run", Next: ""},
	"budget_calls":                  {Short: "the scoper hit its tool-call limit", Long: "the scoper reached its tool-call limit before handing off", Next: ""},
	"cancelled":                     {Short: "its owner or a maintainer cancelled it …", Long: "its owner or a maintainer cancelled it from its room", Next: ""},
	"changes_requested":             {Short: "changes were requested", Long: "a reviewer requested changes, and the factory does not merge over them", Next: ""},
	"checks_truncated":              {Short: "the pull request's check rollup was tru…", Long: "the pull request's check rollup was truncated, so not every check could be read", Next: ""},
	"child_dropped":                 {Short: "a part was dropped", Long: "a part of the split was closed without merging, or its merge was reverted", Next: ""},
	"children_merged":               {Short: "every part merged", Long: "every part of the split merged", Next: ""},
	"ci_cancelled":                  {Short: "a check was cancelled", Long: "a required check was cancelled and re-running it did not settle it", Next: ""},
	"ci_pending":                    {Short: "CI is running", Long: "CI has not finished", Next: "nothing to do: the task goes on when CI finishes"},
	"ci_red":                        {Short: "CI is red", Long: "CI stayed red after the fix runs", Next: ""},
	"ci_wait_expired":               {Short: "CI took too long", Long: "a check it waits on never reported within merge.awaitingCIFor", Next: ""},
	"class_mismatch":                {Short: "does not match the policy's diff shape", Long: "the diff is not the class the triage predicted", Next: ""},
	"class_paused":                  {Short: "auto-merge of this class is paused afte…", Long: "auto-merge of this class is paused after a revert, a follow-up fix or a regression in its recent merges", Next: ""},
	"class_unarmed":                 {Short: "a maintainer merges this policy", Long: "the factory does not merge this class: a maintainer reviews and merges it", Next: ""},
	"closes_parent":                 {Short: "it would close the parent issue", Long: "it would close the parent issue, which only the last part may", Next: ""},
	"committed_no_pr":               {Short: "committed, never pushed: work lost", Long: "the agent committed but never pushed, so its work was lost with the sandbox and no pull request was opened", Next: ""},
	"contract_check_not_run":        {Short: "the contract check did not run to a pas…", Long: "the contract check did not run to a passing conclusion (skipped or neutral)", Next: ""},
	"contract_check_unset":          {Short: "the contract has no check to prove it", Long: "the task has a test contract but the repository no longer names merge.contractCheck", Next: ""},
	"contract_disputed":             {Short: "the tests are disputed", Long: "the implementer says the tester's tests are wrong; a maintainer decides", Next: ""},
	"contract_invalid":              {Short: "the tester's branch is not valid", Long: "the tester's branch was not the tester's alone, or its contract commit was malformed", Next: ""},
	"contract_modified":             {Short: "the pull request changes the tests", Long: "the pull request changes a test the tester wrote as the task's contract", Next: ""},
	"contract_push_unrecorded":      {Short: "waiting for GitHub to see the tests", Long: "GitHub has not yet recorded the tester's push", Next: "nothing to do: the task goes on when GitHub answers"},
	"contract_too_large":            {Short: "the contract is too big to record", Long: "the tester's contract names more files or test ids than a task records, or one of them is too long", Next: ""},
	"contract_unread":               {Short: "reading the changes to the tests", Long: "the factory has not yet read which files changed since the test contract", Next: "nothing to do: the task goes on when GitHub answers"},
	"coordination_lineage":          {Short: "this coordination child carries another…", Long: "this coordination child carries another lineage than its plan's", Next: ""},
	"coordination_parent":           {Short: "this is a coordination parent's issue: …", Long: "this is a coordination parent's issue: it coordinates its children and never becomes a work task", Next: ""},
	"coordination_parent_missing":   {Short: "this coordination child's parent is not…", Long: "this coordination child's parent is not a filed coordination", Next: ""},
	"coordination_scope_changed":    {Short: "this coordination child's issue no long…", Long: "this coordination child's issue no longer matches its confirmed plan: a change comes through a plan revision its maintainer authorizes again", Next: ""},
	"cross_repository_lineage":      {Short: "this coordination child's task, room, i…", Long: "this coordination child's task, room, issue and plan do not name one repository", Next: ""},
	"daily_task_cap":                {Short: "the factory has reached its daily task …", Long: "the factory has reached its daily task cap", Next: ""},
	"deadline":                      {Short: "the run hit its time limit", Long: "the run hit its wall-clock limit", Next: ""},
	"deleted":                       {Short: "the run's claim was deleted", Long: "the run's claim was deleted", Next: ""},
	"dependency_dropped":            {Short: "a dependency was closed unmerged", Long: "an issue this task depends on was closed without a merged pull request", Next: ""},
	"diff_unreadable":               {Short: "a changed file could not be read, so th…", Long: "a changed file could not be read, so the gate cannot prove the diff", Next: "a maintainer inspects the unreadable file and merges by hand"},
	"diff_unrecorded":               {Short: "the recorded diff is unavailable", Long: "the pull request's immutable diff or original oracle findings were not recorded, so a backtest cannot re-decide it", Next: "inspect the pull request directly; unknown is not unchanged"},
	"discarded":                     {Short: "its owner or a maintainer discarded it …", Long: "its owner or a maintainer discarded it from its room: its pull request is closed, its branch deleted, and this issue closed as not planned (a GitHub App cannot delete an issue; a repository admin can)", Next: ""},
	"edited_after_label":            {Short: "the issue was edited after it was label…", Long: "the issue was edited after it was labelled", Next: ""},
	"escalation_expired":            {Short: "it waited past the escalation time limi…", Long: "it waited past the escalation time limit for a maintainer", Next: ""},
	"family_dropped":                {Short: "the split was dropped", Long: "the split's family was dropped because another part was dropped or reverted", Next: ""},
	"file_added_or_deleted":         {Short: "adds, deletes or renames a file", Long: "it changes a file in a way other than editing its content (an add, delete, rename, copy or mode change), which its class does not allow", Next: ""},
	"file_not_allowed":              {Short: "changes a file outside the allowed files", Long: "it changes a file outside its class's allowed files", Next: ""},
	"file_protected":                {Short: "touches a protected file", Long: "it changes a file its class protects", Next: ""},
	"findings_posted":               {Short: "the findings were posted on the issue", Long: "the read-only investigation posted its findings on the issue", Next: ""},
	"foreign_room":                  {Short: "a room of the task's name exists and is…", Long: "a room of the task's name exists and is not the factory's", Next: ""},
	"foreign_trailer":               {Short: "its head commit comes from another task's run", Long: "its head commit comes from another task's run", Next: ""},
	"fork_pull_request":             {Short: "the pull request is from a fork", Long: "it comes from a fork, which the factory never adopts", Next: ""},
	"gate_path":                     {Short: "touches a gate path", Long: "it touches a gate path, so an agent can never merge it", Next: ""},
	"handoff_unconfirmed":           {Short: "the web start could not be confirmed", Long: "the web start could not be confirmed with the broker; a maintainer can label the issue factory/ready", Next: ""},
	"head_moved":                    {Short: "a new commit landed since the decision,…", Long: "a new commit landed since the decision, so the merge was refused and it is decided again", Next: ""},
	"head_unreported":               {Short: "no run reported the head", Long: "the task's implementer never reported its current head in the room: no handoff of its runs names that commit", Next: ""},
	"held":                          {Short: "on hold (factory/hold)", Long: "a maintainer put this pull request on hold (factory/hold)", Next: ""},
	"holdout_pending":               {Short: "waiting for holdout checks", Long: "a holdout check has not passed yet", Next: "nothing to do: the task goes on when the holdout checks report"},
	"holdout_red":                   {Short: "a holdout check failed", Long: "a holdout check its class requires did not succeed (failed, cancelled, neutral, skipped, or not posted by GitHub Actions), or its checks could not all be read", Next: ""},
	"human_commit_in_pr":            {Short: "a human commit is in the pull request", Long: "it holds a commit no run of the task reported", Next: ""},
	"invalid_draft":                 {Short: "the proposal does not follow the issue …", Long: "the proposal does not follow the issue template (a title `<area>: <what is wrong>`, then the sections What is wrong, What it should be and Acceptance check)", Next: ""},
	"issue_deleted":                 {Short: "its issue was deleted on GitHub", Long: "its issue was deleted on GitHub", Next: ""},
	"judge_impossible":              {Short: "the judge found it cannot be done", Long: "the completion judge found the task cannot be done as asked", Next: ""},
	"kill_switch":                   {Short: "the factory's kill switch is engaged", Long: "the factory's kill switch is engaged", Next: ""},
	"label_missing":                 {Short: "the required label is missing", Long: "it does not carry the label its class requires", Next: ""},
	"label_not_adopted":             {Short: "the wrong login applied the label", Long: "its required label was not applied by a login the class adopts", Next: ""},
	"labels_truncated":              {Short: "the pull request labels could not all b…", Long: "the pull request labels could not all be read, so the hold state is unknown", Next: "a maintainer checks `factory/hold` and merges by hand"},
	"ledger_truncated":              {Short: "the merge ledger read is incomplete", Long: "the merge ledger read is incomplete (a full page of 100 labelled pull requests), so the factory does not arm", Next: ""},
	"live_reference":                {Short: "the proposal holds a link, a mention or…", Long: "the proposal holds a link, a mention or a reference the factory cannot make inert, so it stays in the room; a multi-line code span, a table row with more cells than its header or an unclosed code fence is the usual cause", Next: ""},
	"loop_unsupported":              {Short: "the cluster lacks loop support", Long: "the cluster does not support the loop policy or repository checks yet, so no run started", Next: ""},
	"main_red":                      {Short: "main's CI went red after the merge", Long: "main's CI went red after the merge", Next: ""},
	"maintainer_approved":           {Short: "a maintainer approved it", Long: "a maintainer approved it", Next: ""},
	"merge_gate_off":                {Short: "the merge gate is off", Long: "no merge class is live, so the factory does not gate this pull request", Next: ""},
	"merged":                        {Short: "the pull request was merged", Long: "the pull request was merged", Next: ""},
	"merged_unverified":             {Short: "merged, and main's CI on the merge comm…", Long: "merged, and main's CI on the merge commit gave no result within twice merge.verifyFor (a check was cancelled or never finished)", Next: ""},
	"merged_verified":               {Short: "merged, and main stayed green for 30 mi…", Long: "merged, and main stayed green for 30 minutes", Next: ""},
	"model_unavailable_auth":        {Short: "the model provider refused: auth", Long: "the model provider refused: authentication (the API key was rejected)", Next: ""},
	"model_unavailable_billing":     {Short: "the model provider refused: billing", Long: "the model provider refused: billing (insufficient balance or no resource package)", Next: ""},
	"model_unavailable_quota":       {Short: "the model provider refused: quota", Long: "the model provider refused: quota or rate limit", Next: ""},
	"needs_author":                  {Short: "the issue's author must answer", Long: "the scoper needs an answer from the issue's author before any code is written", Next: "the issue author answers, then a maintainer re-applies `factory/ready`"},
	"no_action":                     {Short: "the triager found nothing to change", Long: "the triager found nothing to change", Next: ""},
	"no_approving_verdict":          {Short: "the reviewer did not approve it", Long: "the reviewer did not approve it", Next: ""},
	"no_policy_matched":             {Short: "no merge policy holds on this diff", Long: "no merge policy of the repository holds on this diff: a maintainer reviews and merges it", Next: ""},
	"no_pr":                         {Short: "the agent opened no pull request", Long: "the agent opened no pull request", Next: ""},
	"no_proposal":                   {Short: "the triager's run made no proposal to p…", Long: "the triager's run made no proposal to publish", Next: ""},
	"no_verdict":                    {Short: "the review gave no verdict", Long: "the last review ended without a verdict the factory can act on", Next: ""},
	"not_adopted_author":            {Short: "the pull request's author is not adopted", Long: "it is not authored by a login the class adopts", Next: ""},
	"not_agent_authored":            {Short: "it is not the agents' pull request", Long: "it is not the agents' pull request", Next: ""},
	"not_mergeable":                 {Short: "the pull request is not mergeable", Long: "GitHub does not allow merging it as it stands", Next: ""},
	"not_met":                       {Short: "the acceptance checks are unmet", Long: "the completion judge still found the acceptance checks unmet after the last continuation, or a continuation did not finish within its time box", Next: ""},
	"not_owned":                     {Short: "the repository does not name this factory", Long: "this repository's .github/agent-factory.yaml does not name this factory", Next: ""},
	"oracle_finding_open":           {Short: "an oracle finding is still open", Long: "a link its task was opened to fix is still a link target in the head, or the file holding it was not changed", Next: ""},
	"oracle_unrequested":            {Short: "changes an unrequested link target", Long: "it moves a link target the task's findings did not ask to move, or moves one anywhere but the exact snapshot its finding gave", Next: ""},
	"outcome_conflict":              {Short: "two outcome labels applied at once", Long: "more than one outcome label was applied on the issue, so the task runs as change", Next: ""},
	"outside_declared_files":        {Short: "touched a file outside declared files", Long: "this split part modified a file outside its declared files; the family returns to one part at a time", Next: ""},
	"paused":                        {Short: "merge policies are paused", Long: "automatic policy decisions are paused by the repository operator", Next: "a maintainer reads the pause reason, then resumes or merges by hand"},
	"overlap_active_duplicate":      {Short: "an active task is already pursuing the …", Long: "an active task is already pursuing the same outcome", Next: ""},
	"parent_missing":                {Short: "the parent task is gone", Long: "its parent task no longer exists", Next: ""},
	"parent_not_split":              {Short: "the parent is no longer waiting", Long: "its parent task is no longer waiting on its parts", Next: ""},
	"patch_too_large":               {Short: "exceeds the file or line limit", Long: "it changes more files or lines than its class allows", Next: ""},
	"plan_review":                   {Short: "waiting on plan approval", Long: "the scoper proposed a plan and waits for the author or a maintainer to approve, revise, or split it", Next: "comment `/factory plan approve`, `/factory plan revise <comment>`, or `/factory plan split`"},
	"plan_revision_limit":           {Short: "plan revised once; maintainer decides", Long: "the plan was already revised once; a second revision goes to a maintainer", Next: "a maintainer reviews the plan and chooses `/factory retry` or edits the issue"},
	"pod_lost":                      {Short: "the sandbox was lost", Long: "the sandbox was lost (spot reclaim or eviction)", Next: ""},
	"policy_absent":                 {Short: "no merge policy evaluation reported", Long: "policy-bot has not evaluated the pull request", Next: ""},
	"policy_invalid":                {Short: "the merge policy is invalid", Long: "the repository's merge policy is invalid, so auto-merge is paused", Next: ""},
	"policy_pending":                {Short: "the merge policy is not ready", Long: "the policy needs a maintainer's approval", Next: ""},
	"policy_unavailable":            {Short: "waiting to read the merge policy", Long: "the repository's merge policy could not be read recently, so auto-merge waits", Next: ""},
	"pr_closed":                     {Short: "the pull request was closed", Long: "the pull request was closed", Next: ""},
	"principal_unknown":             {Short: "the API principal that requested it is …", Long: "the API principal that requested it is no longer in the factory's configuration", Next: ""},
	"private_code_unacknowledged":   {Short: "this repository is private code; a fact…", Long: "this repository is private code. " + config.PrivateCodeSentence + " A maintainer of the factory sets it on the Repository, then labels the issue again", Next: ""},
	"private_repository":            {Short: "This factory runs as a demo, which serv…", Long: "This factory runs as a demo, which serves public repositories only: in the demo, an agent can read the model key and the GitHub token", Next: ""},
	"proposal_ready":                {Short: "the triager proposed a change for a mai…", Long: "the triager proposed a change for a maintainer to publish (factory/publish)", Next: ""},
	"push_awaiting_approval":        {Short: "a push waits for a maintainer", Long: "the agent's push changes a protected path and waits for a maintainer's approval in the room or in Slack", Next: ""},
	"push_refused":                  {Short: "the factory refused the push", Long: "the factory refused the agent's push, so its work never reached the branch and no pull request was opened", Next: ""},
	"push_unrecorded":               {Short: "GitHub has not recorded the push", Long: "GitHub has not recorded the push of its head", Next: "nothing to do: the task goes on when GitHub records the push"},
	"pushed_no_pr":                  {Short: "pushed, but no pull request opened", Long: "the agent pushed its branch but opened no pull request", Next: ""},
	"redacted":                      {Short: "the proposal holds something that looks…", Long: "the proposal holds something that looks like a secret, so it stays in the room", Next: ""},
	"reentered_approved":            {Short: "approved, decided again", Long: "a maintainer approved the head it was handed off at, so the gate decides it again", Next: ""},
	"reentered_day_turned":          {Short: "a new day, decided again", Long: "the UTC day turned, so the cap is counted again", Next: ""},
	"reentered_head_reported":       {Short: "the room now reports its head as a comm…", Long: "the room now reports its head as a commit of the task's run", Next: ""},
	"reentered_new_head":            {Short: "its author pushed a new head, which is …", Long: "its author pushed a new head, which is decided from the start", Next: ""},
	"reentered_policy":              {Short: "policy-bot now passes the head it was h…", Long: "policy-bot now passes the head it was handed off at", Next: ""},
	"reentered_unheld":              {Short: "a maintainer removed factory/hold, so t…", Long: "a maintainer removed factory/hold, so the gate decides it again", Next: ""},
	"reentered_unpaused":            {Short: "the pause lifted, decided again", Long: "the class's pause lifted, so the gate decides it again", Next: ""},
	"release_age_pending":           {Short: "the release is too new", Long: "the release age check has not passed yet", Next: "nothing to do: the task goes on when the release age check passes"},
	"release_age_red":               {Short: "the release age check failed", Long: "the release age check failed, or was not posted by the adopted login", Next: ""},
	"required_stage_missing":        {Short: "a required stage could not run", Long: "a stage required by the merge policy could not run on this repository", Next: ""},
	"rereview_verdict_stale":        {Short: "the factory moved its head after the ap…", Long: "the factory moved its head after the approval, so it is reviewed again", Next: ""},
	"resume_budget":                 {Short: "no budget to resume the task", Long: "the sandbox was lost and the task's token budget cannot cover another run", Next: ""},
	"resumes_exhausted":             {Short: "the sandbox was lost again and the auto…", Long: "the sandbox was lost again and the automatic resumes are used up", Next: ""},
	"revert_requested":              {Short: "a maintainer asked for a revert", Long: "a maintainer asked for a revert", Next: ""},
	"review_posted":                 {Short: "the review verdict was posted on the PR", Long: "the read-only reviewer posted its verdict on the pull request", Next: ""},
	"review_ref_invalid":            {Short: "the review pull request ref is invalid", Long: "the review task does not name a valid pull request number", Next: ""},
	"review_rounds_exhausted":       {Short: "the reviewer still wants changes", Long: "the reviewer still asked for changes after the last review round", Next: ""},
	"reviewer_verdict_missing":      {Short: "no reviewer read this change", Long: "no reviewer read this change, and its policy has no shape to check it", Next: ""},
	"revoked":                       {Short: "the run was stopped by hand", Long: "the run was stopped by hand", Next: ""},
	"room_log_too_long":             {Short: "the room's log was too long to read to …", Long: "the room's log was too long to read to its end", Next: ""},
	"room_log_unreadable":           {Short: "the room's log, which holds the review …", Long: "the room's log, which holds the review verdict, could not be read", Next: ""},
	"rooms_crd_outdated":            {Short: "the cluster's Rooms CRD is older than t…", Long: "the cluster's Rooms CRD is older than the factory and refuses a system: member; an operator must refresh the CRD", Next: ""},
	"run_lost":                      {Short: "the run disappeared", Long: "the run disappeared", Next: ""},
	"run_unschedulable":             {Short: "the cluster never admitted the run", Long: "the cluster never admitted the task's run in time", Next: ""},
	"scope_changed":                 {Short: "the revision changed the task's scope", Long: "the revision took a path the task names out of the pull request: a maintainer must confirm the scope change", Next: ""},
	"scoper_failed":                 {Short: "the scope was not checked", Long: "the scoper failed before it checked the scope (scope not checked), so the implementer was not started", Next: ""},
	"secret_scan_red":               {Short: "the secret scan found a credential", Long: "the secret scan found a live credential in the pull request", Next: "a maintainer revokes the leaked key and closes the pull request; the factory does not retry"},
	"shadow_would_arm":              {Short: "it would merge, the gate is in shadow", Long: "it would auto-merge, and the merge gate is in shadow", Next: ""},
	"shadow_would_require_approval": {Short: "a shadow policy would merge, except a p…", Long: "a shadow policy would merge, except a path needs a maintainer approval", Next: "a maintainer approves the current head; this is not clean promotion evidence"},
	"split_proposed":                {Short: "a split is proposed", Long: "the scoper proposes to split the task into parts, and a maintainer decides", Next: "a maintainer comments `/factory split` to approve the parts, or `/factory retry` to run the task whole"},
	"stale":                         {Short: "the pull request had no maintainer acti…", Long: "the pull request had no maintainer activity (a review, a comment or a push) for 14 days", Next: ""},
	"stalled":                       {Short: "the run made no commit", Long: "the run made no commit within its step limit, a judge continuation added none within its time box, or two continuations in a row added none", Next: ""},
	"steering_path":                 {Short: "changes a steering note", Long: "it changes a steering note, which only a maintainer on the default branch may do", Next: ""},
	"stopped_by_annotation":         {Short: "an operator stopped it", Long: "an operator stopped it", Next: ""},
	"stopped_by_label":              {Short: "a maintainer applied factory/stop", Long: "a maintainer applied factory/stop", Next: ""},
	"stopped_by_slack":              {Short: "a maintainer stopped it from Slack", Long: "a maintainer stopped it from Slack", Next: ""},
	"stuck":                         {Short: "the run showed no activity", Long: "the run showed no activity for 10 minutes", Next: ""},
	"superseded":                    {Short: "a new factory/ready replaced it", Long: "a new factory/ready replaced it", Next: ""},
	"task_active":                   {Short: "a task for this issue is still running", Long: "a task for this issue is still running", Next: ""},
	"task_not_ended":                {Short: "the task is still running, and its prop…", Long: "the task is still running, and its proposal is final only once it ends", Next: ""},
	"text_too_long":                 {Short: "the issue text is longer than the facto…", Long: "the issue text is longer than the factory accepts (14 KiB)", Next: ""},
	"unauthorised_labeller":         {Short: "only a maintainer's factory/ready start…", Long: "only a maintainer's factory/ready starts a task", Next: ""},
	"unauthorised_stopper":          {Short: "only a maintainer's factory/stop stops …", Long: "only a maintainer's factory/stop stops a task", Next: ""},
	"unsanitisable":                 {Short: "the issue text could not be made safe f…", Long: "the issue text could not be made safe for an agent to read; simplify its markup", Next: ""},
	"verdict_missing":               {Short: "it recorded no verdict", Long: "it recorded no verdict", Next: ""},
	"verdict_stale":                 {Short: "the review verdict is for an older head", Long: "the reviewer's approval was not for the pull request's current head", Next: "a maintainer reviews the current head, or the factory asks for a new review"},
	"waiting_active_tasks":          {Short: "waiting for other tasks to finish", Long: "the factory's cap on active tasks is reached", Next: ""},
	"waiting_broker_permission":     {Short: "waiting for the broker's permission", Long: "the broker does not let the factory write to rooms yet", Next: ""},
	"waiting_daily_budget":          {Short: "waiting for tomorrow's budget", Long: "the factory's daily budget in dollars is spent", Next: "nothing to do: it resumes after 00:00 UTC"},
	"waiting_dependency":            {Short: "waiting for a dependency to merge", Long: "an issue this task depends on has no merged pull request yet", Next: "nothing to do: it starts once its dependencies merge"},
	"waiting_human_driver":          {Short: "a person drives its room; the hold ends…", Long: "a person drives its room; the hold ends when they hand the driver back or their lease lapses", Next: ""},
	"waiting_review_wip":            {Short: "waiting for a review slot", Long: "the reviewers are at their limit of pull requests in review", Next: ""},
	"waiting_revision_first":        {Short: "letting a revision run first", Long: "a revision of another task runs before new intake", Next: "nothing to do: it starts once no revision waits for the slot"},
	"waiting_room_busy":             {Short: "another run in its room is still going;…", Long: "another run in its room is still going; the hold ends when that run ends", Next: ""},
	"waiting_room_log":              {Short: "waiting for the room's log", Long: "the room's log is not open on the broker yet", Next: ""},
	"waiting_run_slot":              {Short: "waiting for a run slot", Long: "the factory runs its tasks one at a time and another one has the slot", Next: ""},
}

var policyBotWords = map[string]string{
	"policy_absent":    "the merge policy has not evaluated the pull request",
	"policy_pending":   "the merge policy is not ready",
	"reentered_policy": "the merge policy now passes the head it was handed off at",
}

// For returns a reason's words. policyBot enables legacy policy-bot wording; it is off by default.
func For(code string, policyBot ...bool) Words {
	w, ok := table[code]
	if !ok {
		w.Long, w.Short = code, code
	}
	if w.Short == "" {
		w.Short = w.Long
	}
	if short := []rune(w.Short); len(short) > 40 {
		w.Short = string(short[:39]) + "…"
	}
	if w.Next == "" {
		w.Next = "a maintainer reviews the task and chooses the next step"
	}
	if replacement, legacy := policyBotWords[code]; legacy && (len(policyBot) == 0 || !policyBot[0]) {
		w.Long = replacement
	}
	return w
}

// Next returns the table's own next step, or false when the phase supplies its default.
func Next(code string) (string, bool) {
	w, ok := table[code]
	return w.Next, ok && w.Next != ""
}

// Codes lists every known reason, sorted.
func Codes() []string {
	out := make([]string, 0, len(table))
	for code := range table {
		out = append(out, code)
	}
	slices.Sort(out)
	return out
}

// Longs returns the long wording for codes that this table knows.
func Longs(codes ...string) map[string]string {
	out := make(map[string]string, len(codes))
	for _, code := range codes {
		if words, ok := table[code]; ok {
			out[code] = words.Long
		}
	}
	return out
}

// Markdown renders a documentation table from the same words the product serves.
func Markdown(codes ...string) string {
	var b strings.Builder
	b.WriteString("| Reason | Means | Next step |\n|---|---|---|\n")
	for _, code := range codes {
		w := For(code)
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", code, w.Long, w.Next)
	}
	return b.String()
}
