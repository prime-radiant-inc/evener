package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

var markdownRenderer *glamour.TermRenderer

func initMarkdownRenderer(width int) {
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width-4),
	)
	if err == nil {
		markdownRenderer = r
	}
}

func renderMarkdown(text string) string {
	if markdownRenderer == nil {
		return text
	}
	rendered, err := markdownRenderer.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimSpace(rendered)
}

type messageKind int

const (
	msgUser messageKind = iota
	msgAssistant
	msgTool
	msgSystem
)

type toolCallInfo struct {
	Name        string
	Description string
	Output      string
	Error       string
	Duration    time.Duration
	Expanded    bool
	Done        bool
}

const toolCollapseThreshold = 5

func renderMessage(msg chatMessage, width int) string {
	switch msg.Kind {
	case msgUser:
		label := userLabelStyle.Render("▌ User")
		return fmt.Sprintf("%s\n%s", label, msg.Text)
	case msgAssistant:
		label := assistantLabelStyle.Render("▌ Assistant")
		return fmt.Sprintf("%s\n%s", label, renderMarkdown(msg.Text))
	case msgTool:
		if msg.Tool == nil {
			return ""
		}
		return renderToolCall(*msg.Tool, width)
	case msgSystem:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Italic(true).Render(msg.Text)
	}
	return ""
}

func renderToolCall(tc toolCallInfo, width int) string {
	arrow := "▸"
	if tc.Expanded {
		arrow = "▾"
	}

	dur := ""
	if tc.Done {
		dur = toolDurationStyle.Render(fmt.Sprintf("[%.1fs]", tc.Duration.Seconds()))
	} else {
		dur = toolDurationStyle.Render("...")
	}

	name := toolNameStyle.Render(tc.Name)
	desc := tc.Description
	if len(desc) > 40 {
		desc = desc[:37] + "..."
	}

	header := fmt.Sprintf("%s %s  %s  %s", arrow, name, desc, dur)

	if !tc.Expanded || tc.Output == "" {
		return toolCollapsedStyle.Render(header)
	}

	output := toolExpandedStyle.Width(width - 4).Render(tc.Output)
	return header + "\n" + output
}

type chatMessage struct {
	Kind messageKind
	Text string
	Tool *toolCallInfo
}
