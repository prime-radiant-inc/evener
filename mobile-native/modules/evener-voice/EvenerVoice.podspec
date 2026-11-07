require 'json'

Pod::Spec.new do |s|
  s.name = 'EvenerVoice'
  s.version = '0.1.0'
  s.summary = 'App-lifetime scoped voice audio for Evener'
  s.description = s.summary
  s.license = { :type => 'Proprietary' }
  s.author = 'Prime Radiant'
  s.homepage = 'https://github.com/prime-radiant-inc/evener'
  s.source = { :git => s.homepage }
  s.platforms = { :ios => '16.4' }
  s.swift_version = '5.9'
  s.static_framework = true
  s.dependency 'ExpoModulesCore'
  s.dependency 'onnxruntime-objc', '1.20.0'
  s.source_files = 'ios/**/*.{h,m,mm,swift}', 'core/**/*.swift'
  # postinstall generates this manifest. Keep cached preview models and the
  # training export out of the app, without deleting either from the checkout.
  wake = JSON.parse(File.read(File.join(__dir__, 'resources/wake-models.json')))
  models = %w[melspectrogram embedding classifier].map { |key| "resources/#{wake.fetch(key).fetch('file')}" }
  notices = ['resources/*-LICENSE', 'resources/*-NOTICE', 'resources/wake-word/*.txt', 'resources/wake-word/NOTICE.md']
  s.resource_bundles = { 'EvenerVoiceModels' => ['resources/silero_vad.onnx', 'resources/wake-models.json'] + models + notices }
  s.pod_target_xcconfig = { 'DEFINES_MODULE' => 'YES' }
end
