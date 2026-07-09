# PocketFrame
Digital picture frame from the PocketBook Basic Touch e-reader. See the [blog](https://www.malgregator.com/post/pocketframe/) for more information.

<img src='https://github.com/viralpoetry/pocketframe/raw/main/pocketframe_final.jpeg'/>

## Usage

Building from source requires Docker or Docker compatible software and the internet connection for pulling dependencies.

1) Build binary from source by running `./build.sh` on Linux/WSL or `.\build.ps1` on Windows PowerShell.
2) Copy `pocketframe.app` binary from the `build/` directory to the device `applications` directory.
3) Create folder `My pictures/PocketFrame/` and copy desired `jpeg` pictures to it.

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
