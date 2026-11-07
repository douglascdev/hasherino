# Hasherino

A Twitch chat client with support for Twitch and 7TV emotes, user badges, chat
history, and a mobile-friendly settings dialog.

The repository contains two frontends:

- `main.go` — the Gio UI, used for the Flatpak build.
- `fyne_ui.go` — the Fyne UI.

## Install with Flatpak

### From a prebuilt bundle

If you already have `com.hashysoftware.hasherino.flatpak`, install it for your
user:

```bash
flatpak install --user ./com.hashysoftware.hasherino.flatpak
```

Run it:

```bash
flatpak run com.hashysoftware.hasherino
```

Uninstall it:

```bash
flatpak uninstall --user com.hashysoftware.hasherino
```

You need Flatpak installed and the Flathub remote configured. The Flatpak
runtime (`org.freedesktop.Platform//25.08`) is installed automatically when
needed.

### Build the bundle from source

Building requires [Nix](https://nixos.org/) with flakes enabled. The dev shell
provides Go, `flatpak`, `flatpak-builder`, and the other packaging tools.

```bash
git clone https://github.com/Hashy-Software/hasherino-go.git
cd hasherino-go
nix develop -c ./build-flatpak.sh
```

The script creates `com.hashysoftware.hasherino.flatpak` in the repository root.
You can also pass an output path:

```bash
nix develop -c ./build-flatpak.sh /tmp/hasherino.flatpak
```

Then install it:

```bash
flatpak install --user ./com.hashysoftware.hasherino.flatpak
flatpak run com.hashysoftware.hasherino
```

Notes:

- The Flatpak build compiles `main.go` (the Gio UI), not `fyne_ui.go`.
- The first build downloads the Freedesktop SDK and Go SDK extension from
  Flathub, as well as the Go modules needed by the application.
- The Flatpak requests network access, Wayland/X11, IPC, and GPU access.
- Application data is stored in
  `~/.var/app/com.hashysoftware.hasherino/config/hasherino`.

### Troubleshooting: opening the Twitch authorization page

Flatpak apps open external links through the host's desktop portal. If the
portal is not available, "Open Twitch" shows a message instead of launching a
browser; use **Copy link** and open the URL manually. The underlying host error
looks like:

```text
Failed to call portal: GDBus.Error:org.freedesktop.DBus.Error.NameHasNoOwner:
Could not activate remote peer 'org.freedesktop.portal.Desktop': startup job failed
```

This means `xdg-desktop-portal` is not running or failed to start on the host,
not that the application is broken. Enabling/fixing the desktop portal service
restores automatic browser opening.

## Build without Flatpak

With the Nix dev shell active:

```bash
nix develop -c go build -o hasherino main.go
./hasherino
```
