package webui

import (
	"strings"
	"testing"
)

func TestEmbeddedBoardIncludesSubtaskAndRankingControls(t *testing.T) {
	html := string(BoardHTML)
	markers := []string{
		`class="subcheck"`,
		`width: 16px !important`,
		`data-toggle=`,
		`data-add-subtask=`,
		`data-sub-select=`,
		`data-rank=`,
		`function rankTask`,
	}
	for _, marker := range markers {
		if !strings.Contains(html, marker) {
			t.Errorf("embedded board is missing %q", marker)
		}
	}
}

func TestEmbeddedBoardIncludesFocusViewsAndKindControls(t *testing.T) {
	html := string(BoardHTML)
	markers := []string{
		`id="focusView"`,
		`id="reviewsView"`,
		`id="everythingView"`,
		`localStorage.getItem("board.focus")`,
		`task.lane === "blocked"`,
		`focus === "reviews" && kind === "review"`,
		`class="chip kind"`,
		`id="fKind"`,
		`kind: $("fKind").value`,
		`kind: "work"`,
		`body: JSON.stringify({ ...input, source })`,
		`function linkChips`,
		`rel="noopener noreferrer"`,
		`id="fReconcileMode"`,
		`id="draftLinks"`,
		`links: draft.links.map`,
	}
	for _, marker := range markers {
		if !strings.Contains(html, marker) {
			t.Errorf("embedded board is missing Focus/kind behavior %q", marker)
		}
	}
}

func TestEmbeddedBoardIncludesAccessibleProjectDropdown(t *testing.T) {
	html := string(BoardHTML)
	markers := []string{
		`role="combobox"`,
		`aria-haspopup="listbox"`,
		`role="listbox"`,
		`popover="manual"`,
		`function createDropdown`,
		`aria-activedescendant`,
		`projectDropdown.sync()`,
	}
	for _, marker := range markers {
		if !strings.Contains(html, marker) {
			t.Errorf("embedded board is missing project dropdown marker %q", marker)
		}
	}
}
