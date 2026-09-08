//go:build linux

package main

import (
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
)

func sdNotifyReady() { _, _ = daemon.SdNotify(false, daemon.SdNotifyReady) }
func sdWatchdog()    { _, _ = daemon.SdNotify(false, daemon.SdNotifyWatchdog) }
func sdWatchdogEnabled() bool {
	d, err := daemon.SdWatchdogEnabled(false)
	return err == nil && d > 0
}

// sdWatchdogLoop 每 interval 喂一次狗(systemd WatchdogSec 的一半)。
func sdWatchdogLoop(interval time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			sdWatchdog()
		}
	}
}
