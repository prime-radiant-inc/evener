import Foundation
import XCTest
import EvenerVoiceCore
@testable import EvenerVoiceTransport

final class VoiceRecoveryTransportTests: XCTestCase {
    func testRecoveryRetiresOnWireAndAllowsFreshSpeechWithoutNewInput() async throws {
        let peer = try VoiceWebSocketTestPeer(scenario: #"""
        self.send_event({'type': 'speech_start', 'generation': 9, 'deliveryId': 'cancelled'})
        controls = []
        while True:
            event = self.receive_event()
            if event is None: return
            if event['type'] == 'fixture_probe': break
            controls.append(event)
        self.send_event({'type': 'fixture_observed', 'controls': controls})
        if controls != [{'type': 'interrupt', 'generation': 9, 'playedSamples': {}}]:
            self.receive_event()
            return
        assert self.receive_event() == {'type': 'fixture_recovered'}
        self.send_event({'type': 'stop', 'generation': 10})
        self.send_event({'type': 'speech_start', 'generation': 9, 'deliveryId': 'cancelled'})
        self.send_event({'type': 'speech_start', 'generation': 11, 'deliveryId': 'fresh'})
        self.send_event({'type': 'speech_start', 'generation': 9, 'deliveryId': 'cancelled'})
        self.send_event({'type': 'fixture_finished'})
        self.receive_event()
"""#)
        defer { peer.stop() }
        let queue = DispatchQueue(label: "voice-recovery-transport-test")
        let socket = VoiceSocket(queue: queue)
        defer { queue.sync { socket.close() } }
        let observed = expectation(description: "peer observed recovery retirement")
        var finished: XCTestExpectation?
        var voice = VoiceController()
        var rejectedDeliveries = 0
        var acceptedGenerations: [Int] = []
        var controls: [[String: Any]] = []
        var flushes = 0
        try queue.sync {
            voice.arm(policy: VoicePolicy()); voice.ready(generation: 5)
            _ = voice.stopSpeaking(played: [:])
            _ = voice.recovering(advanceGeneration: false)
            XCTAssertEqual(voice.state, .recovering)
            socket.failed = { XCTFail("Loopback voice connection failed") }
            socket.received = { event in
                switch event["type"] as? String {
                case "speech_start":
                    let generation = event["generation"] as! Int
                    switch voice.admitSpeech(generation: generation, speakerConsent: true) {
                    case .accepted: acceptedGenerations.append(generation)
                    case .rejected(let actions):
                        rejectedDeliveries += 1
                        for action in actions {
                            switch action {
                            case .flushOutput: flushes += 1
                            case .send(let message): socket.send(message.json)
                            default: XCTFail("Unexpected recovery action")
                            }
                        }
                    }
                    if voice.state == .recovering { socket.send(["type": "fixture_probe"]) }
                case "fixture_observed":
                    controls = event["controls"] as! [[String: Any]]
                    observed.fulfill()
                case "stop": voice.remoteStop(generation: event["generation"] as! Int)
                case "fixture_finished": finished?.fulfill()
                default: XCTFail("Unexpected loopback event")
                }
            }
            try socket.connect(origin: "http://127.0.0.1:\(peer.port)", path: "/api/voice/socket", grant: "fixture-only", binding: "binding", bindingEpoch: "epoch", device: "device")
        }
        await fulfillment(of: [observed], timeout: 5)
        let retired = queue.sync { () -> Bool in
            XCTAssertEqual(rejectedDeliveries, 1)
            XCTAssertEqual(acceptedGenerations, [])
            XCTAssertEqual(flushes, 1)
            let expected: [[String: Any]] = [["type": "interrupt", "generation": 9, "playedSamples": [:] as [String: Int]]]
            XCTAssertEqual(controls as NSArray, expected as NSArray)
            XCTAssertEqual(voice.minimumGeneration, 10)
            XCTAssertEqual(voice.state, .recovering)
            return (controls as NSArray) == (expected as NSArray)
        }
        guard retired else { return }
        let deliveryFinished = expectation(description: "fresh delivery after audio recovery")
        queue.sync {
            finished = deliveryFinished
            _ = voice.recovering(advanceGeneration: false)
            voice.ready(generation: voice.minimumGeneration)
            socket.send(["type": "fixture_recovered"])
        }
        await fulfillment(of: [deliveryFinished], timeout: 5)
        queue.sync {
            XCTAssertEqual(rejectedDeliveries, 3)
            XCTAssertEqual(acceptedGenerations, [11])
            XCTAssertEqual(flushes, 1)
            XCTAssertEqual(voice.minimumGeneration, 11)
            XCTAssertEqual(voice.state, .speaking)
            XCTAssertEqual(voice.sequence, 0)
            XCTAssertFalse(voice.cloudMicrophone)
        }
    }
}
