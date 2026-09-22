// swift-tools-version: 5.9
import PackageDescription

// EmbeddedKit wraps the gomobile-generated LatticeEmbedded framework (built
// by apple/scripts/build_embedded_framework.sh) in a small Swift-idiomatic
// API, plus an XCTest harness that verifies the engine end to end against
// the local mac-demo control plane and containers.
let package = Package(
    name: "EmbeddedKit",
    platforms: [
        .iOS(.v15),
        .macOS(.v13),
    ],
    products: [
        .library(name: "EmbeddedKit", targets: ["EmbeddedKit"]),
    ],
    targets: [
        .target(
            name: "EmbeddedKit",
            dependencies: ["LatticeEmbedded"],
            linkerSettings: [
                // The static gomobile binary resolves DNS via libresolv.
                .linkedLibrary("resolv"),
            ]
        ),
        .binaryTarget(
            name: "LatticeEmbedded",
            path: "../Frameworks/Combined/LatticeEmbedded.xcframework"
        ),
        .testTarget(
            name: "EmbeddedKitTests",
            dependencies: ["EmbeddedKit"]
        ),
    ]
)
