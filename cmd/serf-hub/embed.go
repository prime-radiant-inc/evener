package main

import "embed"

//go:embed templates/*.html templates/partials/*.html templates/partials/settings/*.html
var templatesFS embed.FS

//go:embed assets/*
var assetsFS embed.FS
