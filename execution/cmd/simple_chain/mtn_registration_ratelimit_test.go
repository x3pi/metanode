package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/time/rate"
)

func newTestRegistrationLimiter(perMin int) *registrationIPLimiter {
	return &registrationIPLimiter{perMin: perMin, byIP: make(map[string]*rate.Limiter)}
}

func TestRegistrationIPLimiter_PerIPBudget(t *testing.T) {
	l := newTestRegistrationLimiter(3)
	for i := 0; i < 3; i++ {
		assert.True(t, l.allow("10.0.0.1"), "request %d within the burst", i+1)
	}
	assert.False(t, l.allow("10.0.0.1"), "4th request in the same minute is refused")
	assert.True(t, l.allow("10.0.0.2"), "another client IP has its own budget")
}

func TestRegistrationIPLimiter_DisabledAndUnknownIP(t *testing.T) {
	off := newTestRegistrationLimiter(0)
	for i := 0; i < 100; i++ {
		assert.True(t, off.allow("10.0.0.1"), "perMin=0 disables the limit")
	}
	on := newTestRegistrationLimiter(1)
	for i := 0; i < 10; i++ {
		assert.True(t, on.allow(""), "in-process calls without a peer address are not limited")
	}
}

func TestRegistrationIPLimiter_BoundedMemory(t *testing.T) {
	l := newTestRegistrationLimiter(1)
	for i := 0; i < maxRegistrationLimiterIPs+5; i++ {
		l.allow(string(rune(i)) + ".ip")
	}
	assert.LessOrEqual(t, len(l.byIP), maxRegistrationLimiterIPs)
}
