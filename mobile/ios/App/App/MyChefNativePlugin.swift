import Foundation
import Security
import Capacitor
import UIKit

func nativeReply(_ call: CAPPluginCall?, _ data: [String: Any] = [:]) {
    guard let call = call else { return }
    var value = data; value["_callNonce"] = call.getString("_callNonce") ?? ""
    call.resolve(value)
}

private enum NativeFailure: Error { case invalid }

// This-device-only Keychain items are unavailable while the device is locked.
// The app-container marker also prevents a reinstall reviving a retained item.
final class ProtectedRefresh {
    private let service = "com.gkldevelopment.mychef.preview.refresh.v1"
    private var query: [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: service,
         kSecAttrAccount as String: "installation",
         kSecAttrSynchronizable as String: false]
    }
    init() throws {
        let manager = FileManager.default
        var directory = try manager.url(for: .applicationSupportDirectory, in: .userDomainMask,
                                        appropriateFor: nil, create: true).appendingPathComponent("MyChefNative", isDirectory: true)
        try manager.createDirectory(at: directory, withIntermediateDirectories: true,
                                    attributes: [.protectionKey: FileProtectionType.complete])
        var resources = URLResourceValues(); resources.isExcludedFromBackup = true
        try directory.setResourceValues(resources)
        let marker = directory.appendingPathComponent("installation.v1")
        if !manager.fileExists(atPath: marker.path) {
            let status = SecItemDelete(query as CFDictionary)
            guard status == errSecSuccess || status == errSecItemNotFound else { throw NativeFailure.invalid }
            try Data([1]).write(to: marker, options: [.atomic, .completeFileProtection])
        }
    }
    func read() throws -> String? {
        var input = query; input[kSecReturnData as String] = true; input[kSecMatchLimit as String] = kSecMatchLimitOne
        var output: CFTypeRef?
        let status = SecItemCopyMatching(input as CFDictionary, &output)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = output as? Data,
              let value = String(data: data, encoding: .utf8),
              value.range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil else { throw NativeFailure.invalid }
        return value
    }
    func write(_ value: String?) throws {
        guard let value = value else {
            let status = SecItemDelete(query as CFDictionary)
            guard status == errSecSuccess || status == errSecItemNotFound else { throw NativeFailure.invalid }
            return
        }
        guard value.range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil else { throw NativeFailure.invalid }
        let attributes: [String: Any] = [kSecValueData as String: Data(value.utf8),
                                       kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly]
        let status = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if status == errSecItemNotFound {
            var input = query; attributes.forEach { input[$0.key] = $0.value }
            guard SecItemAdd(input as CFDictionary, nil) == errSecSuccess else { throw NativeFailure.invalid }
        } else if status != errSecSuccess { throw NativeFailure.invalid }
    }
}

private final class NativeRequestState {
    let call: CAPPluginCall
    let requestId: String
    let task: URLSessionDataTask
    var response: HTTPURLResponse?
    var bytes = Data()
    let image: Bool
    init(_ call: CAPPluginCall, _ requestId: String, _ task: URLSessionDataTask, image: Bool = false) {
        self.call = call; self.requestId = requestId; self.task = task
        self.image = image
    }
}

@objc(MyChefNativePlugin)
public class MyChefNativePlugin: CAPPlugin, CAPBridgedPlugin, URLSessionDataDelegate {
    public let identifier = "MyChefNativePlugin"
    public let jsName = "MyChefNative"
    public let pluginMethods: [CAPPluginMethod] = [
        CAPPluginMethod(name: "readRuntime", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "configuration", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "readRefresh", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "writeRefresh", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "readShopping", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "writeShopping", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "clearShopping", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "pickPhotos", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "saveExport", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "openExternal", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "publicImage", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "cancelMedia", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "request", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "cancelRequest", returnType: CAPPluginReturnPromise)
    ]
    private let storageQueue = DispatchQueue(label: "com.gkldevelopment.mychef.native-storage")
    private var refresh: ProtectedRefresh?
    private var shopping: ProtectedShopping?
    private var media: NativeMedia?
    private var runtimeObserver: NSObjectProtocol?
    private var origin: String?
    private let lock = NSLock()
    private var pending: [Int: NativeRequestState] = [:]
    private let maximumResponse = 32 * 1024 * 1024
    private let safeRequestHeaders: Set<String> = ["accept", "content-type", "authorization", "idempotency-key", "if-match", "if-none-match"]
    private let safeResponseHeaders: Set<String> = ["content-type", "content-disposition", "date", "etag", "x-mychef-user", "retry-after"]
    private var http: URLSession?
    private func configureHTTP() {
        let config = URLSessionConfiguration.ephemeral
        config.httpCookieStorage = nil; config.httpShouldSetCookies = false; config.urlCache = nil
        config.urlCredentialStorage = nil; config.requestCachePolicy = .reloadIgnoringLocalCacheData
        config.timeoutIntervalForRequest = 60; config.timeoutIntervalForResource = 90
        let queue = OperationQueue(); queue.maxConcurrentOperationCount = 1
        http = URLSession(configuration: config, delegate: self, delegateQueue: queue)
    }
    public override func load() {
        runtimeObserver = NotificationCenter.default.addObserver(forName: NativeRuntime.changed, object: nil, queue: .main) { [weak self] _ in
            self?.notifyListeners("runtime", data: [:])
        }
        let value = getConfig().getString("apiOrigin") ?? ""
        if let url = URLComponents(string: value), url.scheme == "https", let host = url.host,
           host == "localhost" || host.range(of: "^[a-z0-9-]+\\.tail[a-z0-9]+\\.ts\\.net$", options: .regularExpression) != nil,
           url.user == nil, url.password == nil, url.path.isEmpty, url.query == nil, url.fragment == nil,
           (url.port == nil || (1...65535).contains(url.port!)) { origin = value; configureHTTP() }
    }
    @objc func readRuntime(_ call: CAPPluginCall) {
        DispatchQueue.main.async { [self] in
            guard let origin = origin else { call.reject("App state is unavailable", "native_runtime"); return }
            nativeReply(call, NativeRuntime.shared.snapshot(origin: origin))
        }
    }
    @objc func configuration(_ call: CAPPluginCall) {
        guard let origin = origin else { call.reject("Private API configuration is unavailable", "native_configuration"); return }
        nativeReply(call, ["platform": "ios", "apiOrigin": origin])
    }
    @objc func readRefresh(_ call: CAPPluginCall) {
        storageQueue.async { [self] in
            do {
                if refresh == nil { refresh = try ProtectedRefresh() }
                let value = try refresh!.read()
                nativeReply(call, ["value": value as Any? ?? NSNull()])
            } catch { call.reject("Protected device storage is unavailable", "native_storage") }
        }
    }
    @objc func writeRefresh(_ call: CAPPluginCall) {
        storageQueue.async { [self] in
            do {
                guard let input = call.options["value"], input is String || input is NSNull else { throw NativeFailure.invalid }
                if refresh == nil { refresh = try ProtectedRefresh() }
                try refresh!.write(input as? String); nativeReply(call)
            } catch { call.reject("Protected device storage is unavailable", "native_storage") }
        }
    }
    @objc func request(_ call: CAPPluginCall) {
        do {
            guard let origin = origin, let http = http, let path = call.getString("path"),
                  path.hasPrefix("/api/native/v1/"), !path.contains("\\"), !path.contains("#"),
                  !path.contains("\r"), !path.contains("\n"),
                  !path.split(separator: "?", maxSplits: 1)[0].contains("%"),
                  !path.split(separator: "?", maxSplits: 1)[0].split(separator: "/").contains(where: { $0 == "." || $0 == ".." }),
                  let target = URL(string: origin + path),
                  let requestId = call.getString("requestId"), UUID(uuidString: requestId) != nil,
                  let method = call.getString("method"), ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"].contains(method)
            else { throw NativeFailure.invalid }
            var request = URLRequest(url: target, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 60)
            request.httpMethod = method; request.httpShouldHandleCookies = false
            request.setValue("no-store", forHTTPHeaderField: "Cache-Control")
            for (key, raw) in call.getObject("headers") ?? [:] {
                let name = key.lowercased()
                if !safeRequestHeaders.contains(name) { continue }
                guard let value = raw as? String, value.utf8.count <= 8192, !value.contains("\r"), !value.contains("\n"),
                      name != "authorization" || value.range(of: "^Bearer [a-f0-9]{64}$", options: .regularExpression) != nil
                else { throw NativeFailure.invalid }
                request.setValue(value, forHTTPHeaderField: name)
            }
            if let encoded = call.getString("body") {
                guard encoded.count <= 32 * 1024 * 1024,
                      let bytes = Data(base64Encoded: encoded), bytes.count <= 24 * 1024 * 1024,
                      method != "GET", method != "HEAD" else { throw NativeFailure.invalid }
                request.httpBody = bytes
            }
            lock.lock()
            guard pending.count < 32, !pending.values.contains(where: { $0.requestId == requestId }) else { lock.unlock(); throw NativeFailure.invalid }
            let task = http.dataTask(with: request)
            pending[task.taskIdentifier] = NativeRequestState(call, requestId, task)
            lock.unlock(); task.resume()
        } catch { call.reject("The private API request failed", "native_request") }
    }
    @objc func readShopping(_ call: CAPPluginCall) {
        storageQueue.async { [self] in
            do { if shopping == nil { shopping = try ProtectedShopping() }; nativeReply(call, try shopping!.read()) }
            catch { call.reject("Protected shopping storage is unavailable", "native_storage") }
        }
    }
    @objc func writeShopping(_ call: CAPPluginCall) {
        storageQueue.async { [self] in
            do {
                guard let raw = call.options["value"], raw is [String: Any] || raw is NSNull else { throw NativeFailure.invalid }
                if shopping == nil { shopping = try ProtectedShopping() }
                try shopping!.write(expected: call.getString("generation") ?? "", value: raw as? [String: Any]); nativeReply(call)
            } catch { call.reject("Protected shopping storage is unavailable", "native_storage") }
        }
    }
    @objc func clearShopping(_ call: CAPPluginCall) {
        storageQueue.async { [self] in
            do { if shopping == nil { shopping = try ProtectedShopping() }; nativeReply(call, try shopping!.clear()) }
            catch { call.reject("Protected shopping storage is unavailable", "native_storage") }
        }
    }
    @objc func cancelRequest(_ call: CAPPluginCall) {
        lock.lock(); let task = pending.values.first(where: { $0.requestId == call.getString("requestId") })?.task; lock.unlock()
        task?.cancel(); nativeReply(call)
    }
    private func mediaProvider() throws -> NativeMedia {
        if let media = media { return media }
        guard let controller = bridge?.viewController else { throw NativeMediaFailure.unavailable }
        let provider = try NativeMedia(presenter: controller); media = provider; return provider
    }
    @objc func pickPhotos(_ call: CAPPluginCall) {
        DispatchQueue.main.async { [self] in
            do { try mediaProvider().pick(call) }
            catch { call.reject("The photo picker could not be opened", "native_media") }
        }
    }
    @objc func saveExport(_ call: CAPPluginCall) {
        DispatchQueue.main.async { [self] in
            do { try mediaProvider().save(call) }
            catch { call.reject("The download could not be saved", "native_media") }
        }
    }
    @objc func openExternal(_ call: CAPPluginCall) {
        do {
            let url = try NativeMedia.external(call.getString("url") ?? "")
            DispatchQueue.main.async {
                UIApplication.shared.open(url, options: [:]) { opened in
                    if opened { nativeReply(call) } else { call.reject("This link could not be opened", "native_media") }
                }
            }
        } catch { call.reject("This link could not be opened", "native_media") }
    }
    @objc func publicImage(_ call: CAPPluginCall) {
        guard let http = http, let value = call.getString("url"), value.count <= 4096, let parts = URLComponents(string: value),
              parts.scheme == "https", parts.host == "res.cloudinary.com", parts.user == nil, parts.password == nil,
              parts.fragment == nil, parts.port == nil || parts.port == 443, let url = parts.url else {
            call.reject("The photo is unavailable", "native_media"); return
        }
        var request = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 60)
        request.httpMethod = "GET"; request.httpShouldHandleCookies = false
        request.setValue("image/jpeg,image/png,image/webp", forHTTPHeaderField: "Accept")
        lock.lock()
        guard pending.count < 32 else { lock.unlock(); call.reject("The photo is unavailable", "native_media"); return }
        let task = http.dataTask(with: request)
        pending[task.taskIdentifier] = NativeRequestState(call, UUID().uuidString.lowercased(), task, image: true)
        lock.unlock(); task.resume()
    }
    @objc func cancelMedia(_ call: CAPPluginCall) {
        lock.lock()
        let tasks = pending.values.filter { $0.image || $0.task.currentRequest?.url?.path.hasPrefix("/api/native/v1/personal-photos/") == true }.map { $0.task }
        lock.unlock(); tasks.forEach { $0.cancel() }
        DispatchQueue.main.async { [self] in
            do {
                try mediaProvider().cancel { cleaned in
                    if cleaned { nativeReply(call) } else { call.reject("Private file cleanup could not be completed", "native_media") }
                }
            } catch { call.reject("Private file cleanup could not be completed", "native_media") }
        }
    }
    func shutdown() {
        if let runtimeObserver = runtimeObserver { NotificationCenter.default.removeObserver(runtimeObserver) }
        runtimeObserver = nil;
        http?.invalidateAndCancel()
        DispatchQueue.main.async { [media = self.media] in media?.cancel { _ in } }
    }
    public func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                           newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
    public func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge,
                           completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        // Keep OS certificate validation; bearer APIs never negotiate a saved
        // password, client certificate or interactive authentication prompt.
        completionHandler(challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust
                          ? .performDefaultHandling : .cancelAuthenticationChallenge, nil)
    }
    public func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse,
                           completionHandler: @escaping (URLSession.ResponseDisposition) -> Void) {
        lock.lock(); defer { lock.unlock() }
        guard let state = pending[dataTask.taskIdentifier], let response = response as? HTTPURLResponse,
              !(300...399).contains(response.statusCode),
              !state.image || (200...299).contains(response.statusCode),
              response.expectedContentLength <= Int64(state.image ? 8 * 1024 * 1024 : maximumResponse) else { completionHandler(.cancel); return }
        state.response = response; completionHandler(.allow)
    }
    public func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        lock.lock(); defer { lock.unlock() }
        guard let state = pending[dataTask.taskIdentifier] else { dataTask.cancel(); return }
        if state.bytes.count + data.count > (state.image ? 8 * 1024 * 1024 : maximumResponse) { dataTask.cancel(); return }
        state.bytes.append(data)
    }
    public func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        lock.lock(); let state = pending.removeValue(forKey: task.taskIdentifier); lock.unlock()
        guard let state = state else { return }
        guard error == nil, let response = state.response else { state.call.reject("The private API request failed", "native_request"); return }
        if state.image {
            let mime = response.value(forHTTPHeaderField: "Content-Type")?.split(separator: ";").first?.trimmingCharacters(in: .whitespaces).lowercased() ?? ""
            guard ["image/jpeg", "image/png", "image/webp"].contains(mime) else { state.call.reject("The photo is unavailable", "native_media"); return }
            nativeReply(state.call, ["mimeType": mime, "data": state.bytes.base64EncodedString()]); return
        }
        var headers: [String: String] = [:]
        for (key, value) in response.allHeaderFields {
            if let key = key as? String, safeResponseHeaders.contains(key.lowercased()), let value = value as? String { headers[key] = value }
        }
        nativeReply(state.call, ["status": response.statusCode, "headers": headers, "body": state.bytes.base64EncodedString()])
    }
}

final class MyChefBridgeViewController: CAPBridgeViewController {
    private let nativePlugin = MyChefNativePlugin()
    override func capacitorDidLoad() { bridge?.registerPluginInstance(nativePlugin) }
    deinit { nativePlugin.shutdown() }
}
