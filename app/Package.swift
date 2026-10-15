// swift-tools-version: 6.2
import PackageDescription

let package = Package(
 name: "Offscreen",
 platforms: [.macOS(.v14)],
 products: [.executable(name: "Offscreen", targets: ["Offscreen"])],
 targets: [
  .executableTarget(name: "Offscreen"),
  .testTarget(name: "OffscreenTests", dependencies: ["Offscreen"])
 ]
)
