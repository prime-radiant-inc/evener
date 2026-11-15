import XCTest
@testable import EvenerVoiceCore

final class WakeTrialLedgerTests: XCTestCase {
    private var suite: String!
    private var defaults: UserDefaults!
    private let settings = WakeWordSettings(threshold: 0.99, releaseThreshold: 0.5, hopSamples: 1280, holdoffMs: 2500, embeddingFrames: 16)

    override func setUp() {
        super.setUp()
        suite = "WakeTrialLedgerTests.\(UUID().uuidString)"
        defaults = UserDefaults(suiteName: suite)!
    }
    override func tearDown() {
        defaults.removePersistentDomain(forName: suite)
        super.tearDown()
    }
    private func start(_ ledger: WakeTrialLedger, at: Double = 1) throws {
        try ledger.start(modelId: "test-model", phrase: "Hey Evener.", settings: settings, at: at)
    }
    private func assertFailure(_ code: String, _ action: () throws -> Void, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertThrowsError(try action(), file: file, line: line) {
            XCTAssertEqual(($0 as? VoiceFailure)?.code, code, file: file, line: line)
        }
    }

    func testStartRequiresPreviousAttemptMarkedAndFinishOnlyOnce() throws {
        let ledger = WakeTrialLedger(defaults: defaults)
        assertFailure("wake_trial_missing") { try ledger.finish(.positive) }
        try start(ledger)
        XCTAssertTrue(ledger.running)
        let first = try XCTUnwrap(ledger.attempts.last)
        XCTAssertEqual(first.modelId, "test-model")
        XCTAssertEqual(first.phrase, "Hey Evener.")
        XCTAssertEqual(first.settings.threshold, 0.99)
        XCTAssertEqual(first.startedAt, 1)
        assertFailure("wake_trial_unmarked") { try start(ledger, at: 2) }
        ledger.interrupt()
        XCTAssertFalse(ledger.running)
        XCTAssertTrue(ledger.attempts[0].interrupted)
        assertFailure("wake_trial_unmarked") { try start(ledger, at: 3) }
        try ledger.finish(.positive)
        assertFailure("wake_trial_missing") { try ledger.finish(.negative) }
        try start(ledger, at: 4)
        XCTAssertEqual(ledger.attempts.count, 2)
        XCTAssertEqual(ledger.attempts[0].label, .positive)
        XCTAssertNotEqual(ledger.attempts[1].id, first.id)
        XCTAssertTrue(ledger.running)
    }

    func testOnlyRunningScoredFramesCountAndFinishedAttemptIsFrozen() throws {
        let ledger = WakeTrialLedger(defaults: defaults)
        ledger.record(WakeWordResult(state: .listening, score: 1, detected: true, sampleOffset: 0))
        ledger.dropped(1280)
        XCTAssertTrue(ledger.attempts.isEmpty)
        try start(ledger)
        ledger.record(WakeWordResult(state: .warming, score: nil, detected: false, sampleOffset: 1280))
        XCTAssertEqual(ledger.attempts[0].scoredSamples, 0)
        ledger.record(WakeWordResult(state: .holdoff, score: 0.99, detected: true, sampleOffset: 2560))
        ledger.record(WakeWordResult(state: .holdoff, score: 0.2, detected: false, sampleOffset: 3840))
        ledger.dropped(640)
        try ledger.finish(.falseActivation)
        ledger.record(WakeWordResult(state: .listening, score: 1, detected: true, sampleOffset: 5120))
        ledger.dropped(1280)
        ledger.interrupt()
        let attempt = ledger.attempts[0]
        XCTAssertEqual(attempt.detections, 1)
        XCTAssertEqual(attempt.scoredSamples, 2560)
        XCTAssertEqual(attempt.droppedSamples, 640)
        XCTAssertEqual(attempt.lastScore, 0.2)
        XCTAssertEqual(attempt.label, .falseActivation)
        XCTAssertFalse(attempt.interrupted)
        XCTAssertFalse(attempt.running)
        let reloaded = WakeTrialLedger(defaults: defaults)
        XCTAssertEqual(reloaded.attempts[0].label, .falseActivation)
        XCTAssertEqual(reloaded.json.count, 1)
        try start(reloaded)
        try reloaded.finish(.negative)
        XCTAssertEqual(reloaded.attempts[1].label, .negative)
    }

    func testReloadInterruptsActiveAttemptAndRequiresLabelBeforeRestart() throws {
        let ledger = WakeTrialLedger(defaults: defaults)
        try start(ledger)
        ledger.record(WakeWordResult(state: .holdoff, score: 1, detected: true, sampleOffset: 1280))
        ledger.dropped(2560)
        ledger.save()
        let reloaded = WakeTrialLedger(defaults: defaults)
        XCTAssertFalse(reloaded.running)
        XCTAssertTrue(reloaded.attempts[0].interrupted)
        XCTAssertNil(reloaded.attempts[0].label)
        XCTAssertEqual(reloaded.attempts[0].detections, 1)
        XCTAssertEqual(reloaded.attempts[0].scoredSamples, 1280)
        XCTAssertEqual(reloaded.attempts[0].droppedSamples, 2560)
        reloaded.record(WakeWordResult(state: .listening, score: 0, detected: false, sampleOffset: 2560))
        reloaded.dropped(1280)
        XCTAssertEqual(reloaded.attempts[0].lastScore, 1)
        XCTAssertEqual(reloaded.attempts[0].droppedSamples, 2560)
        assertFailure("wake_trial_unmarked") { try start(reloaded) }
        try reloaded.finish(.positive)
        XCTAssertTrue(WakeTrialLedger(defaults: defaults).attempts[0].interrupted)
    }

    func testPrunesOldestAttemptsOnLoadAndStartButKeepsUnmarkedLastAttempt() throws {
        // Seed an older unbounded ledger through the actual persisted schema/key.
        let legacy = (0..<260).map { index -> WakeTrialAttempt in
            var attempt = WakeTrialAttempt(id: "old-\(index)", modelId: "test-model", phrase: "Hey Evener.", settings: settings, startedAt: Double(index))
            attempt.running = false
            attempt.label = .negative
            return attempt
        }
        defaults.set(try JSONEncoder().encode(legacy), forKey: "evener.voice.wakeTrials.v1")
        let ledger = WakeTrialLedger(defaults: defaults)
        XCTAssertEqual(ledger.attempts.count, 256)
        XCTAssertEqual(ledger.attempts.first?.id, "old-4")
        XCTAssertEqual(WakeTrialLedger(defaults: defaults).attempts.count, 256)
        try start(ledger, at: 300)
        XCTAssertEqual(ledger.attempts.count, 256)
        XCTAssertEqual(ledger.attempts.first?.id, "old-5")
        let currentId = ledger.attempts.last?.id
        let reloaded = WakeTrialLedger(defaults: defaults)
        XCTAssertEqual(reloaded.attempts.count, 256)
        XCTAssertEqual(reloaded.attempts.last?.id, currentId)
        XCTAssertTrue(reloaded.attempts.last?.interrupted == true)
        XCTAssertNil(reloaded.attempts.last?.label)
        assertFailure("wake_trial_unmarked") { try start(reloaded) }
        try reloaded.finish(.positive)
    }

    func testTrialJSONEncodesOncePerRevisionNotOncePerPublish() throws {
        var encodes = 0
        let ledger = WakeTrialLedger(defaults: defaults, encode: { attempts in
            encodes += 1
            return attempts.map { ["id": $0.id] }
        })
        try start(ledger)
        let revisionAtStart = ledger.revision
        // A running trial publishes twice a second and every observer read
        // builds the snapshot: none of that may re-encode the ledger (#5683).
        for _ in 0..<100 { _ = ledger.json }
        XCTAssertEqual(encodes, 1)
        ledger.record(WakeWordResult(state: .listening, score: 0.5, detected: false, sampleOffset: 1280))
        XCTAssertGreaterThan(ledger.revision, revisionAtStart)
        for _ in 0..<100 { _ = ledger.json }
        XCTAssertEqual(encodes, 2)
    }

    func testWarmingRecordsDoNotBumpTheRevisionOrDropTheCache() throws {
        var encodes = 0
        let ledger = WakeTrialLedger(defaults: defaults, encode: { attempts in
            encodes += 1
            return attempts.map { ["id": $0.id] }
        })
        try start(ledger)
        _ = ledger.json
        XCTAssertEqual(encodes, 1)
        let revision = ledger.revision
        // The detector emits nil-score, not-detected results while its state
        // recovers after a reset; those change nothing and dirty nothing (#5683).
        ledger.record(WakeWordResult(state: .warming, score: nil, detected: false, sampleOffset: 1280))
        XCTAssertEqual(ledger.revision, revision)
        _ = ledger.json
        XCTAssertEqual(encodes, 1)
        ledger.record(WakeWordResult(state: .holdoff, score: 0.5, detected: false, sampleOffset: 2560))
        XCTAssertEqual(ledger.revision, revision + 1)
        _ = ledger.json
        XCTAssertEqual(encodes, 2)
    }

    func testEveryMutationBumpsTheRevisionAndReadsDoNot() throws {
        let ledger = WakeTrialLedger(defaults: defaults)
        XCTAssertEqual(ledger.revision, 0)
        _ = ledger.json
        XCTAssertEqual(ledger.revision, 0)
        try start(ledger)
        XCTAssertEqual(ledger.revision, 1)
        ledger.dropped(1280)
        XCTAssertEqual(ledger.revision, 2)
        ledger.record(WakeWordResult(state: .listening, score: 1, detected: true, sampleOffset: 1280))
        XCTAssertEqual(ledger.revision, 3)
        try ledger.finish(.positive)
        XCTAssertEqual(ledger.revision, 4)
        _ = ledger.json
        XCTAssertEqual(ledger.revision, 4)
    }
}
