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
    func screenState() {
        print("Analytics iOS app state: \(app.state.rawValue) webview=\(app.webViews.count) alert=\(app.alerts.count)")
        for (name, phrase) in [("signed out", "Welcome back."), ("profile", "Your profile."), ("shopping", "Shopping list."), ("startup", "Loading MyChef"), ("inbox", "Check your inbox")] {
            let present = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", phrase)).firstMatch.exists
            print("Analytics iOS screen state: " + name + " present=\(present)")
        }
    }
    func waitFor(_ condition: @escaping () -> Bool, _ message: String) {
        let end = Date().addingTimeInterval(35)
        while Date() < end {
            if condition() { return }
            RunLoop.current.run(until: Date().addingTimeInterval(0.2))
        }
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
            if element.isHittable { return }
            // Keep the content gesture above the software keyboard. A whole-
            // screen swipe can land entirely on the keyboard on a small phone.
            scrollContent(up: true)
        }
        XCTAssertTrue(element.isHittable, "Control must be reachable at OS text size")
    }
    func tapSettled(_ element: XCUIElement) {
        let end = Date().addingTimeInterval(20)
        var previous = CGRect.null
        var unchanged = 0
        while Date() < end {
            if !element.exists {
                unchanged = 0
                RunLoop.current.run(until: Date().addingTimeInterval(0.2))
                continue
            }
            let frame = element.frame
            let stable = !previous.isNull && abs(frame.minX - previous.minX) <= 1
                && abs(frame.minY - previous.minY) <= 1 && abs(frame.width - previous.width) <= 1
                && abs(frame.height - previous.height) <= 1
            unchanged = element.isHittable && stable ? unchanged + 1 : 0
            if unchanged >= 2 { element.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap(); return }
            previous = frame
            RunLoop.current.run(until: Date().addingTimeInterval(0.2))
        }
        let exists = element.exists
        print("Analytics iOS tap state: type=\(element.elementType.rawValue) exists=\(exists) hittable=\(exists && element.isHittable) stable=\(unchanged)")
        screenState()
        XCTFail("Native tap target must finish layout before input")
    }
    func tap(_ label: String) {
        print("Analytics iOS control: " + label)
        if app.keyboards.firstMatch.exists {
            let done = app.buttons.matching(NSPredicate(format: "label == %@", "Done")).firstMatch
            if done.exists && done.isHittable { done.tap() }
        }
        if ["Shopping list", "Profile", "Meals"].contains(label) {
            // Reach the normal-flow header before touching sticky navigation;
            // this avoids relying on WebKit's scrolled sticky hit point.
            let home = app.buttons["Go to Meals"].firstMatch
            for _ in 0..<18 {
                if home.exists && home.isHittable && home.frame.minY >= app.frame.minY { break }
                scrollContent(up: false)
            }
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
        let webFrames = app.webViews.buttons.matching(predicate).allElementsBoundByIndex.filter { $0.exists }.map { $0.frame }
        let native = app.buttons.matching(predicate).allElementsBoundByIndex.filter {
            $0.exists && $0.isHittable && !webFrames.contains($0.frame)
        }
        return native.count == 1 ? native[0] : nil
    }
    func waitPickerButton(_ label: String) -> XCUIElement? {
        let end = Date().addingTimeInterval(30)
        while Date() < end {
            if let element = pickerButton(label) { return element }
            RunLoop.current.run(until: Date().addingTimeInterval(0.2))
        }
        screenState()
        for name in ["Cancel", "Save", "Browse", "Locations", "On My iPhone", "Downloads"] {
            let p = NSPredicate(format: "label == %@", name)
            print("Analytics iOS picker control: " + name + " total=\(app.buttons.matching(p).count) web=\(app.webViews.buttons.matching(p).count)")
        }
        XCTFail("System picker control must exist outside WebKit")
        return nil
    }
    func pickerAction(save: Bool) {
        // The HTML dialog also has Cancel. Require an actual system control
        // outside every WebView, including remote file-provider presentations.
        guard let cancel = waitPickerButton("Cancel") else { return }
        if !save { tapSettled(cancel); return }
        let local = app.staticTexts["On My iPhone"].firstMatch
        if !local.exists || !local.isHittable {
            for label in ["Browse", "Locations"] {
                if let back = pickerButton(label) { tapSettled(back); break }
            }
        }
        if local.waitForExistence(timeout: 5) && local.isHittable { tapSettled(local) }
        let downloads = app.staticTexts["Downloads"].firstMatch
        if downloads.exists && downloads.isHittable { tapSettled(downloads) }
        guard let saveButton = waitPickerButton("Save") else { return }
        waitFor({ saveButton.isEnabled && saveButton.isHittable }, "System picker needs a writable local destination")
        tapSettled(saveButton)
    }
    func fill(_ label: String, _ text: String, secure: Bool = false) {
        print("Analytics iOS control: " + label)
        hasEnteredData = true
        let ordinary = app.descendants(matching: .any).matching(NSPredicate(format:
            "label == %@ AND (elementType == %d OR elementType == %d)", label,
            XCUIElement.ElementType.textField.rawValue, XCUIElement.ElementType.textView.rawValue)).firstMatch
        let element = secure ? app.secureTextFields[label].firstMatch : ordinary
        reveal(element); tapSettled(element)
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
    func signup(_ recipient: String, assessed: Bool) throws {
        print("Analytics iOS phase: " + (assessed ? "assessed signup" : "disabled signup"))
        tap("Create an account")
        let box = checkbox(); reveal(box)
        if assessed {
            XCTAssertTrue(box.isEnabled); XCTAssertEqual(box.value as? String, "1")
            tapSettled(box); mark("signup", "assessedNotice", "offBeforeCompletion")
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
        tapSettled(checkbox())
        waitFor({ self.text("Your setting changed elsewhere") }, "Stale choice must show conflict")
        XCTAssertEqual(checkbox().value as? String, "0"); mark("conflict", "staleConflict", "currentState")
        _ = try control("maintenance"); enabled(true)
        _ = try control("offline"); tapSettled(checkbox())
        waitFor({ self.text("Turning off was not confirmed") }, "Offline Off must remain unconfirmed")
        XCTAssertEqual(checkbox().value as? String, "1"); mark("conflict", "offlineOffUnconfirmed")
        _ = try control("online"); enabled(false)
        waitFor({ self.text("Active deletion is in progress") }, "Active cleanup is truthful")
        mark("conflict", "reconnectFenceAcknowledged"); mark("lifecycle", "activeCleanup")
        _ = try control("maintenance"); enabled(true)
        tap("Shopping list"); waitFor({ self.text("Saved") }, "Shopping snapshot must load")
        _ = try control("offline")
        let extra = app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "A few extras?")).firstMatch
        reveal(extra); tapSettled(extra)
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
        waitFor({ self.text("Welcome back.") }, "Account deletion must return to sign in")
        mark("lifecycle", "accountDeleted"); relaunch()
        waitFor({ self.text("Welcome back.") }, "Deleted session must stay signed out after relaunch")
        guard let output = configuration["output"] else { throw Failure.invalidFixture }
        try JSONSerialization.data(withJSONObject: ["checks": observations]).write(to: URL(fileURLWithPath: output), options: .atomic)
        // Unobserved speech, layout sizes and private-cache inspection remain
        // false. The coordinator must refuse a full receipt for this run.
    }
    #if compiler(>=6.4)
    @available(iOS 27.0, *)
    func verifyVoiceOver() throws {
        let voice = XCUIDevice.shared.voiceOverService
        func spoken(_ phrase: String, backward: Bool = false, controlRole: Bool = false) throws -> String {
            func matches(_ value: String) -> Bool {
                guard value.localizedCaseInsensitiveContains(phrase) else { return false }
                return !controlRole || value.range(of: "switch|check.?box|button|link", options: [.regularExpression, .caseInsensitive]) != nil
            }
            if let current = try? voice.currentSpeech().utterance, matches(current) { return current }
            let deadline = Date().addingTimeInterval(90)
            for reverse in [backward, !backward] {
                for _ in 0..<64 {
                    if Date() >= deadline { break }
                    let output = try? (reverse ? voice.moveBackward() : voice.moveForward()).utterance
                    guard let output = output else { break }
                    if matches(output) { return output }
                }
            }
            XCTFail("Required app control or error must be spoken by VoiceOver")
            throw Failure.missingControl
        }
        func focusSpeech(_ element: XCUIElement, _ phrase: String) throws -> String {
            XCTAssertTrue(element.exists && element.isHittable, "Spoken control must be in the visible native tree")
            // A real single-finger touch explores the visible WebKit element.
            // This anchors VoiceOver inside its content before sequential moves.
            element.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
            let control = [XCUIElement.ElementType.button, .switch, .checkBox, .link, .disclosureTriangle].contains(element.elementType)
            return try spoken(phrase, backward: control, controlRole: control)
        }
        func state(_ utterance: String) -> String {
            // Inspect only the control/state prefix, before its long hint.
            let prefix = String(utterance.prefix(100)).lowercased()
            if prefix.range(of: "\\b(off|unchecked|not checked|not selected)\\b", options: .regularExpression) != nil { return "off" }
            if prefix.range(of: "\\b(on|checked|selected)\\b", options: .regularExpression) != nil { return "on" }
            let role = utterance.range(of: "switch|check.?box|button", options: [.regularExpression, .caseInsensitive]) != nil
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
        try voice.enable()
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
        try voice.enable()
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
        try voice.enable()
        print("Analytics iOS voice phase: profile order")
        _ = try focusSpeech(privacyHeading, "Privacy and data")
        var checkboxIndex: Int?, statusIndex: Int?, refreshIndex: Int?
        for index in 0..<24 {
            let utterance = try voice.moveForward().utterance
            if utterance.localizedCaseInsensitiveContains("Help improve MyChef"), checkboxIndex == nil { checkboxIndex = index }
            if checkboxIndex != nil, utterance.lowercased().trimmingCharacters(in: .whitespacesAndNewlines).hasPrefix("off"), statusIndex == nil { statusIndex = index }
            if utterance.localizedCaseInsensitiveContains("Refresh analytics setting") { refreshIndex = index; break }
        }
        XCTAssertNotNil(checkboxIndex); XCTAssertNotNil(statusIndex); XCTAssertNotNil(refreshIndex)
        XCTAssertLessThan(checkboxIndex ?? 100, statusIndex ?? -1)
        XCTAssertLessThan(statusIndex ?? 100, refreshIndex ?? -1)
        mark("accessibility", "focusOrder")
        try voice.disable()
        let enabledBox = checkbox(); reveal(enabledBox)
        try voice.enable()
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
        try voice.enable(); _ = try focusSpeech(offlineBox, "Help improve MyChef")
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
            if done.exists && done.isHittable { done.tap() }
        }
        let save = app.buttons["Save preferences"].firstMatch; reveal(save)
        try voice.enable()
        _ = try focusSpeech(save, "Save preferences"); app.doubleTap()
        waitFor({ ((try? self.control("counts")["queued"]) as? Int) == 1 }, "VoiceOver core action commits one event")
        _ = try control("tick"); _ = try control("backup")
        try voice.disable()
        let erase = checkbox(); reveal(erase)
        try voice.enable()
        _ = try focusSpeech(erase, "Help improve MyChef"); app.doubleTap()
        waitFor({ ((try? self.control("preference")["enabled"]) as? Bool) == false }, "VoiceOver Off must receive server acknowledgement")
        XCTAssertEqual(state(try focusSpeech(checkbox(), "Help improve MyChef")), "off")
        print("Analytics iOS voice phase: active cleanup")
        _ = try spoken("Active deletion is in progress")
        _ = try control("maintenance")
        try voice.disable()
        let refresh = app.buttons["Refresh analytics setting"].firstMatch; reveal(refresh)
        try voice.enable()
        _ = try focusSpeech(refresh, "Refresh analytics setting"); app.doubleTap()
        waitFor({ self.text("isolated backup cleanup is pending") }, "Backup cleanup stays pending")
        print("Analytics iOS voice phase: backup cleanup")
        _ = try spoken("isolated backup cleanup is pending", backward: true)
        mark("accessibility", "platformScreenReader", "spokenStatesAndErrors")
    }
    #endif

}
