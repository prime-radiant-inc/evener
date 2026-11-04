import Foundation

@main
struct VADSmoke {
    static func main() throws {
        let detector = try SileroVAD()
        for _ in 0..<100 {
            guard try !detector.process([Float](repeating: 0, count: 512)) else { throw VoiceFailure("silence_classified_as_speech") }
        }
        print("Actual Silero V5 + ONNX Runtime: 100 recurrent silence windows processed, no speech")
    }
}
