// swift-tools-version:5.10
import PackageDescription

let package = Package(
    name: "SwiftSingle",
    targets: [
        .executableTarget(name: "SwiftSingle"),
        // The contract's test gate runs `swift test`, which fails with
        // "no tests found" unless the package declares a test target, so
        // the scaffold ships one with a passing smoke test.
        .testTarget(
            name: "SwiftSingleTests",
            dependencies: ["SwiftSingle"]
        ),
    ]
)
