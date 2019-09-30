package main

import "testing"

func TestJitter(t *testing.T) {
	jitter := newJitter()
	jitter.calc()
	if !(jitter.min <= jitter.duration || jitter.duration <= jitter.cap) {
		t.Fatal("invalid jitter value: ", jitter.duration)
	}
}
