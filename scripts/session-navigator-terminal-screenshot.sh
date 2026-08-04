#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 <ansi-dir> <out-dir> <pages.json> <viewports.json>" >&2
}

if [ "$#" -ne 4 ]; then
  usage
  exit 2
fi

ansi_dir=$1
out_dir=$2
pages_json=$3
viewports_json=$4

if [ -z "${DISPLAY:-}" ]; then
  echo "DISPLAY is required; run this script under xvfb-run in CI" >&2
  exit 1
fi

for cmd in identify import jq xdotool xterm; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "missing required command: $cmd" >&2
    exit 1
  fi
done

mkdir -p "$out_dir"

safe_artifact_name() {
  [[ "$1" =~ ^[A-Za-z0-9._-]+$ && "$1" != *..* ]]
}

viewport_csv=$(jq -er '[.[].label] | join(",")' "$viewports_json")

valid_screenshot() {
  local out_path=$1
  local stem=$2
  local cols=$3
  local rows=$4
  local min_bytes=$5
  local bytes
  local metadata
  local width
  local height
  local stddev
  local min_width
  local min_height

  bytes=$(wc -c <"$out_path" | tr -d '[:space:]')
  if [ "$bytes" -lt "$min_bytes" ]; then
    echo "captured undersized screenshot for ${stem} (${bytes} bytes)" >&2
    return 1
  fi

  metadata=$(identify -quiet -format '%w %h %[fx:standard_deviation]' "$out_path" 2>/dev/null || true)
  read -r width height stddev <<<"$metadata"
  if [[ ! "$width" =~ ^[0-9]+$ || ! "$height" =~ ^[0-9]+$ ]]; then
    echo "captured invalid PNG for ${stem}" >&2
    return 1
  fi

  min_width=$((cols * 4))
  min_height=$((rows * 8))
  if [ "$width" -lt "$min_width" ] || [ "$height" -lt "$min_height" ]; then
    echo "captured implausible screenshot for ${stem} (${width}x${height}, expected at least ${min_width}x${min_height})" >&2
    return 1
  fi

  if ! awk -v stddev="$stddev" 'BEGIN { exit !(stddev > 0.0001) }'; then
    echo "captured blank or uniform screenshot for ${stem}" >&2
    return 1
  fi
}

capture() {
  local page=$1
  local label=$2
  local cols=$3
  local rows=$4
  local stem="${page}-${label}"
  local ansi_path="${ansi_dir}/${stem}.ansi"
  local out_path="${out_dir}/${stem}.png"
  local title="graith-session-nav-${stem}-$$"
  local ready_title="${title}-ready"
  local hold="${GRAITH_SESSION_NAV_SHOT_HOLD:-30}"
  local min_bytes="${GRAITH_SESSION_NAV_SHOT_MIN_BYTES:-4096}"
  local settle="${GRAITH_SESSION_NAV_SHOT_SETTLE:-0.5}"
  local pid=""
  local win=""
  local ready_win=""
  local attempt
  local captured=0

  if ! safe_artifact_name "$page" || ! safe_artifact_name "$label"; then
    echo "unsafe screenshot artifact name: ${stem}" >&2
    return 1
  fi

  if [[ ! "$min_bytes" =~ ^[0-9]+$ || "$min_bytes" -le 0 ]]; then
    echo "invalid GRAITH_SESSION_NAV_SHOT_MIN_BYTES: ${min_bytes}" >&2
    return 1
  fi

  if [ ! -s "$ansi_path" ]; then
    echo "missing ANSI snapshot: ${ansi_path}" >&2
    return 1
  fi

  # shellcheck disable=SC2016 # $1/$2 expand inside the child bash process.
  xterm \
    -T "$title" \
    -name "$title" \
    -geometry "${cols}x${rows}+0+0" \
    -fa "DejaVu Sans Mono" \
    -fs 10 \
    -bg "#303640" \
    -fg "#d8dee9" \
    -b 0 \
    -bw 0 \
    +sb \
    -xrm "XTerm*directColor: true" \
    -xrm "XTerm*internalBorder: 0" \
    -xrm "XTerm*locale: true" \
    -xrm "XTerm*scrollBar: false" \
    -xrm "XTerm*utf8: 1" \
    -e bash -c 'printf "\033[?1049h\033[2J\033[H\033[?25l"; cat "$1"; printf "\033]0;%s\007" "$3"; sleep "$2"' _ "$ansi_path" "$hold" "$ready_title" &
  pid=$!

  for _ in $(seq 1 100); do
    ready_win=$(xdotool search --name "^${ready_title}$" 2>/dev/null | tail -n 1 || true)
    if [ -n "$ready_win" ]; then
      win=$ready_win
      break
    fi

    if ! kill -0 "$pid" 2>/dev/null; then
      wait "$pid" 2>/dev/null || true
      echo "xterm exited before rendering ${stem}" >&2
      return 1
    fi

    sleep 0.1
  done

  if [ -z "$(xdotool search --name "^${ready_title}$" 2>/dev/null | tail -n 1 || true)" ]; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    echo "xterm did not signal rendered content for ${stem}" >&2
    return 1
  fi

  for attempt in $(seq 1 5); do
    sleep "$settle"
    if import -window "$win" "$out_path"; then
      if valid_screenshot "$out_path" "$stem" "$cols" "$rows" "$min_bytes"; then
        captured=1
        break
      fi

      echo "screenshot validation failed for ${stem} (attempt ${attempt}/5); retrying" >&2
    fi
  done

  if [ "$captured" != "1" ]; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    echo "could not capture nonblank screenshot for ${stem}" >&2
    return 1
  fi

  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}

while IFS=$'\t' read -r page labels; do
  IFS=',' read -r -a page_viewports <<<"$labels"
  for label in "${page_viewports[@]}"; do
    cols=$(jq -er --arg label "$label" '.[] | select(.label == $label) | .width' "$viewports_json")
    rows=$(jq -er --arg label "$label" '.[] | select(.label == $label) | .height' "$viewports_json")

    if [[ ! "$cols" =~ ^[0-9]+$ || ! "$rows" =~ ^[0-9]+$ ]]; then
      echo "invalid terminal size for ${label}: ${cols}x${rows}" >&2
      exit 1
    fi

    capture "$page" "$label" "$cols" "$rows"
  done
done < <(jq -r --arg defaults "$viewport_csv" '.[] | [.name, ((.viewports // ($defaults | split(","))) | join(","))] | @tsv' "$pages_json")
