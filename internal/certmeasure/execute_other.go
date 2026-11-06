//go:build !linux && !darwin

package certmeasure

import (
	"context"
	"errors"
	"time"
)

func execute(context.Context, string, []string) ([]byte, time.Duration, error) {
	return nil, 0, errors.New("native measurement execution is unsupported on this OS")
}
