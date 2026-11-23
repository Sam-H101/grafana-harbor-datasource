# Development environment

Everything runs in Docker: you need Docker with Compose v2 and a Bash shell
(Linux, macOS, or WSL on Windows). Node, Go and the other build tools live in a
toolbox container, so nothing else has to be installed. Run the commands below
from the repository root.

## Toolbox

`devenv/toolbox/Dockerfile` has Node, Go, Mage, golangci-lint and Chromium's
system libraries. `node_modules`, Go caches, the npm cache and Playwright
browsers live in Docker volumes; only build output (`dist/`) is written to the
working tree. `devenv/run.sh` runs a command in the toolbox as your own user, so
files it writes belong to you. Always start the toolbox through it: it builds
the image for your user id.

```sh
devenv/run.sh npm ci                 # install frontend deps (into a volume)
devenv/run.sh npm run build          # frontend
devenv/run.sh npm run dev            # frontend, rebuild on change
devenv/run.sh mage -v build:linux    # backend for the dev stack
devenv/run.sh mage -v buildAll       # backend for every platform
devenv/run.sh npm run typecheck
devenv/run.sh npm run lint
devenv/run.sh npm run test:ci        # frontend unit tests
devenv/run.sh mage test              # backend unit tests
devenv/run.sh golangci-lint run ./...
devenv/run.sh                        # shell
```

The toolbox joins Harbor's Docker network, so start Harbor (below) before the
first `devenv/run.sh`.

## Harbor

Harbor is not one image: its `prepare` image turns a `harbor.yml` into a
`docker-compose.yml` with a dozen services. `devenv/harbor/harbor.sh` runs
that, the way Harbor's installer does, and then seeds the server.

```sh
devenv/harbor/harbor.sh up       # generate, start and seed; a few minutes the first time
devenv/harbor/harbor.sh seed     # seed again (safe to repeat)
devenv/harbor/harbor.sh logs --tail 50 core
devenv/harbor/harbor.sh down     # stop and delete its data
```

Harbor's generated files and data go to
`~/.cache/grafana-harbor-datasource/harbor` (`HARBOR_HOME`), outside the
repository, and belong to root and to Harbor's own users. That is why the
script runs Docker Compose from a container and removes the directory through
one: you do not need `sudo`.

It needs about 2 GB of memory and 2 GB of disk, and downloads the sample
images and Trivy's vulnerability database from the internet.

| Variable                | Default                                     | What it sets                                   |
| ----------------------- | ------------------------------------------- | ---------------------------------------------- |
| `HARBOR_VERSION`        | `v2.15.5`                                   | The Harbor release                             |
| `HARBOR_PORT`           | `8085`                                      | The port Harbor is published on, for a browser |
| `HARBOR_ADMIN_PASSWORD` | `Harbor-dev-0nly`                           | The password of `admin`                        |
| `HARBOR_HOME`           | `~/.cache/grafana-harbor-datasource/harbor` | Generated files and data                       |
| `SOURCE_REGISTRIES`     | ECR Public, then two other mirrors          | Where the sample images come from, in order    |

Harbor is at http://localhost:8085 (admin / `Harbor-dev-0nly`). Inside Docker
it is `http://proxy:8080` on the network `harbor_harbor`.

### Sample data

`harbor.sh` and `devenv/harbor/seed.py` create:

- the projects `platform` (private, 500 MiB quota), `apps` (private, no
  quota) and `sandbox` (public, 50 MiB quota, empty), next to Harbor's own
  `library`; every project scans images when they are pushed;
- the images `library/busybox:1.36`, `platform/alpine:3.10` and `3.20` (also
  tagged `stable`), `apps/nginx:1.19-alpine` and `apps/redis:7-alpine`. The
  old releases have known vulnerabilities, critical ones among them;
- a replication rule, `mirror-library`, that copies `library/**` into the
  project `mirror` of the same Harbor, and one run of it;
- one dry run of garbage collection;
- the robot accounts `robot$grafana`, with every permission the plugin can
  use, and `robot$grafana-limited`, which may only list projects,
  repositories and artifacts. Their generated secrets go to the Docker volume
  `grafana-harbor-seed`, for Grafana and the tests.

## Grafana

Build the plugin into `dist/` and start Harbor first, then:

```sh
docker compose up -d --build --wait  # Grafana, the proxy and the relay
docker compose restart grafana       # pick up a rebuilt backend binary
docker compose logs -f grafana
docker compose down
```

| Service   | Address               | What it is                                                             |
| --------- | --------------------- | ---------------------------------------------------------------------- |
| `grafana` | http://localhost:3000 | Grafana with the plugin from `dist/`, anonymous Admin                  |
| `socks`   |                       | A SOCKS5 proxy, standing in for Private data source connect            |
| `relay`   |                       | Harbor under the name `harbor-private`, on a network Grafana is not on |

`GRAFANA_PORT` moves the published port; `GRAFANA_VERSION` chooses another
version.

Provisioned data sources (`provisioning/datasources/datasources.yml`):

- **Harbor**: `robot$grafana`, with every permission the plugin can use.
- **Harbor (limited account)**: `robot$grafana-limited`. Use it to see the
  messages about missing permissions.
- **Harbor (private network)**: reaches Harbor only through the proxy.

The dashboard **Harbor overview** and three example alert rules are
provisioned too.

### The private network

`relay` forwards to Harbor and has the name `harbor-private` on a second
Docker network, `private`. Grafana is not on that network, so it cannot
resolve the name; the `socks` proxy can. A data source with the secure socks
proxy on therefore reaches `http://harbor-private:8080`, and one without it
does not. That is how the tests prove that requests go through the proxy.

## Tests against the stack

```sh
devenv/run.sh npx playwright install chromium   # once, into the volume
devenv/run.sh npm run e2e                       # Playwright, tests/e2e
devenv/run.sh go test -tags integration ./tests/integration/...
```

Inside the toolbox these reach Harbor and Grafana by name, and the robot
accounts' secrets are mounted at `/run/harbor`. To run them from elsewhere (as
CI does), copy the secrets out with `devenv/harbor/harbor.sh secrets <dir>` and
set `HARBOR_SEED_DIR` to that directory, and `HARBOR_URL` for the integration
tests.

Playwright saves a screenshot of each step under `artifacts/app-tests/`, so you
can check what the tests saw. To regenerate the screenshots shipped with the
plugin:

```sh
devenv/run.sh bash -c 'DOCS_SCREENSHOTS=1 npx playwright test tests/e2e/docs-screenshots.spec.ts'
```

## Exposing the stack on the network

To reach Grafana from other machines, turn off anonymous Admin and set an
admin password. The anonymous setting is built into the image, so rebuild:

```sh
BIND_ADDRESS=0.0.0.0 ANONYMOUS_AUTH_ENABLED=false GRAFANA_ADMIN_PASSWORD='<password>' \
docker compose up -d --build --wait
```

The password only takes effect on a fresh Grafana. On an existing one, change
it with
`docker compose exec grafana grafana cli admin reset-admin-password '<password>'`.
The e2e tests expect anonymous Admin, so run them against the default local
stack.

Harbor publishes its port on every interface, as Harbor's own installer does.
Set `HARBOR_ADMIN_PASSWORD` before `harbor.sh up` if the machine is reachable
from a network you do not trust.
