package fspaths

import "testing"

// FuzzFSPathsBehaviorProgram replays one deterministic behavioral contract selected by the
// fuzz input. The seed corpus covers every production branch; mutation varies
// ordering and repetition without relying on network, wall clock, or host state.
func FuzzFSPathsBehaviorProgram(f *testing.F) {
	checks := []func(*testing.T){
		checkCompletePaths_EmptyHomeUsesRoot,
		checkCompletePaths_DirsOnlyExcludesFilesUnsuffixed,
		checkCompletePaths_IncludeFilesReturnsBoth,
		checkCompletePaths_IncludeFilesMarksDirsWithSeparator,
		checkCompletePaths_IncludeFilesHidesDotfilesUntilDotTyped,
		checkCompletePaths_LoneTrailingDotRevealsDottedEntries,
		checkCompletePaths_ParentTraversalKeepsDotfilesHidden,
		checkCompletePaths_IncludeFilesLimitCapsCombinedResult,
		checkCompletePaths_IncludeFilesSuffixesSymlinkedDirs,
		checkCompletePaths_IncludeFilesUnstattableEntryStaysUnsuffixed,
		checkCompletePaths_StatsEachEntryAtMostOnce,
		checkCompletePaths_DirsOnlyListsSymlinkedDir,
		checkCompletePaths_DirsOnlyDropsUnstattableEntry,
		checkCanonicalizeDir_StatErrorAfterResolution,
		checkCanonicalizeDir_RejectsRelative,
		checkCanonicalizeDir_RejectsEmpty,
		checkCanonicalizeDir_RejectsNonexistent,
		checkCanonicalizeDir_RejectsFile,
		checkCanonicalizeDir_ResolvesSymlink,
		checkCanonicalizeDir_NormalizesTraversal,
		checkSanitizeDirPrefix_PreservesTrailingSlash,
		checkSanitizeDirPrefix_PreservesLoneTrailingDot,
		checkSanitizeDirPrefix_RejectsTraversal,
		checkSanitizeDirPrefix_NormalizesInternalTraversal,
		checkSanitizeDirPrefix_Empty,
		checkResolveInRoot_AllowsFileInsideRoot,
		checkResolveInRoot_AllowsNestedFile,
		checkResolveInRoot_RejectsDotDotTraversal,
		checkResolveInRoot_RejectsAbsoluteOutside,
		checkResolveInRoot_RejectsSymlinkEscape,
		checkResolveInRoot_MissingFileNotEscape,
		checkResolveInRoot_RejectsEmpty,
		checkResolveInRoot_RejectsMissingRoot,
	}
	for i := range checks {
		f.Add(uint8(i))
	}
	f.Fuzz(func(t *testing.T, selector uint8) {
		checks[int(selector)%len(checks)](t)
	})
}
