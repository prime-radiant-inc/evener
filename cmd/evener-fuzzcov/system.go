package main

import (
	"os"
	"path/filepath"
)

// fuzzcovSystem centralizes the handful of host operations whose failures are
// part of the reporter's contract. Production uses the OS implementations;
// deterministic fuzz builds install an equivalent sentinel-aware factory once
// during process initialization.
type fuzzcovSystemOps struct {
	open      func(string) (*os.File, error)
	abs       func(string) (string, error)
	rel       func(string, string) (string, error)
	readFile  func(string) ([]byte, error)
	stat      func(string) (os.FileInfo, error)
	writeFile func(string, []byte, os.FileMode) error
}

var fuzzcovSystem = fuzzcovSystemOps{
	open:      os.Open,
	abs:       filepath.Abs,
	rel:       filepath.Rel,
	readFile:  os.ReadFile,
	stat:      os.Stat,
	writeFile: os.WriteFile,
}
