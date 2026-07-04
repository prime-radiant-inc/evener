package plugins

// computeVersion picks a display version: plugin.json version, else the
// marketplace-declared version, else a 12-char commit sha, else "unknown".
func computeVersion(pluginJSONVer, declaredVer, commitSHA string) string {
	if pluginJSONVer != "" {
		return pluginJSONVer
	}
	if declaredVer != "" {
		return declaredVer
	}
	if commitSHA != "" {
		if len(commitSHA) > 12 {
			return commitSHA[:12]
		}
		return commitSHA
	}
	return "unknown"
}
