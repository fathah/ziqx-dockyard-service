import AppKit
import Foundation

let source = CommandLine.arguments[1]
let output = CommandLine.arguments[2]
let side = 1024

guard let logo = NSImage(contentsOfFile: source),
      let bitmap = NSBitmapImageRep(
        bitmapDataPlanes: nil,
        pixelsWide: side,
        pixelsHigh: side,
        bitsPerSample: 8,
        samplesPerPixel: 4,
        hasAlpha: true,
        isPlanar: false,
        colorSpaceName: .deviceRGB,
        bytesPerRow: 0,
        bitsPerPixel: 0
      ),
      let context = NSGraphicsContext(bitmapImageRep: bitmap)
else {
  fatalError("Could not load the Dockyard logo or create the icon canvas")
}

NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = context
context.imageInterpolation = .high
NSColor.clear.setFill()
NSRect(x: 0, y: 0, width: side, height: side).fill()

let background = NSBezierPath(
  roundedRect: NSRect(x: 0, y: 0, width: side, height: side),
  xRadius: 224,
  yRadius: 224
)
NSColor(srgbRed: 23.0 / 255, green: 59.0 / 255, blue: 49.0 / 255, alpha: 1).setFill()
background.fill()

let logoWidth: CGFloat = 640
let logoHeight = logoWidth * logo.size.height / logo.size.width
let destination = NSRect(
  x: (CGFloat(side) - logoWidth) / 2,
  y: (CGFloat(side) - logoHeight) / 2,
  width: logoWidth,
  height: logoHeight
)
logo.draw(in: destination, from: NSRect(origin: .zero, size: logo.size), operation: .sourceOver, fraction: 1)
context.flushGraphics()
NSGraphicsContext.restoreGraphicsState()

guard let png = bitmap.representation(using: .png, properties: [:]) else {
  fatalError("Could not encode the Dockyard app icon")
}
try png.write(to: URL(fileURLWithPath: output), options: .atomic)
