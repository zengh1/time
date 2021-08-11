package rate

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestAllow(t *testing.T) {
	limiter := NewLimiter(5, 5)

	for i := 0; i < 1000; i++ {
		if limiter.Allow() {
			fmt.Printf("[%d] allow \n", i+1)
		} else {
			fmt.Printf("[%d] no allow \n", i+1)
		}
	}
}

func TestAllowN(t *testing.T) {
	limiter := NewLimiter(5, 5)

	if limiter.AllowN(time.Now(), 30) {
		fmt.Printf("allow \n")
	} else {
		fmt.Printf("no allow \n")
	}
}

func TestWaitN(t *testing.T) {
	limiter := NewLimiter(1, 5)
	ctx := context.Background()

	for i := 0; i < 100; i++ {
		if err := limiter.WaitN(ctx, 5); err != nil {
			fmt.Printf("error: %+v", err)
			return
		} else {
			fmt.Printf("[%d] ok \n", i+1)
		}
	}
}
