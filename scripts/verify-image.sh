#!/bin/sh
set -eu

image=${1:-mmmcp:test}
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
work=$(mktemp -d "$root/.verify-image.XXXXXX")
container=
fixture_container=
network="mmmcp-verify-$$"
fixture_name="mmmcp-fixture-$$"
fixture_image="mmmcp:imagecheck-$$"

cleanup() {
  if [ -n "$container" ]; then
    docker rm -f "$container" >/dev/null 2>&1 || true
  fi
  if [ -n "$fixture_container" ]; then
    docker rm -f "$fixture_container" >/dev/null 2>&1 || true
  fi
  docker network rm "$network" >/dev/null 2>&1 || true
  docker image rm "$fixture_image" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT INT TERM

mkdir -p "$work/data"
chmod 0777 "$work/data"

(
  cd "$root"
  go build -o "$work/imagecheck" ./scripts/imagecheck
  docker build --target imagecheck-source -t "$fixture_image" .
)
docker network create "$network" >/dev/null
fixture_container=$(docker run -d --network "$network" --name "$fixture_name" "$fixture_image" fixture --listen 0.0.0.0:8081)

i=0
until docker run --rm --network "$network" "$fixture_image" client --endpoint "http://${fixture_name}:8081" --tool echo >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -ge 100 ]; then
    docker logs "$fixture_container" >&2 || true
    echo "fixture did not start" >&2
    exit 1
  fi
  sleep 0.1
done

cat >"$work/mmmcp.yaml" <<EOF
servers:
  - name: fixture
    url: http://${fixture_name}:8081
EOF

container=$(docker run -d \
  --network "$network" \
  -p 127.0.0.1:18080:8080 \
  -v "$work/mmmcp.yaml:/etc/mmmcp/config.yaml:ro" \
  -v "$work/data:/var/lib/mmmcp" \
  "$image" -config /etc/mmmcp/config.yaml -listen 0.0.0.0:8080)

endpoint="http://127.0.0.1:18080"

i=0
until (
  "$work/imagecheck" client --endpoint "$endpoint" --tool echo
) >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -ge 100 ]; then
    docker logs "$container" >&2 || true
    echo "image did not become ready" >&2
    exit 1
  fi
  sleep 0.1
done

uid=$(docker exec "$container" id -u)
if [ "$uid" = "0" ]; then
  echo "image runs as root" >&2
  exit 1
fi
if [ ! -f "$work/data/mmmcp.db" ]; then
  echo "default SQLite database was not created on the volume" >&2
  exit 1
fi

echo "verified $image: MCP request succeeded, uid=$uid, SQLite volume writable"
