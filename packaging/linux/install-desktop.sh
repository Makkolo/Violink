#!/bin/sh
# install-desktop.sh [--autostart]
#
# Adds Violink to the application menu (KDE, GNOME, ...) with its icon.
# With --autostart it also starts when you log in.
# Run it from the folder that contains the violink executable.
set -e
here="$(cd "$(dirname "$0")" && pwd)"
icons="$here/icons/hicolor"
share="${XDG_DATA_HOME:-$HOME/.local/share}"

if [ -d "$icons" ]; then
	mkdir -p "$share/icons"
	cp -r "$icons" "$share/icons/"
fi

mkdir -p "$share/applications"
cat > "$share/applications/violink.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=Violink
Comment=Scriptable I/O bridge
Exec="$here/violink"
Path=$here
Icon=violink
Terminal=false
Categories=Utility;Network;
DESKTOP
echo "Added to the application menu: $share/applications/violink.desktop"

if [ "$1" = "--autostart" ]; then
	auto="${XDG_CONFIG_HOME:-$HOME/.config}/autostart"
	mkdir -p "$auto"
	cp "$share/applications/violink.desktop" "$auto/violink.desktop"
	echo "Starts at login: $auto/violink.desktop"
fi
