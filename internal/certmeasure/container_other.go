//go:build !linux

package certmeasure

import (
	"context"
	"errors"
	"time"
)

func validateContainer(options) error { return errors.New("container measurement requires Linux") }
func containerSample(context.Context, options, string, int) ([]byte, time.Duration, float64, error) {
	return nil, 0, 0, errors.New("container measurement requires Linux")
}
