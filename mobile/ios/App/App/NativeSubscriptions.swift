import Foundation
import StoreKit
import Capacitor
import CryptoKit
import DeviceCheck

private enum SubscriptionFailure: Error { case unavailable }

private final class BillingNoRedirects: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest,
                    completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}

// All proof uploads go directly to the private API. The web bridge cannot
// select a path, get an assertion, or supply bytes to the App Attest signer.
private struct AppleCheckoutConnection {
    private enum Endpoint {
        case enrollmentState, enrollmentChallenge, enroll, launch(UUID), cancel(UUID), releaseUnused(UUID)

        var path: String {
            switch self {
            case .enrollmentState, .enroll: return "/apple/enrollment"
            case .enrollmentChallenge: return "/apple/enrollment/challenge"
            case .launch(let id): return "/checkouts/" + id.uuidString.lowercased() + "/launch"
            case .cancel(let id): return "/checkouts/" + id.uuidString.lowercased() + "/cancel"
            case .releaseUnused(let id): return "/checkouts/" + id.uuidString.lowercased()
            }
        }

        var method: String {
            switch self {
            case .enrollmentState: return "GET"
            case .releaseUnused: return "DELETE"
            default: return "POST"
            }
        }
    }

    private struct KeyReply: Decodable { let keyID: String }
    private struct ChallengeReply: Decodable { let challenge: String }
    struct LaunchReply: Decodable {
        let checkout: String
        let binding: String
        let product: String
        let interval: String
        let challenge: String
    }

    private static let network: URLSession = {
        let config = URLSessionConfiguration.ephemeral
        config.httpCookieStorage = nil
        config.httpShouldSetCookies = false
        config.urlCache = nil
        config.urlCredentialStorage = nil
        config.timeoutIntervalForRequest = 15
        config.timeoutIntervalForResource = 20
        return URLSession(configuration: config, delegate: BillingNoRedirects(), delegateQueue: nil)
    }()

    private let origin: String
    private let access: String

    init(origin: String?, call: CAPPluginCall) throws {
        guard let origin, let url = URLComponents(string: origin), url.scheme == "https",
              let host = url.host,
              host.range(of: #"^[a-z0-9-]+\.tail[a-z0-9]+\.ts\.net$"#, options: .regularExpression) != nil,
              url.user == nil, url.password == nil, url.path.isEmpty, url.query == nil, url.fragment == nil,
              let access = call.getString("access"), access.utf8.count == 64,
              access.range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil
        else { throw SubscriptionFailure.unavailable }
        self.origin = origin
        self.access = access
    }

    private func request(_ endpoint: Endpoint, body: [String: String]? = nil) async throws -> (Int, Data) {
        guard let url = URL(string: origin + "/api/native/v1/billing" + endpoint.path)
        else { throw SubscriptionFailure.unavailable }
        var request = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData)
        request.httpMethod = endpoint.method
        request.httpShouldHandleCookies = false
        request.setValue("Bearer " + access, forHTTPHeaderField: "Authorization")
        request.setValue("no-store", forHTTPHeaderField: "Cache-Control")
        if let body {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try JSONEncoder().encode(body)
        }
        let (stream, response) = try await Self.network.bytes(for: request)
        guard let response = response as? HTTPURLResponse, response.url == url,
              response.expectedContentLength <= 4096 else { throw SubscriptionFailure.unavailable }
        var data = Data()
        for try await byte in stream {
            guard data.count < 4096 else { throw SubscriptionFailure.unavailable }
            data.append(byte)
        }
        return (response.statusCode, data)
    }

    private static func base64URL(_ data: Data) -> String {
        data.base64EncodedString().replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "")
    }

    static func identifier(_ value: String) throws -> Data {
        guard value.utf8.count == 43,
              let decoded = Data(base64Encoded: value.replacingOccurrences(of: "-", with: "+")
                                 .replacingOccurrences(of: "_", with: "/") + "="),
              decoded.count == 32, base64URL(decoded) == value else { throw SubscriptionFailure.unavailable }
        return decoded
    }

    func enrolledKey() async throws -> String? {
        let (code, data) = try await request(.enrollmentState)
        if code == 204 { return nil }
        guard code == 200 else { throw SubscriptionFailure.unavailable }
        return try Self.identifier(JSONDecoder().decode(KeyReply.self, from: data).keyID).base64EncodedString()
    }

    @MainActor func enroll(_ attest: DCAppAttestService) async throws -> String {
        let (code, data) = try await request(.enrollmentChallenge)
        guard code == 200 else { throw SubscriptionFailure.unavailable }
        let reply = try JSONDecoder().decode(ChallengeReply.self, from: data)
        let rawChallenge = try Self.identifier(reply.challenge)
        let key = try await attest.generateKey()
        guard let keyBytes = Data(base64Encoded: key), keyBytes.count == 32 else { throw SubscriptionFailure.unavailable }
        let proof = try await attest.attestKey(key, clientDataHash: Data(SHA256.hash(data: rawChallenge)))
        guard !proof.isEmpty, proof.count <= (64 << 10) else { throw SubscriptionFailure.unavailable }
        let (saved, _) = try await request(.enroll, body: ["challenge": reply.challenge, "keyID": Self.base64URL(keyBytes),
                                                         "attestation": Self.base64URL(proof)])
        guard saved == 204 else { throw SubscriptionFailure.unavailable }
        return key
    }

    func releaseUnused(_ checkout: UUID) async throws {
        let (code, _) = try await request(.releaseUnused(checkout))
        guard code == 204 else { throw SubscriptionFailure.unavailable }
    }

    func authorize(_ checkout: UUID) async throws -> LaunchReply {
        let (code, data) = try await request(.launch(checkout))
        guard code == 200 else { throw SubscriptionFailure.unavailable }
        return try JSONDecoder().decode(LaunchReply.self, from: data)
    }

    func cancel(_ checkout: UUID, launch: LaunchReply, assertion: Data) async throws -> Bool {
        guard !assertion.isEmpty, assertion.count <= 1024 else { return false }
        let (code, _) = try await request(.cancel(checkout), body: ["challenge": launch.challenge,
                                                                  "assertion": Self.base64URL(assertion)])
        return code == 204
    }
}

enum NativeSubscriptions {
    private static let previewBundle = "com.gkldevelopment.mychef.preview"
    private static let amounts = ["monthly": 699, "annual": 6990]
    @MainActor private static var checkoutInProgress = false

    @MainActor private static func isVerifiedSandboxInstall() async -> Bool {
        guard #available(iOS 16.0, *),
              Bundle.main.bundleIdentifier == previewBundle,
              let result = try? await AppTransaction.shared,
              case .verified(let install) = result else { return false }
        return install.bundleID == previewBundle && install.environment == .sandbox
    }

    private static func interval(_ product: Product) -> String? {
        guard let period = product.subscription?.subscriptionPeriod, period.value == 1 else { return nil }
        if period.unit == .month { return "monthly" }
        if period.unit == .year { return "annual" }
        return nil
    }

    private static func validProductID(_ value: String) -> Bool {
        value.range(of: #"^[A-Za-z0-9_.-]{1,128}$"#, options: .regularExpression) != nil
    }

    @MainActor private static func quote(_ product: Product, _ storefront: Storefront) -> [String: Any]? {
        guard storefront.countryCode == "BEL", product.type == .autoRenewable,
              let interval = interval(product), let amount = amounts[interval],
              product.price == Decimal(amount) / Decimal(100),
              product.priceFormatStyle.currencyCode == "EUR",
              let terms = product.subscription, terms.introductoryOffer == nil else { return nil }
        return ["store": "apple", "product": product.id, "basePlan": "", "interval": interval,
                "region": storefront.countryCode, "currency": "EUR", "amountMinor": amount,
                "displayPrice": product.displayPrice, "introductory": false]
    }

    @MainActor static func quotes(_ call: CAPPluginCall) async throws -> [[String: Any]] {
        guard let values = call.getArray("products", String.self), values.count <= 2,
              values.allSatisfy(validProductID) else { throw SubscriptionFailure.unavailable }
        guard await isVerifiedSandboxInstall() else { return [] }
        let products = try await Product.products(for: values)
        guard let storefront = await Storefront.current else { return [] }
        return products.compactMap { quote($0, storefront) }
    }

    @MainActor static func prepare(_ call: CAPPluginCall, origin: String?) async throws -> Bool {
        guard !checkoutInProgress, await isVerifiedSandboxInstall() else { return false }
        // Both entry points share the same native lock, including their awaits.
        guard !checkoutInProgress else { return false }
        checkoutInProgress = true
        defer { checkoutInProgress = false }
        let attest = DCAppAttestService.shared
        guard attest.isSupported else { return false }
        let backend = try AppleCheckoutConnection(origin: origin, call: call)
        let key: String
        if let enrolled = try await backend.enrolledKey() {
            key = enrolled
        } else {
            key = try await backend.enroll(attest)
        }
        try await checkKey(attest, key: key)
        return true
    }

    @MainActor static func purchase(_ call: CAPPluginCall, origin: String?) async throws -> [String: Any] {
        guard !checkoutInProgress else { throw SubscriptionFailure.unavailable }
        checkoutInProgress = true
        defer { checkoutInProgress = false }

        let backend = try AppleCheckoutConnection(origin: origin, call: call)
        let attest = DCAppAttestService.shared
        guard let checkout = call.getString("checkout").flatMap(UUID.init(uuidString:))
        else { throw SubscriptionFailure.unavailable }
        let id: String
        let token: UUID
        let wantedInterval: String
        let key: String
        do {
            guard attest.isSupported, await isVerifiedSandboxInstall(),
                  let requestedProduct = call.getString("product"), validProductID(requestedProduct),
                  let binding = call.getString("binding").flatMap(UUID.init(uuidString:)),
                  let interval = call.getString("interval"),
                  let product = try await Product.products(for: [requestedProduct]).first,
                  product.id == requestedProduct, let storefront = await Storefront.current,
                  let current = quote(product, storefront), current["interval"] as? String == interval,
                  current["displayPrice"] as? String == call.getString("displayPrice"),
                  AppStore.canMakePayments, let enrolled = try await backend.enrolledKey()
            else { throw SubscriptionFailure.unavailable }
            // Check that the installation still holds the key before authorizing its sheet.
            try await checkKey(attest, key: enrolled)
            id = requestedProduct
            token = binding
            wantedInterval = interval
            key = enrolled
        } catch {
            // Launch has not been requested. The server only releases an unused reservation.
            try? await backend.releaseUnused(checkout)
            throw error
        }

        // Once launch is requested, never use the unused-release endpoint.
        let launch = try await backend.authorize(checkout)
        guard launch.checkout == checkout.uuidString.lowercased(),
              launch.binding == token.uuidString.lowercased(),
              launch.product == id, launch.interval == wantedInterval,
              let challenge = try? AppleCheckoutConnection.identifier(launch.challenge),
              let liveStorefront = await Storefront.current,
              let freshProduct = try await Product.products(for: [id]).first, freshProduct.id == id,
              let live = quote(freshProduct, liveStorefront), live["interval"] as? String == wantedInterval,
              live["displayPrice"] as? String == call.getString("displayPrice"), AppStore.canMakePayments
        else { return ["state": "pending"] }

        switch try await freshProduct.purchase(options: [.appAccountToken(token)]) {
        case .success(let result):
            guard case .verified(let transaction) = result,
                  transaction.appAccountToken == token, transaction.productID == id,
                  let proof = evidence(transaction, interval: wantedInterval) else { return ["state": "pending"] }
            return ["state": "proof", "proof": proof]
        case .userCancelled:
            guard await hasNoMatchingPurchase(for: id, token: token) else { return ["state": "pending"] }
            let fields = ["mychef.storekit.cancel.v1", "sandbox", launch.checkout, launch.binding, launch.interval, ""]
            var message = Data(fields.joined(separator: "\u{0}").utf8)
            message.append(challenge)
            do {
                let assertion = try await attest.generateAssertion(key, clientDataHash: Data(SHA256.hash(data: message)))
                if try await backend.cancel(checkout, launch: launch, assertion: assertion) {
                    return ["state": "released"]
                }
            } catch {
                // Network loss, expiry or missing proof never clears a launch.
            }
            return ["state": "pending"]
        case .pending:
            return ["state": "pending"]
        @unknown default:
            return ["state": "pending"]
        }
    }

    @MainActor private static func checkKey(_ attest: DCAppAttestService, key: String) async throws {
        // This probe stays local and uses a separate domain from cancellation.
        let probe = Data(("mychef.storekit.ready.v1|" + UUID().uuidString).utf8)
        _ = try await attest.generateAssertion(key, clientDataHash: Data(SHA256.hash(data: probe)))
    }

    @MainActor private static func hasNoMatchingPurchase(for product: String, token: UUID) async -> Bool {
        var scanned = 0
        for await result in Transaction.unfinished {
            scanned += 1
            guard scanned <= 100, case .verified(let transaction) = result,
                  transaction.appAccountToken != token else { return false }
        }
        if let result = await Transaction.latest(for: product) {
            guard case .verified(let transaction) = result, transaction.appAccountToken != token else { return false }
        }
        return true
    }

    // The app server fetches and verifies the signed transaction by ID. No
    // receipt, order token or account binding is written to device storage.
    private static func evidence(_ transaction: Transaction, interval: String? = nil) -> [String: String]? {
        guard #available(iOS 16.0, *), transaction.environment == .sandbox,
              transaction.productType == .autoRenewable, transaction.appAccountToken != nil,
              transaction.revocationDate == nil else { return nil }
        var proof = ["id": String(transaction.id), "receipt": String(transaction.id)]
        proof["interval"] = interval
        return proof
    }

    @MainActor static func receipts(restore: Bool) async throws -> [[String: String]] {
        guard await isVerifiedSandboxInstall() else { throw SubscriptionFailure.unavailable }
        if restore { try await AppStore.sync() }
        var seen: Set<String> = []
        var results: [[String: String]] = []
        for await result in Transaction.unfinished {
            if case .verified(let transaction) = result, let proof = evidence(transaction),
               let id = proof["id"], seen.insert(id).inserted {
                results.append(proof)
                if results.count == 10 { return results }
            }
        }
        if restore {
            for await result in Transaction.currentEntitlements {
                if case .verified(let transaction) = result, let proof = evidence(transaction),
                   let id = proof["id"], seen.insert(id).inserted {
                    results.append(proof)
                    if results.count == 10 { return results }
                }
            }
        }
        return results
    }

    @MainActor static func finish(_ call: CAPPluginCall) async throws {
        guard await isVerifiedSandboxInstall() else { throw SubscriptionFailure.unavailable }
        guard let id = call.getString("id"), let number = UInt64(id), number > 0, String(number) == id
        else { throw SubscriptionFailure.unavailable }
        for await result in Transaction.unfinished {
            if case .verified(let transaction) = result, transaction.id == number, evidence(transaction) != nil {
                await transaction.finish()
                return
            }
        }
    }
}
