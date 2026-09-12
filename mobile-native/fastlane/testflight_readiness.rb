module TestflightReadiness
  TERMINAL_STATES = %w[INVALID FAILED ERROR EXPIRED REJECTED PROCESSING_EXCEPTION].freeze
  READY_INTERNAL_STATES = %w[READY_FOR_BETA_TESTING IN_BETA_TESTING].freeze

  class TimeoutError < StandardError; end
  class TerminalError < StandardError; end
  class AmbiguousError < StandardError; end

  def self.wait(timeout:, interval:, clock: -> { Process.clock_gettime(Process::CLOCK_MONOTONIC) }, sleeper: ->(seconds) { sleep(seconds) })
    deadline = clock.call + timeout
    loop do
      builds, assigned = yield
      raise AmbiguousError, "App Store Connect returned multiple exact iOS builds" if builds.length > 1

      build = builds.first
      processing_state = build&.processing_state
      internal_state = build&.build_beta_detail&.internal_build_state
      if TERMINAL_STATES.include?(processing_state) || TERMINAL_STATES.include?(internal_state)
        raise TerminalError, "App Store Connect rejected iOS build (processing=#{processing_state.inspect}, internal=#{internal_state.inspect})"
      end
      return build if build && processing_state == "VALID" && READY_INTERNAL_STATES.include?(internal_state) && assigned

      remaining = deadline - clock.call
      raise TimeoutError, "Timed out waiting for iOS build to enter the configured internal TestFlight group" if remaining <= 0

      sleeper.call([interval, remaining].min)
    end
  end
end
