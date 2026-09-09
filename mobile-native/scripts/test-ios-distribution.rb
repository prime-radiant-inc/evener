#!/usr/bin/env ruby

require "base64"
require "fileutils"
require "json"
require "net/http"
require "openssl"
require "tmpdir"
require "zip"

require "fastlane"
require "fastlane_core/ipa_file_analyser"
require "xcodeproj"
require_relative "../fastlane/testflight_readiness"

ROOT = File.expand_path("..", __dir__)
Fastlane.load_actions
Net::HTTP.prepend(Module.new do
  def connect
    raise "Network access is disabled in distribution checks"
  end
end)

def assert(condition, message)
  raise message unless condition
end

def expect_failure(message)
  failed = false
  begin
    yield
  rescue FastlaneCore::Interface::FastlaneError
    failed = true
  end
  assert(failed, "expected failure: #{message}")
end

def write_ipa(path, identifier:, marketing_version:, build_number:, device_family: [1])
  Dir.mktmpdir("evener-ipa") do |dir|
    app = File.join(dir, "Payload", "Evener.app")
    FileUtils.mkdir_p(app)
    File.write(File.join(app, "Info.plist"), <<~PLIST)
      <?xml version="1.0" encoding="UTF-8"?>
      <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
      <plist version="1.0"><dict>
      <key>CFBundleIdentifier</key><string>#{identifier}</string>
      <key>CFBundleShortVersionString</key><string>#{marketing_version}</string>
      <key>CFBundleVersion</key><string>#{build_number}</string>
      <key>UIDeviceFamily</key><array>#{device_family.map { |value| "<integer>#{value}</integer>" }.join}</array>
      <key>DTPlatformName</key><string>iphoneos</string>
      </dict></plist>
    PLIST
    Zip::File.open(path, create: true) { |zip| zip.add("Payload/Evener.app/Info.plist", File.join(app, "Info.plist")) }
  end
end

def test_ipa_analyser(tmpdir)
  path = File.join(tmpdir, "good.ipa")
  write_ipa(path, identifier: "com.primeradiant.evener.native", marketing_version: "0.1.0", build_number: "7")
  info = FastlaneCore::IpaFileAnalyser.fetch_info_plist_file(path)
  assert(info["CFBundleIdentifier"] == "com.primeradiant.evener.native", "IPA analyser lost bundle ID")
  assert(info["CFBundleShortVersionString"] == "0.1.0", "IPA analyser lost marketing version")
  assert(info["CFBundleVersion"] == "7", "IPA analyser lost build number")
  assert(info["UIDeviceFamily"] == [1], "IPA analyser lost iPhone family")
end

def test_configure_helper(tmpdir)
  project_path = File.join(tmpdir, "Evener.xcodeproj")
  project = Xcodeproj::Project.new(project_path)
  app = project.new_target(:application, "Evener", :ios, "16.0")
  library = project.new_target(:framework, "Unrelated", :ios, "16.0")
  project.save
  project = Xcodeproj::Project.open(project_path)
  project.targets.each do |target|
    target.add_build_configuration("Release", :release)
    target.add_build_configuration("Debug", :debug)
  end
  project.save
  before = Xcodeproj::Project.open(project_path)
  debug_before = before.targets.find { |target| target.name == "Evener" }.build_configurations.find { |config| config.name == "Debug" }.build_settings.dup
  unrelated_before = before.targets.find { |target| target.name == "Unrelated" }.build_configurations.map { |config| [config.name, config.build_settings.dup] }
  export_options = File.join(tmpdir, "build", "ExportOptions.plist")
  profile_name = "Evener & <Distribution>"
  env = { "APPLE_TEAM_ID" => "TEAM123", "IOS_PROVISIONING_PROFILE_NAME" => profile_name, "IOS_BUNDLE_IDENTIFIER" => "com.primeradiant.evener.native" }
  helper = File.join(ROOT, "scripts", "configure-ios-distribution.rb")
  system(env, RbConfig.ruby, helper, project_path, export_options) || raise("distribution helper failed")
  after = Xcodeproj::Project.open(project_path)
  evener_release = after.targets.find { |target| target.name == "Evener" }.build_configurations.find { |config| config.name == "Release" }
  assert(evener_release.build_settings["DEVELOPMENT_TEAM"] == "TEAM123", "helper did not configure app Release")
  assert(evener_release.build_settings["TARGETED_DEVICE_FAMILY"] == "1", "helper did not force iPhone family")
  assert(evener_release.build_settings["CODE_SIGN_STYLE"] == "Manual", "helper did not select manual signing")
  assert(evener_release.build_settings["CODE_SIGN_IDENTITY"] == "Apple Distribution", "helper did not select distribution identity")
  assert(evener_release.build_settings["PROVISIONING_PROFILE_SPECIFIER"] == profile_name, "helper did not select app profile")
  export = Xcodeproj::Plist.read_from_path(export_options)
  assert(export["method"] == "app-store-connect", "helper did not select App Store Connect export")
  assert(export["manageAppVersionAndBuildNumber"] == false, "helper enabled App Store version management")
  assert(export["teamID"] == "TEAM123", "helper did not serialize team")
  assert(export["provisioningProfiles"] == { "com.primeradiant.evener.native" => profile_name }, "helper did not preserve XML-sensitive profile name")
  debug_after = after.targets.find { |target| target.name == "Evener" }.build_configurations.find { |config| config.name == "Debug" }.build_settings
  assert(debug_before == debug_after, "helper changed app Debug")
  unrelated_after = after.targets.find { |target| target.name == "Unrelated" }.build_configurations.map { |config| [config.name, config.build_settings] }
  assert(unrelated_before == unrelated_after, "helper changed an unrelated target")
end

def test_readiness_waiter
  detail = Struct.new(:internal_build_state)
  build = Struct.new(:id, :processing_state, :build_beta_detail).new("build-7", "VALID", detail.new("READY_FOR_BETA_TESTING"))
  now = 0.0
  observations = [[[build], false], [[build], false], [[build], true]]
  result = TestflightReadiness.wait(timeout: 5, interval: 1, clock: -> { now }, sleeper: ->(seconds) { now += seconds }) { observations.shift }
  assert(result == build && now == 2.0, "readiness waiter did not wait for eventual group membership")

  now = 0.0
  timeout = false
  begin
    TestflightReadiness.wait(timeout: 2, interval: 1, clock: -> { now }, sleeper: ->(seconds) { now += seconds }) { [[build], false] }
  rescue TestflightReadiness::TimeoutError
    timeout = true
  end
  assert(timeout, "readiness waiter did not enforce its monotonic deadline")

  failed = false
  now = 0.0
  build.build_beta_detail.internal_build_state = "PROCESSING_EXCEPTION"
  begin
    TestflightReadiness.wait(timeout: 5, interval: 1, clock: -> { now }, sleeper: ->(seconds) { now += seconds }) { [[build], false] }
  rescue TestflightReadiness::TerminalError
    failed = true
  end
  assert(failed && now == 0.0, "readiness waiter did not immediately reject terminal internal state")
end

def with_test_clock
  original_clock = Process.method(:clock_gettime)
  original_sleep = Kernel.instance_method(:sleep)
  now = 0.0
  sleeps = []
  Process.define_singleton_method(:clock_gettime) do |clock_id, *args|
    clock_id == Process::CLOCK_MONOTONIC ? now : original_clock.call(clock_id, *args)
  end
  Kernel.define_method(:sleep) do |seconds|
    sleeps << seconds
    now += seconds
    seconds.to_i
  end
  Kernel.send(:private, :sleep)
  yield sleeps
ensure
  Process.define_singleton_method(:clock_gettime, original_clock)
  Kernel.define_method(:sleep, original_sleep)
  Kernel.send(:private, :sleep)
end

# App Store Connect and upload are the external boundary; the actual Fastfile,
# API-key action, IPA reader and lane runner execute below it.
class DistributionStore
  class << self
    attr_accessor :app, :group, :builds, :uploads, :assign_build, :internal_build_state, :queries, :build_sequence, :group_assignment_sequence, :group_fetches
  end

  App = Struct.new(:id)
  BuildBetaDetail = Struct.new(:internal_build_state)
  Build = Struct.new(:id, :processing_state, :build_beta_detail) do
    def ready_for_internal_testing?
      build_beta_detail && build_beta_detail.internal_build_state == "READY_FOR_BETA_TESTING"
    end
  end
  Group = Struct.new(:id, :name, :is_internal_group, :has_access_to_all_builds, :builds) do
    def fetch_builds
      DistributionStore.group_fetches += 1
      sequence = DistributionStore.group_assignment_sequence
      sequence && !sequence.empty? ? (sequence.shift ? builds : []) : builds
    end
  end
  Page = Struct.new(:to_models)
  Response = Struct.new(:all_pages)

  def self.reset
    self.app = App.new("app-1")
    self.group = Group.new("group-1", "Internal", true, true, [])
    self.builds = []
    self.uploads = []
    self.queries = []
    self.assign_build = true
    self.internal_build_state = "READY_FOR_BETA_TESTING"
    self.build_sequence = nil
    self.group_assignment_sequence = nil
    self.group_fetches = 0
    Spaceship::ConnectAPI.token = nil
  end

  def self.require_auth
    raise "App Store Connect used before API authentication" unless Spaceship::ConnectAPI.token
  end
end

Spaceship::ConnectAPI::App.define_singleton_method(:find) do |identifier|
  DistributionStore.require_auth
  raise "wrong requested app" unless identifier == "com.primeradiant.evener.native"
  DistributionStore.app
end
Spaceship::ConnectAPI.define_singleton_method(:get_beta_groups) do |filter:, limit:|
  DistributionStore.require_auth
  raise "wrong group app filter" unless filter == { app: "app-1" }
  DistributionStore::Response.new([DistributionStore::Page.new([DistributionStore.group].compact)])
end
Spaceship::ConnectAPI::Build.define_singleton_method(:all) do |**query|
  DistributionStore.require_auth
  expected = { app_id: "app-1", version: "0.1.0", build_number: "7", platform: "IOS" }
  raise "build lookup is not scoped to exact app/version/build/platform" unless query == expected
  DistributionStore.queries << query
  sequence = DistributionStore.build_sequence
  sequence && !sequence.empty? ? [sequence.shift] : DistributionStore.builds
end
Fastlane::Actions::UploadToTestflightAction.define_singleton_method(:run) do |config|
  DistributionStore.require_auth
  raise "upload did not use API key" unless config[:api_key]
  raise "upload attempted to assign an internal group" unless config[:groups].nil?
  raise "upload skipped processing" unless config[:skip_waiting_for_build_processing] == false
  raise "external distribution requested" unless config[:distribute_external] == false && config[:submit_beta_review] == false
  raise "external tester notification requested" unless config[:notify_external_testers] == false
  DistributionStore.uploads << config[:ipa]
  build = DistributionStore::Build.new("build-7", "VALID", DistributionStore::BuildBetaDetail.new(DistributionStore.internal_build_state))
  DistributionStore.builds = [build]
  DistributionStore.group.builds = [build] if DistributionStore.assign_build
end

def test_lanes(tmpdir)
  ENV["APP_STORE_CONNECT_API_KEY_ID"] = "FIXTUREKEY"
  ENV["APP_STORE_CONNECT_API_ISSUER_ID"] = "00000000-0000-0000-0000-000000000001"
  ENV["APP_STORE_CONNECT_API_KEY_CONTENT"] = Base64.strict_encode64(OpenSSL::PKey::EC.generate("prime256v1").to_pem)
  ENV["IOS_BUNDLE_IDENTIFIER"] = "com.primeradiant.evener.native"
  ENV["IOS_MARKETING_VERSION"] = "0.1.0"
  ENV["IOS_BUILD_NUMBER"] = "7"
  ENV["IOS_INTERNAL_TESTFLIGHT_GROUP"] = "Internal"
  ENV["IOS_RECEIPT_PATH"] = File.join(tmpdir, "receipt.json")
  ENV["IOS_IPA_PATH"] = File.join(tmpdir, "good.ipa")
  ENV["IOS_BUILD_READINESS_TIMEOUT_SECONDS"] = "0"
  ENV["IOS_BUILD_READINESS_POLL_INTERVAL_SECONDS"] = "0"
  lane = Fastlane::FastFile.new(File.join(ROOT, "fastlane", "Fastfile"))
  DistributionStore.reset
  lane.runner.execute(:preflight, :ios)
  assert(DistributionStore.queries.length == 1, "preflight omitted duplicate lookup")
  DistributionStore.builds = [DistributionStore::Build.new("existing", "VALID", DistributionStore::BuildBetaDetail.new("READY_FOR_BETA_TESTING"))]
  expect_failure("duplicate build") { lane.runner.execute(:preflight, :ios) }
  DistributionStore.reset
  DistributionStore.app = nil
  expect_failure("missing app") { lane.runner.execute(:preflight, :ios) }
  DistributionStore.reset
  DistributionStore.group = nil
  expect_failure("missing group") { lane.runner.execute(:preflight, :ios) }
  DistributionStore.reset
  DistributionStore.group.has_access_to_all_builds = false
  expect_failure("internal group without automatic build access") { lane.runner.execute(:testflight, :ios) }
  assert(DistributionStore.uploads.empty?, "uploaded without automatic internal distribution")
  DistributionStore.reset
  DistributionStore.group.is_internal_group = false
  expect_failure("external group") { lane.runner.execute(:testflight, :ios) }
  assert(DistributionStore.uploads.empty?, "uploaded before checking group type")

  [[:identifier, "another.app"], [:marketing_version, "0.2.0"], [:build_number, "8"], [:device_family, [1, 2]]].each_with_index do |(key, value), index|
    DistributionStore.reset
    path = File.join(tmpdir, "wrong-#{index}.ipa")
    input = { identifier: "com.primeradiant.evener.native", marketing_version: "0.1.0", build_number: "7" }
    write_ipa(path, **input.merge(key => value))
    ENV["IOS_IPA_PATH"] = path
    expect_failure("IPA #{key}") { lane.runner.execute(:testflight, :ios) }
    assert(DistributionStore.uploads.empty?, "uploaded a mismatched IPA")
  end

  ENV["IOS_IPA_PATH"] = File.join(tmpdir, "good.ipa")
  DistributionStore.reset
  DistributionStore.assign_build = false
  expect_failure("missing exact group membership") { lane.runner.execute(:testflight, :ios) }
  assert(!File.exist?(ENV["IOS_RECEIPT_PATH"]), "receipt written without membership")
  DistributionStore.reset
  processing = DistributionStore::Build.new("build-7", "PROCESSING", DistributionStore::BuildBetaDetail.new("PROCESSING"))
  ready = DistributionStore::Build.new("build-7", "VALID", DistributionStore::BuildBetaDetail.new("READY_FOR_BETA_TESTING"))
  ENV["IOS_BUILD_READINESS_TIMEOUT_SECONDS"] = "5"
  ENV["IOS_BUILD_READINESS_POLL_INTERVAL_SECONDS"] = "1"
  DistributionStore.build_sequence = [processing, ready, ready]
  DistributionStore.group_assignment_sequence = [false, false, true]
  with_test_clock do |sleeps|
    lane.runner.execute(:testflight, :ios)
    assert(sleeps == [1.0, 1.0], "readiness did not wait between fresh availability observations")
  end
  assert(DistributionStore.uploads.length == 1, "eventual assignment uploaded more than once")
  assert(DistributionStore.queries.length == 3 && DistributionStore.group_fetches == 3, "readiness did not repeat exact build and group reads")
  ENV["IOS_BUILD_READINESS_TIMEOUT_SECONDS"] = "0"
  FileUtils.rm_f(ENV["IOS_RECEIPT_PATH"])
  ["PROCESSING", "PROCESSING_EXCEPTION", "EXPIRED"].each do |state|
    DistributionStore.reset
    DistributionStore.internal_build_state = state
    expect_failure("unavailable internal state #{state}") { lane.runner.execute(:testflight, :ios) }
    assert(!File.exist?(ENV["IOS_RECEIPT_PATH"]), "receipt written for unavailable internal state #{state}")
  end
  DistributionStore.reset
  DistributionStore.internal_build_state = "IN_BETA_TESTING"
  lane.runner.execute(:testflight, :ios)
  receipt = JSON.parse(File.read(ENV["IOS_RECEIPT_PATH"]))
  assert(DistributionStore.uploads == [ENV["IOS_IPA_PATH"]], "upload did not use exact IPA once")
  assert(receipt["build_id"] == "build-7" && receipt["internal_group_id"] == "group-1", "receipt has wrong build/group")
  assert(receipt["internal_build_state"] == "IN_BETA_TESTING", "receipt omitted the ready internal build state")
  assert(receipt["ipa_sha256"] == Digest::SHA256.file(ENV["IOS_IPA_PATH"]).hexdigest, "receipt has wrong IPA hash")

  DistributionStore.reset
  FileUtils.rm_f(ENV["IOS_RECEIPT_PATH"])
  wrong_path = File.join(tmpdir, "wrong-verify.ipa")
  write_ipa(wrong_path, identifier: "wrong.verify", marketing_version: "0.1.0", build_number: "7")
  ENV["IOS_IPA_PATH"] = wrong_path
  expect_failure("verify IPA identity") { lane.runner.execute(:verify_testflight, :ios) }
  assert(!File.exist?(ENV["IOS_RECEIPT_PATH"]), "verify wrote a receipt for mismatched IPA")
  ENV["IOS_IPA_PATH"] = File.join(tmpdir, "good.ipa")
  expect_failure("verify missing exact build") { lane.runner.execute(:verify_testflight, :ios) }
  assert(!File.exist?(ENV["IOS_RECEIPT_PATH"]), "verify wrote a receipt without an exact build")
  existing = DistributionStore::Build.new("build-7", "VALID", DistributionStore::BuildBetaDetail.new("IN_BETA_TESTING"))
  DistributionStore.builds = [existing]
  expect_failure("verify missing group membership") { lane.runner.execute(:verify_testflight, :ios) }
  assert(!File.exist?(ENV["IOS_RECEIPT_PATH"]), "verify wrote a receipt without exact group membership")
  DistributionStore.group.builds = [existing]
  lane.runner.execute(:verify_testflight, :ios)
  assert(DistributionStore.uploads.empty?, "verify-only lane uploaded an IPA")
  assert(JSON.parse(File.read(ENV["IOS_RECEIPT_PATH"])) == receipt, "verify-only lane did not write the exact ready build receipt")
end

Dir.mktmpdir("evener-distribution-test") do |tmpdir|
  test_ipa_analyser(tmpdir)
  test_configure_helper(tmpdir)
  test_readiness_waiter
  test_lanes(tmpdir)
  puts "iOS distribution behavior checks passed"
end
