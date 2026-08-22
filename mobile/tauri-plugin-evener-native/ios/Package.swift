// swift-tools-version:5.9
// The swift-tools-version declares the minimum version of Swift required to build this package.

import PackageDescription

let package = Package(
    name: "EvenerNativePlugin",
    platforms: [
        .iOS(.v17),
    ],
    products: [
        .library(
            name: "EvenerNativePlugin",
            type: .static,
            targets: ["EvenerNativePlugin"]),
    ],
    dependencies: [
        .package(name: "Tauri", path: "../.tauri/tauri-api")
    ],
    targets: [
        .target(
            name: "EvenerNativePlugin",
            dependencies: [
                .byName(name: "Tauri")
            ],
            path: "Sources"),
        .testTarget(
            name: "EvenerNativePluginTests",
            dependencies: ["EvenerNativePlugin"],
            path: "Tests/PluginTests",
            resources: [.copy("Fixtures/contract-v1.json")])
    ]
)
