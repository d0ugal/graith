package daemonservice

import "path/filepath"

func controlRootAtReceiptRoot(receiptRoot string) string {
	return filepath.Join(receiptRoot, "bootstrap")
}
