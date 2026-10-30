#!/bin/sh
# Assemble docs/images/demo.gif from the frames preview.mjs wrote.
#
# The frames come from the real UI driving a seeded session, so nothing here
# invents a state the app cannot reach. The pacing is deliberate: each frame
# holds for a readable beat with a short fade in and out, rather than racing
# through states. Plan spec: at least 960px wide, 12-15 fps, 15-25 seconds, at
# most 10 MiB.
#
# Run from the repo root after: npm --prefix web run build && node web/scripts/preview.mjs
set -eu

frames="${1:-/tmp/plan-forge-preview/gif}"
out="${2:-docs/images/demo.gif}"
hold=${HOLD:-2.6}
fade=${FADE:-0.35}
fps=${FPS:-13}

if [ ! -d "$frames" ]; then
  echo "no frames at $frames; run node web/scripts/preview.mjs first" >&2
  exit 1
fi
if ! command -v ffmpeg >/dev/null 2>&1; then
  echo "ffmpeg is required to assemble the GIF" >&2
  exit 1
fi

listing=$(ls "$frames"/gif-*.png 2>/dev/null | sort)
count=$(printf '%s\n' "$listing" | grep -c . || true)
if [ "$count" -lt 2 ]; then
  echo "need at least two frames, found $count" >&2
  exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# One clip per frame: the still held for `hold`, scaled once, fading in and out.
# Clips are then concatenated, which is exact and needs no label juggling.
n=0
for f in $listing; do
  n=$((n + 1))
  # The fade-out start must be computed here, not inside the filter string: a
  # nested awk quote inside double quotes collapses the whole argument.
  fade_out=$(awk -v h="$hold" -v f="$fade" 'BEGIN { printf "%.2f", h - f }')
  ffmpeg -loglevel error -y -loop 1 -t "$hold" -i "$f" \
    -vf "scale=960:-1:flags=lanczos,fps=$fps,fade=t=in:st=0:d=$fade,fade=t=out:st=$fade_out:d=$fade" \
    -c:v libx264 -pix_fmt yuv420p -crf 18 "$work/c$n.mp4"
  printf "file '%s'\n" "$work/c$n.mp4" >> "$work/list.txt"
done

ffmpeg -loglevel error -y -f concat -safe 0 -i "$work/list.txt" -c copy "$work/demo.mp4"

# Two passes: a palette built from the whole clip, then the GIF. This is what
# keeps a UI capture readable and under the size cap instead of banding.
mkdir -p "$(dirname "$out")"
ffmpeg -loglevel error -y -i "$work/demo.mp4" \
  -vf "fps=$fps,palettegen=stats_mode=diff" "$work/palette.png"
ffmpeg -loglevel error -y -i "$work/demo.mp4" -i "$work/palette.png" \
  -lavfi "fps=$fps,paletteuse=dither=bayer:bayer_scale=4" -loop 0 "$out"

bytes=$(wc -c < "$out" | tr -d ' ')
limit=$((10 * 1024 * 1024))
if [ "$bytes" -gt "$limit" ]; then
  echo "demo.gif is $bytes bytes, over the 10 MiB cap; lower HOLD or FPS" >&2
  exit 1
fi
echo "wrote $out ($bytes bytes)"
