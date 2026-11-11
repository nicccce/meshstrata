# Meshstrata frontend contract — F00

Baseline: 2026-11-10. This is the shared contract for P01–P10. The UI is an offline
**Prototype · Demo data** workspace. P01–P10 provide interactive frontend modules. Simulated state does not verify Controller, Agent,
Mihomo, frp, certificates, authentication, DNS or network reachability.

## Ownership and entry points

- `src/app/`: router, navigation, layout, demo controls. `App.vue`: providers only.
- `src/components/`: shared presentation, Controller information and the infrastructure sandbox.
- `src/domain/index.ts`: shared entities, IDs, states, issue shape and collection types.
- `src/demo/`: deterministic seed factory and singleton reactive workspace.
- `src/composables/`: drafts/dirty guard, query state, formatting, finite simulated tasks.
- `src/locales/{en,zh-CN}.ts`: common bilingual resources; `src/i18n.ts`: namespace loader.
- `src/features/<feature>/`: pages, private components/composables, `locales/{en,zh-CN}.ts`.
- `scripts/check-foundation.mjs`: meaningful pure foundation checks, no server or API.

All reserved page entry files from task §2.2 exist with default exports. Routes
lazy-load those files. P01 owns overview; P02 nodes; P03 node-detail; P04 routes;
P05 route-editor; P06 egress; P07 subscriptions; P08 rule-profiles; P09 activity;
P10 settings and auth. Keep these entry paths. Each business task edits only its
feature directories and its own `documents/frontend-delivery/Pxx.md`. Shared
changes go to the integration session; do not overwrite app, domain, workspace,
common components or other features. F00 did not create further chats.

## Routes and navigation

Use `pageLocation(name, params?, query?)` and `entityLocation(kind, id)` from
`src/app/router.ts`, with `<RouterLink :to="...">` or `router.push(...)`.
`PageName` is exported from `app/routes.ts`. Never concatenate object URLs.
Hash history makes the physical URL `/#/nodes/hk`; no Go fallback is necessary.

| Route name | Logical path | Parameters / controlled query |
| --- | --- | --- |
| overview | /overview | — |
| nodes | /nodes | q, mode, location, status, environment |
| node-new | /nodes/new | step=basics/install/verify |
| node-detail | /nodes/:nodeId | tab=overview/protocols/dns/connectivity/routes |
| routes | /routes | q, nodeId, status |
| route-new | /routes/new | step=exit/paths/options/review, exitNodeId, draftId, panel |
| route-detail | /routes/:routeId | tab=overview/measurements/resources/history, panel |
| route-edit | /routes/:routeId/edit | step as above, draftId, panel |
| subscriptions | /client-config/subscriptions | q, status, sync, routeId |
| subscription-new | /client-config/subscriptions/new | step=basics/routes/policy/review, routeIds, profileId, draftId, returnTo |
| subscription-detail | /client-config/subscriptions/:subscriptionId | tab=overview/entries/config/usage, panel |
| subscription-edit | /client-config/subscriptions/:subscriptionId/edit | step as above, profileId, draftId, returnTo |
| rule-profiles | /client-config/rule-profiles | q |
| profile-new | /client-config/rule-profiles/new | draftId, returnTo |
| profile-edit | /client-config/rule-profiles/:profileId/edit | draftId, returnTo |
| activity | /activity | tab=events/releases/logs, kind, entityType, entityId, status, q |
| release-detail | /activity/releases/:releaseId | — |
| settings | /settings | section=general/ports/relay/location/certificates/client/logs/admin |
| login | /login | returnTo |

Lists additionally accept positive page/pageSize (1–9999), sort=name/-name/updatedAt/-updatedAt.
`routeIds` is a comma-separated stable ID string. `panel` is one of egress,
check-egress, preview. Shared parser is `app/query.ts`: unknown keys, repeated
values and invalid enum values are removed with replace navigation. Bounded q
is plain display/search input; filters/IDs are bounded alphanumeric reference
strings. Features validate object existence and business-specific filter values.
No credentials in query, params, hash or returnTo. Changing a feature's query
contract requires integration coordination.

`safeReturnTo(unknown)` accepts known internal paths only and sanitizes their
query. It rejects external/protocol-relative URLs, backslashes, fragments,
control characters, unknown routes, and login loops. Nested returnTo is removed.
Call it again before consuming returnTo. It validates routes, not authorization
or object existence. Use `pageLocation('overview')` as fallback.

`/` redirects to overview, `/client-config` to subscriptions. Unknown paths show
Not Found. Entity pages check their collection for the requested ID;
missing IDs display Not Found with the parent-list link. Feature implementations
must preserve that behavior and never silently select another entity. Page
metadata controls breadcrumbs, active parents and translated document title.
Login has a standalone layout, without authentication protection. Browser back
restores saved scroll positions; list filters remain in URL. No automatic
filter/entity redirection is performed.

## Shared components

All labels supplied as props are already translated by the caller. Events use
Vue `defineEmits` tuple signatures. Naive UI message/dialog providers are mounted
at app level; use `useMessage()` / `useDialog()` in descendants.

| Component | Props | Emits / slots |
| --- | --- | --- |
| PageHeader | title: string; description?: string | actions slot |
| DemoNotice | none | none |
| StatusBadge | status: string; label?: string | none; label overrides common status translation |
| QueryStamp | queriedAt?, expiresAt?, scope?: string | none; locale time, Not Queried, Stale |
| EmptyState | title?, description?, actionLabel?: string; filtered?: boolean | action(), clear() |
| ErrorState | issue: Issue; retryable?: boolean | retry() |
| EntityLink | kind: node/route/subscription/profile/release; id, label: string | standard RouterLink |
| ConfirmImpactDialog | show: boolean; title: string; impacts: string[]; busy?: boolean | update:show(boolean), confirm(), cancel() |
| RoutePath | nodes: readonly {id, name}[] | ordered textual links |
| ValidationSummary | issues: readonly Issue[] | focus(field: string) |
| TaskFeedback | state: idle/running/success/error/cancelled | cancel() |
| FeaturePlaceholder | feature, page: string | common pending wrapper; replace in business pages |
| ControllerInfo | none | explicit optional read; used in Settings |

ConfirmImpactDialog's caller performs the action and closes show on completion;
confirm does not close it or mutate data. Issue is `{code, params?, field?}`.
Common codes map to `errors.CODE`; feature codes may be full keys such as
`features.nodes.errors.PORT_IN_USE`. Feature error text remains feature-owned.
Use StatusBadge.label for feature-specific states.

### Stable P06 drawer exports

Types live in `features/egress/types.ts`; both components have default exports.
They currently only explain pending implementation and close. They do **not**
emit save/completed, validate input, or claim checks succeeded.

- EgressPolicyDrawer props: `show: boolean`, `terminalNodeId: ID`, optional
  `routeId`, `draftId`, `policyId`, `readonly: boolean`.
  Emits `update:show(value: boolean)`, `save(policy: EgressPolicy)`, `cancel()`.
  Future save returns the non-sensitive edited policy; owning caller coordinates
  workspace updates/revision changes and invalidates draft validation.
- CheckEgressDrawer props: `show: boolean`, `routeId: ID`, optional
  `subscriptionId`, `entryId`, `targetId`, `scope: 'route' | 'target_only'`.
  Emits `update:show(value: boolean)`, `cancel()`, `completed(checkIds: ID[])`.
  Future checks are explicitly simulated, stored in workspace.checks; completed
  references those stable IDs. Closing must cancel any finite UI task.

P05/P04/P07 can compile against these now. P06 owns actual UI and should preserve
this surface or request a coordinated contract change. Stub closure supports
Naive UI focus return and Escape; actual focus behavior awaits browser testing.

## Domain and workspace

`domain/index.ts` exports Node, Listener, Route/RouteRevision, Link,
ReverseTunnel, EgressTarget, EgressPolicy, Measurement, EgressCheck, RuleProfile,
DomainSet, Subscription, SubscriptionEntry, Release, Event, UsageSnapshot,
Settings, Draft, Workspace, Issue and state unions. Node control/tunnel/verification
are independent. Route draft/published revisions are separate snapshots; release
per-node applied/verified/rollback and cleanup are separate. Unknown measurement
values are null, not zero. Scope/method/revision/time travel with observations.
Target DNS belongs to nodes; policies have no per-route DNS. No credential value
exists in these persistent models.

```ts
import { useWorkspace } from '../../demo/workspace'
import type { Node } from '../../domain'
const { workspace, commands } = useWorkspace()
// workspace is deep readonly and reactive: derive lists/counts with computed().
const node = workspace.nodes.find(n => n.id === 'hk')
if (node) {
  const edited: Node = JSON.parse(JSON.stringify(node))
  edited.name = 'My demo node'
  commands.upsert('nodes', edited)
}
```

Commands: `upsert(collection, entity)`, `remove(collection, id)`,
`saveDraft(draft)`, `updateSettings(patch)`, `reset(scenario = 'normal')`.
Collection names correspond to Workspace array fields. Commands are synchronous,
local and contain no HTTP. They provide storage mechanics, **not** business
validation, dependency cleanup or publishing algorithms. Feature code must check
references, preserve published revisions and invalidate edited-draft validation
before committing. Do not mutate readonly state or JSON-clone Vue proxies using
structuredClone. Do not add ad hoc feature snapshots alongside workspace.

`createSeed(scenario)` returns a fresh deterministic workspace. Stable node IDs:
relay (local both), hk, sg, jp, reverse, offline. Route IDs: direct, multi,
reverse-route, custom, draft-route, failed-route. Default/Work profiles are default
and work. Subscriptions: sub-active/sub-expiring/sub-expired/sub-pending. Other
references are inspectable in seeds.ts. Reverse uses relay → reverse only, a
loopback SS TCP listener, fixed tcp_tls tunnel and separate mapping. Custom SG
policy has an IP rule and terminal MATCH→REJECT. Failed release models offline
rollback pending. All observation values are demonstrative; no seed is evidence
of protocol verification. Time anchor is 2026-11-10 UTC; expiry is explicit data,
not recalculated using a user's current date.

Demo Controls at the footer select a scenario then Reset Demo. Reset replaces
objects **and saved drafts**, clears memory secrets, resets mounted sandbox state
and persists. An explicit `settings.locale` is retained. Normal is the full seed; empty clears entity arrays; error enables
reproducible simulated query/task failure; stale expires a sample; offline marks
all control/tunnel connections offline; partial provides incomplete usage and a
partial release outcome. Features interpret workspace.scenario for their query
states; F00 does not implement their screens. Error does not hide all entities.
No random results, polling, setInterval, automatic queries or probes.

## Persistence, drafts and tasks

`meshstrata.demo.v1` in localStorage is the only persistent namespace; schemaVersion
is 1. Only non-sensitive demo objects/settings and projected Draft fields belong
here. Loading validates schema, collection IDs and seed field shapes. Corruption,
incompatible schema or storage errors show Reset Demo guidance and keep the old
stored snapshot untouched until reset. Reload can still open an in-memory normal
seed. Do not use this as production storage or a cross-tab synchronization system.

Draft fields: id, feature, objectId?, name, nodeIds, routeIds, profileId?, step?,
updatedAt and optional typed `data: DraftData`. `saveDraft` explicitly projects
these fields and the nested form whitelist in `demo/draft-data.ts`; arbitrary form
payloads are not accepted. `safeJson` also strips sensitive property names recursively as defense in
depth. Never place passwords, proxy auth, install tokens, secrets, private keys,
or YAML containing them into a persistent object, URL, message, error or download.
`sessionSecrets: Map<string,string>` is memory-only; features must clear their keys
when the relevant workflow ends. It is intentionally separate from workspace and
cleared on demo reset. It is not real authentication.

`useDraft(id, initial: Draft)` returns `{value, dirty, save, discard}`. Saved drafts
restore on refresh by stable ID. Unsaved changes prompt **Save Draft / Discard /
Stay** on route leave; refresh uses the native unsaved-change warning. Save is
explicit; unsaved changes are not silently persisted. Discard restores the last
saved baseline. Use one editor instance per draft ID; feature routes must key or
recreate it when the ID changes. Before subscription → rules, save the subscription
draft, pass draftId and validated returnTo, then return with profileId via the
helper; rehydrate the same draft and update that selection. P07/P08 implement this handoff using separate subscription and profile draft IDs.

`useQueryState<T>()` exposes data/loading/error/queriedAt and run(async query).
It prevents duplicate concurrent starts and retains the previous successful data
on failure. Feature owners supply cancellation and stable error mapping for
specific queries; the generic failure is DEMO_ERROR. `useFormat()` exposes
locale-aware date()/number(), including unknown/null handling.

`useSimulatedTask()` returns state/run(outcome?, delay?)/cancel(). One bounded timer
(maximum 10 seconds), no loop. Cancellation resolves the pending promise false;
completion true only for simulated success. Automatically cancels on route leave
and unmount. Drawers additionally call cancel when show becomes false. A page
leaving only stops UI simulation; it must not present that as real server rollback.
The settings sandbox exercises draft persistence and deterministic task outcomes.

ControllerInfo is the sole caller of existing `getSystem(signal)` (`api.ts`).
Only its explicitly labeled query button starts `/api/v1/system`; it times out at
5 seconds and aborts on unmount. It preserves the last successful snapshot on
failure. No mount hook requests Controller information; offline Controller leaves
the demo usable. Vite's existing proxy remains optional.

## i18n and styling

Every feature exports matching English and Simplified Chinese objects from
`locales/en.ts` and `locales/zh-CN.ts`. `i18n.ts` eager-loads both with Vite glob
into `features.<directory>.*`. Resource sets and interpolation parameters must
match; English fallback is disabled. Translate labels, feedback, errors and aria
text. Product/protocol names, CLI/rule syntax, IDs, group names and user inputs
remain data. Switching language never rewrites entity names or event history.

`locale.ts` normalizes Chinese variants to `zh-CN` and other languages to `en`.
With no saved preference, the first browser language is used. `settings.locale`
is the only saved preference, in the existing workspace namespace. Settings changes
it immediately; saving an unrelated settings group does not establish a preference.
Reset Demo preserves an explicit language choice. Workspace must not import i18n
or Vite glob; i18n observes workspace settings in one direction.

Naive UI `locale/dateLocale`, `useFormat()` Intl date/number output, HTML `lang`
and document title follow the UI locale. The title observes locale and current
route, including initialization and catch-all. Avoid bare `toLocaleString()`.
Use `useDisplay()` for known observation presentation enums while retaining
unknown machine identifiers for inspection. Do not translate stored observations.

`check-i18n.mjs` checks exact key/parameter parity, message compilation, static
calls, dynamic states/validation and locale resolution under the page check lock.
`check-i18n-browser.cjs` optionally checks a running local preview in isolated
contexts. Browser tests that import app modules must reuse the exact loaded Vite
resource URL when HMR timestamps are present, avoiding a second singleton instance.

Shared CSS variables in style.css define color, space, radius; panel/actions,
form-content and table-scroll classes are reusable. At <=760px the sidebar becomes
a Naive drawer; links close it. Tables should use table-scroll locally. Touch
navigation is >=44px; skip link and focus outlines are present. Do not add external
fonts/CDNs or a new UI framework.

## Run, checks and limits

```sh
npm --prefix controller/web ci
npm --prefix controller/web run dev       # 127.0.0.1:5173, Controller optional
npm --prefix controller/web run typecheck
npm --prefix controller/web run check:foundation
npm --prefix controller/web run build     # output remains controller/internal/webui/dist for Go embed
git diff --check
```

Run installations/builds serially on this low-memory host. Fixed Vue Router 4.6.4
is in package.json/package-lock.json. No Go behavior was changed. Generated build
output remains ignored, preserving the Go embed placeholder.

F00 validation records actual checks in the development log. Pure checks cover
19 named entries, query/returnTo, memory-router back navigation, catch-all,
six seed scenarios/references, storage round-trip, corrupt storage, sensitive draft
projection and reset. Memory history tests do not prove browser hash history,
refresh, drawer focus, mobile rendering or observed network traffic. No browser
was available in F00; these still require interactive acceptance after integration.

P01–P10 now implement the frontend layouts, navigation, forms and bounded offline
simulations. Real authentication, APIs, node operations, runtime measurements and
configuration deployment remain future work. See the integration acceptance record
in documents/frontend-delivery/集成验收.md for checks and current limitations.

## Integration review before P01–P10

The integration session re-ran F00 checks and build and expanded the minimal
contract before dispatch. Existing F00 snapshots remain readable; new fields are
optional. Features must supply documented defaults when these are absent.

- `Draft.data.node/route/subscription/profile` holds the respective non-sensitive
  form data. Use the exact exported `DraftData` shape and `useDraft`; arbitrary
  feature payloads are deliberately dropped. Base Draft fields still hold name,
  selected node/route/profile IDs and current step. Step changes should update
  both the draft and URL. Never put secrets into these fields.
- `Node.protocolDefaults` now uses `ProtocolSettings` with optional autoPort,
  listenAddress, portPool and public protocol parameters. It is the only protocol
  defaults source; do not introduce a second protocols collection.
- Optional node fields cover location overrides, versions, snapshots, DNS routing,
  DNS draft, probe endpoint and removal. `targetDns` remains the applied DNS;
  `dnsDraft` is pending change, and `dnsRouting` supplies applied outbound/bootstrap.
- Optional Settings cover all eight settings sections. Defaults remain the design
  values (96h/5min, port pool 20000–29999, 8443/9443, warning logs, 5MiB × 4,
  20MiB total/7 days). Passwords/API keys/local secrets remain sessionSecrets only.
- `RuleLine.type` supports parsed Mihomo rule names as strings. Feature validation
  decides supported syntax and reports unsupported lines; do not silently accept
  arbitrary types. Rules can carry options; profiles support description/sourceText.
- Subscription grouping/client preferences, downloadVersion (a non-sensitive
  demo URL revision), entry cleanup, release stages/times/node scope and detailed
  usage metric fields are available. P07/P09 consume the same shared Release model.
- `domain/groups.ts` exports `exitGroupName(revision)` using stable terminal and
  policy IDs. P07 generates groups and P08 offers targets using this helper. It
  does not validate members or matching policy revisions; callers must do that.
- Egress props/emits are unchanged. P06 may write target drafts to the shared
  targets collection on explicit save and return a policy to the owner; this does
  not publish it. Existing policies remain unchanged until their owner handles the
  policy save and review, especially for shared revisions.
- Query adds node-new.draftId, routes.exitNodeId/protocol/udp and
  profile-new/profile-edit.subscriptionId. Activity uses `tab`, not `view`.
- Storage validation now accepts interface/node-default targets without host/port
  and subscription/node releases without routeId. Required entry.routeId remains
  required. Typed draft data is projected both on save and restore.
- Fresh seeds offer all four direct-node protocol defaults; reverse remains SS
  TCP. Subscription SS entries have isolated listeners/ports. The generic mixed
  group is correctly named All-Proxies. Reset Demo loads these improved seeds;
  existing user-edited demo storage is never silently replaced.

### Parallel implementation and serialized checks

All ten page chats may edit their assigned directories concurrently, against this
frozen contract. P04/P05/P07 may import P06's existing stable drawers immediately;
they must not wait by polling another chat's files. Do not edit a sibling feature.
Missing shared requirements go in the task's delivery report for integration.

Run `bash /root/meshstrata/controller/web/scripts/check-pages.sh` for foundation checks,
typecheck and build. It acquires `.local/frontend-check.lock` with flock, so only
one check/build process runs at a time. Do not launch other npm builds/installers,
package changes or Vite servers concurrently. A check may observe an in-progress
sibling edit: report the exact external error without changing that sibling; final
integration repeats the checks once all feature edits settle.

The page chats do **not** commit, push, stage, revert or clean the shared checkout.
The integration session exclusively handles Git after review. Each page chat
records its own actual checks and issues in `documents/frontend-delivery/Pxx.md`.
This is the selected parallel handoff rule, overriding the ordinary per-chat
commit timing (not the repository requirement to eventually commit and push).

## P01–P10 integration

All ten feature directories now contain working offline pages. Run
`bash controller/web/scripts/check-pages.sh` for foundation, cross-feature rules/allocation,
activity lifecycle, TypeScript and production build checks.

- `Draft.data.egress` explicitly projects non-sensitive drawer fields. Each route
  draft/path/terminal has a separate drawer draft. Authentication is memory-only
  and cleared on save or close. Saving the drawer form differs from returning a
  policy to the route editor for publication review.
- `localClientPaired` requires both the stored preference and the current
  session's secret. A refresh never leaves client actions falsely paired.
- Fixed protocol ports fail on conflict; automatic allocation respects the
  protocol pool. Published snapshots remain usable after a failed edit. Successful
  route paths leave the pending draft; failed paths remain for retry.
- Node Default clears the policy ID on a route. Custom policies are saved as
  independent drafts to protect existing published revisions. Shared batch policy
  updates are visibly unavailable until a coordinated publication contract exists.
- Rule targets include published route names and valid groups. Attached DomainSets
  generate both RULE-SET entries before MATCH and DNS policy mappings. Configuration
  previews contain credential placeholders and are not deployable configurations.
- Activity retries deliberately update task records only; the page explains this.
  They do not reconstruct previous route revisions or free referenced resources.
  Pending cleanup and offline confirmations remain explicit.

## Network regulation preferences (2026-11-11)

`AccessEnvironment` now uses `strong_regulation` / `weak_regulation`. The UI says
Strong regulation / Weak regulation, independently of geographic location.
New routes inherit the Settings client preference (weak by default); per-draft
overrides and UDP controls are under collapsed Recommendation preferences.
The first-hop protocol suggestion uses this value; selected listeners and published
paths are not rewritten. Measurement samples retain this scope. Node labels are
separate metadata and never inferred from country/IP.

`domain/access-environment.ts` converts legacy geographic values in saved snapshots
and filter links, preserving data and nested editor drafts. Internal field names
remain `environment` for compatibility. `check-foundation.mjs`, `check-flows.mjs`
and the optional `check-regulation-browser.cjs` cover this behavior.
