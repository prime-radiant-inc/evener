//go:build serffuzz

package tuipick

import "testing"

func init() {
	fuzzCoverageUnion = func(t *testing.T) {
		TestCompleteLastPathSegment(t)
		TestCompleteLastPathSegmentNormalization(t)
		TestDirEntryPredicate(t)
		TestModelPickerRendersAsPopupPane(t)
		TestModelPickerUsesOverlayBorder(t)
		TestModelPicker_ActiveHighlight(t)
		TestModelPicker_Backspace(t)
		TestModelPicker_ConstructorsAndGetters(t)
		TestModelPicker_DisabledItemRendersReasonAndCannotSelect(t)
		TestModelPicker_Escape(t)
		TestModelPicker_FilterAndSelect(t)
		TestModelPicker_Navigation(t)
		TestModelPicker_RendersGroupHeadersOnTransition(t)
		TestModelPicker_RendersMetaTail(t)
		TestModelPicker_ZeroValueGroupMetaUnchangedForActionPicker(t)
		TestPickerPanelCannotSelectDisabledRow(t)
		TestPickerPanelFiltersAndRendersDisabledReasons(t)
		TestPickerPanelRendersAsPopupPane(t)
		TestPickerPanel_ConstructorAndGetters(t)
		TestPickerPanel_SetFilterMatchesAllFields(t)
		TestPickerRenderBoundaryStates(t)
		TestTextInputModalBoundaryStates(t)
		TestTextInputModalConstructors(t)
		TestTextInputModalUsesOverlay(t)
		TestTextInputModal_CapturesAndSubmits(t)
		TestTextInputModal_EscapeCancels(t)
		TestThemePickerRendersAsPopupPane(t)
		TestThemePickerUsesOverlayBorder(t)
		TestThemePicker_InitialCursor(t)
		TestThemePicker_UpdateCursorBounds(t)
		TestThemePicker_UpdateSelectAndCancel(t)
		TestThemePicker_ViewContainsThemes(t)
	}
}
