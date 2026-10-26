package redisrepo

import (
	"context"
	_ "embed" // Lua scripts are compiled into the binary
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

var _ usecase.IdempotencyStore = (*IdempotencyStore)(nil)

var (
	//go:embed scripts/idem_begin.lua
	idemBeginLua string
	//go:embed scripts/idem_release.lua
	idemReleaseLua string
)

const (
	stateInProgress = "in_progress"
	stateDone       = "done"
)

// idemRecord is what an idempotency key holds in Redis.
type idemRecord struct {
	State       string `json:"state"`
	Fingerprint string `json:"fp"`
	Token       string `json:"token,omitempty"`
	Status      int    `json:"status,omitempty"`
	Body        []byte `json:"body,omitempty"`
}

// IdempotencyStore keeps request outcomes by idempotency key in Redis.
type IdempotencyStore struct {
	rdb     *redis.Client
	begin   *redis.Script
	release *redis.Script
}

// NewIdempotencyStore returns a store backed by rdb.
func NewIdempotencyStore(rdb *redis.Client) *IdempotencyStore {
	return &IdempotencyStore{
		rdb:     rdb,
		begin:   redis.NewScript(idemBeginLua),
		release: redis.NewScript(idemReleaseLua),
	}
}

// idemKey builds the Redis key for ref. The {user} hash tag keeps a user's
// keys in one Cluster slot.
func idemKey(ref usecase.IdempotencyRef) string {
	return fmt.Sprintf("idem:{%s}:%s", ref.UserID, ref.Key)
}

// Begin claims ref atomically, or reports what it already holds.
func (s *IdempotencyStore) Begin(
	ctx context.Context, ref usecase.IdempotencyRef, fingerprint, token string, lockTTL time.Duration,
) (usecase.IdempotencyClaim, error) {
	claim, err := json.Marshal(idemRecord{State: stateInProgress, Fingerprint: fingerprint, Token: token})
	if err != nil {
		return usecase.IdempotencyClaim{}, fmt.Errorf("encode idempotency claim: %w", err)
	}
	key := idemKey(ref)
	raw, err := s.begin.Run(ctx, s.rdb, []string{key}, claim, lockTTL.Milliseconds()).Text()
	if errors.Is(err, redis.Nil) { // the script returned false: we own the key
		return usecase.IdempotencyClaim{State: usecase.IdempotencyNew, Fingerprint: fingerprint}, nil
	}
	if err != nil {
		return usecase.IdempotencyClaim{}, unavailable("idempotency begin", err)
	}

	var rec idemRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return usecase.IdempotencyClaim{}, fmt.Errorf("decode idempotency record %s: %w", key, err)
	}
	if rec.State == stateDone {
		return usecase.IdempotencyClaim{
			State: usecase.IdempotencyDone, Fingerprint: rec.Fingerprint, Status: rec.Status, Body: rec.Body,
		}, nil
	}
	return usecase.IdempotencyClaim{State: usecase.IdempotencyInProgress, Fingerprint: rec.Fingerprint}, nil
}

// Complete stores the final response for ref, replacing the claim.
func (s *IdempotencyStore) Complete(
	ctx context.Context, ref usecase.IdempotencyRef, fingerprint string, status int, body []byte, ttl time.Duration,
) error {
	rec, err := json.Marshal(idemRecord{State: stateDone, Fingerprint: fingerprint, Status: status, Body: body})
	if err != nil {
		return fmt.Errorf("encode idempotency record: %w", err)
	}
	if err := s.rdb.Set(ctx, idemKey(ref), rec, ttl).Err(); err != nil {
		return unavailable("idempotency complete", err)
	}
	return nil
}

// Release drops the claim made with token, if it is still in place.
func (s *IdempotencyStore) Release(ctx context.Context, ref usecase.IdempotencyRef, token string) error {
	if err := s.release.Run(ctx, s.rdb, []string{idemKey(ref)}, token).Err(); err != nil {
		return unavailable("idempotency release", err)
	}
	return nil
}
