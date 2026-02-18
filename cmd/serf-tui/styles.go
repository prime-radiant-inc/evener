package main

import "github.com/charmbracelet/lipgloss"

var (
	statusBarStyle = lipgloss.NewStyle().
		Background(lipgloss.Color("235")).
		Foreground(lipgloss.Color("252")).
		Padding(0, 1)

	statusConnected = lipgloss.NewStyle().
		Foreground(lipgloss.Color("42")).
		Bold(true)

	statusDisconnected = lipgloss.NewStyle().
		Foreground(lipgloss.Color("196")).
		Bold(true)

	userLabelStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("33")).
		Bold(true)

	assistantLabelStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("213")).
		Bold(true)

	toolCollapsedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("244"))

	toolExpandedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("244")).
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(lipgloss.Color("238")).
		PaddingLeft(1)

	toolNameStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("179")).
		Bold(true)

	toolDurationStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("240"))

	inputBorderStyle = lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(lipgloss.Color("238"))

	inputPromptStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("42"))
)
