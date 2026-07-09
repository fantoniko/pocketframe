# PocketFrame
Digital picture frame from the PocketBook Basic Touch e-reader. See the [blog](https://www.malgregator.com/post/pocketframe/) for more information.

<img src='https://github.com/viralpoetry/pocketframe/raw/main/pocketframe_final.jpeg'/>

## Usage

Building from source requires Docker or Docker compatible software and the internet connection for pulling dependencies.

1) Build binary from source by running `./build.sh` on Linux/WSL or `.\build.ps1` on Windows PowerShell.
2) Copy `pocketframe.app` binary from the `build/` directory to the device `applications` directory.
3) For LAN frame mode, copy `pocketframe.cfg.example` to
   `system/config/pocketframe.cfg` on the device, rename it to `pocketframe.cfg`,
   and set `url` to a direct `http://` URL that responds with one JPEG image.

Without `system/config/pocketframe.cfg`, PocketFrame keeps the original local
slideshow mode: create `My pictures/PocketFrame/` and copy JPEG pictures there.

### LAN photo frame

`system/config/pocketframe.cfg` is read on startup and before each refresh:

```ini
url=http://192.168.1.50:8080/frame.jpg?token=replace-with-your-token
manifest_url=http://192.168.1.50:8080/manifest?token=replace-with-your-token
interval_minutes=60
retry_minutes=30
timeout_seconds=15
```

The image endpoint must return JPEG bytes themselves, not HTML or an image
gallery. `manifest_url` is optional, but recommended. Its plain-text response
uses `key=value` lines:

```ini
revision=42
next_poll_seconds=3600
retry_after_seconds=1800
```

`revision` is an opaque value, normally a content hash or monotonically
increasing number. When it is unchanged, PocketFrame does not download the JPEG.
`next_poll_seconds` lets the server use longer intervals at night; on an error,
`retry_after_seconds` controls the next attempt. The configuration values are
used when the manifest omits a timer. The app saves the last revision separately,
so restarting it does not force a JPEG download.

Use a fixed LAN IPv4 address where possible. The service should return
`Cache-Control: no-store`. PocketFrame deliberately accepts only plain
`http://`: a local endpoint avoids TLS startup cost and certificate compatibility
issues of the older PocketBook runtime. Use a long random token in both URLs if
the Wi-Fi network is shared.

### Portainer server

The server is a compact static Go image. It accepts an uploaded JPEG, PNG, or
GIF, applies EXIF rotation for JPEG, converts it to a `1404x1872` grayscale JPEG
at the configured quality, atomically publishes it, and returns a SHA-256
revision. The final runtime image is `scratch`: it contains only the server
binary and has no shell or package manager.

Portainer Git Stacks do not reliably support `build:` from a repository. This
project therefore publishes the server image to GHCR using
`.github/workflows/publish-server.yml`; the stack only references that published
image.

1. Push the repository to GitHub. The workflow publishes
   `ghcr.io/<github-owner>/pocketframe-server:latest`.
2. In GitHub Packages, make the resulting container package public, or configure
   GHCR registry credentials in Portainer.
3. In Portainer select **Stacks**, **Add stack**, **Git Repository** and choose
   `server/portainer-stack.yml` as the Compose path.
4. In the stack environment variables set `POCKETFRAME_IMAGE` to the GHCR image
   and set a random `POCKETFRAME_TOKEN` of at least 16 characters. The remaining
   variables are listed in `server/stack.env.example`.

The stack creates a named volume, so the last prepared frame survives container
updates. It exposes the configured host port, by default `8080`.

Find the server's LAN IPv4 address and put it plus the token in the PocketBook config:

```ini
url=http://192.168.1.50:8080/frame.jpg?token=the-guid-from-above
manifest_url=http://192.168.1.50:8080/manifest?token=the-guid-from-above
```

Upload a JPEG, PNG, or GIF as a raw HTTP body. `curl.exe` example:

```powershell
curl.exe -X POST --data-binary "@C:\Photos\new-frame.jpg" -H "Content-Type: image/jpeg" "http://127.0.0.1:8080/api/frame?token=$token"
```

The response is the new manifest. The PocketBook only requests `/manifest` and
`/frame.jpg`; it never uploads or processes the original image.

For battery life, the default interval is one hour (minimum five minutes). The
app connects only for the request, disconnects only when it started the
connection itself, writes the flash cache only when the JPEG changed, and does
not refresh the e-ink screen when the server returns identical image bytes. The
last successful image remains on screen if Wi-Fi or the service is unavailable.

### Frame controls

* Right-arrow, Page Forward, or OK requests an immediate LAN refresh in remote
  mode. The screen changes only if the server returns a new revision.
* Menu (the three-line button) toggles a centered diagnostic window with the
  current time, time of the last image update, and battery percentage.

The diagnostic window uses only a black-and-white partial E-Ink update. While it
is open, the slideshow and network timers are paused; closing it redraws the
current frame and resumes the normal timer.

The device must already know the Wi-Fi network in PocketBook settings. Leaving
the app in the foreground is required for its timer to run; pressing Home pauses
updates, and returning to PocketFrame restores the cached frame before the next
network refresh.

Alternatively, modify hardcoded settings in the source code (directory, time interval, debug switch).

### Preparing pictures

Use `prepare-pocketbook-image.ps1` on Windows PowerShell to resize and convert pictures before
copying them to the device.

Full-screen crop for PocketBook 740 / InkPad 3:

```powershell
.\prepare-pocketbook-image.ps1 .\input.jpg .\output.jpg
```

Fit without cropping, with white margins if needed:

```powershell
.\prepare-pocketbook-image.ps1 .\input.jpg .\output.jpg -Mode Contain
```

If PowerShell blocks local scripts, run it without changing the system policy:

```powershell
powershell -ExecutionPolicy Bypass -File .\prepare-pocketbook-image.ps1 .\input.jpg .\output.jpg
```

The script auto-rotates from EXIF, converts to grayscale, outputs `1404x1872` JPEG by default, and
uses JPEG quality `85`.

### PocketBook 740 / firmware 6.x notes

PocketBook 740 may expose the applications directory as `applications` instead of `Applications`.
If the application appears in the menu but does not show pictures, rebuild from the current source:
the app now prints startup errors on the screen instead of silently exiting.

The app searches these picture directories:

* `My pictures/PocketFrame/` on internal storage
* `My Pictures/PocketFrame/` on internal storage
* `My pictures/PocketFrame/` on SD card
* `My Pictures/PocketFrame/` on SD card

If tapping the app does absolutely nothing and no PocketFrame text appears, the likely issue is an
SDK/runtime mismatch rather than the picture files. Rebuild the binary with the SDK that matches your
firmware generation.

The startup diagnostics briefly show the screen size and decoded JPEG size/depth before the picture
is rendered. This helps catch device-specific bitmap format issues.

### Useful tips

600x800 picture resolution is the actual fullscreen on the original Basic Touch target.
For PocketBook 740 / InkPad 3, use 1404x1872 portrait images for the sharpest fullscreen output.
The app scales JPEG images to fit the screen and centers them without cropping.
SDK `Stretch` can resize photos of other dimensions as well.

To prevent turning device off, disable power saving features.

  * Settings / Saving Power -> Lock Device after = Off
  * Settings / Saving Power -> Power off after = Off
