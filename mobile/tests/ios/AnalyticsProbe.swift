import XCTest
import UIKit
import AVFAudio

// Runs against the already installed, digest-verified simulator app. This test
// bundle is a separate unsigned probe and is never a distributed application.
final class AnalyticsProbe: XCTestCase {
    let app = XCUIApplication(bundleIdentifier: "com.gkldevelopment.mychef.preview")
    var configuration: [String: String] = [:]
    var observations: [String: [String: Bool]] = [:]
    var hasEnteredData = false

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let path = ProcessInfo.processInfo.environment["MYCHEF_ANALYTICS_PROBE_INPUT"],
              let data = try? Data(contentsOf: URL(fileURLWithPath: path)),
              let value = try JSONSerialization.jsonObject(with: data) as? [String: String],
              value["url"]?.range(of: "^http://127.0.0.1:[0-9]{1,5}/control$", options: .regularExpression) != nil,
              value["token"]?.count == 43 else { throw Failure.invalidFixture }
        configuration = value
        app.launchArguments = ["--mychef-analytics-native-fixture"]
        guard let path = value["contract"], let data = try? Data(contentsOf: URL(fileURLWithPath: path)),
              let contract = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              let groups = contract["checks"] as? [String: [String]] else { throw Failure.invalidFixture }
        observations = groups.mapValues { Dictionary(uniqueKeysWithValues: $0.map { ($0, false) }) }
    }

    enum Failure: Error { case invalidFixture, controllerRefused, missingControl }
    func control(_ operation: String, _ fields: [String: Any] = [:]) throws -> [String: Any] {
        guard let url = URL(string: configuration["url"] ?? "") else { throw Failure.invalidFixture }
        var input = fields; input["operation"] = operation
        // The fixture can spend 15 seconds stopping and 35 becoming ready.
        // This is the private test controller, not a product request budget.
        let controllerTimeout: TimeInterval = 60
        var request = URLRequest(url: url, timeoutInterval: controllerTimeout)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("Bearer " + (configuration["token"] ?? ""), forHTTPHeaderField: "Authorization")
        request.httpBody = try JSONSerialization.data(withJSONObject: input)
        let finished = expectation(description: "Private fixture operation")
        var result: [String: Any]?
        URLSession.shared.dataTask(with: request) { data, response, error in
            let status = (response as? HTTPURLResponse)?.statusCode ?? 0
            if status == 200, let data = data {
                result = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
            }
            if result == nil {
                let code = error.map { ($0 as NSError).code } ?? 0
                let network = [0, -999, -1001, -1003, -1004, -1005, -1009, -1022].contains(code) ? String(code) : "other"
                let http = (100...599).contains(status) ? status : 0
                let kind = operation == "approved" ? "restart" : "operation"
                // Fixed categories only: no URL, token, fields or response body.
                print("Analytics iOS controller: kind=\(kind) status=\(http) network=" + network)
            }
            finished.fulfill()
        }.resume()
        wait(for: [finished], timeout: controllerTimeout + 5)
        guard let result = result else { throw Failure.controllerRefused }
        return result
    }
    func mark(_ group: String, _ names: String...) {
        for name in names {
            XCTAssertNotNil(observations[group]?[name], "Closed observation must exist")
            observations[group]?[name] = true
        }
    }
    func screenState() {
        print("Analytics iOS app state: \(app.state.rawValue) webview=\(app.webViews.count) alert=\(app.alerts.count)")
        for (name, phrase) in [("signed out", "Welcome back."), ("profile", "Your profile."), ("shopping", "Shopping list."), ("startup", "Loading MyChef"), ("inbox", "Check your inbox"), ("signup", "Let’s get started."), ("secure failure", "MyChef couldn’t open securely"), ("configuration failure", "Reference: MYC-01"), ("storage failure", "Reference: MYC-02"), ("shopping failure", "Reference: MYC-03"), ("device failure", "Reference: MYC-04")] {
            let present = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", phrase)).firstMatch.exists
            print("Analytics iOS screen state: " + name + " present=\(present)")
        }
    }
    func waitFor(_ condition: @escaping () -> Bool, _ message: String, timeout: TimeInterval = 35,
                 onTimeout: (() -> Void)? = nil) {
        let end = Date().addingTimeInterval(timeout)
        while Date() < end {
            if condition() { return }
            RunLoop.current.run(until: Date().addingTimeInterval(0.2))
        }
        onTimeout?()
        for (name, label) in [("Shopping destination", "Shopping list."), ("Profile destination", "Your profile."), ("Shopping saved", "Saved")] {
            let matches = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", label)).allElementsBoundByIndex
            print("Analytics iOS known control: " + name + " types: " + matches.prefix(6).map { String($0.elementType.rawValue) }.joined(separator: ","))
        }
        screenState()
        XCTFail(message)
    }
    func scrollContent(up: Bool) {
        let start = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: up ? 0.4 : 0.15))
        let end = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: up ? 0.15 : 0.4))
        start.press(forDuration: 0.05, thenDragTo: end)
    }
    func hasTapRegion(_ element: XCUIElement) -> Bool {
        guard element.exists else { return false }
        return hasTapRegion(frame: element.frame)
    }
    func hasTapRegion(frame: CGRect) -> Bool {
        guard !frame.isNull, !frame.isInfinite, frame.width > 1, frame.height > 1,
              [frame.minX, frame.minY, frame.maxX, frame.maxY].allSatisfy({ $0.isFinite }) else { return false }
        // A visible centre can still leave the control under the keyboard
        // or outside the window, where XCTest has no valid activation point.
        guard app.frame.insetBy(dx: 1, dy: 1).contains(frame) else { return false }
        let keyboard = app.keyboards.firstMatch
        return !keyboard.exists || !keyboard.frame.intersects(frame)
    }
    func reveal(_ element: XCUIElement) {
        // WebKit can omit offscreen controls from the OS accessibility tree.
        // Search with actual content gestures before declaring the control absent.
        var exists = element.waitForExistence(timeout: 3)
        if !exists {
            for _ in 0..<8 {
                scrollContent(up: false)
                if element.exists { exists = true; break }
            }
        }
        if !exists {
            for _ in 0..<16 {
                scrollContent(up: true)
                if element.exists { exists = true; break }
            }
        }
        if !exists {
            // Only the empty anonymous screen can be retained. After the first
            // field is touched, screenshots and arbitrary AX labels stay private.
            if !hasEnteredData, let output = configuration["anonymousScreenshot"] {
                let image = XCUIScreen.main.screenshot().pngRepresentation
                if image.count <= 2 * 1024 * 1024 {
                    try? image.write(to: URL(fileURLWithPath: output), options: .atomic)
                }
                let known = ["Create an account", "Sign in to MyChef", "Help improve MyChef", "Privacy", "First name", "Last name", "Username", "Email", "Password", "Create my account", "Create account", "Welcome back."]
                let rows = known.map { label -> [String: Any] in
                    let matches = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", label)).allElementsBoundByIndex
                    return ["control": label, "types": matches.prefix(8).map { Int($0.elementType.rawValue) }]
                }
                if let data = try? JSONSerialization.data(withJSONObject: rows) {
                    try? data.write(to: URL(fileURLWithPath: output).deletingPathExtension().appendingPathExtension("json"), options: .atomic)
                }
            }
            screenState()
            for word in ["Shopping", "Profile", "Meals", "A few extras", "Extra item"] {
                let matches = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", word)).allElementsBoundByIndex
                let types = matches.prefix(6).map { String($0.elementType.rawValue) }.joined(separator: ",")
                print("Analytics iOS known control: " + word + " types: " + types)
            }
            let candidates = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "Help improve MyChef")).allElementsBoundByIndex
            for node in candidates.prefix(8) {
                let value = node.value as? String
                let state = value == "0" ? "off" : value == "1" ? "on" : "other"
                print("Analytics iOS accessibility type: \(node.elementType.rawValue) state: \(state) enabled: \(node.isEnabled)")
            }
        }
        XCTAssertTrue(exists, "Expected installed app control")
        for _ in 0..<12 {
            if hasTapRegion(element) && element.isHittable { return }
            // A restored focus can place the result above the viewport. Use
            // its current native rectangle to choose the scroll direction,
            // keeping the gesture above the software keyboard.
            scrollContent(up: element.frame.midY > app.frame.midY)
        }
        if !hasTapRegion(element) || !element.isHittable {
            screenState()
            let frame = element.frame
            print("Analytics iOS tap rect: \(frame.minX),\(frame.minY),\(frame.width),\(frame.height)")
        }
        XCTAssertTrue(hasTapRegion(element) && element.isHittable, "Control must be reachable at OS text size")
    }
    func tapSettled(_ element: XCUIElement, nativeElement: Bool = false) {
        if !hasTapRegion(element) || !element.isHittable { reveal(element) }
        let end = Date().addingTimeInterval(20)
        var previous = CGRect.null
        var unchanged = 0
        var adjustments = 0
        while Date() < end {
            if !element.exists {
                unchanged = 0
                RunLoop.current.run(until: Date().addingTimeInterval(0.2))
                continue
            }
            let frame = element.frame
            // Reuse one native geometry and hit-test observation per sample.
            // Every next sample still resolves fresh measurements.
            let ready = hasTapRegion(frame: frame) && element.isHittable
            if !ready && adjustments < 3 {
                // A late WebKit reflow can move a previously revealed control.
                // Scroll toward its current position, including upward targets.
                scrollContent(up: frame.midY > app.frame.midY)
                adjustments += 1
                unchanged = 0; previous = .null
                continue
            }
            let stable = !previous.isNull && abs(frame.minX - previous.minX) <= 1
                && abs(frame.minY - previous.minY) <= 1 && abs(frame.width - previous.width) <= 1
                && abs(frame.height - previous.height) <= 1
            unchanged = ready && stable ? unchanged + 1 : 0
            if unchanged >= 2 {
                if nativeElement { element.tap() }
                else { element.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap() }
                return
            }
            previous = frame
            RunLoop.current.run(until: Date().addingTimeInterval(0.2))
        }
        let exists = element.exists
        print("Analytics iOS tap state: type=\(element.elementType.rawValue) exists=\(exists) hittable=\(exists && hasTapRegion(element) && element.isHittable) stable=\(unchanged)")
        if exists {
            let frame = element.frame
            print("Analytics iOS tap rect: \(frame.minX),\(frame.minY),\(frame.width),\(frame.height)")
        }
        screenState()
        XCTFail("Native tap target must finish layout before input")
    }
    func tap(_ label: String) {
        print("Analytics iOS control: " + label)
        if app.keyboards.firstMatch.exists {
            let done = app.buttons.matching(NSPredicate(format: "label == %@", "Done")).firstMatch
            if done.exists && done.isHittable {
                done.tap()
                waitFor({ !self.app.keyboards.firstMatch.exists }, "Software keyboard must dismiss before the next control")
                // Let the native viewport finish resizing before asking WebKit
                // for a new activation point. Hittability stays mandatory.
                RunLoop.current.run(until: Date().addingTimeInterval(0.3))
            }
        }
        if ["Shopping list", "Profile", "Meals"].contains(label) {
            // Reach the normal-flow header before touching sticky navigation;
            // this avoids relying on WebKit's scrolled sticky hit point.
            let home = app.buttons["Go to Meals"].firstMatch
            // Foreground after a cold relaunch precedes the authenticated
            // WebKit page. Wait before scrolling a still-loading viewport.
            // An offscreen header can still be revealed by the gestures below.
            _ = home.waitForExistence(timeout: 20)
            let startup = app.descendants(matching: .any).matching(NSPredicate(format:
                "label CONTAINS %@", "Loading MyChef")).firstMatch
            if !home.exists && startup.exists {
                print("Analytics iOS phase: wait for authenticated startup")
                XCTAssertTrue(startup.waitForNonExistence(timeout: 90),
                              "Authenticated startup must finish before navigation")
            }
            for _ in 0..<18 {
                if home.exists && home.isHittable && home.frame.minY >= app.frame.minY { break }
                scrollContent(up: false)
            }
            if !home.exists || !home.isHittable { screenState() }
            XCTAssertTrue(home.exists && home.isHittable, "Navigation header is reachable")
        }
        if label == "Shopping list" {
            // The OS exposes this wrapped navigation name differently from
            // WebKit's normalized role name. Restrict the prefix to one native
            // button, then verify the destination after the actual OS tap.
            let matches = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "Shopping"))
            let element = matches.firstMatch
            reveal(element)
            XCTAssertEqual(matches.count, 1, "Shopping navigation must be unambiguous")
            tapSettled(element)
            // Navigation preserves scroll position. WebKit can omit an
            // offscreen heading from its accessibility tree; reach it using
            // the same real content gestures as other controls.
            let heading = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "Shopping list.")).firstMatch
            reveal(heading)
            waitFor({ self.text("Shopping list.") }, "Shopping navigation opens the actual page")
            return
        }
        let labelPattern = label.split(whereSeparator: { $0.isWhitespace })
            .map { NSRegularExpression.escapedPattern(for: String($0)) }.joined(separator: "\\s+")
        let element = app.descendants(matching: .any).matching(NSPredicate(format:
            "label MATCHES %@ AND (elementType == %d OR elementType == %d OR elementType == %d OR elementType == %d)", labelPattern,
            XCUIElement.ElementType.button.rawValue, XCUIElement.ElementType.switch.rawValue,
            XCUIElement.ElementType.link.rawValue, XCUIElement.ElementType.disclosureTriangle.rawValue)).firstMatch
        reveal(element); tapSettled(element)
    }
    func pickerButton(_ label: String) -> XCUIElement? {
        let predicate = NSPredicate(format: "label == %@", label)
        // Bind to accessibility identities so an animated picker does not
        // resolve a saved index against a changed result list.
        // UIKit navigation bars give a structural native boundary. Prefer
        // their controls over frame comparisons during sheet animations.
        let navigation = app.navigationBars.buttons.matching(predicate).allElementsBoundByAccessibilityElement
            .filter { $0.exists && $0.isHittable }
        if navigation.count == 1 { return navigation[0] }
        let webFrames = app.webViews.buttons.matching(predicate).allElementsBoundByAccessibilityElement.filter { $0.exists }.map { $0.frame }
        let native = app.buttons.matching(predicate).allElementsBoundByAccessibilityElement.filter { element in
            guard element.exists && element.isHittable else { return false }
            let frame = element.frame
            return !webFrames.contains { web in
                abs(web.minX - frame.minX) <= 2 && abs(web.minY - frame.minY) <= 2
                    && abs(web.width - frame.width) <= 2 && abs(web.height - frame.height) <= 2
            }
        }
        return native.count == 1 ? native[0] : nil
    }
    func waitPickerButton(_ label: String) -> XCUIElement? {
        let end = Date().addingTimeInterval(30)
        var openedBrowse = false
        while Date() < end {
            if let element = pickerButton(label) { return element }
            if label == "Cancel" {
                if let close = pickerButton("Close") { return close }
                if !openedBrowse && end.timeIntervalSinceNow < 25, let browse = pickerButton("Browse") {
                    openedBrowse = true; browse.tap()
                }
            }
            RunLoop.current.run(until: Date().addingTimeInterval(0.2))
        }
        pickerState()
        XCTFail("System picker control must exist outside WebKit")
        return nil
    }
    func pickerState() {
        screenState()
        for name in ["Cancel", "Close", "Done", "Save", "Browse", "Locations", "On My iPhone", "Downloads"] {
            let p = NSPredicate(format: "label == %@", name)
            print("Analytics iOS picker control: " + name + " total=\(app.buttons.matching(p).count) web=\(app.webViews.buttons.matching(p).count)")
            print("Analytics iOS picker navigation: " + name + " count=\(app.navigationBars.buttons.matching(p).count)")
        }
    }
    func pickerAction(save: Bool) {
        // HTTP success precedes presentation of the system UI. Wait for its
        // Save control before looking for Cancel, which is also in the HTML
        // dialog. This avoids mistaking the underlying animated dialog for
        // a system picker that has not appeared yet.
        guard waitPickerButton("Save") != nil else { return }
        print("Analytics iOS phase: native picker presented")
        if !save {
            print("Analytics iOS phase: native picker Cancel")
            guard let cancel = waitPickerButton("Cancel") else { return }
            var button = cancel
            for attempt in 0..<3 {
                button.tap()
                print("Analytics iOS phase: native picker Cancel tapped")
                let end = Date().addingTimeInterval(5)
                while Date() < end {
                    // Hittability can disappear briefly during a native sheet
                    // transition. Require the actual delegate result before
                    // treating that transition as a completed cancellation.
                    let result = app.descendants(matching: .any).matching(NSPredicate(format:
                        "label BEGINSWITH %@", "Download cancelled.")).firstMatch
                    if result.exists && pickerButton("Save") == nil { return }
                    RunLoop.current.run(until: Date().addingTimeInterval(0.2))
                }
                // Native rename/search layers can have their own Cancel.
                // Continue only with another native control; actual app result
                // and callback are still required by the caller.
                print("Analytics iOS picker cancel step: \(attempt + 1)")
                guard let next = pickerButton("Cancel") ?? pickerButton("Close") else { break }
                button = next
            }
            pickerState()
            XCTFail("Native picker cancellation must deliver the actual app result")
            return
        }
        let local = app.staticTexts["On My iPhone"].firstMatch
        if !local.exists || !local.isHittable {
            for label in ["Browse", "Locations"] {
                if let back = pickerButton(label) { back.tap(); break }
            }
        }
        if local.waitForExistence(timeout: 5) && local.isHittable { local.tap() }
        let downloads = app.staticTexts["Downloads"].firstMatch
        if downloads.exists && downloads.isHittable { downloads.tap() }
        guard let saveButton = waitPickerButton("Save") else { return }
        waitFor({ saveButton.isEnabled && saveButton.isHittable }, "System picker needs a writable local destination")
        saveButton.tap()
    }
    func fill(_ label: String, _ text: String, secure: Bool = false, replace: Bool = false) {
        print("Analytics iOS control: " + label)
        let ordinary = app.descendants(matching: .any).matching(NSPredicate(format:
            "label == %@ AND (elementType == %d OR elementType == %d)", label,
            XCUIElement.ElementType.textField.rawValue, XCUIElement.ElementType.textView.rawValue)).firstMatch
        let element = secure ? app.secureTextFields[label].firstMatch : ordinary
        reveal(element); tapSettled(element, nativeElement: true)
        if !hasEnteredData, let path = configuration["anonymousScreenshot"] {
            // Only the untouched anonymous form is eligible for an image.
            // Never replace this after any field input or account proof.
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: path), options: .atomic)
        }
        waitFor({ self.app.keyboards.firstMatch.exists }, "Native text input needs the software keyboard", onTimeout: {
            let frame = element.frame
            print("Analytics iOS tap rect: \(frame.minX),\(frame.minY),\(frame.width),\(frame.height)")
        })
        hasEnteredData = true
        if replace {
            element.press(forDuration: 1.0)
            let menuItem = app.menuItems["Select All"].firstMatch
            let menuButton = app.buttons["Select All"].firstMatch
            waitFor({ (menuItem.exists && menuItem.isHittable) || (menuButton.exists && menuButton.isHittable) },
                    "Native editing must expose Select All for replacement")
            if menuItem.exists && menuItem.isHittable { menuItem.tap() }
            else { menuButton.tap() }
            element.typeText(XCUIKeyboardKey.delete.rawValue)
        }
        // These fictional forms start empty. Use the actual software keyboard
        // without a hardware command chord or AX-placeholder clearing guess.
        // The known nonempty replacement above uses the native editing menu.
        element.typeText(text)
        // Acceptance is established by the visible signup completion and the
        // exact persisted profile checks after Save. An immediate WebKit AX
        // value snapshot is not a reliable duplicate of those outcomes.
        // Do not log field values, passwords or fixture proofs on failure.
    }
    func checkbox() -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format:
            "label BEGINSWITH %@ AND (elementType == %d OR elementType == %d OR elementType == %d OR value == '0' OR value == '1')", "Help improve MyChef",
            XCUIElement.ElementType.switch.rawValue, XCUIElement.ElementType.checkBox.rawValue,
            XCUIElement.ElementType.button.rawValue)).firstMatch
    }
    func preferenceState() {
        if let result = try? control("native-preference"),
           let reads = result["reads"] as? Int, let writes = result["writes"] as? Int,
           let status = result["lastStatus"] as? Int {
            print("Analytics iOS preference requests: reads=\(reads) writes=\(writes) status=\(status)")
        }
        let box = checkbox()
        let value = box.exists ? box.value as? String : nil
        let state = value == "1" ? "on" : value == "0" ? "off" : "unknown"
        print("Analytics iOS preference control: state=" + state)
    }
    func enabled(_ desired: Bool) {
        let box = checkbox(); reveal(box)
        XCTAssertTrue(box.isEnabled, "Analytics control is available")
        let current = (box.value as? String) == "1"
        if current != desired { tapSettled(box) }
        waitFor({ ((try? self.control("preference")["enabled"]) as? Bool) == desired }, "Server preference must acknowledge choice")
    }
    func text(_ phrase: String) -> Bool {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", phrase)).firstMatch.exists
    }
    func profile() {
        tap("Profile")
        let heading = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "Your profile.")).firstMatch
        reveal(heading)
        waitFor({ self.text("Your profile.") }, "Installed Profile must open")
    }
    func details() {
        print("Analytics iOS control: Account details")
        let summary = app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "Account details")).firstMatch
        reveal(summary); tapSettled(summary)
        XCTAssertTrue(checkbox().waitForExistence(timeout: 20), "Profile privacy control is labelled")
    }
    func relaunch() { app.terminate(); app.launch(); XCTAssertTrue(app.wait(for: .runningForeground, timeout: 20)) }
    func verifyDeletedSignOut(_ message: String) {
        // A new document can restore a scroll offset. Reach the actual heading
        // with normal OS gestures before requiring its visible sign-out state.
        let heading = app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "Welcome back.")).firstMatch
        reveal(heading)
        waitFor({ self.text("Welcome back.") }, message)
    }
    func signup(_ recipient: String, assessed: Bool) throws {
        print("Analytics iOS phase: " + (assessed ? "assessed signup" : "disabled signup"))
        tap("Create an account")
        // Follow the form from its top. Inspecting the lower privacy panel
        // first can leave an earlier field clipped above the viewport while
        // WebKit still reports a hittable accessibility rectangle for it.
        fill("First name", "Analytics"); fill("Last name", "Fixture")
        fill("Username", recipient.components(separatedBy: "@")[0]); fill("Email", recipient)
        let box = checkbox(); reveal(box)
        if assessed {
            XCTAssertTrue(box.isEnabled); XCTAssertEqual(box.value as? String, "1")
            tapSettled(box); mark("signup", "assessedNotice", "offBeforeCompletion")
        } else {
            XCTAssertFalse(box.isEnabled); XCTAssertEqual(box.value as? String, "0")
            mark("signup", "disabledNotice")
        }
        tap("Create my account")
        let inbox = app.descendants(matching: .any).matching(NSPredicate(format:
            "label BEGINSWITH %@", "Check your inbox")).firstMatch
        // Replacing the long signup form can preserve its previous scroll
        // offset. Reach the actual new heading with native content gestures.
        reveal(inbox)
        waitFor({ self.text("Check your inbox") }, "Signup submission must reach the inbox screen before proof retrieval")
        let proof = try control("proof", ["recipient": recipient])
        guard let value = proof["url"] as? String,
              value.range(of: "^https://localhost:8977/app/signup#[A-Za-z0-9_-]{43}$", options: .regularExpression) != nil,
              let url = URL(string: value) else { throw Failure.invalidFixture }
        // Exercise the actual SceneDelegate cold-start URL route. Terminate
        // once so XCTest owns this URL launch after the native warmup instance.
        app.terminate()
        XCTAssertTrue(app.wait(for: .notRunning, timeout: 20))
        app.open(url)
        XCTAssertTrue(app.wait(for: .runningForeground, timeout: 20))
        waitFor({ self.text("Finish signing up") }, "Fixture proof reaches real completion UI")
        XCTAssertEqual(checkbox().value as? String, "0")
        fill("Password", configuration["password"] ?? "", secure: true)
        fill("Confirm password", configuration["password"] ?? "", secure: true)
        tap("Create account")
        let completed = app.descendants(matching: .any).matching(NSPredicate(format:
            "label CONTAINS %@", "Account created. Sign in to MyChef.")).firstMatch
        reveal(completed)
        waitFor({ self.text("Account created. Sign in to MyChef.") }, "Installed signup must complete")
        let counts = try control("counts")
        XCTAssertEqual(counts["enabled"] as? Int, 0)
        XCTAssertEqual(counts["queued"] as? Int, 0); XCTAssertEqual(counts["delivered"] as? Int, 0)
        mark("signup", assessed ? "completedOffNoCapture" : "disabledNoCapture")
        tap("Back to MyChef")
    }
    func testAuthenticatedAnalytics() throws {
        if configuration["preserveBootstrap"] == "true" {
            // The coordinator already launched this same unsigned app with
            // fixture arguments and verified its actual HTTPS bootstrap.
            // activate preserves that instance; launch would terminate it.
            app.activate()
            print("Analytics iOS phase: preserved native bootstrap instance")
        } else {
            app.launch()
        }
        XCTAssertTrue(app.wait(for: .runningForeground, timeout: 20))
        waitFor({ self.text("Welcome back.") || self.app.buttons["Create an account"].firstMatch.exists },
                "Native session bootstrap must finish before account controls", timeout: 90)
        if configuration["voiceOverOnly"] == "true" {
            #if compiler(>=6.4)
            if #available(iOS 27.0, *) {
                try verifyVoiceOver()
                guard let output = configuration["output"] else { throw Failure.invalidFixture }
                try JSONSerialization.data(withJSONObject: ["checks": observations]).write(to: URL(fileURLWithPath: output), options: .atomic)
                return
            }
            #endif
            throw Failure.invalidFixture
        }
        if configuration["layoutOnly"] == "true" {
            tap("Create an account")
            XCTAssertGreaterThanOrEqual(app.frame.width, 370, "Small supported iPhone width")
            XCTAssertLessThanOrEqual(app.frame.width, 380, "Small supported iPhone width")
            fill("Email", "layout@example.test")
            let email = app.textFields["Email"].firstMatch
            let keyboard = app.keyboards.firstMatch
            XCTAssertTrue(keyboard.waitForExistence(timeout: 5))
            XCTAssertLessThanOrEqual(email.frame.maxY, keyboard.frame.minY + 1)
            let done = app.buttons["Done"].firstMatch
            if done.exists && done.isHittable {
                done.tap()
                waitFor({ !self.app.keyboards.firstMatch.exists }, "Software keyboard must dismiss before the next control")
                // Let the native viewport finish resizing before asking WebKit
                // for a new activation point. Hittability stays mandatory.
                RunLoop.current.run(until: Date().addingTimeInterval(0.3))
            }
            let box = checkbox(); reveal(box)
            XCTAssertGreaterThanOrEqual(box.frame.minX, app.frame.minX)
            XCTAssertLessThanOrEqual(box.frame.maxX, app.frame.maxX)
            mark("accessibility", "phoneWidths", "keyboardUnobstructed")
            guard let output = configuration["output"] else { throw Failure.invalidFixture }
            try JSONSerialization.data(withJSONObject: ["checks": observations]).write(to: URL(fileURLWithPath: output), options: .atomic)
            return
        }
        XCTAssertGreaterThanOrEqual(app.frame.width, 390, "Regular supported iPhone width")
        try signup("analyticsdisabled@example.test", assessed: false)
        _ = try control("approved"); relaunch()
        try signup("analyticsassessed@example.test", assessed: true)
        print("Analytics iOS phase: primary signup complete")
        fill("Email", configuration["email"] ?? "")
        fill("Password", configuration["password"] ?? "", secure: true)
        tap("Sign in to MyChef"); profile(); details()
        XCTAssertEqual(checkbox().value as? String, "0"); mark("participation", "existingOff")
        enabled(true); mark("participation", "explicitEnable")
        fill("Diet", "Vegetarian meals"); tap("Save preferences")
        waitFor({ ((try? self.control("profile-input", ["target": "initial"])["passed"]) as? Bool) == true }, "Native preference must persist the requested fixture value")
        waitFor({ ((try? self.control("counts")["queued"]) as? Int) == 1 }, "Completed action has one queued event")
        mark("participation", "committedCoreAction")
        relaunch(); profile(); details(); XCTAssertEqual(checkbox().value as? String, "1")
        _ = try control("tick")
        XCTAssertEqual(try control("counts")["delivered"] as? Int, 1)
        print("Analytics iOS phase: primary participation complete")
        mark("participation", "restarted", "exactlyOneEvent")
        let choiceWrites = try control("native-preference")["writes"] as? Int
        XCTAssertNotNil(choiceWrites, "Native preference observation is available")
        _ = try control("other-session-choice", ["enabled": false])
        let staleBox = checkbox(); reveal(staleBox)
        XCTAssertEqual(staleBox.value as? String, "1", "Conflict fixture retains its older visible choice")
        tapSettled(staleBox, nativeElement: true)
        waitFor({ ((try? self.control("native-preference")["writes"]) as? Int ?? 0) > (choiceWrites ?? 0) },
                "Native checkbox must send its preference update", onTimeout: {
            self.preferenceState()
        })
        let choiceResult = try control("native-preference")
        XCTAssertEqual(choiceResult["lastStatus"] as? Int, 409, "Actual stale update must be rejected")
        // The inserted warning can be above the enlarged-text viewport.
        // Reach the real warning with OS gestures before checking its text.
        reveal(app.descendants(matching: .any).matching(NSPredicate(format:
            "label CONTAINS %@", "Your setting changed elsewhere")).firstMatch)
        waitFor({ self.text("Your setting changed elsewhere") }, "Stale choice must show conflict")
        XCTAssertEqual(checkbox().value as? String, "0"); mark("conflict", "staleConflict", "currentState")
        _ = try control("maintenance"); enabled(true)
        _ = try control("offline"); tapSettled(checkbox())
        waitFor({ self.text("Turning off was not confirmed") }, "Offline Off must remain unconfirmed")
        XCTAssertEqual(checkbox().value as? String, "1"); mark("conflict", "offlineOffUnconfirmed")
        _ = try control("online"); enabled(false)
        waitFor({ self.text("Active deletion is in progress") }, "Active cleanup is truthful")
        print("Analytics iOS phase: primary conflict complete")
        mark("conflict", "reconnectFenceAcknowledged"); mark("lifecycle", "activeCleanup")
        _ = try control("maintenance"); enabled(true)
        tap("Shopping list"); waitFor({ self.text("Saved") }, "Shopping snapshot must load")
        _ = try control("offline")
        let extraInput = app.textFields["Extra item"].firstMatch
        let extras = app.buttons.matching(NSPredicate(format: "label == %@", "A few extras?"))
        reveal(extras.firstMatch)
        // The native tree can expose both the details container and its
        // summary as buttons. First look for an already-open field, including
        // below the current viewport, rather than toggling that panel closed.
        for _ in 0..<4 {
            if extraInput.exists { break }
            scrollContent(up: true)
        }
        if !extraInput.exists {
            reveal(extras.firstMatch)
            let visible = extras.allElementsBoundByIndex.filter {
                $0.exists && $0.isHittable && $0.frame.width > 0 && $0.frame.height > 0
            }.sorted { $0.frame.width * $0.frame.height < $1.frame.width * $1.frame.height }
            guard let summary = visible.first else { throw Failure.missingControl }
            summary.tap()
        }
        fill("Extra item", "Analytics queued oats"); tap("Add extra")
        waitFor({ self.text("Pending sync") }, "Offline edit must enter durable queue")
        _ = try control("other-session-choice", ["enabled": false]); _ = try control("maintenance")
        _ = try control("other-session-choice", ["enabled": true]); _ = try control("online")
        relaunch(); tap("Shopping list")
        waitFor({ ((try? self.control("counts")["queuedProductItems"]) as? Int) == 1 }, "Original queued edit must reach product storage")
        _ = try control("tick")
        XCTAssertEqual(try control("counts")["delivered"] as? Int, 0)
        mark("queue", "originalRevision", "productEditCommitted", "noOldEpochEvent")
        relaunch(); tap("Shopping list"); waitFor({ self.text("Saved") }, "Recovered queue must settle")
        XCTAssertEqual(try control("counts")["queuedProductItems"] as? Int, 1)
        XCTAssertEqual(try control("counts")["delivered"] as? Int, 0)
        mark("queue", "retryNoDuplicate", "relaunchNoDuplicate")
        profile(); fill("Diet", "Mediterranean", replace: true); tap("Save preferences")
        waitFor({ ((try? self.control("profile-input", ["target": "updated"])["passed"]) as? Bool) == true }, "Native replacement must persist the requested fixture value")
        waitFor({ ((try? self.control("counts")["queued"]) as? Int) == 1 }, "New permitted action must commit")
        _ = try control("backup"); details(); enabled(false); _ = try control("maintenance")
        tap("Refresh analytics setting")
        waitFor({ self.text("isolated backup cleanup is pending") }, "Backup cleanup remains pending")
        mark("lifecycle", "isolatedBackupPending")
        for save in [false, true] {
            tap("Download my data"); fill("Current password", configuration["password"] ?? "", secure: true)
            let input = app.secureTextFields["Current password"].firstMatch
            let keyboard = app.keyboards.firstMatch
            XCTAssertTrue(keyboard.waitForExistence(timeout: 5), "Native keyboard must be present")
            XCTAssertLessThanOrEqual(input.frame.maxY, keyboard.frame.minY + 1, "Password remains above keyboard")
            mark("accessibility", "keyboardUnobstructed")
            let before = try control("native-export")["attempts"] as? Int ?? -1
            tap("Download ZIP")
            waitFor({
                guard let value = try? self.control("native-export") else { return false }
                return (value["attempts"] as? Int ?? 0) > before && (value["lastStatus"] as? Int) == 200
            }, "Native export API must succeed before opening the picker")
            // These controls belong to UIDocumentPicker, reached by OS UI taps.
            pickerAction(save: save)
            let resultPrefix = save ? "Your download is ready" : "Download cancelled."
            let result = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", resultPrefix)).firstMatch
            reveal(result)
            waitFor({ self.text(resultPrefix) }, "Native export result must match Save or Cancel")
            mark("lifecycle", save ? "nativeSave" : "nativeCancel")
        }
        // Automated OS audits supplement, but never replace, audible VoiceOver.
        _ = try control("foreign-export"); mark("lifecycle", "foreignExportRefused")
        try app.performAccessibilityAudit(for: [.elementDetection, .hitRegion, .sufficientElementDescription])
        tap("Delete my account"); fill("Current password", configuration["password"] ?? "", secure: true)
        fill("Type DELETE to confirm", "DELETE"); tap("Permanently delete account")
        verifyDeletedSignOut("Account deletion must return to sign in")
        print("Analytics iOS phase: primary deletion acknowledged")
        mark("lifecycle", "accountDeleted"); relaunch()
        verifyDeletedSignOut("Deleted session must stay signed out after relaunch")
        guard let output = configuration["output"] else { throw Failure.invalidFixture }
        try JSONSerialization.data(withJSONObject: ["checks": observations]).write(to: URL(fileURLWithPath: output), options: .atomic)
        // Unobserved speech, layout sizes and private-cache inspection remain
        // false. The coordinator must refuse a full receipt for this run.
    }
    #if compiler(>=6.4)
    @available(iOS 27.0, *)
    func verifyVoiceOver() throws {
        let voice = XCUIDevice.shared.voiceOverService
        if voice.isEnabled { try voice.disable() }
        print("Analytics iOS voice phase: configure flat navigation")
        let settings = XCUIApplication(bundleIdentifier: "com.apple.Preferences")
        // The coordinator warmed this owned simulator's Settings before the
        // app bootstrap. Preserve that instance instead of cold-launching it.
        settings.activate()
        guard settings.wait(for: .runningForeground, timeout: 20) else { throw Failure.missingControl }
        // Foreground state precedes Settings' first usable accessibility tree.
        // Use XCTest's bounded readiness wait before querying individual rows.
        guard settings.navigationBars.firstMatch.waitForExistence(timeout: 45) else {
            XCTFail("Native Settings navigation must become ready")
            throw Failure.missingControl
        }
        func openSetting(_ name: String) throws {
            guard ["Accessibility", "VoiceOver", "Navigation Style", "Flat"].contains(name) else { throw Failure.invalidFixture }
            print("Analytics iOS voice phase: Settings " + name)
            let item = settings.staticTexts[name].firstMatch
            for _ in 0..<16 {
                // Settings exposes these row labels as StaticText. XCTest
                // can reject their activation point even while their measured
                // rectangle is visible. Use actual stable geometry for OS input;
                // never ask a noninteractive label for a computed hit point.
                let readyDeadline = Date().addingTimeInterval(2)
                var previous = CGRect.null
                var stable = 0
                while Date() < readyDeadline && item.exists {
                    let frame = item.frame
                    let bounds = settings.frame.insetBy(dx: 1, dy: 1)
                    let navigation = settings.navigationBars.firstMatch.frame
                    let visible = !frame.isNull && !frame.isInfinite && frame.width > 1 && frame.height > 1
                        && [frame.minX, frame.minY, frame.maxX, frame.maxY].allSatisfy({ $0.isFinite })
                        && bounds.contains(frame) && frame.minY > navigation.maxY
                    let unchanged = !previous.isNull && abs(frame.minX - previous.minX) <= 1
                        && abs(frame.minY - previous.minY) <= 1 && abs(frame.width - previous.width) <= 1
                        && abs(frame.height - previous.height) <= 1
                    stable = visible && unchanged ? stable + 1 : 0
                    if stable >= 2 {
                        item.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
                        if name != "Flat" {
                            guard settings.navigationBars[name].waitForExistence(timeout: 20) else {
                                XCTFail("Required native Settings destination must become ready")
                                throw Failure.missingControl
                            }
                        }
                        return
                    }
                    previous = frame
                    RunLoop.current.run(until: Date().addingTimeInterval(0.2))
                }
                let start = settings.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.7))
                let end = settings.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.35))
                start.press(forDuration: 0.2, thenDragTo: end)
            }
            XCTFail("Required native VoiceOver setting must be reachable")
            throw Failure.missingControl
        }
        try openSetting("Accessibility")
        try openSetting("VoiceOver")
        try openSetting("Navigation Style")
        try openSetting("Flat")
        func flatSelected() -> Bool {
            let label = settings.staticTexts["Flat"].firstMatch
            let row = settings.cells.containing(.staticText, identifier: "Flat").firstMatch
            let button = settings.buttons["Flat"].firstMatch
            let parents = [settings.cells.containing(.staticText, identifier: "Navigation Style").firstMatch,
                           settings.buttons.containing(.staticText, identifier: "Navigation Style").firstMatch]
            // Settings can attach the selected accessibility trait to the
            // text itself rather than its containing cell or button.
            return label.exists && label.isSelected || row.exists && row.isSelected || button.exists && button.isSelected
                || parents.contains { $0.exists && (($0.value as? String) == "Flat" || $0.staticTexts["Flat"].exists) }
        }
        if !flatSelected() {
            // A checked option need not expose a selected trait. Return to
            // the parent screen to inspect this setting's current value.
            let back = settings.navigationBars.buttons.matching(NSPredicate(format:
                "label == %@ OR label == %@", "VoiceOver", "Back")).firstMatch
            if back.wait(for: \.isHittable, toEqual: true, timeout: 5) {
                back.tap()
            }
        }
        let selectionDeadline = Date().addingTimeInterval(15)
        while !flatSelected() && Date() < selectionDeadline {
            RunLoop.current.run(until: Date().addingTimeInterval(0.2))
        }
        guard flatSelected() else {
            let label = settings.staticTexts["Flat"].firstMatch
            let row = settings.cells.containing(.staticText, identifier: "Flat").firstMatch
            let button = settings.buttons["Flat"].firstMatch
            print("Analytics iOS Flat selection: label=\(label.exists && label.isSelected) cell=\(row.exists && row.isSelected) button=\(button.exists && button.isSelected)")
            XCTFail("Native Settings must confirm flat VoiceOver navigation")
            throw Failure.missingControl
        }
        print("Analytics iOS VoiceOver navigation: flat selected=true")
        settings.terminate()
        app.activate()
        XCTAssertTrue(app.wait(for: .runningForeground, timeout: 20))
        func enableVoice() throws {
            try voice.enable()
            XCTAssertTrue(voice.isEnabled)
            app.activate()
            XCTAssertTrue(app.wait(for: .runningForeground, timeout: 20))
            let nativeEnabled = Thread.isMainThread
                ? MainActor.assumeIsolated { UIAccessibility.isVoiceOverRunning }
                : DispatchQueue.main.sync { UIAccessibility.isVoiceOverRunning }
            let voices = AVSpeechSynthesisVoice.speechVoices()
            let english = voices.filter { $0.language == "en" || $0.language.hasPrefix("en-") }.count
            // Availability and flags diagnose missing speech; they never set
            // a spoken-output observation or synthesize a replacement voice.
            print("Analytics iOS voice environment: api=\(voice.isEnabled) native=\(nativeEnabled) voices=\(min(999, voices.count)) english=\(min(999, english))")
        }
        func spoken(_ phrase: String, backward: Bool = false, controlRole: Bool = false) throws -> String {
            func words(_ value: String) -> String {
                value.lowercased().components(separatedBy: CharacterSet.alphanumerics.inverted)
                    .filter { !$0.isEmpty }.joined(separator: " ")
                    .replacingOccurrences(of: "\\bmy chef\\b", with: "mychef", options: .regularExpression)
            }
            let expected = words(phrase)
            var seen = Set<String>()
            var candidateFacts = Set<String>()
            func matches(_ value: String) -> Bool {
                let normalized = words(value)
                for token in ["help improve mychef", "analytics is off", "unavailable", "check box", "checkbox", "checked", "button", "web content", "create an account", "privacy"] {
                    if normalized.contains(token) { seen.insert(token) }
                }
                guard normalized.contains(expected) else { return false }
                let role = value.range(of: "switch|check.?box|tick.?box|button|link", options: [.regularExpression, .caseInsensitive]) != nil
                if controlRole && !role && candidateFacts.count < 8 {
                    let prefix = String(normalized.prefix(100))
                    let on = prefix.range(of: "\\b(on|checked|selected)\\b", options: .regularExpression) != nil
                    let off = prefix.range(of: "\\b(off|unchecked|not checked|not selected)\\b", options: .regularExpression) != nil
                    let facts = "on=\(on) off=\(off)"
                    if candidateFacts.insert(facts).inserted {
                        print("Analytics iOS spoken candidate without role: " + facts)
                    }
                }
                return !controlRole || role
            }
            func observe(_ operation: () throws -> String) -> String? {
                do { return try operation() }
                catch {
                    let code = (error as NSError).code
                    let category = code == XCUIVoiceOverService.Error.Code.noSpeech.rawValue ? "no-speech"
                        : code == XCUIVoiceOverService.Error.Code.notRunning.rawValue ? "not-running" : "other"
                    print("Analytics iOS voice error category: " + category)
                    print("Analytics iOS voice API error: " + ((0...10).contains(code) ? String(code) : "other"))
                    return nil
                }
            }
            var inspectedLabelCandidate = false
            func candidate(_ output: String) -> String? {
                if matches(output) { return output }
                guard controlRole, words(output).contains(expected), !inspectedLabelCandidate else { return nil }
                inspectedLabelCandidate = true
                // Do not interrupt a just-focused control's announcement.
                // Every sample must independently include its label and role.
                for _ in 0..<3 {
                    RunLoop.current.run(until: Date().addingTimeInterval(0.3))
                    if let current = observe({ try voice.currentSpeech().utterance }), matches(current) { return current }
                }
                // A spoken label can be a container around the input. Enter
                // that observed candidate once, retaining the same strict
                // speech match; never combine separate utterances.
                if let nested = observe({ try voice.moveIn().utterance }) {
                    if matches(nested) { return nested }
                    _ = observe({ try voice.moveOut().utterance })
                }
                return nil
            }
            // Search sequentially. Only an observed target-label candidate
            // can be entered; AX text never substitutes for real speech.
            if let current = observe({ try voice.currentSpeech().utterance }), let found = candidate(current) { return found }
            for reverse in [backward, !backward] {
                // Reserve time for both directions if the first reaches a
                // boundary without throwing or changing its announcement.
                let deadline = Date().addingTimeInterval(45)
                for _ in 0..<64 {
                    if Date() >= deadline { break }
                    let output = observe { try (reverse ? voice.moveBackward() : voice.moveForward()).utterance }
                    guard let output = output else {
                        // A navigation target can be silent. Keep searching
                        // within the bounded budget instead of abandoning the
                        // direction after the first NoSpeech response.
                        RunLoop.current.run(until: Date().addingTimeInterval(0.2))
                        continue
                    }
                    if let found = candidate(output) { return found }
                }
            }
            // Describe only known choice nodes if speech remains missing.
            let choices = app.descendants(matching: .any).matching(NSPredicate(format:
                "label BEGINSWITH %@", "Help improve MyChef")).allElementsBoundByIndex
            for node in choices.prefix(8) {
                let value = node.value as? String
                let choice = value == "0" ? "off" : value == "1" ? "on" : "other"
                print("Analytics iOS accessibility type: \(node.elementType.rawValue) state: \(choice) enabled: \(node.isEnabled)")
            }
            // Only fixed app vocabulary, never arbitrary speech or fields.
            print("Analytics iOS spoken vocabulary: " + seen.sorted().joined(separator: ","))
            XCTFail("Required app control or error must be spoken by VoiceOver")
            throw Failure.missingControl
        }
        func focusSpeech(_ element: XCUIElement, _ phrase: String) throws -> String {
            // Enabling VoiceOver may scroll to its current focus. Use a real
            // visible touch anchor when available; otherwise let VoiceOver's
            // own navigation reach and scroll to the required announcement.
            let control = ["Help improve MyChef", "Save preferences", "Refresh analytics setting"].contains(phrase)
            if hasTapRegion(element) {
                if control {
                    if element.isHittable {
                        if phrase == "Help improve MyChef" {
                            let frame = element.frame
                            print("Analytics iOS choice frame: width=\(Int(frame.width)) height=\(Int(frame.height))")
                            // Use the same measured, stable native rectangle
                            // as other input, rather than a label hit point.
                            tapSettled(element)
                        } else { element.tap() }
                    }
                } else {
                    // Readable static text has no activation point. A real
                    // visible touch can anchor VoiceOver without activating it.
                    element.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
                }
            }
            return try spoken(phrase, backward: control, controlRole: control)
        }
        func state(_ utterance: String) -> String {
            // Inspect only the control/state prefix, before its long hint.
            let prefix = String(utterance.prefix(100)).lowercased()
            if prefix.range(of: "\\b(off|unchecked|not checked|not selected)\\b", options: .regularExpression) != nil { return "off" }
            if prefix.range(of: "\\b(on|checked|selected)\\b", options: .regularExpression) != nil { return "on" }
            let role = utterance.range(of: "switch|check.?box|tick.?box|button", options: [.regularExpression, .caseInsensitive]) != nil
            let disabled = utterance.range(of: "dimmed|disabled|unavailable", options: [.regularExpression, .caseInsensitive]) != nil
            print("Analytics iOS spoken control: role=\(role) disabled=\(disabled) state=unknown")
            return "unknown"
        }
        // This uses the OS VoiceOver service's actual spoken output. No TTS
        // substitute, DOM label or accessibility audit can set these flags.
        tap("Create an account")
        let signupBox = checkbox(); reveal(signupBox)
        XCTAssertFalse(signupBox.isEnabled); XCTAssertEqual(signupBox.value as? String, "0")
        let unavailable = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "Analytics is Off while unavailable")).firstMatch
        reveal(unavailable)
        waitFor({ ((try? self.control("native-notice")["disabledObserved"]) as? Bool) == true },
                "Native disabled notice response must precede VoiceOver observation")
        try enableVoice()
        defer { try? voice.disable() }
        XCTAssertTrue(voice.isEnabled)
        print("Analytics iOS voice phase: unavailable status")
        // Disabled controls cannot be operated. Its adjacent spoken status
        // must communicate both Off and why; assessed controls are exercised below.
        _ = try focusSpeech(unavailable, "Analytics is Off while unavailable")
        try voice.disable()
        _ = try control("approved"); relaunch()
        tap("Create an account")
        let assessedBox = checkbox(); reveal(assessedBox)
        waitFor({ self.checkbox().isEnabled && (self.checkbox().value as? String) == "1" }, "Assessed signup notice must arrive before interaction")
        try enableVoice()
        print("Analytics iOS voice phase: assessed signup choice")
        XCTAssertEqual(state(try focusSpeech(assessedBox, "Help improve MyChef")), "on")
        app.doubleTap()
        waitFor({ (self.checkbox().value as? String) == "0" }, "VoiceOver signup choice must become Off")
        XCTAssertEqual(state(try focusSpeech(checkbox(), "Help improve MyChef")), "off")
        XCTAssertEqual(try control("counts")["enabled"] as? Int, 0, "Incomplete signup creates no enrollment")
        try voice.disable()
        tap("Sign in")
        fill("Email", configuration["email"] ?? "")
        fill("Password", configuration["password"] ?? "", secure: true)
        tap("Sign in to MyChef"); profile(); details()
        relaunch(); profile(); details()
        let privacyHeading = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "Privacy and data")).firstMatch
        reveal(privacyHeading)
        try enableVoice()
        print("Analytics iOS voice phase: profile order")
        _ = try focusSpeech(privacyHeading, "Privacy and data")
        var checkboxIndex: Int?, statusIndex: Int?, refreshIndex: Int?
        for index in 0..<24 {
            let utterance = try voice.moveForward().utterance
            let role = utterance.range(of: "switch|check.?box|tick.?box|button", options: [.regularExpression, .caseInsensitive]) != nil
            if utterance.localizedCaseInsensitiveContains("Help improve MyChef"), role, checkboxIndex == nil { checkboxIndex = index }
            if let controlIndex = checkboxIndex, index > controlIndex,
               utterance.lowercased().trimmingCharacters(in: .whitespacesAndNewlines).hasPrefix("off"), statusIndex == nil { statusIndex = index }
            if utterance.localizedCaseInsensitiveContains("Refresh analytics setting") { refreshIndex = index; break }
        }
        XCTAssertNotNil(checkboxIndex); XCTAssertNotNil(statusIndex); XCTAssertNotNil(refreshIndex)
        XCTAssertLessThan(checkboxIndex ?? 100, statusIndex ?? -1)
        XCTAssertLessThan(statusIndex ?? 100, refreshIndex ?? -1)
        mark("accessibility", "focusOrder")
        try voice.disable()
        let enabledBox = checkbox(); reveal(enabledBox)
        try enableVoice()
        print("Analytics iOS voice phase: enabled state")
        XCTAssertEqual(state(try focusSpeech(enabledBox, "Help improve MyChef")), "off")
        app.doubleTap()
        waitFor({ ((try? self.control("preference")["enabled"]) as? Bool) == true }, "VoiceOver enable receives server acknowledgement")
        XCTAssertEqual(state(try focusSpeech(checkbox(), "Help improve MyChef")), "on", "VoiceOver must convey enabled participation")
        XCTAssertEqual(try control("preference")["enabled"] as? Bool, true, "Touch exploration must not change participation")
        // Change the server choice from another session while the focused
        // control remains On, then activate it with the real VoiceOver gesture.
        print("Analytics iOS voice phase: stale choice")
        _ = try control("other-session-choice", ["enabled": false])
        app.doubleTap()
        waitFor({ self.text("Your setting changed elsewhere") }, "VoiceOver action must encounter stale-choice error")
        _ = try spoken("Your setting changed elsewhere", backward: true)
        XCTAssertEqual(state(try spoken("Help improve MyChef")), "off")
        try voice.disable(); _ = try control("maintenance"); enabled(true)
        _ = try control("offline")
        print("Analytics iOS voice phase: interrupted off")
        let offlineBox = checkbox(); reveal(offlineBox)
        try enableVoice(); _ = try focusSpeech(offlineBox, "Help improve MyChef")
        XCTAssertEqual(try control("preference")["enabled"] as? Bool, true, "Touch exploration preserves On")
        app.doubleTap()
        waitFor({ self.text("Turning off was not confirmed") }, "VoiceOver action must encounter unconfirmed Off")
        _ = try spoken("Turning off was not confirmed", backward: true)
        XCTAssertEqual(state(try spoken("Help improve MyChef")), "on")
        print("Analytics iOS voice phase: acknowledged off")
        _ = try control("online")
        try voice.disable()
        fill("Diet", "Mediterranean")
        if app.keyboards.firstMatch.exists {
            let done = app.buttons["Done"].firstMatch
            if done.exists && done.isHittable {
                done.tap()
                waitFor({ !self.app.keyboards.firstMatch.exists }, "Software keyboard must dismiss before the next control")
                // Let the native viewport finish resizing before asking WebKit
                // for a new activation point. Hittability stays mandatory.
                RunLoop.current.run(until: Date().addingTimeInterval(0.3))
            }
        }
        let save = app.buttons["Save preferences"].firstMatch; reveal(save)
        try enableVoice()
        _ = try focusSpeech(save, "Save preferences"); app.doubleTap()
        waitFor({ ((try? self.control("profile-input", ["target": "updated"])["passed"]) as? Bool) == true }, "VoiceOver Save must persist the requested fixture value")
        waitFor({ ((try? self.control("counts")["queued"]) as? Int) == 1 }, "VoiceOver core action commits one event")
        _ = try control("tick"); _ = try control("backup")
        try voice.disable()
        let erase = checkbox(); reveal(erase)
        try enableVoice()
        _ = try focusSpeech(erase, "Help improve MyChef"); app.doubleTap()
        waitFor({ ((try? self.control("preference")["enabled"]) as? Bool) == false }, "VoiceOver Off must receive server acknowledgement")
        XCTAssertEqual(state(try focusSpeech(checkbox(), "Help improve MyChef")), "off")
        print("Analytics iOS voice phase: active cleanup")
        _ = try spoken("Active deletion is in progress")
        _ = try control("maintenance")
        try voice.disable()
        let refresh = app.buttons["Refresh analytics setting"].firstMatch; reveal(refresh)
        try enableVoice()
        _ = try focusSpeech(refresh, "Refresh analytics setting"); app.doubleTap()
        waitFor({ self.text("isolated backup cleanup is pending") }, "Backup cleanup stays pending")
        print("Analytics iOS voice phase: backup cleanup")
        _ = try spoken("isolated backup cleanup is pending", backward: true)
        mark("accessibility", "platformScreenReader", "spokenStatesAndErrors")
    }
    #endif

}
