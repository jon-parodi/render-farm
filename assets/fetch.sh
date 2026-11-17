#!/bin/sh
# Downloads the third-party scene assets (CC0 / Khronos sample licences) into
# the directory given as $1, then verifies them against SHA256SUMS.
# Sources are pinned to a commit so the checksums stay valid.
set -eu
out=${1:-$(dirname "$0")}
sums=$(cd "$(dirname "$0")" && pwd)/SHA256SUMS
khronos=https://raw.githubusercontent.com/KhronosGroup/glTF-Sample-Assets/f0920f5b32d172a30efe3f9257e965918831e6ea/Models

get() { # url dest
  [ -s "$2" ] || curl -fsSL --retry 4 -o "$2" "$1"
}

model() { # name
  mkdir -p "$out/$1"
  get "$khronos/$1/glTF/$1.gltf" "$out/$1/$1.gltf"
  grep -o '"uri" *: *"[^"]*"' "$out/$1/$1.gltf" | sed 's/.*: *"//; s/"$//' | sort -u |
    while read -r f; do get "$khronos/$1/glTF/$f" "$out/$1/$f"; done
}

model Sponza
model Suzanne
get https://dl.polyhaven.org/file/ph-assets/HDRIs/hdr/2k/studio_small_09_2k.hdr "$out/studio.hdr"

cd "$out"
if [ -f "$sums" ]; then
  sha256sum -c "$sums" > /dev/null
else
  find Sponza Suzanne studio.hdr -type f ! -name '.*' | sort | xargs sha256sum > "$sums"
  echo "wrote $sums"
fi
