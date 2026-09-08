//go:build !linux

package main

import "time"

func sdNotifyReady()          {}
func sdWatchdog()             {}
func sdWatchdogEnabled() bool { return false }
func sdWatchdogLoop(interval time.Duration, stop <-chan struct{}) {
	<-stop
}
