package proxyguard

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRestartUntilErr(t *testing.T) {
	tErr := errors.New("test")
	// test if a function exits when an error occurs
	gErr := restartUntilErr(context.Background(), func(_ context.Context) error {
		return tErr
	}, []time.Duration{0 * time.Second}, time.Duration(1*time.Hour))

	if !errors.Is(gErr, tErr) {
		t.Fatalf("got error: %v, not equal to want error: %v", gErr, tErr)
	}

	// test if a function takes n time
	wt := []time.Duration{
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
	}
	d := 1 * time.Hour
	n := len(wt)
	restarted := 0

	st := time.Now()
	_ = restartUntilErr(context.Background(), func(_ context.Context) error {
		if restarted == n {
			return errors.New("return here")
		}
		restarted++
		return nil
	}, wt, d)
	et := time.Now()

	if restarted != n {
		t.Fatalf("restart count: %v, not equal to want count: %v", restarted, n)
	}

	if time.Duration(et.Sub(st)) < time.Duration(3500*time.Millisecond) {
		t.Fatalf("execution time did not take more or equal to 3.5s: %v", et.Sub(st))
	}
}
