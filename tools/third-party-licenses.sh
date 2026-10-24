#!/bin/sh
# third-party-licenses.sh <output file>
#
# Writes one text file with the license of every library that is compiled
# into violink (Linux and Windows builds), plus the Go runtime's own license.
# MIT, BSD and Apache licenses all ask for their text to travel with the
# program, so `make release` puts this file next to the executable.
#
# It reads the licenses from Go's module cache, so the libraries must have
# been downloaded (any build does that).
set -e
out="$1"
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
GO="${GO:-go}"

# Every module used by the program, on both platforms: "path version folder"
modules=$(for os in linux windows; do
	GOOS=$os $GO list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' ./cmd/violink
done | sort -u)

{
	echo "Violink includes the third-party software listed below."
	echo "Each part is used under the license that follows its name."
	echo
	echo "================================================================================"
	echo "Go runtime and standard library ($($GO env GOVERSION)) - https://go.dev"
	echo "================================================================================"
	echo
	cat "$($GO env GOROOT)/LICENSE"

	echo "$modules" | while read -r path version dir; do
		[ -n "$path" ] || continue
		echo
		echo "================================================================================"
		echo "$path $version"
		echo "================================================================================"
		echo
		found=0
		for f in "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/COPYING* "$dir"/NOTICE*; do
			if [ -f "$f" ]; then
				cat "$f"
				echo
				found=1
			fi
		done
		if [ $found = 0 ]; then
			echo "third-party-licenses.sh: no license file found for $path" >&2
			exit 1
		fi
	done
} > "$out"

echo "wrote $out ($(echo "$modules" | wc -l) libraries + Go runtime)"
