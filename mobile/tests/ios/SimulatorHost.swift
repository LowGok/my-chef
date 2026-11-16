import AppKit
import Foundation

func sameFile(_ left: URL, _ right: URL) -> Bool {
    let keys: Set<URLResourceKey> = [.fileResourceIdentifierKey, .volumeIdentifierKey]
    guard let lhs = try? left.resourceValues(forKeys: keys), let rhs = try? right.resourceValues(forKeys: keys),
          let leftFile = lhs.fileResourceIdentifier as? NSObject, let rightFile = rhs.fileResourceIdentifier as? NSObject,
          let leftVolume = lhs.volumeIdentifier as? NSObject, let rightVolume = rhs.volumeIdentifier as? NSObject else { return false }
    return leftFile.isEqual(rightFile) && leftVolume.isEqual(rightVolume)
}
func executableCandidates(_ bundleURL: URL, _ identifier: String, _ declared: URL) -> [URL] {
    var candidates = [declared]
    // Device Hub's metadata can name its trampoline while the actual launched
    // app executes this specific native binary. Do not allow arbitrary helpers.
    let native = bundleURL.appendingPathComponent("Contents/MacOS/DeviceHub").resolvingSymlinksInPath()
    let attributes = try? FileManager.default.attributesOfItem(atPath: native.path)
    if identifier == "com.apple.dt.Devices" && declared.lastPathComponent == "DevicesTrampoline",
       attributes?[.type] as? FileAttributeType == .typeRegular,
       FileManager.default.isExecutableFile(atPath: native.path) {
        candidates.append(native)
    }
    return candidates
}

// Exercise the actual filesystem identity implementation on the selected Mac.
// A hard link must match; a separate file with the same bytes must not.
func identitySelfTest() -> Bool {
    let manager = FileManager.default
    let root = manager.temporaryDirectory.appendingPathComponent("mychef-host-identity-" + UUID().uuidString, isDirectory: true)
    defer { try? manager.removeItem(at: root) }
    do {
        try manager.createDirectory(at: root, withIntermediateDirectories: false)
        let original = root.appendingPathComponent("original")
        let alias = root.appendingPathComponent("alias")
        let separate = root.appendingPathComponent("separate")
        let missing = root.appendingPathComponent("missing")
        let bytes = Data("fictional simulator identity".utf8)
        try bytes.write(to: original)
        try bytes.write(to: separate)
        try manager.linkItem(at: original, to: alias)
        let filesPassed = sameFile(original, original) && sameFile(original, alias)
            && !sameFile(original, separate) && !sameFile(original, missing)
        let app = root.appendingPathComponent("DeviceHub.app")
        let binaries = app.appendingPathComponent("Contents/MacOS", isDirectory: true)
        try manager.createDirectory(at: binaries, withIntermediateDirectories: true)
        let trampoline = binaries.appendingPathComponent("DevicesTrampoline")
        let native = binaries.appendingPathComponent("DeviceHub")
        try bytes.write(to: trampoline)
        try bytes.write(to: native)
        try manager.setAttributes([.posixPermissions: 0o700], ofItemAtPath: native.path)
        let allowed = executableCandidates(app, "com.apple.dt.Devices", trampoline)
        let scopePassed = allowed.count == 2 && allowed.contains(where: { sameFile($0, native) })
            && executableCandidates(app, "com.example.other", trampoline).count == 1
            && executableCandidates(app, "com.apple.dt.Devices", original).count == 1
        try manager.removeItem(at: native)
        let missingPassed = executableCandidates(app, "com.apple.dt.Devices", trampoline).count == 1
        try manager.createDirectory(at: native, withIntermediateDirectories: false)
        let directoryRefused = executableCandidates(app, "com.apple.dt.Devices", trampoline).count == 1
        return filesPassed && scopePassed && missingPassed && directoryRefused
    } catch { return false }
}
if CommandLine.arguments.count == 2 && CommandLine.arguments[1] == "--identity-self-test" {
    let passed = identitySelfTest()
    print(passed ? "Simulator host file identity checks passed" : "Simulator host file identity checks failed")
    exit(passed ? 0 : 1)
}
// Runner-local UI readiness only; never compiled into the MyChef app.
guard CommandLine.arguments.count == 2,
      let bundle = Bundle(url: URL(fileURLWithPath: CommandLine.arguments[1])),
      let identifier = bundle.bundleIdentifier,
      let executable = bundle.executableURL else {
    print("Selected Xcode UI metadata unavailable")
    exit(1)
}
let candidates = executableCandidates(bundle.bundleURL, identifier, executable)
let selectedBundle = bundle.bundleURL.resolvingSymlinksInPath().standardizedFileURL.path
func executableKind(_ url: URL) -> String {
    switch url.lastPathComponent {
    case "DeviceHub": return "devicehub"
    case "DevicesTrampoline": return "trampoline"
    case "Simulator": return "simulator"
    default: return "other"
    }
}
// Obtain the actual app returned for this explicit SDK URL. Do not select a
// process merely because it shares a bundle identifier or installation folder.
let configuration = NSWorkspace.OpenConfiguration()
configuration.createsNewApplicationInstance = true
configuration.allowsRunningApplicationSubstitution = false
configuration.promptsUserIfNeeded = false
configuration.addsToRecentItems = false
if let developer = ProcessInfo.processInfo.environment["DEVELOPER_DIR"], !developer.isEmpty {
    configuration.environment = ["DEVELOPER_DIR": developer]
}
var launched: NSRunningApplication?
var launchCompleted = false
var launchFailed = false
NSWorkspace.shared.openApplication(at: bundle.bundleURL, configuration: configuration) { application, error in
    DispatchQueue.main.async {
        launched = application
        launchFailed = error != nil
        launchCompleted = true
    }
}
let deadline = Date().addingTimeInterval(60)
var sawApplication = false
var sawOtherExecutable = false
var sawSameBundle = false
var sawSameFile = false
var actualKind = "other"
while Date() < deadline {
    if launchCompleted && launchFailed { break }
    if let application = launched, application.processIdentifier > 0, !application.isTerminated,
       let actual = application.executableURL {
        sawApplication = true
        actualKind = executableKind(actual)
        let actualPath = actual.resolvingSymlinksInPath().standardizedFileURL.path
        let actualBundle = application.bundleURL?.resolvingSymlinksInPath().standardizedFileURL.path
        let inSelectedBundle = actualBundle == selectedBundle && actualPath.hasPrefix(selectedBundle + "/")
        sawSameBundle = sawSameBundle || inSelectedBundle
        let identicalFile = inSelectedBundle && candidates.contains { candidate in
            actualPath == candidate.resolvingSymlinksInPath().standardizedFileURL.path || sameFile(actual, candidate)
        }
        sawSameFile = sawSameFile || identicalFile
        if inSelectedBundle && application.bundleIdentifier == identifier && identicalFile {
            print("Selected Xcode UI process is running")
            exit(0)
        }
        sawOtherExecutable = true
    }
    RunLoop.current.run(until: Date().addingTimeInterval(0.5))
}
print("Selected Xcode UI readiness: application=\(sawApplication) otherExecutable=\(sawOtherExecutable) sameBundle=\(sawSameBundle) sameFile=\(sawSameFile) launchCompleted=\(launchCompleted) launchFailed=\(launchFailed) expected=\(executableKind(executable)) actual=\(actualKind) candidates=\(candidates.count)")
exit(1)
