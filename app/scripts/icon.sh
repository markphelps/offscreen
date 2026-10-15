#!/bin/bash
# Regenerate app/Resources/AppIcon.icns from docs/assets/icon.svg. The icon is
# committed, so builds don't need this; rerun it after changing the logo.
# macOS draws app icons at 824 of 1024 points with the rest left as margin, so
# the logo is scaled into that box rather than filling the canvas.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
iconset="$work/AppIcon.iconset"
mkdir -p "$iconset" "$root/app/Resources"

cat > "$work/render.swift" <<'SWIFT'
import AppKit

let args = CommandLine.arguments
guard let logo = NSImage(contentsOfFile: args[1]) else {
    FileHandle.standardError.write("cannot read \(args[1])\n".data(using: .utf8)!)
    exit(1)
}
let dir = args[2]
for points in [16, 32, 128, 256, 512] {
    for scale in [1, 2] {
        let pixels = points * scale
        guard let rep = NSBitmapImageRep(
            bitmapDataPlanes: nil, pixelsWide: pixels, pixelsHigh: pixels,
            bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
            colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)
        else { exit(1) }
        let size = CGFloat(pixels)
        let margin = size * 100 / 1024
        NSGraphicsContext.saveGraphicsState()
        NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
        NSGraphicsContext.current?.imageInterpolation = .high
        logo.draw(in: NSRect(x: margin, y: margin, width: size - 2 * margin, height: size - 2 * margin))
        NSGraphicsContext.restoreGraphicsState()
        let name = scale == 1 ? "icon_\(points)x\(points).png" : "icon_\(points)x\(points)@2x.png"
        try rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: "\(dir)/\(name)"))
    }
}
SWIFT

swift "$work/render.swift" "$root/docs/assets/icon.svg" "$iconset"
iconutil --convert icns --output "$root/app/Resources/AppIcon.icns" "$iconset"
printf '%s\n' "$root/app/Resources/AppIcon.icns"
