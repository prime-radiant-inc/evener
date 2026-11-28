//go:build !linux && !darwin

package execenv

import "context"

func walkProjectFiles(context.Context, string, ProjectFilesOptions, *GlobBudget) (ProjectFilesResult, error) {
	return ProjectFilesResult{}, errSandboxUnsupported()
}
