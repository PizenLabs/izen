package ephemeral

import (
	"context"
	"errors"
	"net"
)

// error helpers for FailureClassifier transport/cancellation detection.
// errors.As-based so wrapped/joined provider errors are handled.

func isContextCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}

func isContextDeadline(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}

func asNetError(err error, target *net.Error) bool {
	return errors.As(err, target)
}

func asDNSError(err error, target **net.DNSError) bool {
	return errors.As(err, target)
}

func asOpError(err error, target **net.OpError) bool {
	return errors.As(err, target)
}
