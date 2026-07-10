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
url=http://192.168.1.50:8080/frame.jpg?token=replace-with-read-token
manifest_url=http://192.168.1.50:8080/manifest?token=replace-with-read-token
interval_minutes=60
refresh_mode=battery_saver
retry_minutes=30
retry_max_minutes=240
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
`retry_after_seconds` controls the first retry attempt. Each consecutive error
doubles that delay, capped by `retry_max_minutes` (four hours by default), and a
successful request resets the backoff. The configuration values are used when
the manifest omits a timer. The app saves the last revision separately, so
restarting it does not force a JPEG download.

`refresh_mode=battery_saver` is the default and recommended for an e-reader: it
checks once at app start and whenever PocketFrame returns to the foreground, or
when Right, Page Forward, or OK is pressed. It does not continue timed polling
after a successful check. If no cached frame exists, failed first-time setup
continues with exponential retries. Set `refresh_mode=always_on` to schedule
automatic checks using `interval_minutes`; keep PocketBook awake for that mode.

Use a fixed LAN IPv4 address where possible. The service should return
`Cache-Control: no-store`. PocketFrame deliberately accepts only plain
`http://`: a local endpoint avoids TLS startup cost and certificate compatibility
issues of the older PocketBook runtime. Use the read-only token in both URLs.

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

1. Push the repository to GitHub. The workflow publishes an immutable `sha-*`
   tag and a sanitized branch tag. Pushes to `main` also publish `latest`.
2. In GitHub Packages, make the resulting container package public, or configure
   GHCR registry credentials in Portainer.
3. In Portainer select **Stacks**, **Add stack**, **Git Repository** and choose
   `server/portainer-stack.yml` as the Compose path.
4. In the stack environment variables set `POCKETFRAME_IMAGE` to the GHCR image,
   then set different random `POCKETFRAME_READ_TOKEN` and
   `POCKETFRAME_UPLOAD_TOKEN` values of at least 16 characters. The remaining
   variables are listed in `server/stack.env.example`.

The stack creates a named volume, so the last prepared frame survives container
updates. It exposes the configured host port, by default `8080`, and runs with a
read-only root filesystem, dropped Linux capabilities, a memory limit, and a
built-in liveness check. Back up the `pocketframe-data` volume separately if the
published frame must survive host disk loss.

For an existing deployment, `POCKETFRAME_TOKEN` remains a backwards-compatible
fallback for both operations. New deployments should use separate tokens. When
deploying directly from a feature branch, use its published branch tag, for
example `codex-pocketbook-740-fixes`, so Portainer can pull updates without
editing the immutable SHA tag.

Find the server's LAN IPv4 address and put it plus the token in the PocketBook config:

```ini
url=http://192.168.1.50:8080/frame.jpg?token=the-read-token-from-above
manifest_url=http://192.168.1.50:8080/manifest?token=the-read-token-from-above
```

Upload a JPEG, PNG, or GIF as a raw HTTP body. `curl.exe` example:

```powershell
curl.exe -X POST --data-binary "@C:\Photos\new-frame.jpg" -H "Content-Type: image/jpeg" -H "Authorization: Bearer $uploadToken" "http://127.0.0.1:8080/api/frame"
```

The response is the new manifest. The PocketBook only requests `/manifest` and
`/frame.jpg`; it never uploads or processes the original image. A protected
status endpoint is also available for monitoring the active prepared frame:

```powershell
curl.exe "http://127.0.0.1:8080/status?token=$readToken"
```

It returns JSON with readiness, revision, publication time, prepared JPEG size,
target dimensions, and the server's requested polling interval. Uploads are
limited both by encoded size and source pixel count; the defaults are 15 MB and
25 megapixels.

For battery life, the default mode avoids timed polling. Each request connects
only for the request and disconnects immediately afterwards, writes the flash
cache only when the JPEG changed, and does not refresh the e-ink screen when the
server returns an identical revision. The last successful image remains on
screen if Wi-Fi or the service is unavailable.

### Frame controls

* Right-arrow, Page Forward, or OK requests an immediate LAN refresh in remote
  mode. The screen changes only if the server returns a new revision.
* Menu (the three-line button) toggles a centered diagnostic window with the
  current time, image-update time, last network attempt/result/duration, and
  success/failure counters.

The diagnostic window uses only a black-and-white partial E-Ink update. While it
is open, the slideshow and network timers are paused; closing it redraws the
current frame. In `always_on` mode, the normal timer resumes.

The device must already know the Wi-Fi network in PocketBook settings. Leaving
the app in the foreground is required for its timer to run; pressing Home closes
PocketFrame, and opening it again restores the cached frame before checking the
network.

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

### Useful tips

600x800 picture resolution is the actual fullscreen on the original Basic Touch target.
For PocketBook 740 / InkPad 3, use 1404x1872 portrait images for the sharpest fullscreen output.
The app scales JPEG images to fit the screen and centers them without cropping.
SDK `Stretch` can resize photos of other dimensions as well.

### Battery saver mode

For the longest battery life, keep the normal PocketBook power-saving settings
enabled, for example `Lock Device after = 5 or 10 min`. The displayed E-Ink frame
remains visible while locked and consumes almost no display power. Network timers
are paused during sleep; when the device returns to PocketFrame, it immediately
checks the server again. The right-arrow button can always request a manual
refresh while the app is open.

Automatic updates all night require the device to stay awake and will consume
substantially more battery. PocketFrame disconnects Wi-Fi immediately after
every request.
