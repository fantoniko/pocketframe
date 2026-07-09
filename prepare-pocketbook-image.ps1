param(
    [Parameter(Mandatory = $true, Position = 0)]
    [string] $InputPath,

    [Parameter(Position = 1)]
    [string] $OutputPath,

    [ValidateSet("Cover", "Contain")]
    [string] $Mode = "Cover",

    [int] $Width = 1404,
    [int] $Height = 1872,

    [ValidateRange(1, 100)]
    [int] $Quality = 85
)

if (-not (Test-Path -LiteralPath $InputPath)) {
    throw "Input file not found: $InputPath"
}

if (-not $OutputPath) {
    $inputItem = Get-Item -LiteralPath $InputPath
    $OutputPath = Join-Path $inputItem.DirectoryName "$($inputItem.BaseName)_pocketbook.jpg"
}

Add-Type -AssemblyName System.Drawing

function Get-JpegEncoder {
    $encoders = [System.Drawing.Imaging.ImageCodecInfo]::GetImageEncoders()
    foreach ($encoder in $encoders) {
        if ($encoder.MimeType -eq "image/jpeg") {
            return $encoder
        }
    }
    throw "JPEG encoder is not available."
}

function Apply-ExifOrientation {
    param(
        [Parameter(Mandatory = $true)]
        [System.Drawing.Image] $Image
    )

    $orientationId = 274
    if ($Image.PropertyIdList -notcontains $orientationId) {
        return
    }

    $orientation = [BitConverter]::ToUInt16($Image.GetPropertyItem($orientationId).Value, 0)
    switch ($orientation) {
        2 { $Image.RotateFlip([System.Drawing.RotateFlipType]::RotateNoneFlipX) }
        3 { $Image.RotateFlip([System.Drawing.RotateFlipType]::Rotate180FlipNone) }
        4 { $Image.RotateFlip([System.Drawing.RotateFlipType]::Rotate180FlipX) }
        5 { $Image.RotateFlip([System.Drawing.RotateFlipType]::Rotate90FlipX) }
        6 { $Image.RotateFlip([System.Drawing.RotateFlipType]::Rotate90FlipNone) }
        7 { $Image.RotateFlip([System.Drawing.RotateFlipType]::Rotate270FlipX) }
        8 { $Image.RotateFlip([System.Drawing.RotateFlipType]::Rotate270FlipNone) }
    }

    try {
        $Image.RemovePropertyItem($orientationId)
    } catch {
        # Some image formats expose read-only metadata.
    }
}

function Get-DrawRectangle {
    param(
        [Parameter(Mandatory = $true)]
        [System.Drawing.Image] $Image,

        [Parameter(Mandatory = $true)]
        [string] $Mode,

        [Parameter(Mandatory = $true)]
        [int] $TargetWidth,

        [Parameter(Mandatory = $true)]
        [int] $TargetHeight
    )

    $scaleX = $TargetWidth / [double] $Image.Width
    $scaleY = $TargetHeight / [double] $Image.Height
    if ($Mode -eq "Cover") {
        $scale = [Math]::Max($scaleX, $scaleY)
    } else {
        $scale = [Math]::Min($scaleX, $scaleY)
    }

    $drawWidth = [int] [Math]::Round($Image.Width * $scale)
    $drawHeight = [int] [Math]::Round($Image.Height * $scale)
    $x = [int] [Math]::Floor(($TargetWidth - $drawWidth) / 2)
    $y = [int] [Math]::Floor(($TargetHeight - $drawHeight) / 2)

    return [System.Drawing.Rectangle]::new($x, $y, $drawWidth, $drawHeight)
}

$source = $null
$target = $null
$graphics = $null
$imageAttributes = $null
$encoderParams = $null

try {
    $source = [System.Drawing.Image]::FromFile((Resolve-Path -LiteralPath $InputPath))
    Apply-ExifOrientation -Image $source

    $target = [System.Drawing.Bitmap]::new($Width, $Height, [System.Drawing.Imaging.PixelFormat]::Format24bppRgb)
    $target.SetResolution(72, 72)

    $graphics = [System.Drawing.Graphics]::FromImage($target)
    $graphics.CompositingQuality = [System.Drawing.Drawing2D.CompositingQuality]::HighQuality
    $graphics.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
    $graphics.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
    $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::HighQuality
    $graphics.Clear([System.Drawing.Color]::White)

    $colorMatrix = [System.Drawing.Imaging.ColorMatrix]::new(@(
        @(0.299, 0.299, 0.299, 0, 0),
        @(0.587, 0.587, 0.587, 0, 0),
        @(0.114, 0.114, 0.114, 0, 0),
        @(0,     0,     0,     1, 0),
        @(0,     0,     0,     0, 1)
    ))
    $imageAttributes = [System.Drawing.Imaging.ImageAttributes]::new()
    $imageAttributes.SetColorMatrix($colorMatrix)

    $destinationRect = Get-DrawRectangle -Image $source -Mode $Mode -TargetWidth $Width -TargetHeight $Height
    $graphics.DrawImage(
        $source,
        $destinationRect,
        0,
        0,
        $source.Width,
        $source.Height,
        [System.Drawing.GraphicsUnit]::Pixel,
        $imageAttributes
    )

    $outputDirectory = Split-Path -Parent $OutputPath
    if ($outputDirectory) {
        New-Item -ItemType Directory -Force -Path $outputDirectory | Out-Null
    }

    $qualityEncoder = [System.Drawing.Imaging.Encoder]::Quality
    $encoderParams = [System.Drawing.Imaging.EncoderParameters]::new(1)
    $encoderParams.Param[0] = [System.Drawing.Imaging.EncoderParameter]::new($qualityEncoder, [long] $Quality)

    $jpegEncoder = Get-JpegEncoder
    $target.Save($OutputPath, $jpegEncoder, $encoderParams)

    $outputItem = Get-Item -LiteralPath $OutputPath
    Write-Host "Saved $($outputItem.FullName)"
    Write-Host "Mode=$Mode Size=${Width}x${Height} Grayscale JPEG Quality=$Quality"
} finally {
    if ($encoderParams) {
        $encoderParams.Dispose()
    }
    if ($imageAttributes) {
        $imageAttributes.Dispose()
    }
    if ($graphics) {
        $graphics.Dispose()
    }
    if ($target) {
        $target.Dispose()
    }
    if ($source) {
        $source.Dispose()
    }
}
