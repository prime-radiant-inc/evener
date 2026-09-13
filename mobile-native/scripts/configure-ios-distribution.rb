require "xcodeproj"
require "fileutils"

project_path = ARGV.fetch(0)
export_options_path = ARGV.fetch(1)
team_id = ENV.fetch("APPLE_TEAM_ID")
profile_name = ENV.fetch("IOS_PROVISIONING_PROFILE_NAME")
bundle_identifier = ENV.fetch("IOS_BUNDLE_IDENTIFIER")
project = Xcodeproj::Project.open(project_path)
target = project.targets.find { |candidate| candidate.name == "Evener" }
abort "Evener target not found" unless target

configuration = target.build_configurations.find { |candidate| candidate.name == "Release" }
abort "Evener Release configuration not found" unless configuration

configuration.build_settings["DEVELOPMENT_TEAM"] = team_id
configuration.build_settings["CODE_SIGN_STYLE"] = "Manual"
configuration.build_settings["CODE_SIGN_IDENTITY"] = "Apple Distribution"
configuration.build_settings["PROVISIONING_PROFILE_SPECIFIER"] = profile_name
configuration.build_settings["TARGETED_DEVICE_FAMILY"] = "1"
project.save

FileUtils.mkdir_p(File.dirname(export_options_path))
Xcodeproj::Plist.write_to_path({
  "method" => "app-store-connect",
  "manageAppVersionAndBuildNumber" => false,
  "signingStyle" => "manual",
  "teamID" => team_id,
  "provisioningProfiles" => { bundle_identifier => profile_name },
}, export_options_path)
