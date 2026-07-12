//go:build serffuzz

package tuiprim

import "testing"

func init() {
	fuzzCoverageUnion = func(t *testing.T) {
		TestActionBarForWidth(t)
		TestActionBarForWidthWrapsByRenderedWidth(t)
		TestActionBarJoinsKeys(t)
		TestAppShellNeverExceedsHeight(t)
		TestDotLeaderFillsMiddle(t)
		TestDotLeaderHandlesOverflow(t)
		TestFocusedStateBarReturnsDoubleGlyph(t)
		TestKbdHintFormatsKeyAndAction(t)
		TestOverlayContainsTitleBodyFooter(t)
		TestOverlayDrawsRoundedBorder(t)
		TestPopupPaneContentWidth(t)
		TestPopupPaneWidth(t)
		TestPrimitiveBoundaryStates(t)
		TestSectionDividerEmitsLeftRight(t)
		TestSectionDividerTruncatesAtNarrowWidth(t)
		TestSectionDividerUsesRuleGlyphs(t)
		TestStateBarReturnsSingleGlyph(t)
		TestStatusBadgeContainsLabelAndDot(t)
		TestStatusBadgeIsBoldUppercase(t)
	}
}
