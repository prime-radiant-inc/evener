# frozen_string_literal: true

# E16 lab scaffolding (never leaves the lab branch): hoist the Metro bundle out
# of the app target's serial tail.
#
# The app target's "Bundle React Native code and images" phase (42.56 s in the
# E15 timeline, 716.2 s -> 758.8 s of a 777.9 s build) runs only after every pod
# target has finished, so its whole cost lands in the serial tail. This patch
# moves the identical script text onto a pod target that finishes early
# (ZXingObjC, 343.2 s in the same timeline) and rewrites the app phase into a
# wait-then-copy step. The bundle therefore runs while the pod compile wave is
# still building, and the app target's own copy costs milliseconds.
#
# Equivalence by construction: the hoisted phase runs the *same* phase script
# text with the same Xcode-supplied environment, except that SRCROOT is pinned
# to the app's ios directory (a pod target's SRCROOT is ios/Pods) and the
# destination is a staging directory instead of the built .app. The app phase's
# copy is byte-preserving (rsync -a), so the bundle the app ships is the bundle
# this script produced.
#
# The patch verifies itself by reopening both projects and checking the phase
# text, and it fails loudly when it cannot find the app bundle phase or the
# carrier target: a silent no-op would measure nothing.

require "xcodeproj"
require "optparse"

options = {}
OptionParser.new do |parser|
  parser.on("--app-project PATH") { |value| options[:app_project] = value }
  parser.on("--pods-project PATH") { |value| options[:pods_project] = value }
  parser.on("--host-target NAME") { |value| options[:host_target] = value }
  parser.on("--carrier-target NAME") { |value| options[:carrier_target] = value }
  parser.on("--staging PATH") { |value| options[:staging] = value }
end.parse!

%i[app_project pods_project host_target carrier_target staging].each do |key|
  abort("metro_hoist: missing --#{key.to_s.tr('_', '-')}") unless options[key]
end

STAGING = options[:staging].freeze
abort("metro_hoist: --staging must be absolute") unless STAGING.start_with?("/")
abort("metro_hoist: #{options[:app_project]} does not exist") unless File.exist?(options[:app_project])
abort("metro_hoist: #{options[:pods_project]} does not exist") unless File.exist?(options[:pods_project])

BUNDLE_SCRIPT_MARKER = "react-native-xcode.sh"
HOIST_PHASE_NAME = "[E16] Metro hoist"
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
ios_dir = File.expand_path(File.dirname(options[:app_project]))
abort("metro_hoist: #{ios_dir} does not exist") unless File.directory?(ios_dir)

hoist_script = <<~SHELL
  # #{HOIST_PHASE_MARKER} (E16 lab): the app target's Metro phase, run on a pod
  # target that finishes early so the bundle overlaps the compile wave.
  set -e
  export SRCROOT="#{ios_dir}"
  export PROJECT_DIR="$SRCROOT"
  export TARGET_NAME="#{options[:host_target]}"
  export PRODUCT_NAME="#{options[:host_target]}"
  export CONFIGURATION_BUILD_DIR="#{STAGING}"
  export UNLOCALIZED_RESOURCES_FOLDER_PATH=""
  echo "#{HOIST_PHASE_MARKER}: begin $(date -u +%H:%M:%S)"
  rm -rf "#{STAGING}"
  mkdir -p "#{STAGING}"
  #{original_script}
  touch "#{STAGING}/.complete"
  echo "#{HOIST_PHASE_MARKER}: done $(date -u +%H:%M:%S)"
SHELL

copy_script = <<~SHELL
  # #{COPY_PHASE_MARKER} (E16 lab): the bundle was produced during the compile
  # wave; this phase waits for its completion marker and copies it into place.
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

pods_project = Xcodeproj::Project.open(options[:pods_project])
carrier = pods_project.targets.find { |target| target.name == options[:carrier_target] }
if carrier.nil?
  available = pods_project.targets.map(&:name).sort
  abort("metro_hoist: no carrier target #{options[:carrier_target]} in " \
        "#{options[:pods_project]}; targets: #{available.inspect}")
end
if carrier.shell_script_build_phases.any? { |phase| phase.name.to_s.include?(HOIST_PHASE_MARKER) }
  abort("metro_hoist: #{carrier.name} already carries #{HOIST_PHASE_NAME}")
end

hoist_phase = carrier.new_shell_script_build_phase(HOIST_PHASE_NAME)
hoist_phase.shell_script = hoist_script
bundle_phase.shell_script = copy_script
pods_project.save
app_project.save

# Reopen both projects: the patch is only real if the saved files carry it.
reopened_app = Xcodeproj::Project.open(options[:app_project])
reopened_host = reopened_app.targets.find { |target| target.name == options[:host_target] }
reopened_phase = reopened_host.shell_script_build_phases.find do |phase|
  phase.shell_script.to_s.include?(COPY_PHASE_MARKER)
end
abort("metro_hoist: verification failed, #{options[:app_project]} has no copy phase") unless reopened_phase
if reopened_host.shell_script_build_phases.any? { |phase| phase.shell_script.to_s.include?(BUNDLE_SCRIPT_MARKER) }
  abort("metro_hoist: verification failed, bundle script still referenced in #{options[:app_project]}")
end

reopened_pods = Xcodeproj::Project.open(options[:pods_project])
reopened_carrier = reopened_pods.targets.find { |target| target.name == options[:carrier_target] }
hoisted = reopened_carrier.shell_script_build_phases.select do |phase|
  phase.shell_script.to_s.include?(HOIST_PHASE_MARKER)
end
if hoisted.length != 1
  abort("metro_hoist: verification failed, #{options[:carrier_target]} carries #{hoisted.length} hoist phases")
end

puts "E16 metro_hoist: patched #{options[:app_project]} (#{host.name} bundle phase -> copy) " \
     "and #{options[:pods_project]} (#{carrier.name} -> #{HOIST_PHASE_NAME})"
puts "E16 metro_hoist: staging #{STAGING}"
puts "E16 metro_hoist: hoisted script bytes #{original_script.bytesize}"
