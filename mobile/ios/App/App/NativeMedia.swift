import Foundation
import UIKit
import PhotosUI
import UniformTypeIdentifiers
import ImageIO
import Capacitor

enum NativeMediaFailure: Error { case unavailable }

final class NativeMedia: NSObject, PHPickerViewControllerDelegate, UIDocumentPickerDelegate {
    private weak var presenter: UIViewController?
    private let exports: URL
    private var pending: CAPPluginCall?
    private var controller: UIViewController?
    private var purpose = ""
    private var exporting = false
    private var staged: URL?
    private var progress: Progress?
    private var generation = UUID()

    init(presenter: UIViewController) throws {
        self.presenter = presenter
        var directory = try FileManager.default.url(for: .cachesDirectory, in: .userDomainMask,
                                                     appropriateFor: nil, create: true).appendingPathComponent("MyChefExports", isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true,
                                                attributes: [.protectionKey: FileProtectionType.complete])
        var values = URLResourceValues(); values.isExcludedFromBackup = true
        try directory.setResourceValues(values); exports = directory
        super.init(); try cleanup()
    }
    private func cleanup() throws {
        // This private directory contains only copies created by this adapter.
        // Picked originals and provider-owned source-photo URLs never enter it.
        for file in try FileManager.default.contentsOfDirectory(at: exports, includingPropertiesForKeys: nil) {
            try FileManager.default.removeItem(at: file)
        }
        staged = nil
    }
    private func start(_ call: CAPPluginCall) throws {
        guard pending == nil, staged == nil, let presenter = presenter,
              presenter.presentedViewController == nil else { throw NativeMediaFailure.unavailable }
        pending = call; generation = UUID()
    }
    func pick(_ call: CAPPluginCall) {
        do {
            guard let purpose = call.getString("purpose"), ["dish", "recipe-pages"].contains(purpose) else { throw NativeMediaFailure.unavailable }
            try start(call); self.purpose = purpose; exporting = false
            let token = generation
            let sheet = UIAlertController(title: purpose == "dish" ? "Choose recipe photo" : "Choose recipe pages", message: nil, preferredStyle: .actionSheet)
            sheet.addAction(UIAlertAction(title: "Photo library", style: .default) { [weak self, weak sheet] _ in
                sheet?.dismiss(animated: true) { self?.library(token) }
            })
            sheet.addAction(UIAlertAction(title: "Browse files", style: .default) { [weak self, weak sheet] _ in
                sheet?.dismiss(animated: true) { self?.documents(token) }
            })
            sheet.addAction(UIAlertAction(title: "Cancel", style: .cancel) { [weak self] _ in self?.finish(["files": []]) })
            if let popover = sheet.popoverPresentationController, let view = presenter?.view {
                popover.sourceView = view; popover.sourceRect = CGRect(x: view.bounds.midX, y: view.bounds.midY, width: 1, height: 1)
            }
            controller = sheet; presenter?.present(sheet, animated: true)
        } catch { call.reject("The photo picker could not be opened", "native_media") }
    }
    private func library(_ token: UUID) {
        guard token == generation, pending != nil else { return }
        var config = PHPickerConfiguration(photoLibrary: .shared())
        config.filter = .images; config.selectionLimit = purpose == "dish" ? 1 : 3
        config.preferredAssetRepresentationMode = purpose == "dish" ? .compatible : .current
        let picker = PHPickerViewController(configuration: config); picker.delegate = self
        controller = picker; presenter?.present(picker, animated: true)
    }
    private func documents(_ token: UUID) {
        guard token == generation, pending != nil else { return }
        let types: [UTType] = purpose == "dish" ? [.jpeg, .png, .webP] : [.jpeg, .png, .heic]
        let picker = UIDocumentPickerViewController(forOpeningContentTypes: types, asCopy: false)
        picker.allowsMultipleSelection = purpose == "recipe-pages"; picker.delegate = self
        controller = picker; presenter?.present(picker, animated: true)
    }
    func picker(_ picker: PHPickerViewController, didFinishPicking results: [PHPickerResult]) {
        guard controller === picker, pending != nil else { return }
        let token = generation, purpose = self.purpose
        guard results.count <= (purpose == "dish" ? 1 : 3) else { fail(); return }
        picker.dismiss(animated: true) { [weak self] in
            self?.readLibrary(results, index: 0, files: [], size: 0, token: token, purpose: purpose)
        }
    }
    private func readLibrary(_ results: [PHPickerResult], index: Int, files: [[String: Any]], size: Int, token: UUID, purpose: String) {
        guard token == generation, pending != nil else { return }
        if index == results.count { finish(["files": files]); return }
        progress = results[index].itemProvider.loadFileRepresentation(forTypeIdentifier: UTType.image.identifier) { [weak self] url, _ in
            guard let self = self else { return }
            do {
                guard let url = url else { throw NativeMediaFailure.unavailable }
                let photo = try Self.photo(url, purpose: purpose, library: true)
                guard size + photo.size <= 12 * 1024 * 1024 else { throw NativeMediaFailure.unavailable }
                DispatchQueue.main.async {
                    guard token == self.generation, self.pending != nil else { return }
                    self.readLibrary(results, index: index + 1, files: files + [photo.value], size: size + photo.size, token: token, purpose: purpose)
                }
            } catch { DispatchQueue.main.async { if token == self.generation { self.fail() } } }
        }
    }
    func documentPicker(_ picker: UIDocumentPickerViewController, didPickDocumentsAt urls: [URL]) {
        guard controller === picker, pending != nil else { return }
        if exporting { finish(["saved": !urls.isEmpty]); return }
        let token = generation, purpose = self.purpose
        guard urls.count <= (purpose == "dish" ? 1 : 3) else { fail(); return }
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            do {
                var files: [[String: Any]] = []; var size = 0
                for url in urls {
                    let photo = try Self.photo(url, purpose: purpose, library: false)
                    size += photo.size; guard size <= 12 * 1024 * 1024 else { throw NativeMediaFailure.unavailable }
                    files.append(photo.value)
                }
                DispatchQueue.main.async { if let self = self, token == self.generation { self.finish(["files": files]) } }
            } catch { DispatchQueue.main.async { if let self = self, token == self.generation { self.fail() } } }
        }
    }
    func documentPickerWasCancelled(_ picker: UIDocumentPickerViewController) {
        guard controller === picker else { return }
        finish(exporting ? ["saved": false] : ["files": []])
    }
    private static func photo(_ url: URL, purpose: String, library: Bool) throws -> (value: [String: Any], size: Int) {
        let access = url.startAccessingSecurityScopedResource(); defer { if access { url.stopAccessingSecurityScopedResource() } }
        let limit = (purpose == "dish" ? 12 : 10) * 1024 * 1024
        if let size = try url.resourceValues(forKeys: [.fileSizeKey]).fileSize, size > limit { throw NativeMediaFailure.unavailable }
        guard let stream = InputStream(url: url) else { throw NativeMediaFailure.unavailable }
        stream.open(); defer { stream.close() }
        var data = Data(), buffer = [UInt8](repeating: 0, count: 8192)
        while true {
            let count = stream.read(&buffer, maxLength: buffer.count)
            if count < 0 || data.count + count > limit { throw NativeMediaFailure.unavailable }
            if count == 0 { break }; data.append(contentsOf: buffer.prefix(count))
        }
        guard !data.isEmpty, let source = CGImageSourceCreateWithData(data as CFData, nil),
              CGImageSourceGetCount(source) == 1, let type = CGImageSourceGetType(source) as String?,
              let properties = CGImageSourceCopyPropertiesAtIndex(source, 0, nil) as? [CFString: Any],
              let width = properties[kCGImagePropertyPixelWidth] as? Int, let height = properties[kCGImagePropertyPixelHeight] as? Int,
              width > 0, height > 0, width <= 8192, height <= 8192, Int64(width) * Int64(height) <= 20_000_000 else { throw NativeMediaFailure.unavailable }
        var mime: String
        switch type {
        case UTType.jpeg.identifier: mime = "image/jpeg"
        case UTType.png.identifier: mime = "image/png"
        case UTType.webP.identifier where purpose == "dish": mime = "image/webp"
        case UTType.heic.identifier, "public.heif":
            if purpose == "recipe-pages" { mime = "image/heic" }
            else {
                guard library, let image = CGImageSourceCreateThumbnailAtIndex(source, 0, [kCGImageSourceCreateThumbnailFromImageAlways: true, kCGImageSourceCreateThumbnailWithTransform: true, kCGImageSourceThumbnailMaxPixelSize: 8192] as CFDictionary) else { throw NativeMediaFailure.unavailable }
                let encoded = NSMutableData()
                guard let target = CGImageDestinationCreateWithData(encoded, UTType.jpeg.identifier as CFString, 1, nil) else { throw NativeMediaFailure.unavailable }
                CGImageDestinationAddImage(target, image, [kCGImageDestinationLossyCompressionQuality: 0.9] as CFDictionary)
                guard CGImageDestinationFinalize(target), encoded.length <= limit else { throw NativeMediaFailure.unavailable }
                data = encoded as Data; mime = "image/jpeg"
            }
        default: throw NativeMediaFailure.unavailable
        }
        return (["mimeType": mime, "data": data.base64EncodedString()], data.count)
    }
    func save(_ call: CAPPluginCall) {
        do {
            guard let filename = call.getString("filename"), filename.range(of: "^mychef-data-[a-f0-9]{32}\\.zip$", options: .regularExpression) != nil,
                  let encoded = call.getString("data"), encoded.count <= 44 * 1024 * 1024,
                  let bytes = Data(base64Encoded: encoded), bytes.count >= 22, bytes.count <= 32 * 1024 * 1024,
                  Array(bytes.suffix(22).prefix(4)) == [0x50, 0x4b, 0x05, 0x06] else { throw NativeMediaFailure.unavailable }
            try start(call); call.options = ["_callNonce": call.getString("_callNonce") ?? ""]; exporting = true
            let file = exports.appendingPathComponent(filename); staged = file
            try bytes.write(to: file, options: [.atomic, .completeFileProtection])
            let picker = UIDocumentPickerViewController(forExporting: [file], asCopy: true)
            picker.delegate = self; controller = picker; presenter?.present(picker, animated: true)
        } catch {
            if pending === call { fail(call) }
            else { call.reject("The download could not be saved", "native_media") }
        }
    }
    private func finish(_ response: [String: Any]) {
        let call = pending; pending = nil; progress = nil; controller = nil
        do { try cleanup(); nativeReply(call, response) }
        catch { call?.reject("Private file cleanup could not be completed", "native_media") }
    }
    private func fail(_ call: CAPPluginCall? = nil) {
        let current = call ?? pending; pending = nil; progress?.cancel(); progress = nil
        controller?.dismiss(animated: false); controller = nil
        do { try cleanup() } catch { /* A later cleanup attempt must still see the staged URL. */ }
        current?.reject("The file could not be opened or saved", "native_media")
    }
    func cancel(_ completion: @escaping (Bool) -> Void) {
        generation = UUID(); progress?.cancel(); progress = nil
        let call = pending, view = controller; pending = nil; controller = nil
        var cleaned = true
        do { try cleanup() } catch { cleaned = false }
        call?.reject("The account changed", "native_cancelled")
        let result = cleaned
        if view?.presentingViewController != nil { view?.dismiss(animated: false) { completion(result) } }
        else { completion(result) }
    }
    static func external(_ value: String) throws -> URL {
        guard value.count <= 4096, !value.contains("\r"), !value.contains("\n"), let parts = URLComponents(string: value),
              parts.user == nil, parts.password == nil, let url = parts.url else { throw NativeMediaFailure.unavailable }
        let mail = value.range(of: "^mailto:[A-Za-z0-9._+-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}$", options: .regularExpression) != nil
        guard mail || (parts.scheme == "https" && parts.host != nil) else { throw NativeMediaFailure.unavailable }
        return url
    }
}
