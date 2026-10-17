package relay_test

import (
	"context"
	"fmt"
	"github.com/hualinli/f50_pro_relay"
	"time"
)

// This example requires an Android device with the configured network tools.
func ExampleManager() {
	manager, err := relay.New(relay.Options{
		BaseDir:    "/data/adb/ufi-relay",
		RuntimeDir: "/data/vendor/wifi/ufi-relay",
	})
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	startup, stopWaiting := context.WithTimeout(ctx, 20*time.Second)
	err = manager.Ready(startup)
	stopWaiting()
	if err != nil {
		cancel()
		<-done
		panic(err)
	}

	// A future application's handlers can call this object directly in this process.
	status, err := manager.Status(ctx)
	if err != nil {
		cancel()
		<-done
		panic(err)
	}
	fmt.Println(status.State, status.Exit)

	cancel()
	if err := <-done; err != nil {
		panic(err)
	}
}
