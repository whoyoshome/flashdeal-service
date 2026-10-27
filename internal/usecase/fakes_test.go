package usecase_test

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

// Hand-written in-memory fakes of the ports. Each records the calls the
// tests assert on and can be told to fail.

var (
	_ usecase.Clock                 = (*fakeClock)(nil)
	_ usecase.IDGenerator           = (*fakeIDs)(nil)
	_ usecase.StockGate             = (*fakeGate)(nil)
	_ usecase.DealRepository        = (*fakeDeals)(nil)
	_ usecase.DealStock             = (*fakeDeals)(nil)
	_ usecase.ReservationRepository = (*fakeReservations)(nil)
	_ usecase.OrderRepository       = (*fakeOrders)(nil)
	_ usecase.ExpiryQueue           = (*fakeQueue)(nil)
	_ usecase.StockLedger           = (*fakeLedger)(nil)
	_ usecase.GateRepair            = (*fakeRepair)(nil)
	_ usecase.TxManager             = (*fakeTx)(nil)
	_ usecase.OutboxQueue           = (*fakeOutbox)(nil)
	_ usecase.EventPublisher        = (*fakePublisher)(nil)
)

// t0 is the fixed "now" of every test.
var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

var discardLog = slog.New(slog.DiscardHandler)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type fakeIDs struct {
	n   int
	err error
}

func (g *fakeIDs) New() (string, error) {
	if g.err != nil {
		return "", g.err
	}
	g.n++
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", g.n), nil
}

type gateCall struct {
	dealID int64
	userID string
	qty    int
	limit  int
	// ctxErr is the context's error when the call happened; compensation
	// must run with a live context even if the request was cancelled.
	ctxErr error
}

type fakeGate struct {
	mu         sync.Mutex
	loadErr    error
	reserveErr error
	releaseErr error
	loaded     []int64
	reserves   []gateCall
	releases   []gateCall
}

func (g *fakeGate) Load(_ context.Context, d domain.Deal) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.loaded = append(g.loaded, d.ID())
	return g.loadErr
}

func (g *fakeGate) TryReserve(ctx context.Context, dealID int64, userID string, qty, limit int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reserves = append(g.reserves, gateCall{dealID: dealID, userID: userID, qty: qty, limit: limit, ctxErr: ctx.Err()})
	return g.reserveErr
}

func (g *fakeGate) Release(ctx context.Context, dealID int64, userID string, qty int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.releases = append(g.releases, gateCall{dealID: dealID, userID: userID, qty: qty, ctxErr: ctx.Err()})
	return g.releaseErr
}

type stockMove struct {
	dealID int64
	qty    int
}

type fakeDeals struct {
	byID       map[int64]domain.Deal
	nextID     int64
	createErr  error
	listErr    error
	commitErr  error
	releaseErr error
	listArgs   []listCall
	commits    []stockMove
	releases   []stockMove
	// releaseCalls counts ReleaseReserved calls; releases has one entry per
	// reservation released.
	releaseCalls int
}

type listCall struct {
	now     time.Time
	afterID int64
	limit   int
}

func newFakeDeals(deals ...domain.Deal) *fakeDeals {
	f := &fakeDeals{byID: map[int64]domain.Deal{}, nextID: 100}
	for _, d := range deals {
		f.byID[d.ID()] = d
	}
	return f
}

func (f *fakeDeals) Create(_ context.Context, d domain.Deal) (domain.Deal, error) {
	if f.createErr != nil {
		return domain.Deal{}, f.createErr
	}
	f.nextID++
	stored, err := domain.RestoreDeal(domain.DealState{
		ID: f.nextID, ProductID: d.ProductID(), Title: d.Title(), Price: d.Price(),
		TotalStock: d.TotalStock(), PerUserLimit: d.PerUserLimit(),
		StartsAt: d.StartsAt(), EndsAt: d.EndsAt(), CreatedAt: t0, UpdatedAt: t0,
	})
	if err != nil {
		return domain.Deal{}, err
	}
	f.byID[stored.ID()] = stored
	return stored, nil
}

func (f *fakeDeals) GetByID(_ context.Context, id int64) (domain.Deal, error) {
	d, ok := f.byID[id]
	if !ok {
		return domain.Deal{}, domain.ErrDealNotFound
	}
	return d, nil
}

func (f *fakeDeals) ListActive(_ context.Context, now time.Time, afterID int64, limit int) ([]domain.Deal, error) {
	f.listArgs = append(f.listArgs, listCall{now: now, afterID: afterID, limit: limit})
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []domain.Deal
	for id := afterID + 1; len(out) < limit && id <= f.nextID+1000; id++ {
		if d, ok := f.byID[id]; ok && d.IsActive(now) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeDeals) CommitReserved(_ context.Context, r domain.Reservation) error {
	f.commits = append(f.commits, stockMove{r.DealID(), r.Quantity()})
	return f.commitErr
}

func (f *fakeDeals) ReleaseReserved(_ context.Context, rs []domain.Reservation) error {
	f.releaseCalls++
	for _, r := range rs {
		f.releases = append(f.releases, stockMove{r.DealID(), r.Quantity()})
	}
	return f.releaseErr
}

type fakeReservations struct {
	byID      map[string]domain.Reservation
	createErr error
	// replayOf, when set, makes Create report an idempotent retry returning
	// this stored reservation.
	replayOf  *domain.Reservation
	updateErr error
	created   []domain.Reservation
	updates   []domain.Reservation
}

func newFakeReservations(rs ...domain.Reservation) *fakeReservations {
	f := &fakeReservations{byID: map[string]domain.Reservation{}}
	for _, r := range rs {
		f.byID[r.ID()] = r
	}
	return f
}

func (f *fakeReservations) Create(_ context.Context, r domain.Reservation) (domain.Reservation, bool, error) {
	if f.createErr != nil {
		return domain.Reservation{}, false, f.createErr
	}
	if f.replayOf != nil {
		return *f.replayOf, false, nil
	}
	f.created = append(f.created, r)
	f.byID[r.ID()] = r
	return r, true, nil
}

func (f *fakeReservations) Get(_ context.Context, id string) (domain.Reservation, error) {
	return f.GetForUpdate(context.Background(), id)
}

func (f *fakeReservations) GetForUpdate(_ context.Context, id string) (domain.Reservation, error) {
	r, ok := f.byID[id]
	if !ok {
		return domain.Reservation{}, domain.ErrReservationNotFound
	}
	return r, nil
}

type fakePayments struct {
	err     error
	charges []usecase.PaymentCharge
}

func (f *fakePayments) Charge(_ context.Context, in usecase.PaymentCharge) error {
	f.charges = append(f.charges, in)
	return f.err
}

func (f *fakeReservations) UpdateStatus(_ context.Context, r domain.Reservation) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updates = append(f.updates, r)
	f.byID[r.ID()] = r
	return nil
}

type fakeOrders struct {
	err     error
	created []domain.Order
}

func (f *fakeOrders) Create(_ context.Context, o domain.Order) error {
	if f.err != nil {
		return f.err
	}
	f.created = append(f.created, o)
	return nil
}

// fakeQueue holds reservations in memory and hands out the pending ones due
// at the requested time, oldest first, like the SQL queue does.
type fakeQueue struct {
	pending   []domain.Reservation
	lockErr   error
	markErr   error
	lockCalls []listCall
	marked    []domain.Reservation
}

func (f *fakeQueue) LockDue(_ context.Context, now time.Time, limit int) ([]domain.Reservation, error) {
	f.lockCalls = append(f.lockCalls, listCall{now: now, limit: limit})
	if f.lockErr != nil {
		return nil, f.lockErr
	}
	var due []domain.Reservation
	for _, r := range f.pending {
		if len(due) < limit && r.IsExpired(now) {
			due = append(due, r)
		}
	}
	return due, nil
}

func (f *fakeQueue) MarkExpired(_ context.Context, rs []domain.Reservation) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.marked = append(f.marked, rs...)
	f.pending = slices.DeleteFunc(f.pending, func(p domain.Reservation) bool {
		return slices.ContainsFunc(rs, func(r domain.Reservation) bool { return r.ID() == p.ID() })
	})
	return nil
}

type fakeLedger struct {
	states map[int64]usecase.GateState
	err    error
	calls  int
}

func (f *fakeLedger) GateState(_ context.Context, dealID int64) (usecase.GateState, error) {
	f.calls++
	if f.err != nil {
		return usecase.GateState{}, f.err
	}
	s, ok := f.states[dealID]
	if !ok {
		return usecase.GateState{}, domain.ErrDealNotFound
	}
	return s, nil
}

type restoreCall struct {
	dealID   int64
	expected int
	want     usecase.GateState
}

// fakeRepair is a gate holding one stock counter per loaded deal. Restore is
// a real compare-and-set over that counter.
type fakeRepair struct {
	stock      map[int64]int
	stockErr   error
	restoreErr error
	// takeBeforeRestore simulates a buyer taking a unit between the pass
	// reading the gate and repairing it.
	takeBeforeRestore bool
	restores          []restoreCall
}

func (f *fakeRepair) Stock(_ context.Context, dealID int64) (int, bool, error) {
	if f.stockErr != nil {
		return 0, false, f.stockErr
	}
	s, ok := f.stock[dealID]
	return s, ok, nil
}

func (f *fakeRepair) Restore(_ context.Context, dealID int64, expected int, want usecase.GateState) (bool, error) {
	f.restores = append(f.restores, restoreCall{dealID: dealID, expected: expected, want: want})
	if f.restoreErr != nil {
		return false, f.restoreErr
	}
	if f.takeBeforeRestore {
		f.stock[dealID]--
	}
	if f.stock[dealID] != expected {
		return false, nil
	}
	f.stock[dealID] = want.Available
	return true, nil
}

// fakeOutbox holds recorded events in memory, oldest first.
type fakeOutbox struct {
	events    []usecase.OutboxEvent
	lockErr   error
	removeErr error
	lockCalls []int
	removed   []int64
}

func (f *fakeOutbox) LockPending(_ context.Context, limit int) ([]usecase.OutboxEvent, error) {
	f.lockCalls = append(f.lockCalls, limit)
	if f.lockErr != nil {
		return nil, f.lockErr
	}
	return slices.Clone(f.events[:min(limit, len(f.events))]), nil
}

func (f *fakeOutbox) Remove(_ context.Context, ids []int64) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, ids...)
	f.events = slices.DeleteFunc(f.events, func(e usecase.OutboxEvent) bool { return slices.Contains(ids, e.ID) })
	return nil
}

type fakePublisher struct {
	err       error
	published []usecase.OutboxEvent
}

func (f *fakePublisher) Publish(_ context.Context, events []usecase.OutboxEvent) error {
	if f.err != nil {
		return f.err
	}
	f.published = append(f.published, events...)
	return nil
}

// fakeTx runs fn directly and records how many transactions were opened.
// It does not simulate rollback; tests assert on which steps ran instead.
type fakeTx struct{ calls int }

func (f *fakeTx) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	f.calls++
	return fn(ctx)
}

// Fixtures.

func usd(t *testing.T, cents int64) domain.Money {
	t.Helper()
	m, err := domain.NewMoney(cents, "USD")
	require.NoError(t, err)
	return m
}

// activeDeal returns a stored deal (ID 7) active around t0: price $49.99,
// 100 units, per-user limit 2.
func activeDeal(t *testing.T) domain.Deal {
	t.Helper()
	d, err := domain.RestoreDeal(domain.DealState{
		ID: 7, ProductID: "sku-1", Title: "Headphones", Price: usd(t, 4999),
		TotalStock: 100, PerUserLimit: 2,
		StartsAt: t0.Add(-time.Hour), EndsAt: t0.Add(time.Hour),
		CreatedAt: t0.Add(-2 * time.Hour), UpdatedAt: t0.Add(-2 * time.Hour),
	})
	require.NoError(t, err)
	return d
}

// storedReservation returns a reservation of 2 units of deal 7 for user-1,
// created at t0 and expiring at t0+5m, in the given status.
func storedReservation(t *testing.T, status domain.ReservationStatus) domain.Reservation {
	t.Helper()
	r, err := domain.RestoreReservation(domain.ReservationState{
		ID: "00000000-0000-7000-8000-999999999999", DealID: 7, UserID: "user-1", Quantity: 2,
		Status: status, IdempotencyKey: "key-1",
		ExpiresAt: t0.Add(5 * time.Minute), CreatedAt: t0, UpdatedAt: t0,
	})
	require.NoError(t, err)
	return r
}
