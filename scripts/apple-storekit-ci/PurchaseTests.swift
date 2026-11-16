import DeviceCheck
import StoreKit
import StoreKitTest
import XCTest

final class PurchaseTests: XCTestCase {
    @MainActor
    func testBelgianPurchaseAndRestoreWithStoreKit() async throws {
        let url = try XCTUnwrap(Bundle(for: Self.self).url(forResource: "Premium", withExtension: "storekit"))
        let session = try SKTestSession(contentsOf: url)
        session.resetToDefaultState()
        session.clearTransactions()
        session.disableDialogs = true
        session.storefront = "BEL"
        defer { session.clearTransactions() }
        NSLog("StoreKit test session ready; Simulator App Attest support: \(DCAppAttestService.shared.isSupported)")

        let prefix = "com.gkldevelopment.mychef.preview.premium."
        let terms: [(String, Int, Product.SubscriptionPeriod.Unit)] = [
            ("monthly", 699, .month), ("annual", 6990, .year),
        ]
        let products = try await Product.products(for: terms.map { prefix + $0.0 })
        XCTAssertEqual(products.count, terms.count)
        let storefront = await Storefront.current
        XCTAssertEqual(storefront?.countryCode, "BEL")
        for (name, cents, unit) in terms {
            session.clearTransactions()
            let id = prefix + name
            let product = try XCTUnwrap(products.first { $0.id == id })
            XCTAssertEqual(product.type, .autoRenewable)
            XCTAssertEqual(product.price, Decimal(cents) / 100)
            XCTAssertEqual(product.priceFormatStyle.currencyCode, "EUR")
            XCTAssertEqual(product.subscription?.subscriptionPeriod.value, 1)
            XCTAssertEqual(product.subscription?.subscriptionPeriod.unit, unit)
            XCTAssertNil(product.subscription?.introductoryOffer)

            let account = UUID()
            // Exercise Apple's real StoreKit framework without a foreground payment sheet in CI.
            NSLog("Creating local StoreKit purchase for \(name)")
            let purchase = try await session.buyProduct(identifier: id, options: [.appAccountToken(account)])
            XCTAssertEqual(purchase.productID, id)
            XCTAssertEqual(purchase.appAccountToken, account)
            XCTAssertEqual(purchase.environment, .xcode)
            await purchase.finish()

            // MyChef uses StoreKit sync followed by current entitlements on restore.
            NSLog("Syncing local StoreKit entitlement for \(name)")
            try await AppStore.sync()
            NSLog("Reading restored local StoreKit entitlement for \(name)")
            var found = false
            for await result in Transaction.currentEntitlements {
                guard case .verified(let restored) = result, restored.productID == id else { continue }
                XCTAssertEqual(restored.id, purchase.id)
                XCTAssertEqual(restored.appAccountToken, account)
                XCTAssertEqual(restored.environment, .xcode)
                found = true
            }
            XCTAssertTrue(found, "Restore did not return the verified local entitlement.")
        }
    }
}
