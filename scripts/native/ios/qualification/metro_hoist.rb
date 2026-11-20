# frozen_string_literal: true

# E16b lab scaffolding (never leaves the lab branch): hoist the Metro bundle out
# of the app target's serial tail, with a start time the lane controls.
#
# E16 put the phase on an early pod target; the pod target's compiles ended at
# 45.9 s but xcodebuild did not start the phase until 724.5 s (script phases are
# batched), so the hit came from scheduler luck rather than design. This version
# takes the scheduler out of it: the qualification tool runs the same script
# itself, at the start of the build, and the app target keeps the wait-then-copy
# phase E16 measured at 0.262 s.
#
# This script does two things and verifies both:
#   1. extract the app target's Metro phase script into --emit, wrapped with the
#      begin/done markers and a completion marker the app phase waits for;
#   2. rewrite that phase into the wait-then-copy step.
# The environment for the emitted script is supplied by the caller from the app
# target's own resolved build settings, so the script text sees the same values
# Xcode gave it.

require "xcodeproj"
require "optparse"
require "fileutils"

options = {}
OptionParser.new do |parser|
  parser.on("--app-project PATH") { |value| options[:app_project] = value }
  parser.on("--host-target NAME") { |value| options[:host_target] = value }
  parser.on("--staging PATH") { |value| options[:staging] = value }
  parser.on("--emit PATH") { |value| options[:emit] = value }
end.parse!

%i[app_project host_target staging emit].each do |key|
  abort("metro_hoist: missing --#{key.to_s.tr('_', '-')}") unless options[key]
end

STAGING = options[:staging].freeze
EMIT = options[:emit].freeze
abort("metro_hoist: --staging must be absolute") unless STAGING.start_with?("/")
abort("metro_hoist: --emit must be absolute") unless EMIT.start_with?("/")
abort("metro_hoist: #{options[:app_project]} does not exist") unless File.exist?(options[:app_project])

BUNDLE_SCRIPT_MARKER = "react-native-xcode.sh"
COPY_PHASE_MARKER = "E16 metro-copy"
HOIST_PHASE_MARKER = "E16 metro-hoist"

app_project = Xcodeproj::Project.open(options[:app_project])
host = app_project.targets.find { |target| target.name == options[:host_target] }
abort("metro_hoist: no target #{options[:host_target]} in #{options[:app_project]}") unless host

bundle_phases = host.shell_script_build_phases.select do |phase|
  phase.shell_script.to_s.include?(BUNDLE_SCRIPT_MARKER)
end
if bundle_phases.length != 1
  names = host.shell_script_build_phases.map { |phase| phase.display_name }
  abort("metro_hoist: expected exactly one #{BUNDLE_SCRIPT_MARKER} phase on " \
        "#{host.name}, found #{bundle_phases.length}; phases: #{names.inspect}")
end

bundle_phase = bundle_phases.first
original_script = bundle_phase.shell_script.to_s

hoist_script = <<~SHELL
  #!/bin/bash
  # #{HOIST_PHASE_MARKER}: the app target's Metro phase, run by the
  # qualification tool at the start of the build instead of inside the app
  # target's serial tail. The environment is the app target's own build
  # settings, and this script's outputs are copied into the app by the phase
  # rewritten below.
  set -e
  echo "#{HOIST_PHASE_MARKER}: begin $(date -u +%H:%M:%S)"
  #{original_script}
  touch "#{STAGING}/.complete"
  echo "#{HOIST_PHASE_MARKER}: done $(date -u +%H:%M:%S)"
SHELL

copy_script = <<~SHELL
  # #{COPY_PHASE_MARKER}: the bundle was produced before the build began; this
  # phase waits for its completion marker and copies it into place.
  set -e
  STAGING="#{STAGING}"
  DEST="$CONFIGURATION_BUILD_DIR/$UNLOCALIZED_RESOURCES_FOLDER_PATH"
  echo "#{COPY_PHASE_MARKER}: waiting for $STAGING/.complete"
  waited=0
  while [[ ! -f "$STAGING/.complete" ]]; do
    sleep 1
    waited=$((waited + 1))
    if [[ $waited -gt 900 ]]; then
      echo "#{COPY_PHASE_MARKER}: hoisted bundle never completed at $STAGING" >&2
      exit 2
    fi
  done
  mkdir -p "$DEST"
  rsync -a --exclude .complete "$STAGING/" "$DEST/"
  echo "#{COPY_PHASE_MARKER}: copied $(ls -1 "$DEST" | wc -l | tr -d ' ') entries"
SHELL

FileUtils.mkdir_p(File.dirname(EMIT))
abort("metro_hoist: #{EMIT} already exists") if File.exist?(EMIT)
File.write(EMIT, hoist_script)
FileUtils.chmod(0o755, EMIT)

bundle_phase.shell_script = copy_script
app_project.save

# Reopen: the patch is only real if the saved project carries it. The emitted
# script must carry the original text, or the copy phase would move an empty
# directory while the build looked green.
reopened = Xcodeproj::Project.open(options[:app_project])
reopened_host = reopened.targets.find { |target| target.name == options[:host_target] }
unless reopened_host.shell_script_build_phases.any? { |phase| phase.shell_script.to_s.include?(COPY_PHASE_MARKER) }
  abort("metro_hoist: verification failed, #{options[:app_project]} has no copy phase")
end
if reopened_host.shell_script_build_phases.any? { |phase| phase.shell_script.to_s.include?(BUNDLE_SCRIPT_MARKER) }
  abort("metro_hoist: verification failed, bundle script still referenced in #{options[:app_project]}")
end
written = File.read(EMIT)
unless written.include?(BUNDLE_SCRIPT_MARKER) && written.include?(HOIST_PHASE_MARKER) && written.include?("#{STAGING}/.complete")
  abort("metro_hoist: verification failed, #{EMIT} does not carry the hoist script")
end

puts "E16 metro_hoist: #{options[:app_project]} #{host.name} bundle phase -> copy"
puts "E16 metro_hoist: emitted #{EMIT} (#{written.bytesize} bytes)"
puts "E16 metro_hoist: staging #{STAGING}"
