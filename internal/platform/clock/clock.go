// Package clock provides the production time source. Business code depends
// on a Clock interface instead, so tests can fix the time.
package clock

import "time"

// System reads the machine clock and always returns UTC.
type System struct{}

// Now returns the current time in UTC, truncated to microseconds. PostgreSQL
// stores timestamps with microsecond precision, so truncating up front makes
// a value read back from the database identical to the one first returned
// (a retried reservation reports exactly the same expires_at).
func (System) Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
