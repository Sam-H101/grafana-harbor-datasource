#!/usr/bin/env bash
# Start, seed or stop the throwaway Harbor of the dev stack.
#
#   devenv/harbor/harbor.sh up      generate Harbor's compose file, start it and seed it
#   devenv/harbor/harbor.sh seed    seed a running Harbor again
#   devenv/harbor/harbor.sh secrets <dir>
#                                   copy the robot accounts' secrets to a directory,
#                                   for tests that run outside the toolbox
#   devenv/harbor/harbor.sh down    stop it and delete its data
#   devenv/harbor/harbor.sh logs    show the logs (arguments go to docker compose logs)
#
# Harbor has no single image: its "prepare" image turns harbor.yml into a
# docker-compose.yml with a dozen services and their configuration. That is
# what this script runs, the same way Harbor's installer does. Everything it
# generates, and Harbor's data, goes under HARBOR_HOME, outside the repository.
set -euo pipefail

HARBOR_VERSION="${HARBOR_VERSION:-v2.15.5}"
HARBOR_HOME="${HARBOR_HOME:-$HOME/.cache/grafana-harbor-datasource/harbor}"
HARBOR_PORT="${HARBOR_PORT:-8085}"
HARBOR_ADMIN_PASSWORD="${HARBOR_ADMIN_PASSWORD:-Harbor-dev-0nly}"
# Where the sample images are copied from: Docker's official images, from
# mirrors first. Each limits anonymous pulls in its own way, and shared CI
# machines run into those limits, so the next one is tried when one refuses.
SOURCE_REGISTRIES="${SOURCE_REGISTRIES:-public.ecr.aws/docker/library mirror.gcr.io/library docker.io/library}"
SKOPEO_IMAGE="${SKOPEO_IMAGE:-quay.io/skopeo/stable:latest}"
SEED_IMAGE="${SEED_IMAGE:-python:3.13-alpine}"
# The Docker network Harbor's compose project creates, and the volume the
# seed writes the robot accounts' secrets to. docker-compose.yaml uses both.
NETWORK=harbor_harbor
SEED_VOLUME=grafana-harbor-seed
here="$(cd "$(dirname "$0")" && pwd)"

# The files "prepare" generates belong to root and to Harbor's own users, and
# some are not readable by anyone else. Compose therefore runs in a container,
# as root, which saves running this script with sudo.
compose() {
  docker run --rm \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$HARBOR_HOME":"$HARBOR_HOME" \
    docker:cli compose -p harbor -f "$HARBOR_HOME/docker-compose.yml" "$@"
}

seed_step() {
  docker run --rm --network "$NETWORK" \
    -e HARBOR_URL=http://proxy:8080 \
    -e HARBOR_ADMIN_PASSWORD="$HARBOR_ADMIN_PASSWORD" \
    -e SEED_OUT=/seed-out \
    -v "$here/seed.py":/seed.py:ro \
    -v "$SEED_VOLUME":/seed-out \
    "$SEED_IMAGE" python3 /seed.py "$1"
}

skopeo() {
  docker run --rm --network "$NETWORK" "$SKOPEO_IMAGE" "$@"
}

# push <source image:tag> <project/repository:tag>
push() {
  local source
  for source in $SOURCE_REGISTRIES; do
    echo "Copying $source/$1 to $2"
    if skopeo copy --quiet --retry-times 2 \
      --dest-tls-verify=false --dest-creds "admin:$HARBOR_ADMIN_PASSWORD" \
      "docker://$source/$1" "docker://proxy:8080/$2"; then
      return 0
    fi
  done
  echo "Could not copy $1 from any of: $SOURCE_REGISTRIES" >&2
  return 1
}

# pull <project/repository:tag> <times>: so that pull counts are not all zero
pull() {
  for _ in $(seq 1 "$2"); do
    skopeo inspect --tls-verify=false --creds "admin:$HARBOR_ADMIN_PASSWORD" \
      --format '{{.Digest}}' "docker://proxy:8080/$1" > /dev/null
  done
}

seed() {
  docker volume create "$SEED_VOLUME" > /dev/null
  seed_step projects
  push busybox:1.36 library/busybox:1.36
  # An old release with known vulnerabilities, and a current one.
  push alpine:3.10 platform/alpine:3.10
  push alpine:3.20 platform/alpine:3.20
  push nginx:1.19-alpine apps/nginx:1.19-alpine
  push redis:7-alpine apps/redis:7-alpine
  pull apps/nginx:1.19-alpine 3
  pull platform/alpine:3.20 1
  seed_step finish
}

wipe() {
  if [ -d "$HARBOR_HOME" ]; then
    # Harbor's containers write as their own users: remove their files as root.
    docker run --rm -v "$(dirname "$HARBOR_HOME")":/parent alpine:3 rm -rf "/parent/$(basename "$HARBOR_HOME")"
  fi
}

case "${1:-up}" in
  up)
    mkdir -p "$HARBOR_HOME/input" "$HARBOR_HOME/data" "$HARBOR_HOME/log" "$HARBOR_HOME/common/config"
    sed -e "s|@HARBOR_HOME@|$HARBOR_HOME|g" \
        -e "s|@HARBOR_PORT@|$HARBOR_PORT|g" \
        -e "s|@HARBOR_ADMIN_PASSWORD@|$HARBOR_ADMIN_PASSWORD|g" \
        "$here/harbor.yml.tmpl" > "$HARBOR_HOME/input/harbor.yml"
    docker run --rm \
      -v "$HARBOR_HOME/input":/input \
      -v "$HARBOR_HOME/data":/data \
      -v "$HARBOR_HOME":/compose_location \
      -v "$HARBOR_HOME/common/config":/config \
      -v /:/hostfs \
      --privileged \
      "goharbor/prepare:$HARBOR_VERSION" prepare --with-trivy
    compose up -d
    seed
    echo "Harbor $HARBOR_VERSION is up at http://localhost:$HARBOR_PORT (admin / $HARBOR_ADMIN_PASSWORD)"
    ;;
  seed)
    seed
    ;;
  down)
    if [ -f "$HARBOR_HOME/docker-compose.yml" ]; then
      compose down -v
    fi
    wipe
    docker volume rm -f "$SEED_VOLUME" > /dev/null
    ;;
  secrets)
    mkdir -p "$2"
    for f in secret limited-secret; do
      docker run --rm -v "$SEED_VOLUME":/seed-out:ro alpine:3 cat "/seed-out/$f" > "$2/$f"
    done
    ;;
  logs)
    shift
    compose logs "$@"
    ;;
  *)
    echo "usage: $0 up|seed|secrets <dir>|down|logs" >&2
    exit 2
    ;;
esac
