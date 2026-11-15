import XCTest

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
        var request = URLRequest(url: url, timeoutInterval: 40)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("Bearer " + (configuration["token"] ?? ""), forHTTPHeaderField: "Authorization")
        request.httpBody = try JSONSerialization.data(withJSONObject: input)
        let finished = expectation(description: "Private fixture operation")
        var result: [String: Any]?
        URLSession.shared.dataTask(with: request) { data, response, _ in
            if (response as? HTTPURLResponse)?.statusCode == 200, let data = data {
                result = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
            }
            finished.fulfill()
        }.resume()
        wait(for: [finished], timeout: 45)
        guard let result = result else { throw Failure.controllerRefused }
        return result
    }
    func mark(_ group: String, _ names: String...) {
        for name in names {
            XCTAssertNotNil(observations[group]?[name], "Closed observation must exist")
            observations[group]?[name] = true
        }
    }
    func waitFor(_ condition: @escaping () -> Bool, _ message: String) {
        let end = Date().addingTimeInterval(35)
        while Date() < end {
            if condition() { return }
            RunLoop.current.run(until: Date().addingTimeInterval(0.2))
        }
        XCTFail(message)
    }
    func scrollContent(up: Bool) {
        let start = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: up ? 0.4 : 0.15))
        let end = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: up ? 0.15 : 0.4))
        start.press(forDuration: 0.05, thenDragTo: end)
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
            if element.isHittable { return }
            // Keep the content gesture above the software keyboard. A whole-
            // screen swipe can land entirely on the keyboard on a small phone.
            scrollContent(up: true)
        }
        XCTAssertTrue(element.isHittable, "Control must be reachable at OS text size")
    }
    func tap(_ label: String) {
        print("Analytics iOS control: " + label)
        if app.keyboards.firstMatch.exists {
            let done = app.buttons.matching(NSPredicate(format: "label == %@", "Done")).firstMatch
            if done.exists && done.isHittable { done.tap() }
        }
        if label == "Shopping list" {
            // The OS exposes this wrapped navigation name differently from
            // WebKit's normalized role name. Restrict the prefix to one native
            // button, then verify the destination after the actual OS tap.
            let matches = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "Shopping"))
            let element = matches.firstMatch
            reveal(element)
            XCTAssertEqual(matches.count, 1, "Shopping navigation must be unambiguous")
            element.tap()
            waitFor({ self.text("Shopping list.") }, "Shopping navigation opens the actual page")
            return
        }
        let labelPattern = label.split(whereSeparator: { $0.isWhitespace })
            .map { NSRegularExpression.escapedPattern(for: String($0)) }.joined(separator: "\\s+")
        let element = app.descendants(matching: .any).matching(NSPredicate(format:
            "label MATCHES %@ AND (elementType == %d OR elementType == %d OR elementType == %d OR elementType == %d)", labelPattern,
            XCUIElement.ElementType.button.rawValue, XCUIElement.ElementType.switch.rawValue,
            XCUIElement.ElementType.link.rawValue, XCUIElement.ElementType.disclosureTriangle.rawValue)).firstMatch
        reveal(element); element.tap()
    }
    func fill(_ label: String, _ text: String, secure: Bool = false) {
        print("Analytics iOS control: " + label)
        hasEnteredData = true
        let ordinary = app.descendants(matching: .any).matching(NSPredicate(format:
            "label == %@ AND (elementType == %d OR elementType == %d)", label,
            XCUIElement.ElementType.textField.rawValue, XCUIElement.ElementType.textView.rawValue)).firstMatch
        let element = secure ? app.secureTextFields[label].firstMatch : ordinary
        reveal(element); element.tap()
        if let current = element.value as? String, !current.isEmpty,
           current != element.placeholderValue {
            element.press(forDuration: 1.1)
            if app.menuItems["Select All"].waitForExistence(timeout: 2) {
                app.menuItems["Select All"].tap()
                element.typeText(XCUIKeyboardKey.delete.rawValue)
            }
        }
        element.typeText(text)
        // Do not log field values, passwords or fixture proofs on failure.
    }
    func checkbox() -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format:
            "label BEGINSWITH %@ AND (elementType == %d OR elementType == %d OR elementType == %d OR value == '0' OR value == '1')", "Help improve MyChef",
            XCUIElement.ElementType.switch.rawValue, XCUIElement.ElementType.checkBox.rawValue,
            XCUIElement.ElementType.button.rawValue)).firstMatch
    }
    func enabled(_ desired: Bool) {
        let box = checkbox(); reveal(box)
        XCTAssertTrue(box.isEnabled, "Analytics control is available")
        let current = (box.value as? String) == "1"
        if current != desired { box.tap() }
        waitFor({ ((try? self.control("preference")["enabled"]) as? Bool) == desired }, "Server preference must acknowledge choice")
    }
    func text(_ phrase: String) -> Bool {
        app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", phrase)).firstMatch.exists
    }
    func profile() {
        tap("Profile")
        waitFor({ self.text("Your profile.") }, "Installed Profile must open")
    }
    func details() {
        print("Analytics iOS control: Account details")
        let summary = app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "Account details")).firstMatch
        reveal(summary); summary.tap()
        XCTAssertTrue(checkbox().waitForExistence(timeout: 20), "Profile privacy control is labelled")
    }
    func relaunch() { app.terminate(); app.launch(); XCTAssertTrue(app.wait(for: .runningForeground, timeout: 20)) }
    func signup(_ recipient: String, assessed: Bool) throws {
        print("Analytics iOS phase: " + (assessed ? "assessed signup" : "disabled signup"))
        tap("Create an account")
        let box = checkbox(); reveal(box)
        if assessed {
            XCTAssertTrue(box.isEnabled); XCTAssertEqual(box.value as? String, "1")
            box.tap(); mark("signup", "assessedNotice", "offBeforeCompletion")
        } else {
            XCTAssertFalse(box.isEnabled); XCTAssertEqual(box.value as? String, "0")
            mark("signup", "disabledNotice")
        }
        fill("First name", "Analytics"); fill("Last name", "Fixture")
        fill("Username", recipient.components(separatedBy: "@")[0]); fill("Email", recipient)
        tap("Create my account")
        let proof = try control("proof", ["recipient": recipient])
        guard let url = proof["url"] as? String, let container = configuration["appContainer"],
              url.range(of: "^https://localhost:8977/app/signup#[A-Za-z0-9_-]{43}$", options: .regularExpression) != nil else { throw Failure.invalidFixture }
        let path = URL(fileURLWithPath: container).appendingPathComponent("tmp/analytics-fixture-link")
        try Data(url.utf8).write(to: path, options: .atomic)
        app.terminate(); app.launchArguments = ["--mychef-analytics-native-fixture", "--mychef-analytics-fixture-link"]; app.launch()
        app.launchArguments = ["--mychef-analytics-native-fixture"]
        waitFor({ self.text("Finish signing up") }, "Fixture proof reaches real completion UI")
        XCTAssertEqual(checkbox().value as? String, "0")
        fill("Password", configuration["password"] ?? "", secure: true)
        fill("Confirm password", configuration["password"] ?? "", secure: true)
        tap("Create account")
        waitFor({ self.text("Account created. Sign in to MyChef.") }, "Installed signup must complete")
        let counts = try control("counts")
        XCTAssertEqual(counts["enabled"] as? Int, 0)
        XCTAssertEqual(counts["queued"] as? Int, 0); XCTAssertEqual(counts["delivered"] as? Int, 0)
        mark("signup", assessed ? "completedOffNoCapture" : "disabledNoCapture")
        tap("Back to MyChef")
    }
    func testAuthenticatedAnalytics() throws {
        app.launch()
        if configuration["layoutOnly"] == "true" {
            tap("Create an account")
            let box = checkbox(); reveal(box)
            XCTAssertGreaterThanOrEqual(app.frame.width, 370, "Small supported iPhone width")
            XCTAssertLessThanOrEqual(app.frame.width, 380, "Small supported iPhone width")
            XCTAssertGreaterThanOrEqual(box.frame.minX, app.frame.minX)
            XCTAssertLessThanOrEqual(box.frame.maxX, app.frame.maxX)
            fill("Email", "layout@example.test")
            let email = app.textFields["Email"].firstMatch
            let keyboard = app.keyboards.firstMatch
            XCTAssertTrue(keyboard.waitForExistence(timeout: 5))
            XCTAssertLessThanOrEqual(email.frame.maxY, keyboard.frame.minY + 1)
            mark("accessibility", "phoneWidths", "keyboardUnobstructed")
            guard let output = configuration["output"] else { throw Failure.invalidFixture }
            try JSONSerialization.data(withJSONObject: ["checks": observations]).write(to: URL(fileURLWithPath: output), options: .atomic)
            return
        }
        XCTAssertGreaterThanOrEqual(app.frame.width, 390, "Regular supported iPhone width")
        try signup("analyticsdisabled@example.test", assessed: false)
        _ = try control("approved"); relaunch()
        try signup("analyticsassessed@example.test", assessed: true)
        fill("Email", configuration["email"] ?? "")
        fill("Password", configuration["password"] ?? "", secure: true)
        tap("Sign in to MyChef"); profile(); details()
        XCTAssertEqual(checkbox().value as? String, "0"); mark("participation", "existingOff")
        enabled(true); mark("participation", "explicitEnable")
        fill("Diet", "Vegetarian"); tap("Save preferences")
        waitFor({ ((try? self.control("counts")["queued"]) as? Int) == 1 }, "Completed action has one queued event")
        mark("participation", "committedCoreAction")
        relaunch(); profile(); details(); XCTAssertEqual(checkbox().value as? String, "1")
        _ = try control("tick")
        XCTAssertEqual(try control("counts")["delivered"] as? Int, 1)
        mark("participation", "restarted", "exactlyOneEvent")
        _ = try control("other-session-choice", ["enabled": false])
        checkbox().tap()
        waitFor({ self.text("Your setting changed elsewhere") }, "Stale choice must show conflict")
        XCTAssertEqual(checkbox().value as? String, "0"); mark("conflict", "staleConflict", "currentState")
        _ = try control("maintenance"); enabled(true)
        _ = try control("offline"); checkbox().tap()
        waitFor({ self.text("Turning off was not confirmed") }, "Offline Off must remain unconfirmed")
        XCTAssertEqual(checkbox().value as? String, "1"); mark("conflict", "offlineOffUnconfirmed")
        _ = try control("online"); enabled(false)
        waitFor({ self.text("Active deletion is in progress") }, "Active cleanup is truthful")
        mark("conflict", "reconnectFenceAcknowledged"); mark("lifecycle", "activeCleanup")
        _ = try control("maintenance"); enabled(true)
        tap("Shopping list"); waitFor({ self.text("Saved") }, "Shopping snapshot must load")
        _ = try control("offline")
        let extra = app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "A few extras?")).firstMatch
        reveal(extra); extra.tap()
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
        profile(); fill("Diet", "Mediterranean"); tap("Save preferences")
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
            tap("Download ZIP")
            // These controls belong to UIDocumentPicker, reached by OS UI taps.
            if save {
                let onDevice = app.staticTexts["On My iPhone"].firstMatch
                if onDevice.waitForExistence(timeout: 5) { onDevice.tap() }
                tap("Save")
            } else { tap("Cancel") }
            waitFor({ self.text(save ? "Your download is ready" : "Download cancelled.") }, "Native export result must match Save or Cancel")
            mark("lifecycle", save ? "nativeSave" : "nativeCancel")
        }
        // Automated OS audits supplement, but never replace, audible VoiceOver.
        _ = try control("foreign-export"); mark("lifecycle", "foreignExportRefused")
        try app.performAccessibilityAudit(for: [.elementDetection, .hitRegion, .sufficientElementDescription])
        tap("Delete my account"); fill("Current password", configuration["password"] ?? "", secure: true)
        fill("Type DELETE to confirm", "DELETE"); tap("Permanently delete account")
        waitFor({ self.text("Welcome back.") }, "Account deletion must return to sign in")
        mark("lifecycle", "accountDeleted"); relaunch()
        waitFor({ self.text("Welcome back.") }, "Deleted session must stay signed out after relaunch")
        guard let output = configuration["output"] else { throw Failure.invalidFixture }
        try JSONSerialization.data(withJSONObject: ["checks": observations]).write(to: URL(fileURLWithPath: output), options: .atomic)
        // Unobserved speech, layout sizes and private-cache inspection remain
        // false. The coordinator must refuse a full receipt for this run.
    }
}
