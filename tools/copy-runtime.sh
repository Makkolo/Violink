#!/bin/sh
# copy-runtime.sh <target folder> <linux|windows>
#
# Copies the files that belong next to the executable (runtime/ and the
# platform's packaging/ files) into a build or release folder. Existing files
# are left alone, so edits made while testing are not overwritten.
set -e
target="$1"
platform="$2"
root="$(cd "$(dirname "$0")/.." && pwd)"

copy_tree() {
	src="$1"
	[ -d "$src" ] || return 0
	(cd "$src" && find . -type f) | while read -r f; do
		if [ ! -e "$target/$f" ]; then
			mkdir -p "$target/$(dirname "$f")"
			cp "$src/$f" "$target/$f"
		fi
	done
}

mkdir -p "$target"
copy_tree "$root/runtime"
copy_tree "$root/packaging/$platform"
if [ "$platform" = "linux" ]; then
	# Icons for install-desktop.sh (menu entry, desktop icon)
	mkdir -p "$target/icons"
	(cd "$root/assets/linux" && find hicolor -type f) | while read -r f; do
		if [ ! -e "$target/icons/$f" ]; then
			mkdir -p "$target/icons/$(dirname "$f")"
			cp "$root/assets/linux/$f" "$target/icons/$f"
		fi
	done
fi
[ -e "$target/LICENSE" ] || cp "$root/LICENSE" "$target/LICENSE"
