# Harbor API notes

What the backend relies on, as observed on Harbor 2.15.5 (the dev stack,
`devenv/harbor`). Each point is covered by a test against the fake server
(`pkg/harbor/harbortest`), the real one (`tests/integration`), or both. The
API is described in Harbor's `api/v2.0/swagger.yaml`.

## Addresses and errors

- Everything is under `/api/v2.0/`. Any other path, existing or not, returns
  Harbor's web page (HTML, status 200).
- Errors are JSON: `{"errors":[{"code":"NOT_FOUND","message":"..."}]}`. A 404
  without that body comes from another server; the client reports "not
  Harbor" for it.
- **A project that does not exist answers 403**, not 404, for a logged-in
  account. The plugin checks project names against the project list first, so
  that a typo is not reported as a missing permission.
- **A repository that does not exist answers 200 with an empty list** of
  artifacts. For a named repository with no artifacts the plugin checks that
  the repository exists.

## Logging in

- Basic authentication, with a user or a robot account (`robot$name` and its
  secret).
- `GET systeminfo` and `GET health` answer without a login. Without one,
  `systeminfo` leaves out `harbor_version`.
- **Wrong credentials are not rejected everywhere.** `GET projects` and
  `GET repositories` treat a request with a wrong password like one without a
  login and return the public projects, with status 200.
- `GET statistics` needs a login and no particular permission: it answers 401
  to wrong credentials. The health check uses it to tell whether the login is
  accepted.
- `GET users/current` answers 412 for a robot account, so it cannot be used
  for that.

## Permissions of a robot account

- `GET statistics` gives the overall totals to system administrators only. A
  robot account gets `total_project_count`, `total_repo_count` and
  `total_storage_consumption` as 0, even when it can list every project. The
  Totals query therefore adds up projects, their `repo_count` and the quotas'
  `used.storage` itself.
- `GET auditlog-exts` answers 200 with an empty list, not 403, to an account
  that may not read the audit log.
- Each of `quotas`, `systeminfo/volumes`, `security/*`, `replication/*`,
  `system/gc`, `jobservice/queues` and `robots` answers 403 without its
  permission.

## Listings

- `page` and `page_size`; the largest page is 100. `X-Total-Count` gives the
  number of items.
- `security/vul` with `tune_count=true` answers `X-Total-Count: -1` beyond
  1000 findings; a full page then means there may be more.
- `q` filters: `key=value` is an exact match, `key=~value` a "contains" match
  (used for repository names), `key=[a~b]` a range (used for the audit log's
  `op_time`, in UTC as `2006-01-02 15:04:05`). Conditions are separated by
  commas, so the plugin refuses commas and the other characters `q` gives a
  meaning to in filter values.
- `sort=field` or `sort=-field`. Used: `name`, `pull_count`,
  `artifact_count`, `update_time` (repositories), `-push_time` (artifacts),
  `-creation_time` (garbage collection), `-op_time` (audit log).
- The slashes of a nested repository name are encoded twice in the path:
  `projects/apps/repositories/team%252Fapi/artifacts`.

## Fields

- "Never" is written as year 1 (`0001-01-01T00:00:00.000Z`), for example the
  `pull_time` of an artifact that was not pulled.
- A project's `metadata.public` is the text `"true"` or `"false"`.
- A quota's `hard.storage` is -1 for unlimited. Quotas are matched to
  projects by `ref.id`.
- An artifact's `scan_overview` is an object keyed by the report's MIME type;
  the vulnerability report's key contains `vulnerability`. Its
  `summary.summary` holds a count per severity and leaves out severities
  without findings.
- A garbage collection run's `job_parameters` is a JSON document inside a
  string; `freed_space`, `purged_blobs` and `purged_manifests` are in it.
  `update_time` is when the run ended.
- `jobservice/queues` leaves out `count` and `latency` when they are 0.
- A robot account's `expires_at` is a Unix time in seconds, or -1 for never.
- The audit log moved from `audit-logs` to `auditlog-exts` in Harbor 2.13,
  which added `operation_description` and `operation_result`. On 2.15
  `audit-logs` still exists and is empty. The client asks the new address and
  falls back to the old one on a 404; the fallback is tested against the fake
  server only.

## The dev stack

- Scans are started by pushing to a project with `auto_scan`. A scan that
  was running when Harbor stopped stays "Running" for good; the seed stops
  and restarts a scan that makes no progress.
- A replication rule can copy from a Harbor into another project of the same
  Harbor, which is how the seed gets a replication run without a second
  registry.
