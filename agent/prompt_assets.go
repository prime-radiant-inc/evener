package agent

import "embed"

//go:embed prompts/templates/* prompts/sections/*
var embeddedPrompts embed.FS
