import Foundation
import XCTest
@testable import EvenerVoiceCore

final class VoicePlaybackPacketTests: XCTestCase {
    func testWireSizedPacketsPreserveEverySignedLittleEndianSample() throws {
        // The enqueue validator/converter runs without starting microphone hardware.
        // Byte fixtures and Float expectations are independent of its Int16 loads.
        let bytes: [UInt8] = [0, 128, 255, 127, 255, 255, 0, 0, 1, 0, 0, 64]
        let expected: [Float] = [-1, 32767.0 / 32768, -1.0 / 32768, 0, 1.0 / 32768, 0.5]
        for byteCount in [48000, 48002, 65536] {
            let pcm = Data((0..<byteCount).map { bytes[$0 % bytes.count] })
            do {
                let packet = try VoicePlaybackPacket(pcm)
                XCTAssertEqual(packet.sampleCount, byteCount / 2)
                var samples = [Float](repeating: .nan, count: packet.sampleCount)
                samples.withUnsafeMutableBufferPointer { packet.copySamples(to: $0) }
                for i in samples.indices { XCTAssertEqual(samples[i], expected[i % expected.count]) }
            } catch {
                XCTFail("Production playback packet rejected \(byteCount) decoded bytes: \(error)")
            }
        }
    }

    func testEmptyOddAndOverLimitPacketsAreRejected() {
        for byteCount in [0, 1, 47999, 48001, 65535, 65537, 65538] {
            XCTAssertThrowsError(try VoicePlaybackPacket(Data(repeating: 0, count: byteCount))) { error in
                XCTAssertEqual((error as? VoiceFailure)?.code, "audio_packet_invalid")
            }
        }
    }

    func testWireSizedPacketsStillChargeOutstandingSamplesUntilDrained() throws {
        let packet = try VoicePlaybackPacket(Data(repeating: 0, count: 65536))
        var timeline = PlaybackTimeline()
        for _ in 0..<21 {
            try timeline.append(delivery: "d", generation: 7, item: "a", providerSamples: packet.sampleCount, outputSamples: 65536, outputRate: 48000)
        }
        // 21 * 32768 + 31872 = 720000, exactly the outstanding sample budget.
        let tail = try VoicePlaybackPacket(Data(repeating: 0, count: 63744))
        try timeline.append(delivery: "d", generation: 7, item: "a", providerSamples: tail.sampleCount, outputSamples: 63744, outputRate: 48000)
        let end = timeline.scheduledOutputSamples
        XCTAssertEqual(end, 1440000)
        XCTAssertEqual(timeline.watermarks(renderedOutputSamples: 0), ["a": 0])
        XCTAssertEqual(timeline.watermarks(renderedOutputSamples: 32768), ["a": 16384])
        XCTAssertThrowsError(try timeline.append(delivery: "d", generation: 7, item: "a", providerSamples: 1, outputSamples: 2, outputRate: 48000)) { error in
            guard case PlaybackError.overflow = error else { return XCTFail("Unexpected error: \(error)") }
        }
        XCTAssertEqual(timeline.scheduledOutputSamples, end)
        XCTAssertNil(timeline.drained(item: "a", outputEnd: 65536))
        try timeline.append(delivery: "d", generation: 7, item: "a", providerSamples: packet.sampleCount, outputSamples: 65536, outputRate: 48000)
        XCTAssertEqual(timeline.watermarks(renderedOutputSamples: 0), ["a": 32768])
        XCTAssertNil(timeline.providerEnded(item: "a"))
        XCTAssertEqual(timeline.drained(item: "a", outputEnd: timeline.scheduledOutputSamples), PlayedReceipt(delivery: "d", generation: 7, item: "a", samples: 752768, complete: true))
    }
}
