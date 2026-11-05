import Foundation
import XCTest
import EvenerVoiceCore
@testable import EvenerVoiceTransport

final class VoiceConsentTransportTests: XCTestCase {
    func testPendingSpeakerConsentRetiresOnWireBeforeFreshDelivery() async throws {
        // Only the remote peer is scripted. Admission and ordered JSON transport
        // are the same production controller/socket used by VoiceService.
        let peer = try VoiceWebSocketTestPeer(scenario: #"""
        self.send_event({'type': 'speech_start', 'generation': 9, 'deliveryId': 'denied'})
        controls = []
        while True:
            event = self.receive_event()
            if event is None: return
            if event['type'] == 'fixture_probe': break
            controls.append(event)
        # Probe is ordered after the actual admission actions, so an absent
        # interrupt is observable without waiting for a playback timeout.
        self.send_event({'type': 'fixture_observed', 'controls': controls})
        if controls != [{'type': 'interrupt', 'generation': 9, 'playedSamples': {}}]:
            self.receive_event()
            return
        assert self.receive_event() == {'type': 'fixture_consent'}
        self.send_event({'type': 'stop', 'generation': 10})
        self.send_event({'type': 'speech_start', 'generation': 9, 'deliveryId': 'denied'})
        self.send_event({'type': 'speech_start', 'generation': 11, 'deliveryId': 'fresh'})
        self.send_event({'type': 'speech_start', 'generation': 9, 'deliveryId': 'denied'})
        self.send_event({'type': 'fixture_finished'})
        self.receive_event()
"""#)
        defer { peer.stop() }
        let port = peer.port
        let queue = DispatchQueue(label: "voice-consent-transport-test")
        let socket = VoiceSocket(queue: queue)
        defer { queue.sync { socket.close() } }
        let observed = expectation(description: "peer observed admission controls")
        var finished: XCTestExpectation?
        var voice = VoiceController()
        var speakerConsent = false
        var rejectedDeliveries = 0
        var acceptedGenerations: [Int] = []
        var controls: [[String: Any]] = []
        try queue.sync {
            voice.arm(policy: VoicePolicy()); voice.ready(generation: 5)
            socket.failed = { XCTFail("Loopback voice connection failed") }
            socket.received = { event in
                switch event["type"] as? String {
                case "speech_start":
                    let generation = event["generation"] as! Int
                    switch voice.admitSpeech(generation: generation, speakerConsent: speakerConsent) {
                    case .accepted: acceptedGenerations.append(generation)
                    case .rejected(let actions):
                        rejectedDeliveries += 1
                        for action in actions {
                            if case .send(let message) = action { socket.send(message.json) }
                        }
                    }
                    if !speakerConsent { socket.send(["type": "fixture_probe"]) }
                case "fixture_observed":
                    controls = event["controls"] as! [[String: Any]]
                    observed.fulfill()
                case "stop": voice.remoteStop(generation: event["generation"] as! Int)
                case "fixture_finished": finished?.fulfill()
                default: XCTFail("Unexpected loopback event")
                }
            }
            try socket.connect(origin: "http://127.0.0.1:\(port)", path: "/api/voice/socket", grant: "fixture-only", binding: "binding", bindingEpoch: "epoch", device: "device")
        }
        await fulfillment(of: [observed], timeout: 5)
        let retired = queue.sync { () -> Bool in
            XCTAssertEqual(rejectedDeliveries, 1)
            XCTAssertEqual(acceptedGenerations, [])
            let expected: [[String: Any]] = [["type": "interrupt", "generation": 9, "playedSamples": [:] as [String: Int]]]
            XCTAssertEqual(controls as NSArray, expected as NSArray)
            XCTAssertEqual(voice.minimumGeneration, 10)
            XCTAssertEqual(voice.state, .quiet)
            return (controls as NSArray) == (expected as NSArray)
        }
        guard retired else { return }
        let deliveryFinished = expectation(description: "fresh speech admitted after consent")
        queue.sync {
            finished = deliveryFinished
            speakerConsent = true
            socket.send(["type": "fixture_consent"])
        }
        await fulfillment(of: [deliveryFinished], timeout: 5)
        queue.sync {
            XCTAssertEqual(rejectedDeliveries, 3)
            XCTAssertEqual(acceptedGenerations, [11])
            XCTAssertEqual(voice.minimumGeneration, 11)
            XCTAssertEqual(voice.state, .speaking)
        }
    }
}
