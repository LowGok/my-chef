import Foundation
import StoreKit
import Capacitor

private enum SubscriptionFailure: Error { case unavailable }

enum NativeSubscriptions {
    private static let previewBundle = "com.gkldevelopment.mychef.preview"
    private static let amounts = ["monthly": 699, "annual": 6990]

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

    @MainActor static func purchase(_ call: CAPPluginCall) async throws -> [String: Any] {
        guard await isVerifiedSandboxInstall() else { throw SubscriptionFailure.unavailable }
        guard let id = call.getString("product"), validProductID(id),
              let token = call.getString("binding").flatMap(UUID.init(uuidString:)),
              let wantedInterval = call.getString("interval"),
              let product = try await Product.products(for: [id]).first, product.id == id,
              let storefront = await Storefront.current,
              let current = quote(product, storefront), current["interval"] as? String == wantedInterval,
              current["displayPrice"] as? String == call.getString("displayPrice"),
              AppStore.canMakePayments, await isVerifiedSandboxInstall() else { throw SubscriptionFailure.unavailable }
        switch try await product.purchase(options: [.appAccountToken(token)]) {
        case .success(let result):
            guard case .verified(let transaction) = result,
                  transaction.appAccountToken == token, transaction.productID == id,
                  let proof = evidence(transaction, interval: wantedInterval) else { return ["state": "pending"] }
            return ["state": "proof", "proof": proof]
        case .userCancelled:
            return ["state": "cancelled"]
        case .pending:
            return ["state": "pending"]
        @unknown default:
            return ["state": "pending"]
        }
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
