// swift-tools-version:5.10
import PackageDescription

let package = Package(
    name: "SwiftSingle",
    targets: [
        // The module names are PascalCase (Swift identifiers), but the
        // source tree is kebab-case so the repository passes its own
        // kebab-case naming rule; the explicit paths point the targets
        // at it.
        .executableTarget(name: "SwiftSingle", path: "Sources/swift-single"),
        // The contract's test gate runs `swift test`, which fails with
        // "no tests found" unless the package declares a test target, so
        // the scaffold ships one with a passing smoke test.
        .testTarget(
            name: "SwiftSingleTests",
            dependencies: ["SwiftSingle"],
            path: "Tests/swift-single-tests"
        ),
    ]
)
