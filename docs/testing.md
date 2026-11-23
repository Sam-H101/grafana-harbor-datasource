# Testing the Harbor data source

This guide is for anyone who wants to try the plugin or review it. It starts a
Harbor filled with sample images and a Grafana with the plugin, and lists what
to try.

## The test environment

You need Docker with Compose v2 and a Bash shell. From the repository root:

```sh
devenv/harbor/harbor.sh up
devenv/run.sh npm ci
devenv/run.sh npm run build
devenv/run.sh mage -v build:linux
docker compose up -d --build --wait
```

The first start downloads Harbor, the sample images and the vulnerability
database, and takes a few minutes. Then open http://localhost:3000 (no login
needed). Harbor itself is at http://localhost:8085 (admin /
`Harbor-dev-0nly`).

What is there:

|                                          |                                                                         |
| ---------------------------------------- | ----------------------------------------------------------------------- |
| Data source **Harbor**                   | A robot account with every permission the plugin can use                |
| Data source **Harbor (limited account)** | A robot account that may only list projects, repositories and artifacts |
| Data source **Harbor (private network)** | Reaches Harbor only through a SOCKS proxy                               |
| Dashboard **Harbor overview**            | Every query type in a panel, with Project and Severity variables        |
| Alert rules, folder **Harbor**           | Component health, project quota, and a rule that fires                  |

The sample Harbor has five projects (two private, three public; two with a
quota), five repositories with six images, about a hundred vulnerability
findings from old Alpine and nginx images, one replication run, one garbage
collection run and two robot accounts. [devenv/README.md](../devenv/README.md)
lists them.

## What to try

### Settings

1. Open **Connections > Data sources > Harbor** and press **Save & test**. It
   reports the Harbor version and "The account robot$grafana can see 5
   projects."
2. Do the same on **Harbor (limited account)**: it connects, and lists what
   that account cannot read, with the permission for each.
3. Add a new data source of type Harbor:
   - URL `http://proxy:8080` and nothing else: it connects, and says that
     anonymous access sees the 3 public projects only.
   - The user name `robot$grafana` and a wrong password: "Harbor rejected the
     user name or password."
   - URL `http://proxy:8080/wrong`: "did not answer like Harbor."
   - URL `http://harbor.invalid:8080`: "host not found."

### Queries

In **Explore** or a new panel, with the data source **Harbor**:

| Query                | Try                                                   | Expect                                                             |
| -------------------- | ----------------------------------------------------- | ------------------------------------------------------------------ |
| Projects             |                                                       | Five rows; `platform` has a 500 MiB quota and a share of it in use |
| Projects             | Show: Quota used %                                    | One number each for `platform` and `sandbox`                       |
| Repositories         | Sort by Pulls, Descending                             | Five rows, `apps/nginx` near the top                               |
| Repositories         | Name contains `busy`                                  | `library/busybox` and `mirror/busybox`                             |
| Artifacts            | No project                                            | A note asking for a project; nothing is sent                       |
| Artifacts            | Project `platform`                                    | Two images, one tagged `3.20, stable`, with scan results           |
| Artifacts            | Project `apps`, type a repository that does not exist | An error naming the repository                                     |
| Vulnerabilities      | Severity Critical                                     | About ten findings, each with package and fixed version            |
| Vulnerabilities      | Repository `platform/alpine`                          | The findings of that repository only                               |
| Vulnerability totals | Use the Stat visualization                            | Counts by severity                                                 |
| Vulnerability totals | Number: Critical                                      | One number                                                         |
| Totals               |                                                       | Projects 5, repositories 5, storage used, disk size and free space |
| Health               |                                                       | Eight components, all healthy                                      |
| Replications         | Rule `mirror-library`                                 | One run that succeeded                                             |
| Garbage collection   |                                                       | One dry run                                                        |
| Audit log            | Resource type `robot`                                 | The creation of the two robot accounts                             |
| Job queues           |                                                       | A row per job type, none pending                                   |
| Robot accounts       |                                                       | `robot$grafana` and `robot$grafana-limited`                        |

With the data source **Harbor (limited account)**, Robot accounts fails with
"Give the robot account the permission Robot account: List (system)", and
Projects works but notes why the storage columns are blank.

### Dashboard, variables and alerts

- Open the dashboard and set **Project** to `apps`: the tables narrow to that
  project. Set **Severity** to High: the vulnerability table follows.
- In the dashboard settings, add a variable of type Query on this data source:
  the editor offers projects and repositories, and previews the values.
- Under **Alerting > Alert rules**, the folder Harbor has two rules in state
  Normal and one, "Critical vulnerabilities found", firing.

## Private data source connect

The dev stack turns on Grafana's secure socks proxy and runs a plain SOCKS5
proxy, which is the mechanism behind Grafana Cloud's Private data source
connect. Harbor has a second name, `harbor-private`, that only the proxy can
resolve.

1. Add a data source with URL `http://harbor-private:8080`, the user name
   `robot$grafana` and its secret
   (`docker run --rm -v grafana-harbor-seed:/s alpine cat /s/secret`).
   **Save & test** fails: "host not found."
2. Turn on the switch under **Private network** and test again: it connects.

This shows that the plugin sends its requests through the proxy. It does not
test Grafana Cloud's own service, which has not been tried.

## Automated tests

| Suite         | Command                                                           | What it covers                                                                                                                          |
| ------------- | ----------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| Frontend unit | `devenv/run.sh npm run test:ci`                                   | Query defaults, variables, the list of query types                                                                                      |
| Backend unit  | `devenv/run.sh mage test`                                         | Settings and query validation, the API client against a fake Harbor built from captured responses, frames, error messages, health check |
| Integration   | `devenv/run.sh go test -tags integration ./tests/integration/...` | Every query type, both robot accounts and anonymous access, against the dev stack's Harbor                                              |
| Browser       | `devenv/run.sh npm run e2e`                                       | Settings page, query editor, dashboard, variables, alert rules, the private network path                                                |

CI runs all four on every push: the browser tests on several Grafana versions,
and the integration tests against Harbor 2.15.

## What has not been tested

- Harbor versions other than 2.15, and Harbor installed with the Helm chart
  or with external storage, database or Redis.
- A large Harbor: the sample holds six images. Queries that ask Harbor once
  per project or per repository (artifacts of several projects,
  vulnerabilities of several projects) will be slower there.
- Harbor users from LDAP or OIDC as the data source's account; the tests use
  robot accounts.
- Proxy cache projects, and artifacts other than container images (Helm
  charts, signatures, SBOMs).
- Grafana Cloud with Private data source connect.
