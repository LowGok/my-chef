import Foundation
import Security
import CryptoKit

private enum ShoppingFailure: Error { case unavailable }
private struct ShoppingEnvelope: Codable {
    let version: Int
    let accountId: String
    let accountInstance: String
    let ciphertext: Data
}

final class ProtectedShopping {
    private let service = "com.gkldevelopment.mychef.preview.shopping.v1"
    private let directory: URL
    private var generation = UUID().uuidString.lowercased()
    private var file: URL { directory.appendingPathComponent("shopping.v1") }
    private var marker: URL { directory.appendingPathComponent("shopping-erased.v1") }
    private var query: [String: Any] {
        [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service,
         kSecAttrAccount as String: "key", kSecAttrSynchronizable as String: false]
    }
    init() throws {
        let manager = FileManager.default
        var directory = try manager.url(for: .applicationSupportDirectory, in: .userDomainMask,
                                        appropriateFor: nil, create: true).appendingPathComponent("MyChefNative", isDirectory: true)
        try manager.createDirectory(at: directory, withIntermediateDirectories: true,
                                    attributes: [.protectionKey: FileProtectionType.complete])
        var values = URLResourceValues(); values.isExcludedFromBackup = true
        try directory.setResourceValues(values); self.directory = directory
    }
    private func key(create: Bool) throws -> SymmetricKey {
        var input = query; input[kSecReturnData as String] = true; input[kSecMatchLimit as String] = kSecMatchLimitOne
        var output: CFTypeRef?
        let status = SecItemCopyMatching(input as CFDictionary, &output)
        if status == errSecSuccess, let data = output as? Data, data.count == 32 { return SymmetricKey(data: data) }
        guard status == errSecItemNotFound, create else { throw ShoppingFailure.unavailable }
        let key = SymmetricKey(size: .bits256)
        var attributes = query
        attributes[kSecValueData as String] = key.withUnsafeBytes { Data($0) }
        attributes[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        guard SecItemAdd(attributes as CFDictionary, nil) == errSecSuccess else { throw ShoppingFailure.unavailable }
        return key
    }
    private func aad(_ owner: String, _ instance: String) throws -> Data {
        guard owner.range(of: "^[0-9]{1,20}$", options: .regularExpression) != nil,
              instance.range(of: "^[a-f0-9]{32}$", options: .regularExpression) != nil else { throw ShoppingFailure.unavailable }
        return Data("mychef:shopping:v1:\(owner):\(instance)".utf8)
    }
    private func eraseContent() throws {
        // Marker first: interruption can never expose the prior encrypted file.
        try Data([1]).write(to: marker, options: [.atomic, .completeFileProtection])
        let status = SecItemDelete(query as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else { throw ShoppingFailure.unavailable }
        if FileManager.default.fileExists(atPath: file.path) { try FileManager.default.removeItem(at: file) }
    }
    func read() throws -> [String: Any] {
        var result: [String: Any] = ["generation": generation, "value": NSNull()]
        if FileManager.default.fileExists(atPath: marker.path) || !FileManager.default.fileExists(atPath: file.path) { return result }
        let size = try file.resourceValues(forKeys: [.fileSizeKey]).fileSize ?? 0
        guard size <= 12 * 1024 * 1024 else { throw ShoppingFailure.unavailable }
        let envelope = try JSONDecoder().decode(ShoppingEnvelope.self, from: Data(contentsOf: file))
        guard envelope.version == 1, envelope.ciphertext.count <= 8 * 1024 * 1024 + 28 else { throw ShoppingFailure.unavailable }
        let bytes = try AES.GCM.open(AES.GCM.SealedBox(combined: envelope.ciphertext), using: key(create: false),
                                     authenticating: aad(envelope.accountId, envelope.accountInstance))
        guard let payload = String(data: bytes, encoding: .utf8) else { throw ShoppingFailure.unavailable }
        result["value"] = ["version": 1, "accountId": envelope.accountId, "accountInstance": envelope.accountInstance, "payload": payload]
        return result
    }
    func write(expected: String, value: [String: Any]?) throws {
        guard expected == generation else { throw ShoppingFailure.unavailable }
        guard let value = value else { try eraseContent(); return }
        guard value["version"] as? Int == 1, let owner = value["accountId"] as? String,
              let instance = value["accountInstance"] as? String, let payload = value["payload"] as? String,
              payload.utf8.count <= 8 * 1024 * 1024 else { throw ShoppingFailure.unavailable }
        let sealed = try AES.GCM.seal(Data(payload.utf8), using: key(create: true), authenticating: aad(owner, instance))
        guard let ciphertext = sealed.combined else { throw ShoppingFailure.unavailable }
        let envelope = ShoppingEnvelope(version: 1, accountId: owner, accountInstance: instance, ciphertext: ciphertext)
        try JSONEncoder().encode(envelope).write(to: file, options: [.atomic, .completeFileProtection])
        if FileManager.default.fileExists(atPath: marker.path) { try FileManager.default.removeItem(at: marker) }
    }
    func clear() throws -> [String: String] {
        generation = UUID().uuidString.lowercased(); try eraseContent()
        return ["generation": generation]
    }
}
