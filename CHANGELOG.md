# Changelog

## 1.0.0 (2026-11-23)

First version.

- Twelve queries against Harbor: projects, repositories, artifacts,
  vulnerabilities, vulnerability totals, totals, health, replications,
  garbage collection, audit log, job queues and robot accounts.
- Projects with storage used, quota and the share of the quota in use.
- Artifacts with tags, size, push and pull times and the result of the last
  vulnerability scan; vulnerabilities by project, repository, severity, CVE
  and package.
- One number per project, job type or component, and single totals, for alert
  rules, stats and gauges.
- Dashboard variables for projects and repositories; annotations from the
  audit log and from pushes; queries work in alert rules.
- Settings: URL, robot account or user, TLS options, query timeout, and
  Grafana's secure socks proxy for Private data source connect.
- Save & test reports the Harbor version and lists what the account may not
  read; a query that lacks a permission names the permission to grant.
- Developed and tested against Harbor 2.15.
