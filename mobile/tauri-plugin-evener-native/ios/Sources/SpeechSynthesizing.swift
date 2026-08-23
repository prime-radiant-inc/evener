import AVFoundation
import Foundation

/// Abstraction over `AVSpeechSynthesizer` used for testable synthesis playback.
///
/// Conforms to the synthesis delegate contract so the production synthesizer
/// and test doubles can be swapped freely. Raw spoken text never flows through
/// the delegate callbacks — only stable chunk identifiers are emitted.
protocol SpeechSynthesizing: AnyObject {
    var delegate: SpeechSynthesizingDelegate? { get set }
    func speak(_ utterance: SpeechUtterance)
    func stopSpeaking()
    var isSpeaking: Bool { get }
    var rate: Float { get set }
}

protocol SpeechSynthesizingDelegate: AnyObject {
    func speechDidStart(chunkId: String)
    func speechDidFinish(chunkId: String)
    func speechDidCancel(chunkId: String)
}

/// A single unit of speech to synthesize. The `chunkId` is a stable,
/// caller-provided identifier; `text` is the spoken payload and never
/// appears in diagnostic events.
struct SpeechUtterance {
    let chunkId: String
    let text: String
    let rate: Float
}
