// Package clock provides the production time source. Business code depends
// on a Clock interface instead, so tests can fix the time.
package clock

import "time"

// System reads the machine clock and always returns UTC.
type System struct{}

// Now returns the current time in UTC.
func (System) Now() time.Time { return time.Now().UTC() }
