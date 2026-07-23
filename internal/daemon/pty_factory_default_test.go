//go:build !libghostty

package daemon

func configureDaemonTestPTYFactory() { newPTYSession = newDaemonPTYSessionWithFactory }
