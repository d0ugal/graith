//go:build darwin

package daemonservice

import (
	"context"
)

func ServiceControlRoot(uid int) (string, error) {
	return ServiceControlRootContext(context.Background(), uid)
}

func ServiceControlRootContext(ctx context.Context, uid int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	root, err := ReceiptRoot(uid)
	if err != nil {
		return "", err
	}

	return controlRootAtReceiptRoot(root), nil
}
