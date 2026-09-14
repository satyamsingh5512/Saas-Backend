#!/usr/bin/env sh
#
# Assembles the Vercel static output from the same directory the Go binary
# embeds, so the two deployments cannot drift. Nothing is generated or
# transformed: the UI has no build step, and this script deliberately does not
# introduce one.
#
# The only change is which document is the directory index. The Go router maps
# "/" to landing.html and "/app" to index.html, but Vercel resolves "/" against
# the filesystem before it consults any rewrite, so index.html would win "/" and
# put the dashboard where the landing page belongs. Swapping the two names here
# reproduces the server's split with a rewrite that Vercel will actually reach.
#
# Usage: sh scripts/build_vercel_static.sh [output-dir]

set -eu

SRC="internal/routes/web"
OUT="${1:-build/web}"

if [ ! -f "$SRC/index.html" ] || [ ! -f "$SRC/landing.html" ]; then
	echo "error: expected $SRC to contain index.html and landing.html" >&2
	echo "       run this from the repository root" >&2
	exit 1
fi

rm -rf "$OUT"
mkdir -p "$OUT"
cp -R "$SRC/." "$OUT/"

# Order matters: move the dashboard aside before the landing page takes its name.
mv "$OUT/index.html" "$OUT/app.html"
mv "$OUT/landing.html" "$OUT/index.html"

echo "built $OUT (landing -> index.html, dashboard -> app.html)"
