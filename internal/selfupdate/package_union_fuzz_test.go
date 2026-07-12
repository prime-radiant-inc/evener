//go:build serffuzz

package selfupdate

import "testing"

// FuzzPackageUnion replays every deterministic package scenario under fuzz coverage.

func FuzzPackageUnion(f *testing.F) {

	f.Add(uint8(0))

	f.Fuzz(func(t *testing.T, _ uint8) {

		t.Run("TestResolveTargetTracksCurrentChannel", TestResolveTargetTracksCurrentChannel)
		t.Run("TestUpgradeInstallsReleaseArchive", TestUpgradeInstallsReleaseArchive)
		t.Run("TestUpgradeReleaseChannelUsesLatestDownloadURL", TestUpgradeReleaseChannelUsesLatestDownloadURL)
		t.Run("TestUpgradeRejectsUnsupportedPlatform", TestUpgradeRejectsUnsupportedPlatform)
		t.Run("TestResolveTargetUnknownTarget", TestResolveTargetUnknownTarget)
		t.Run("TestReleaseAssetPlatforms", TestReleaseAssetPlatforms)
		t.Run("TestInstallPrefix", TestInstallPrefix)
		t.Run("TestFirstNonEmpty", TestFirstNonEmpty)
		t.Run("TestDownloadErrors", TestDownloadErrors)
		t.Run("TestDownloadSucceeds", TestDownloadSucceeds)
		t.Run("TestExtractReleaseArchiveMissingBinary", TestExtractReleaseArchiveMissingBinary)
		t.Run("TestExtractReleaseArchiveRejectsNonRegularFile", TestExtractReleaseArchiveRejectsNonRegularFile)
		t.Run("TestExtractReleaseArchiveBadGzip", TestExtractReleaseArchiveBadGzip)
		t.Run("TestCopyExecutableMissingSource", TestCopyExecutableMissingSource)
		t.Run("TestInstallExtractedBinariesMissingSource", TestInstallExtractedBinariesMissingSource)
		t.Run("TestUpgradeDownloadFailurePropagates", TestUpgradeDownloadFailurePropagates)
		t.Run("TestUpgradeStageFailures", TestUpgradeStageFailures)
		t.Run("TestExtractArchiveErrors", TestExtractArchiveErrors)
		t.Run("TestInstallDirectoryAndSymlinkErrors", TestInstallDirectoryAndSymlinkErrors)
		t.Run("TestDownloadTransportAndIOErrors", TestDownloadTransportAndIOErrors)
		t.Run("TestExtractCopyAndCloseErrors", TestExtractCopyAndCloseErrors)
		t.Run("TestCopyExecutableIOErrors", TestCopyExecutableIOErrors)
	})
}
