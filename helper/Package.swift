// swift-tools-version: 6.2
import PackageDescription

let package = Package(
 name: "OffscreenHelper",
 platforms: [.macOS(.v14)],
 products: [.executable(name: "OffscreenHelper", targets: ["OffscreenHelper"])],
 targets: [
  .executableTarget(name: "OffscreenHelper"),
  .testTarget(name: "OffscreenHelperTests", dependencies: ["OffscreenHelper"])
 ]
)
