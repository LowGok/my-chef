import AppKit
import Foundation

func sameFile(_ left: URL, _ right: URL) -> Bool {
    let keys: Set<URLResourceKey> = [.fileResourceIdentifierKey, .volumeIdentifierKey]
    guard let lhs = try? left.resourceValues(forKeys: keys), let rhs = try? right.resourceValues(forKeys: keys),
          let leftFile = lhs.fileResourceIdentifier as? NSObject, let rightFile = rhs.fileResourceIdentifier as? NSObject,
          let leftVolume = lhs.volumeIdentifier as? NSObject, let rightVolume = rhs.volumeIdentifier as? NSObject else { return false }
    return leftFile.isEqual(rightFile) && leftVolume.isEqual(rightVolume)
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
        return sameFile(original, original) && sameFile(original, alias)
            && !sameFile(original, separate) && !sameFile(original, missing)
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
let expected = executable.resolvingSymlinksInPath().standardizedFileURL.path
let deadline = Date().addingTimeInterval(60)
var sawApplication = false
var sawOtherExecutable = false
var sawSameBundle = false
var sawSameFile = false
while Date() < deadline {
    let candidates = NSRunningApplication.runningApplications(withBundleIdentifier: identifier)
    sawApplication = sawApplication || !candidates.isEmpty
    for application in candidates where !application.isTerminated {
        guard let actual = application.executableURL else { continue }
        let actualPath = actual.resolvingSymlinksInPath().standardizedFileURL.path
        let inSelectedBundle = actualPath.hasPrefix(bundle.bundleURL.resolvingSymlinksInPath().path + "/")
        sawSameBundle = sawSameBundle || inSelectedBundle
        let identicalFile = inSelectedBundle && sameFile(actual, executable)
        sawSameFile = sawSameFile || identicalFile
        if actualPath == expected || identicalFile {
            print("Selected Xcode UI process is running")
            exit(0)
        }
        sawOtherExecutable = true
    }
    RunLoop.current.run(until: Date().addingTimeInterval(0.5))
}
print("Selected Xcode UI readiness: application=\(sawApplication) otherExecutable=\(sawOtherExecutable) sameBundle=\(sawSameBundle) sameFile=\(sawSameFile)")
exit(1)
