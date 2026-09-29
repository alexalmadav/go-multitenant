package tenant

import (
	"context"
	"database/sql"
	"errors"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// testPools returns Pools over one stub driver, and a per-tenant count of how
// many times each tenant's pool was opened.
func testPools(t *testing.T, cfg PoolsConfig) (*Pools, *stubDriver, func(uuid.UUID) int) {
	t.Helper()
	d := &stubDriver{}
	var mu sync.Mutex
	opens := map[uuid.UUID]int{}
	p, err := NewPools(cfg, func(_ context.Context, id uuid.UUID) (*sql.DB, error) {
		mu.Lock()
		opens[id]++
		mu.Unlock()
		return d.db(cfg.PerTenantMaxConns), nil
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p, d, func(id uuid.UUID) int { mu.Lock(); defer mu.Unlock(); return opens[id] }
}

func acquire(t *testing.T, p *Pools, id uuid.UUID) (*sql.Conn, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, release, err := p.Acquire(ctx, id)
	if err != nil {
		t.Fatalf("Acquire(%s): %v", id, err)
	}
	return conn, func() { conn.Close(); release() }
}

func acquireWithin(p *Pools, id uuid.UUID, d time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	conn, release, err := p.Acquire(ctx, id)
	if err == nil {
		conn.Close()
		release()
	}
	return err
}

var bigLimits = PoolsConfig{MaxConns: 10, PerTenantMaxConns: 10, MaxWarmTenants: 10, IdleTimeout: time.Hour}

func TestPoolsPerTenantCap(t *testing.T) {
	p, _, _ := testPools(t, PoolsConfig{MaxConns: 10, PerTenantMaxConns: 2, MaxWarmTenants: 10, IdleTimeout: time.Hour})
	a := uuid.New()
	_, r1 := acquire(t, p, a)
	_, r2 := acquire(t, p, a)

	if err := acquireWithin(p, a, 50*time.Millisecond); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("third acquisition for one tenant = %v, want ErrPoolExhausted", err)
	}
	r1()
	if err := acquireWithin(p, a, time.Second); err != nil {
		t.Fatalf("acquisition after a release = %v, want success", err)
	}
	r2()
}

func TestPoolsGlobalCap(t *testing.T) {
	p, _, _ := testPools(t, PoolsConfig{MaxConns: 2, PerTenantMaxConns: 2, MaxWarmTenants: 10, IdleTimeout: time.Hour})
	_, ra := acquire(t, p, uuid.New())
	_, rb := acquire(t, p, uuid.New())
	defer ra()
	defer rb()

	if err := acquireWithin(p, uuid.New(), 50*time.Millisecond); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("acquisition past MaxConns = %v, want ErrPoolExhausted", err)
	}
}

// A saturated tenant's queued requests must not hold global capacity. If
// Acquire took the global slot before the per-tenant one, the queued requests
// for tenant a would each take a global slot and wait, and b would starve.
func TestPoolsSaturatedTenantDoesNotStarveOthers(t *testing.T) {
	p, _, _ := testPools(t, PoolsConfig{MaxConns: 4, PerTenantMaxConns: 2, MaxWarmTenants: 10, IdleTimeout: time.Hour})
	a := uuid.New()
	_, r1 := acquire(t, p, a)
	_, r2 := acquire(t, p, a)
	defer r1()
	defer r2()

	queued, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if conn, release, err := p.Acquire(queued, a); err == nil {
				conn.Close()
				release()
			}
		}()
	}
	// Wait until the ten are observably queued on a's slot: 2 holders + 10 waiters.
	deadline := time.Now().Add(2 * time.Second)
	for {
		p.mu.Lock()
		refs := 0
		if ts := p.slots[a]; ts != nil {
			refs = ts.refs
		}
		p.mu.Unlock()
		if refs == 12 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of 12 acquisitions queued on tenant a's slots", refs)
		}
		time.Sleep(time.Millisecond)
	}

	if err := acquireWithin(p, uuid.New(), 200*time.Millisecond); err != nil {
		t.Fatalf("another tenant could not acquire while one tenant was saturated: %v", err)
	}
	cancel()
	wg.Wait()
}

func TestPoolsReleaseIsIdempotent(t *testing.T) {
	p, _, _ := testPools(t, PoolsConfig{MaxConns: 2, PerTenantMaxConns: 2, MaxWarmTenants: 10, IdleTimeout: time.Hour})
	a := uuid.New()
	conn, release, err := p.Acquire(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	release()

	done := make(chan struct{})
	go func() { release(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a second release blocked; release is not idempotent")
	}

	// Exactly MaxConns must be available again: two succeed, a third waits.
	_, r1 := acquire(t, p, a)
	_, r2 := acquire(t, p, uuid.New())
	defer r1()
	defer r2()
	if err := acquireWithin(p, uuid.New(), 50*time.Millisecond); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("a double release granted extra capacity: third acquisition = %v", err)
	}
}

func TestPoolsEvictLeastRecentlyUsedAndNeverBusy(t *testing.T) {
	p, _, opens := testPools(t, PoolsConfig{MaxConns: 10, PerTenantMaxConns: 2, MaxWarmTenants: 2, IdleTimeout: time.Hour})
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	_, ra := acquire(t, p, a)
	ra()
	time.Sleep(2 * time.Millisecond)
	bConn, rb := acquire(t, p, b) // b stays busy
	defer rb()

	_, rc := acquire(t, p, c) // over MaxWarmTenants: a is idle and oldest; c stays busy until rc
	if got := p.Stats().Warm; got != 2 {
		t.Fatalf("Warm = %d after evicting, want 2", got)
	}

	_, rd := acquire(t, p, d) // b and c both busy: nothing evictable, so exceed
	defer rd()
	if got := p.Stats().Warm; got != 3 {
		t.Errorf("Warm = %d with every pool busy, want 3 (exceeding rather than evicting a busy pool)", got)
	}
	if _, err := bConn.ExecContext(context.Background(), "SELECT 1"); err != nil {
		t.Errorf("busy pool b was closed: %v", err)
	}
	rc()

	_, ra2 := acquire(t, p, a)
	ra2()
	if got := opens(a); got != 2 {
		t.Errorf("tenant a opened %d times, want 2 (it was evicted once)", got)
	}
}

func TestPoolsCancelledWaitReleasesNothing(t *testing.T) {
	p, _, _ := testPools(t, PoolsConfig{MaxConns: 10, PerTenantMaxConns: 1, MaxWarmTenants: 10, IdleTimeout: time.Hour})
	a := uuid.New()
	_, r := acquire(t, p, a)

	if err := acquireWithin(p, a, 30*time.Millisecond); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("wait = %v, want ErrPoolExhausted", err)
	}
	if got := p.Stats().InUseByTenant[a]; got != 1 {
		t.Errorf("InUseByTenant[a] = %d after a cancelled wait, want 1", got)
	}
	r()
	if err := acquireWithin(p, a, time.Second); err != nil {
		t.Errorf("tenant capacity did not return after release: %v", err)
	}
}

// The fake clock replaces p.now after NewPools. That is safe only because a
// one-minute IdleTimeout keeps the janitor from ticking, and so from reading
// p.now, while the test runs.
func TestPoolsCloseIdle(t *testing.T) {
	p, d, _ := testPools(t, PoolsConfig{MaxConns: 10, PerTenantMaxConns: 2, MaxWarmTenants: 10, IdleTimeout: time.Minute})
	now := time.Now()
	p.now = func() time.Time { return now }

	_, r := acquire(t, p, uuid.New())
	r()
	now = now.Add(2 * time.Minute)
	p.closeIdle()

	if got := p.Stats().Warm; got != 0 {
		t.Errorf("Warm = %d after IdleTimeout, want 0", got)
	}
	if opened, closed := d.counts(); closed != opened {
		t.Errorf("stub connections opened %d, closed %d; the idle pool was not closed", opened, closed)
	}
}

func TestPoolsEvictWhileInUse(t *testing.T) {
	p, d, opens := testPools(t, bigLimits)
	a := uuid.New()
	conn, release, err := p.Acquire(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}

	p.Evict(a)
	if _, err := conn.ExecContext(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("an in-use connection stopped working when its pool was evicted: %v", err)
	}
	conn.Close()
	release()
	if opened, closed := d.counts(); closed != opened {
		t.Errorf("the evicted pool was not closed once its last connection was released (opened %d, closed %d)", opened, closed)
	}

	_, r := acquire(t, p, a)
	r()
	if got := opens(a); got != 2 {
		t.Errorf("tenant a opened %d times, want 2 (a fresh pool after eviction)", got)
	}
}

func TestPoolsOpenerErrorIsNotCached(t *testing.T) {
	d := &stubDriver{}
	var calls atomic.Int32
	p, err := NewPools(bigLimits, func(context.Context, uuid.UUID) (*sql.DB, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("login failed")
		}
		return d.db(10), nil
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	a := uuid.New()
	if err := acquireWithin(p, a, time.Second); err == nil || errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("first acquisition = %v, want the opener's error", err)
	}
	if err := acquireWithin(p, a, time.Second); err != nil {
		t.Fatalf("second acquisition = %v, want success; a failed open must not be cached", err)
	}
}

func TestPoolsNeverExceedLimitsUnderLoad(t *testing.T) {
	cfg := PoolsConfig{MaxConns: 8, PerTenantMaxConns: 3, MaxWarmTenants: 6, IdleTimeout: time.Hour}
	p, _, _ := testPools(t, cfg)
	tenants := make([]uuid.UUID, 20)
	for i := range tenants {
		tenants[i] = uuid.New()
	}

	var inUse, peak atomic.Int64
	perTenant := make([]atomic.Int64, len(tenants))
	perPeak := make([]atomic.Int64, len(tenants))
	raise := func(peak *atomic.Int64, n int64) {
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				return
			}
		}
	}

	var wg sync.WaitGroup
	for w := 0; w < 50; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < 200; i++ {
				k := rng.Intn(len(tenants))
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				conn, release, err := p.Acquire(ctx, tenants[k])
				cancel()
				if err != nil {
					t.Errorf("Acquire: %v", err)
					return
				}
				raise(&peak, inUse.Add(1))
				raise(&perPeak[k], perTenant[k].Add(1))
				time.Sleep(20 * time.Microsecond)
				perTenant[k].Add(-1)
				inUse.Add(-1)
				conn.Close()
				release()
			}
		}(int64(w))
	}
	wg.Wait()

	if got := peak.Load(); got > int64(cfg.MaxConns) {
		t.Errorf("peak connections in use = %d, over MaxConns %d", got, cfg.MaxConns)
	}
	for k := range perPeak {
		if got := perPeak[k].Load(); got > int64(cfg.PerTenantMaxConns) {
			t.Errorf("tenant %d peaked at %d connections, over PerTenantMaxConns %d", k, got, cfg.PerTenantMaxConns)
		}
	}
	if s := p.Stats(); s.InUse != 0 {
		t.Errorf("InUse = %d after every release, want 0", s.InUse)
	}
}

func TestNewPoolsRejectsNonPositiveLimits(t *testing.T) {
	if _, err := NewPools(PoolsConfig{MaxConns: 0, PerTenantMaxConns: 1, MaxWarmTenants: 1, IdleTimeout: time.Second}, nil, zap.NewNop()); err == nil {
		t.Error("NewPools accepted MaxConns 0")
	}
}

// blockingPools returns Pools whose opener signals on started, then waits for
// gate to close before opening.
func blockingPools(t *testing.T, cfg PoolsConfig) (*Pools, *stubDriver, chan struct{}, chan struct{}) {
	t.Helper()
	d := &stubDriver{}
	started := make(chan struct{}, 16)
	gate := make(chan struct{})
	p, err := NewPools(cfg, func(context.Context, uuid.UUID) (*sql.DB, error) {
		started <- struct{}{}
		<-gate
		return d.db(cfg.PerTenantMaxConns), nil
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p, d, started, gate
}

func waitForInUse(t *testing.T, p *Pools, id uuid.UUID, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		p.mu.Lock()
		got := 0
		if e := p.entries[id]; e != nil {
			got = e.inUse
		}
		p.mu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("entry in-use count = %d, want %d", got, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPoolsCloseWhileAcquireInFlight(t *testing.T) {
	p, d, started, gate := blockingPools(t, bigLimits)
	a := uuid.New()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, release, err := p.Acquire(context.Background(), a)
		if err == nil {
			conn.Close()
			release()
		}
	}()
	<-started
	p.Close()
	close(gate)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire did not return after Close and the opener finishing")
	}
	if opened, closed := d.counts(); closed != opened {
		t.Errorf("stub connections opened %d, closed %d; a pool leaked", opened, closed)
	}
}

func TestPoolsCloseWithConnectionInUse(t *testing.T) {
	p, d, _ := testPools(t, bigLimits)
	conn, release, err := p.Acquire(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("an in-use connection stopped working when Pools closed: %v", err)
	}
	conn.Close()
	release()
	if opened, closed := d.counts(); closed != opened {
		t.Errorf("opened %d, closed %d; the pool was not closed after its last release", opened, closed)
	}
}

func TestPoolsCancelledOpenerDoesNotFailOthers(t *testing.T) {
	p, _, started, gate := blockingPools(t, bigLimits)
	a := uuid.New()

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	err1 := make(chan error, 1)
	go func() {
		_, _, err := p.Acquire(ctx1, a)
		err1 <- err
	}()
	<-started

	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	type result struct {
		conn    *sql.Conn
		release func()
		err     error
	}
	res2 := make(chan result, 1)
	go func() {
		c, r, err := p.Acquire(ctx2, a)
		res2 <- result{c, r, err}
	}()
	waitForInUse(t, p, a, 2)

	cancel1()
	if err := <-err1; err == nil {
		t.Error("requester 1 succeeded after its context was cancelled")
	}
	close(gate)

	r := <-res2
	if r.err != nil {
		t.Fatalf("requester 2 failed because requester 1 was cancelled: %v", r.err)
	}
	r.conn.Close()
	r.release()
}

func TestPoolsOpenerPanicDoesNotWedge(t *testing.T) {
	d := &stubDriver{}
	var calls atomic.Int32
	p, err := NewPools(bigLimits, func(context.Context, uuid.UUID) (*sql.DB, error) {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		return d.db(10), nil
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	a := uuid.New()
	start := time.Now()
	err = acquireWithin(p, a, time.Second)
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("first acquisition = %v, want an error mentioning \"panicked\"", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("panicking opener took %v to report; waiters were wedged", time.Since(start))
	}
	if err := acquireWithin(p, a, time.Second); err != nil {
		t.Fatalf("second acquisition = %v, want success", err)
	}
}
