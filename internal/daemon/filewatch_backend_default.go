//go:build !darwin || !cgo

package daemon

func (sm *SessionManager) newDefaultWatchBackend(root string, matcher *watchMatcher) (watchBackend, map[string]int, string) {
	return sm.newFSNotifyRecursiveWatchBackend(root, matcher)
}
