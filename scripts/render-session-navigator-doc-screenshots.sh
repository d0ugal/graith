#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

ansi_dir="${GRAITH_SESSION_NAV_DOCS_ANSI_DIR:-shots/session-navigator/docs/ansi}"
out_dir="${GRAITH_SESSION_NAV_DOCS_OUT_DIR:-website/static/images/docs/session-navigator}"
pages_json="$ansi_dir/pages.json"
viewports_json="$ansi_dir/viewports.json"

case "$ansi_dir" in
  /*)
    echo "GRAITH_SESSION_NAV_DOCS_ANSI_DIR must be repo-relative for Docker fallback" >&2
    exit 2
    ;;
esac

case "$out_dir" in
  /*)
    echo "GRAITH_SESSION_NAV_DOCS_OUT_DIR must be repo-relative for Docker fallback" >&2
    exit 2
    ;;
esac

go run ./cmd/sessionnavshots \
  -suite docs \
  -out "$ansi_dir" \
  -pages "$pages_json" \
  -viewports "$viewports_json"

mkdir -p "$out_dir"
find "$out_dir" -maxdepth 1 -type f -name 'session-navigator-*.png' -delete

have_local_capture=1
for cmd in identify import jq xdotool xterm xvfb-run; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    have_local_capture=0
  fi
done

if [ "$have_local_capture" = "1" ]; then
  xvfb-run -a --server-args="-screen 0 4096x1600x24 -nolisten tcp" \
    scripts/session-navigator-terminal-screenshot.sh \
    "$ansi_dir" "$out_dir" "$pages_json" "$viewports_json"
  exit 0
fi

if ! command -v docker >/dev/null 2>&1; then
  cat >&2 <<'EOF'
missing terminal screenshot tools and Docker fallback is unavailable.
Install ImageMagick, jq, xdotool, xterm, Xvfb, and DejaVu fonts, or install Docker.
EOF
  exit 1
fi

docker_image="${GRAITH_SESSION_NAV_DOCS_DOCKER_IMAGE:-ubuntu:24.04}"
docker_config="$(mktemp -d)"
trap 'rm -rf "$docker_config"' EXIT
printf '{"auths":{}}\n' >"$docker_config/config.json"

DOCKER_CONFIG="$docker_config" docker run --rm \
  -e ANSI_DIR="$ansi_dir" \
  -e OUT_DIR="$out_dir" \
  -e PAGES_JSON="$pages_json" \
  -e VIEWPORTS_JSON="$viewports_json" \
  -e GRAITH_SESSION_NAV_SHOT_HOLD="${GRAITH_SESSION_NAV_SHOT_HOLD:-30}" \
  -e GRAITH_SESSION_NAV_SHOT_MIN_BYTES="${GRAITH_SESSION_NAV_SHOT_MIN_BYTES:-4096}" \
  -e GRAITH_SESSION_NAV_SHOT_SETTLE="${GRAITH_SESSION_NAV_SHOT_SETTLE:-0.5}" \
  -v "$root:/repo" \
  -w /repo \
  "$docker_image" \
  bash -lc '
    set -euo pipefail
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq >/dev/null
    apt-get install -y -qq --no-install-recommends \
      fonts-dejavu-core \
      imagemagick \
      jq \
      xdotool \
      xterm \
      xvfb >/dev/null
    xvfb-run -a --server-args="-screen 0 4096x1600x24 -nolisten tcp" \
      scripts/session-navigator-terminal-screenshot.sh \
      "$ANSI_DIR" "$OUT_DIR" "$PAGES_JSON" "$VIEWPORTS_JSON"
  '
