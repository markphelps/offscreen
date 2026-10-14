import AppKit

/// The offscreen logo as a menu bar template image: an empty display in front
/// of a filled one, cut apart by a gap. While a job runs, a cursor shows on the
/// display behind. Coordinates are Retina pixels (half a point), so edges land
/// on whole pixels.
enum MenuBarIcon {
    static let size = NSSize(width: 23, height: 19)

    static func image(running: Bool) -> NSImage {
        let image = NSImage(size: size, flipped: true) { _ in
            draw(running: running)
            return true
        }
        image.isTemplate = true
        image.accessibilityDescription = running ? "Offscreen, job running" : "Offscreen"
        return image
    }

    private static func draw(running: Bool) {
        guard let context = NSGraphicsContext.current else { return }
        context.saveGraphicsState()
        defer { context.restoreGraphicsState() }
        let transform = NSAffineTransform()
        transform.scale(by: 0.5)
        transform.concat()

        NSColor.black.set()
        NSBezierPath(roundedRect: NSRect(x: 17, y: 2, width: 27, height: 20), xRadius: 4, yRadius: 4).fill()

        context.compositingOperation = .clear
        let front = NSRect(x: 3.5, y: 14.5, width: 29, height: 21)
        let gap = NSBezierPath(roundedRect: front, xRadius: 4, yRadius: 4)
        gap.lineWidth = 9
        gap.fill()
        gap.stroke()
        if running {
            NSBezierPath(rect: NSRect(x: 40, y: 5, width: 2, height: 6)).fill()
        }

        context.compositingOperation = .sourceOver
        let outline = NSBezierPath(roundedRect: front, xRadius: 4, yRadius: 4)
        outline.lineWidth = 3
        outline.stroke()
    }
}
