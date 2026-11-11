# Issue tracker: Jira

Issues and specs live in Jira project `EPMDEDP`. Use the `mcp__mcp-atlassian__jira_*` tools. No `gh issue` calls: the GitHub repo has no issue tracker in use.

## Conventions

- **Label**: every issue a skill creates carries the label `krci-agent-dispatcher`.
- **Spec**: issue type `Story`. Link it to its parent Task with a `relates to` link when one is named.
- **Ticket**: issue type `Sub-task` with `{"parent": "<spec key>"}` in `additional_fields`. Sub-tasks have no children.
- **Create**: `jira_create_issue` with `project_key: EPMDEDP`, Markdown `description`, `additional_fields: {"labels": ["krci-agent-dispatcher", ...]}`.
- **Read**: `jira_get_issue` with `include: comments`.
- **List**: `jira_search`, JQL `project = EPMDEDP AND labels = krci-agent-dispatcher AND statusCategory != Done AND status != "In review"`, plus `labels = <triage label>` or `parent = <spec key>`.
- **Blocking**: `jira_create_issue_link` with `link_type: Blocks` (id 12300): the blocker `blocks` the child. After the first link of a session, `jira_get_issue` the child and confirm it shows `is_blocked_by <blocker>`; if reversed, swap `inward_issue_key` and `outward_issue_key` for the rest of the session. Remove a wrong edge with `jira_remove_issue_link`, taking the `link_id` from the issue's `issuelinks`. Also put `Blocked by: EPMDEDP-<n>, EPMDEDP-<n>` as the first line of the child description.
- **Count open blockers**: read each key in the `Blocked by:` line and the `is_blocked_by` links; a ticket is startable when every blocker is `In review` (its PR is merged) or has `statusCategory = Done`.
- **Comment**: `jira_add_comment`.
- **Labels**: `jira_update_issue` with `fields: {"labels": [...]}`. Send the full list; it replaces the existing labels.
- **Close** (not done through a PR: `wontfix`, duplicate, obsolete): comment the reason, then `jira_get_transitions` and `jira_transition_issue` to a status in the Done category.
- **Commit titles**: format in `CLAUDE.md` Conventions; `EPMDEDP-<n>` is the ticket being worked.
- **Issue references**: an `EPMDEDP-<n>` prefix in a commit title is the issue reference. Fetch it from Jira, not as a GitHub `#<n>`.

## Pull requests

Code review happens in GitHub pull requests on `KubeRocketCI/krci-agent-dispatcher` through the `gh` CLI. Issues stay in Jira.

- **Title**: commit-title format.
- **Body**: the `/pr` template (Summary, Evidence, Merge Danger), preceded by one line `Jira: EPMDEDP-<n>` (the ticket, or the spec for a PR that covers a whole spec). Pass it inline: `gh pr create --body-file - <<'EOF'`.
- **Draft PR**: allowed for a spec built on one integration branch.
- **After merge**: no Jira change. A ticket can have several PRs; a person moves its status.
- **PRs as a request surface: no.** `/triage` reads Jira issues only.

## When a skill says "publish to the issue tracker"

Create a Jira issue as above.

## When a skill says "fetch the relevant ticket"

`jira_get_issue` with `include: comments`.

## Wayfinding operations

Used by `/wayfinder`. The map is one issue; tickets are its sub-tasks.

- **Map**: a `Story` labelled `wayfinder:map` and `krci-agent-dispatcher`; its description holds Notes / Decisions-so-far / Fog.
- **Child ticket**: a `Sub-task` of the map, labelled `wayfinder:<type>` (`research`/`prototype`/`grilling`/`task`).
- **Blocking**: as under Conventions.
- **Frontier query**: JQL `parent = <map> AND statusCategory != Done AND status != "In review" AND assignee is EMPTY ORDER BY created ASC`; drop tickets with an open blocker; first wins.
- **Claim**: `jira_update_issue` with `fields: {"assignee": "<user>"}`, the session's first write.
- **Resolve**: comment the answer, transition to Done, append a pointer (gist + key) to the map's Decisions-so-far.
