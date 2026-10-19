require "xcodeproj"
require "fileutils"

project_path = ARGV.fetch(0)
export_options_path = ARGV.fetch(1)
team_id = ENV.fetch("APPLE_TEAM_ID")
profile_name = ENV.fetch("IOS_PROVISIONING_PROFILE_NAME")
share_profile_name = ENV["IOS_SHARE_PROVISIONING_PROFILE_NAME"]
bundle_identifier = ENV.fetch("IOS_BUNDLE_IDENTIFIER")
project = Xcodeproj::Project.open(project_path)

def sign(project, name, team_id:, profile_name:)
  target = project.targets.find { |candidate| candidate.name == name }
  return nil unless target

  configuration = target.build_configurations.find { |candidate| candidate.name == "Release" }
  abort "#{name} Release configuration not found" unless configuration

  configuration.build_settings["DEVELOPMENT_TEAM"] = team_id
  configuration.build_settings["CODE_SIGN_STYLE"] = "Manual"
  configuration.build_settings["CODE_SIGN_IDENTITY"] = "Apple Distribution"
  configuration.build_settings["PROVISIONING_PROFILE_SPECIFIER"] = profile_name
  # The product is iPhone-only, both the app and its extension.
  configuration.build_settings["TARGETED_DEVICE_FAMILY"] = "1"
  target
end

abort "Evener target not found" unless sign(project, "Evener", team_id: team_id, profile_name: profile_name)

profiles = { bundle_identifier => profile_name }
# The share extension ships inside the app and is signed as its own bundle, so
# it needs its own profile: signing it with the app's profile fails export.
# Prebuild generates it from targets/share; a project without it (an older
# prebuild, or a simulator-only run) simply has nothing to sign here.
share_bundle_identifier = "#{bundle_identifier}.ShareExtension"
if project.targets.any? { |candidate| candidate.name == "EvenerShareExtension" }
  abort "IOS_SHARE_PROVISIONING_PROFILE_NAME is required: the app contains EvenerShareExtension, whose bundle id needs its own profile." unless share_profile_name
  sign(project, "EvenerShareExtension", team_id: team_id, profile_name: share_profile_name)
  profiles[share_bundle_identifier] = share_profile_name
end
project.save

FileUtils.mkdir_p(File.dirname(export_options_path))
Xcodeproj::Plist.write_to_path({
  "method" => "app-store-connect",
  "manageAppVersionAndBuildNumber" => false,
  "signingStyle" => "manual",
  "teamID" => team_id,
  "provisioningProfiles" => profiles,
}, export_options_path)
