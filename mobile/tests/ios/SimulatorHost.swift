import AppKit
import Foundation

// Runner-local UI readiness only; never compiled into the MyChef app.
guard CommandLine.arguments.count == 2,
      let bundle = Bundle(url: URL(fileURLWithPath: CommandLine.arguments[1])),
      let identifier = bundle.bundleIdentifier,
      let executable = bundle.executableURL else {
    print("Selected Xcode UI metadata unavailable")
    exit(1)
}
let expected = executable.resolvingSymlinksInPath().standardizedFileURL
let deadline = Date().addingTimeInterval(60)
var sawApplication = false
var sawOtherExecutable = false
while Date() < deadline {
    let candidates = NSRunningApplication.runningApplications(withBundleIdentifier: identifier)
    sawApplication = sawApplication || !candidates.isEmpty
    for application in candidates where !application.isTerminated {
        guard let actual = application.executableURL else { continue }
        if actual.resolvingSymlinksInPath().standardizedFileURL == expected {
            print("Selected Xcode UI process is running")
            exit(0)
        }
        sawOtherExecutable = true
    }
    RunLoop.current.run(until: Date().addingTimeInterval(0.5))
}
print("Selected Xcode UI readiness: application=\(sawApplication) otherExecutable=\(sawOtherExecutable)")
exit(1)
