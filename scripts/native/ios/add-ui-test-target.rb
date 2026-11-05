#!/usr/bin/env ruby
# Adds the generated-only EvenerQualificationUITests target to a prebuilt
# Evener project: one UI-test bundle whose only source is the repository's
# EvenerQualificationUITests.swift, plus a shared-scheme test action so the
# Evener scheme runs the qualification journeys under `xcodebuild test`.
# Idempotent: a project that already carries the target and the scheme entry
# is left untouched. The generated project is disposable output; the source
# itself lives in the repository, outside ignored directories.
#
# Run from mobile-native after prebuild:
#   bundle exec ruby ../scripts/native/ios/add-ui-test-target.rb ios/Evener.xcodeproj

require "xcodeproj"

project_path = ARGV[0]
if project_path.nil? || !File.directory?(project_path)
	abort "usage: add-ui-test-target.rb PATH_TO_GENERATED_XCODEPROJ"
end

source_path = File.expand_path("EvenerQualificationUITests.swift", __dir__)
unless File.file?(source_path)
	abort "missing UI test source at #{source_path}"
end

target_name = "EvenerQualificationUITests"
project = Xcodeproj::Project.open(project_path)

if project.targets.any? { |existing| existing.name == target_name }
	puts "#{target_name} target already present"
else
	target = project.new_target(:ui_test_bundle, target_name, :ios, "Release")
	target.build_configurations.each do |config|
		config.build_settings["PRODUCT_BUNDLE_IDENTIFIER"] = "com.primeradiant.evener.native.QualificationUITests"
		# The UI-test bundle drives the app under test by name; the run's
		# xctestrun carries the built app the build runner already produced.
		config.build_settings["TEST_TARGET_NAME"] = "Evener"
	end
	source_ref = Pathname.new(source_path).relative_path_from(
		Pathname.new(File.expand_path(project_path)).parent,
	).to_s
	file = project.main_group.new_file(source_ref)
	target.source_build_phase.add_file_reference(file)
	project.save
	puts "added #{target_name} target with #{source_ref}"
end

scheme_path = File.join(project_path, "xcshareddata", "xcschemes", "Evener.xcscheme")
unless File.file?(scheme_path)
	abort "no shared Evener scheme at #{scheme_path}; prebuild must generate it first"
end
scheme = Xcodeproj::XCScheme.new(scheme_path)
listed = scheme.test_action.testables.any? do |testable|
	testable.buildable_references.any? { |reference| reference.buildable_name == "#{target_name}.xctest" }
end
if listed
	puts "scheme already lists #{target_name}"
else
	scheme.add_test_target(project.targets.find { |existing| existing.name == target_name })
	scheme.save!
	puts "scheme now lists #{target_name}"
end
