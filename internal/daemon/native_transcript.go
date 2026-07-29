package daemon

func nativeTranscriptRootForLaunch(agent, agentSessionID, envRoot, existingRoot string) string {
	if !scrapesID(agent) {
		return ""
	}

	if agentSessionID == "" {
		return envRoot
	}

	// A captured id belongs to the root where it was observed. Keep that root
	// even if a later relaunch environment changes CODEX_HOME.
	if existingRoot != "" {
		return existingRoot
	}

	return envRoot
}

func sessionNativeTranscriptRoot(s *SessionState) string {
	if s == nil {
		return ""
	}

	if s.NativeTranscriptRoot != "" {
		return s.NativeTranscriptRoot
	}

	return s.NativeStateRoot
}
