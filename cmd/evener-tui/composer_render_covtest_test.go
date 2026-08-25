package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuitheme"
)

// renderModeChip builds a mode chip fragment matching the production code's
// inline rendering, for testing fitRightContent.
func renderModeChip(mode string, th tuitheme.Theme) string {
	return lipgloss.NewStyle().Background(th.SurfaceSecondary).Foreground(th.Accent).Bold(true).Render("● " + strings.ToUpper(mode))
}

// ---- renderComposerChipStrip: various contexts ------------------------------

func TestCovRenderComposerChipStrip_FullContext(t *testing.T) {
	withTestColorProfile(t)
	ctx := composerContext{
		Harness:    "evener",
		Model:      "gpt-5",
		Branch:     "main",
		WorkingDir: "/home/user/project",
		Connected:  true,
		HubAddr:    "http://hub:8080",
		Provider:   "openai",
		Width:      120,
	}
	got := renderComposerChipStrip(ctx)
	plain := ansiPattern.ReplaceAllString(got, "")
	for _, want := range []string{"harness evener", "model openai/gpt-5", "branch main", "project", "connected", "http://hub:8080"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("full chip strip missing %q:\n%s", want, plain)
		}
	}
	if width := lipgloss.Width(got); width != ctx.Width {
		t.Fatalf("chip strip width = %d, want %d", width, ctx.Width)
	}
}

func TestCovRenderComposerChipStrip_NarrowWidth(t *testing.T) {
	withTestColorProfile(t)
	ctx := composerContext{
		Harness:    "evener",
		Model:      "gpt-5",
		Branch:     "main",
		WorkingDir: "/home/user/very/long/path",
		Connected:  true,
		HubAddr:    "http://hub:8080",
		Provider:   "openai",
		Width:      30,
		Mode:       "QUEUE",
	}
	got := renderComposerChipStrip(ctx)
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "harness") || !strings.Contains(plain, "…") {
		t.Fatalf("narrow chip strip should preserve leading context and show truncation:\n%s", plain)
	}
	if width := lipgloss.Width(got); width != ctx.Width {
		t.Fatalf("narrow chip strip width = %d, want %d", width, ctx.Width)
	}
}

func TestCovRenderComposerChipStrip_NoContext(t *testing.T) {
	withTestColorProfile(t)
	ctx := composerContext{Width: 80}
	got := renderComposerChipStrip(ctx)
	if plain := ansiPattern.ReplaceAllString(got, ""); strings.TrimSpace(plain) != "" {
		t.Fatalf("context-free chip strip should contain only band padding: %q", plain)
	}
	if width := lipgloss.Width(got); width != ctx.Width {
		t.Fatalf("context-free chip strip width = %d, want %d", width, ctx.Width)
	}
}

func TestCovRenderComposerChipStrip_AwaitingMode(t *testing.T) {
	withTestColorProfile(t)
	ctx := composerContext{
		Harness:   "evener",
		Width:     80,
		Mode:      "AWAITING",
		Connected: true,
	}
	got := renderComposerChipStrip(ctx)
	if plain := ansiPattern.ReplaceAllString(got, ""); !strings.Contains(plain, "● AWAITING") {
		t.Fatalf("awaiting chip strip lost mode marker: %q", plain)
	}
}

func TestCovRenderComposerChipStrip_ForkMode(t *testing.T) {
	withTestColorProfile(t)
	ctx := composerContext{
		Harness:   "evener",
		Width:     80,
		Mode:      "FORK DRAFT",
		Connected: true,
	}
	got := renderComposerChipStrip(ctx)
	if plain := ansiPattern.ReplaceAllString(got, ""); !strings.Contains(plain, "● FORK DRAFT") {
		t.Fatalf("fork chip strip lost mode marker: %q", plain)
	}
}

// ---- fitRightContent: various room budgets -----------------------------------

func TestCovFitRightContent_StatusFitsWithMode(t *testing.T) {
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()
	ctx := composerContext{Connected: true, HubAddr: "http://hub", Provider: "openai"}
	modeFrag := renderModeChip("QUEUE", th)
	got := fitRightContent(ctx, th, modeFrag, 100)
	plain := ansiPattern.ReplaceAllString(got, "")
	for _, want := range []string{"connected", "http://hub", "● QUEUE"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("generous right content missing %q: %q", want, plain)
		}
	}
}

func TestCovFitRightContent_TightRoomOnlyMode(t *testing.T) {
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()
	ctx := composerContext{Connected: true, HubAddr: "http://hub", Provider: "openai"}
	modeFrag := renderModeChip("QUEUE", th)
	got := fitRightContent(ctx, th, modeFrag, 10)
	plain := ansiPattern.ReplaceAllString(got, "")
	if plain != "…  ● QUEUE" {
		t.Fatalf("tight right content = %q, want truncated health plus full mode", plain)
	}
	if width := lipgloss.Width(got); width != 10 {
		t.Fatalf("tight right content width = %d, want 10", width)
	}
}

func TestCovFitRightContent_NoStatusNoMode(t *testing.T) {
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()
	ctx := composerContext{}
	got := fitRightContent(ctx, th, "", 80)
	if got != "" {
		t.Fatalf("fitRightContent with no status and no mode should be empty: %q", got)
	}
}

func TestCovFitRightContent_StatusOnlyNoMode(t *testing.T) {
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()
	ctx := composerContext{Connected: true, HubAddr: "http://hub", Provider: "openai"}
	got := fitRightContent(ctx, th, "", 80)
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "connected") || !strings.Contains(plain, "http://hub") {
		t.Fatalf("status-only content lost health or address: %q", plain)
	}
}

func TestCovFitRightContent_ModeOnlyNoStatus(t *testing.T) {
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()
	modeFrag := renderModeChip("QUEUE", th)
	got := fitRightContent(composerContext{}, th, modeFrag, 80)
	if plain := ansiPattern.ReplaceAllString(got, ""); plain != "● QUEUE" {
		t.Fatalf("mode-only content = %q, want %q", plain, "● QUEUE")
	}
}

func TestCovFitRightContent_VerySmallRoomTruncates(t *testing.T) {
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()
	ctx := composerContext{Connected: true, HubAddr: "http://hub", Provider: "openai"}
	modeFrag := renderModeChip("QUEUE", th)
	got := fitRightContent(ctx, th, modeFrag, 3)
	if width := lipgloss.Width(got); width != 3 {
		t.Fatalf("very-small right content width = %d, want 3: %q", width, ansiPattern.ReplaceAllString(got, ""))
	}
	if plain := ansiPattern.ReplaceAllString(got, ""); !strings.HasSuffix(plain, "…") {
		t.Fatalf("very-small right content should signal truncation: %q", plain)
	}
}

// ---- renderChipStatus: budget truncation ------------------------------------

func TestCovRenderChipStatus_NoContextReturnsEmpty(t *testing.T) {
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()
	if got := renderChipStatus(composerContext{}, th, 0); got != "" {
		t.Fatalf("no context = %q, want empty", got)
	}
}

func TestCovRenderChipStatus_UnlimitedBudget(t *testing.T) {
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()
	ctx := composerContext{Connected: true, HubAddr: "http://hub:9999", Provider: "openai"}
	got := renderChipStatus(ctx, th, 0)
	if !strings.Contains(got, "connected") {
		t.Fatalf("unlimited budget should show connected:\n%s", got)
	}
	if !strings.Contains(got, "hub:9999") {
		t.Fatalf("unlimited budget should show hub addr:\n%s", got)
	}
}

func TestCovRenderChipStatus_Disconnected(t *testing.T) {
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()
	ctx := composerContext{Connected: false, HubAddr: "http://hub", Provider: "openai"}
	got := renderChipStatus(ctx, th, 0)
	if !strings.Contains(got, "disconnected") {
		t.Fatalf("disconnected should show 'disconnected':\n%s", got)
	}
}

func TestCovRenderChipStatus_RetryChip(t *testing.T) {
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()
	ctx := composerContext{Connected: true, Retry: "rate limited — attempt 2/3"}
	got := renderChipStatus(ctx, th, 0)
	if !strings.Contains(got, "rate limited") {
		t.Fatalf("retry chip should show in status:\n%s", got)
	}
}

func TestCovRenderChipStatus_HealthTruncatedByBudget(t *testing.T) {
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()
	ctx := composerContext{Connected: true, HubAddr: "http://hub:9999", Provider: "openai"}
	got := renderChipStatus(ctx, th, 3)
	if width := lipgloss.Width(got); width != 3 {
		t.Fatalf("truncated health width = %d, want 3", width)
	}
	if plain := ansiPattern.ReplaceAllString(got, ""); !strings.HasSuffix(plain, "…") {
		t.Fatalf("truncated health should end in ellipsis: %q", plain)
	}
}

func TestCovRenderChipStatus_RetryTruncatedByBudget(t *testing.T) {
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()
	ctx := composerContext{Connected: true, Retry: "rate limited — attempt 2/3 — retrying in 30s"}
	got := renderChipStatus(ctx, th, 40)
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "connected") || !strings.Contains(plain, "rate limited") || !strings.HasSuffix(plain, "…") {
		t.Fatalf("truncated retry lost priority or truncation signal: %q", plain)
	}
	if width := lipgloss.Width(got); width != 40 {
		t.Fatalf("truncated retry width = %d, want 40", width)
	}
}

func TestCovRenderChipStatus_HubAddrDroppedByBudget(t *testing.T) {
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()
	ctx := composerContext{Connected: true, HubAddr: "http://hub:9999", Provider: "openai"}
	// Budget enough for health+retry but not hub addr (hub addr is lowest priority, shown whole or not at all)
	got := renderChipStatus(ctx, th, 20)
	if strings.Contains(got, "hub:9999") {
		t.Fatalf("hub addr should be dropped when budget is tight:\n%s", got)
	}
}

func TestCovRenderChipStatus_HubAddrFitsInBudget(t *testing.T) {
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()
	ctx := composerContext{Connected: true, HubAddr: "http://h:9", Provider: "openai"}
	got := renderChipStatus(ctx, th, 200)
	if !strings.Contains(got, "h:9") {
		t.Fatalf("hub addr should show when budget is large:\n%s", got)
	}
}

// ---- composerFooterHints: all modes ------------------------------------------

func TestCovComposerFooterHints_QueueWithSteer(t *testing.T) {
	got := composerFooterHints("queue", 80, true)
	if !strings.Contains(got, "queue") || !strings.Contains(got, "steer") {
		t.Fatalf("queue+steer footer should contain both:\n%s", got)
	}
}

func TestCovComposerFooterHints_QueueWithoutSteer(t *testing.T) {
	got := composerFooterHints("queue", 80, false)
	if !strings.Contains(got, "queue") {
		t.Fatalf("queue footer should contain queue:\n%s", got)
	}
	if strings.Contains(got, "steer") {
		t.Fatalf("queue footer without steer should not contain steer:\n%s", got)
	}
}

func TestCovComposerFooterHints_Fork(t *testing.T) {
	got := composerFooterHints("fork", 80, false)
	if !strings.Contains(got, "fork") {
		t.Fatalf("fork footer should contain fork:\n%s", got)
	}
}

func TestCovComposerFooterHints_ScrollBrowse(t *testing.T) {
	got := composerFooterHints("scroll-browse", 80, false)
	if !strings.Contains(got, "select") || !strings.Contains(got, "fork") {
		t.Fatalf("scroll-browse footer should contain select and fork:\n%s", got)
	}
}

func TestCovComposerFooterHints_Compose(t *testing.T) {
	got := composerFooterHints("compose", 80, false)
	if !strings.Contains(got, "send") {
		t.Fatalf("compose footer should contain send:\n%s", got)
	}
}

func TestCovComposerFooterHints_DefaultMode(t *testing.T) {
	got := composerFooterHints("unknown-mode", 80, false)
	if !strings.Contains(got, "send") {
		t.Fatalf("unknown mode should default to compose:\n%s", got)
	}
}

// ---- composerRetryChip -------------------------------------------------------

func TestCovComposerRetryChip_NilReturnsEmpty(t *testing.T) {
	if got := composerRetryChip(nil, "model", false); got != "" {
		t.Fatalf("nil retry = %q, want empty", got)
	}
}

func TestCovComposerRetryChip_RateLimited(t *testing.T) {
	retry := &appwire.ThreadModelRetryParams{
		ErrorClass:  "rate_limit",
		DelayMS:     30000,
		Attempt:     2,
		MaxAttempts: 4,
	}
	got := composerRetryChip(retry, "model", false)
	if !strings.Contains(got, "rate limited") {
		t.Fatalf("rate limited retry = %q, want contains 'rate limited'", got)
	}
}

func TestCovComposerRetryChip_GenericError(t *testing.T) {
	retry := &appwire.ThreadModelRetryParams{
		ErrorClass:  "server_error",
		DelayMS:     30000,
		Attempt:     2,
		MaxAttempts: 4,
	}
	got := composerRetryChip(retry, "model", false)
	if !strings.Contains(got, "provider error") {
		t.Fatalf("generic error retry = %q, want contains 'provider error'", got)
	}
}

func TestCovComposerRetryChip_InProgress(t *testing.T) {
	retry := &appwire.ThreadModelRetryParams{
		ErrorClass:  "rate_limit",
		DelayMS:     30000,
		Attempt:     2,
		MaxAttempts: 4,
	}
	got := composerRetryChip(retry, "model", true)
	if !strings.Contains(got, "in progress") {
		t.Fatalf("in progress retry = %q, want contains 'in progress'", got)
	}
}

func TestCovComposerRetryChip_DifferentModelTag(t *testing.T) {
	retry := &appwire.ThreadModelRetryParams{
		ErrorClass:  "rate_limit",
		Model:       "fallback-model",
		DelayMS:     30000,
		Attempt:     2,
		MaxAttempts: 4,
	}
	got := composerRetryChip(retry, "primary-model", false)
	if !strings.Contains(got, "fallback-model") {
		t.Fatalf("different model tag should appear: %q", got)
	}
}

func TestCovComposerRetryChip_SameModelNoTag(t *testing.T) {
	retry := &appwire.ThreadModelRetryParams{
		ErrorClass:  "rate_limit",
		Model:       "same-model",
		DelayMS:     30000,
		Attempt:     2,
		MaxAttempts: 4,
	}
	got := composerRetryChip(retry, "same-model", false)
	if strings.Contains(got, "(same-model)") {
		t.Fatalf("same model should not show tag: %q", got)
	}
}

func TestCovComposerRetryChip_AttemptCapFallback(t *testing.T) {
	retry := &appwire.ThreadModelRetryParams{
		ErrorClass:  "rate_limit",
		DelayMS:     30000,
		Attempt:     3,
		MaxAttempts: 5,
		AttemptCap:  0,
	}
	got := composerRetryChip(retry, "model", false)
	if !strings.Contains(got, "3/5") {
		t.Fatalf("attempt cap fallback = %q, want contains '3/5'", got)
	}
}

func TestCovComposerRetryChip_AttemptCapUsed(t *testing.T) {
	retry := &appwire.ThreadModelRetryParams{
		ErrorClass:  "rate_limit",
		DelayMS:     30000,
		Attempt:     3,
		MaxAttempts: 5,
		AttemptCap:  4,
	}
	got := composerRetryChip(retry, "model", false)
	if !strings.Contains(got, "3/4") {
		t.Fatalf("attempt cap = %q, want contains '3/4'", got)
	}
}

func TestCovComposerRetryChip_ZeroAttemptCapAndMaxDropsFraction(t *testing.T) {
	retry := &appwire.ThreadModelRetryParams{
		ErrorClass:  "rate_limit",
		DelayMS:     30000,
		Attempt:     3,
		MaxAttempts: 0,
		AttemptCap:  0,
	}
	got := composerRetryChip(retry, "model", false)
	if strings.Contains(got, "3/") {
		t.Fatalf("zero cap and max should drop fraction: %q", got)
	}
}

// ---- formatExactGap ----------------------------------------------------------

func TestCovFormatExactGap_Under60Seconds(t *testing.T) {
	if got := formatExactGap(45000); got != "45s" {
		t.Fatalf("45s gap = %q, want 45s", got)
	}
}

func TestCovFormatExactGap_ExactMinutes(t *testing.T) {
	if got := formatExactGap(180000); got != "3m" {
		t.Fatalf("3m gap = %q, want 3m", got)
	}
}

func TestCovFormatExactGap_MinutesWithSeconds(t *testing.T) {
	if got := formatExactGap(185000); got != "3m 5s" {
		t.Fatalf("3m 5s gap = %q, want '3m 5s'", got)
	}
}

func TestCovFormatExactGap_Zero(t *testing.T) {
	if got := formatExactGap(0); got != "0s" {
		t.Fatalf("0 gap = %q, want 0s", got)
	}
}

// ---- composeProviderModel ----------------------------------------------------

func TestCovComposeProviderModel_BothPresent(t *testing.T) {
	got := composeProviderModel("openai", "gpt-5")
	if got != "openai/gpt-5" {
		t.Fatalf("provider+model = %q, want openai/gpt-5", got)
	}
}

func TestCovComposeProviderModel_ModelOnly(t *testing.T) {
	got := composeProviderModel("openai", "")
	if got != "openai" {
		t.Fatalf("provider only = %q, want openai", got)
	}
}

func TestCovComposeProviderModel_Neither(t *testing.T) {
	got := composeProviderModel("", "")
	if got != "" {
		t.Fatalf("neither = %q, want empty", got)
	}
}
