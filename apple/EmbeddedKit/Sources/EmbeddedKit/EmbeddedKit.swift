import Foundation
import LatticeEmbedded

/// A Lattice mesh node running entirely in user space, bridged from the
/// gomobile-generated LatticeEmbedded framework. Thin wrapper — the
/// Swift-idiomatic API surface is deliberately minimal until the first
/// consumer app settles the style (spec §4.3).
public final class EmbeddedEngine {
    private let inner: LatticeEmbeddedEmbeddedEngine

    /// Creates an engine from the same JSON config the Go side takes:
    /// `{"serverURL":"http://host:8080","token":"lt-...","name":"my-app"}`.
    public init(configJSON: String) throws {
        var err: NSError?
        guard let engine = LatticeEmbeddedNew(configJSON, &err) else {
            throw err ?? EmbeddedKitError.unknown
        }
        inner = engine
    }

    /// Launches the engine in the background; returns immediately.
    /// Poll `overlayAddress` until registration completes.
    public func start() throws {
        try inner.startAsync()
    }

    /// Cancels the engine and blocks (bounded) until teardown finishes.
    public func stop() {
        try? inner.stop()
    }

    /// The node's assigned overlay IP, or nil before registration completes.
    public var overlayAddress: String? {
        let addr = inner.overlayAddress()
        return addr.isEmpty ? nil : addr
    }

    /// Dials a remote overlay address.
    public func dial(network: String, addr: String) throws -> EmbeddedConnection {
        guard let conn = try? inner.dialConn(network, addr: addr) else {
            throw EmbeddedKitError.dialFailed
        }
        return EmbeddedConnection(inner: conn)
    }

    /// Listens for TCP connections on the overlay netstack.
    public func listen(network: String, addr: String) throws -> EmbeddedListener {
        guard let listener = try? inner.listenTCP(network, addr: addr) else {
            throw EmbeddedKitError.listenFailed
        }
        return EmbeddedListener(inner: listener)
    }
}

/// A TCP connection on the Lattice overlay.
public final class EmbeddedConnection {
    let inner: LatticeEmbeddedEmbeddedConn

    init(inner: LatticeEmbeddedEmbeddedConn) {
        self.inner = inner
    }

    /// Reads up to maxBytes bytes; blocks until at least one byte or EOF.
    /// Returns nil on EOF.
    public func read(maxBytes: Int = 64 * 1024) throws -> Data? {
        let chunk: Data? = try inner.readUp(to: maxBytes)
        return chunk
    }

    public func write(_ data: Data) throws {
        var n: Int = 0
        try inner.write(data, ret0_: &n)
    }

    public func close() {
        try? inner.close()
    }
}

/// A TCP listener on the Lattice overlay.
public final class EmbeddedListener {
    let inner: LatticeEmbeddedEmbeddedListener

    init(inner: LatticeEmbeddedEmbeddedListener) {
        self.inner = inner
    }

    /// Blocks until the next overlay connection arrives.
    public func accept() throws -> EmbeddedConnection {
        EmbeddedConnection(inner: try inner.accept())
    }

    public func close() {
        try? inner.close()
    }
}

public enum EmbeddedKitError: Error {
    case unknown
    case dialFailed
    case listenFailed
}
