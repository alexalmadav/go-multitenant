package tenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ErrPoolExhausted is returned when a tenant connection could not be
// acquired before the request's context ended, because the tenant, or every
// tenant together, was at its connection limit.
var ErrPoolExhausted = errors.New("tenant: connection pool exhausted")

// poolOpenTimeout bounds one opener call, which runs detached from the
// requests waiting on it.
const poolOpenTimeout = 30 * time.Second

var errPoolsClosed = errors.New("tenant: connection pools are closed")

// PoolsConfig bounds Pools. Every field must be positive; see
// RoleIsolationConfig.WithDefaults.
type PoolsConfig struct {
	MaxConns          int
	PerTenantMaxConns int
	MaxWarmTenants    int
	IdleTimeout       time.Duration
}

// PoolOpener opens the *sql.DB for one tenant. Pools calls it outside any
// lock, once per warm period of a tenant.
type PoolOpener func(ctx context.Context, tenantID uuid.UUID) (*sql.DB, error)

// PoolStats is a snapshot of Pools.
type PoolStats struct {
	Warm          int               // tenant pools open
	InUse         int               // connections in use, all tenants
	InUseByTenant map[uuid.UUID]int // connections in use, for each tenant with any
	Hits          uint64            // acquisitions served by an already-open pool
	ColdOpens     uint64            // pools opened
	Evictions     uint64            // pools closed to stay within MaxWarmTenants
	SlotWaits     uint64            // acquisitions that had to wait for a slot
}

type poolEntry struct {
	db       *sql.DB
	ready    chan struct{} // closed once db or err is set
	err      error
	inUse    int
	lastUsed time.Time
	retired  bool // removed from the map; close db when inUse reaches 0
}

type tenantSlots struct {
	sem  chan struct{}
	refs int // acquisitions holding or waiting for a slot
}

// Pools keeps one small *sql.DB per tenant, bounded per tenant and across all
// tenants.
//
// An acquisition takes a per-tenant slot, then a global slot, then a
// connection. The order is the fairness guarantee: a tenant at its own limit
// queues on its per-tenant slot, holding no global capacity, so it cannot
// starve other tenants.
type Pools struct {
	cfg    PoolsConfig
	open   PoolOpener
	logger *zap.Logger
	global chan struct{}
	now    func() time.Time

	mu      sync.Mutex
	slots   map[uuid.UUID]*tenantSlots
	entries map[uuid.UUID]*poolEntry
	stats   PoolStats
	closed  bool

	done chan struct{}
	wg   sync.WaitGroup
}

// NewPools returns Pools that open tenant pools with open.
func NewPools(cfg PoolsConfig, open PoolOpener, logger *zap.Logger) (*Pools, error) {
	if cfg.MaxConns <= 0 || cfg.PerTenantMaxConns <= 0 || cfg.MaxWarmTenants <= 0 || cfg.IdleTimeout <= 0 {
		return nil, fmt.Errorf("tenant: pool limits must be positive: %+v", cfg)
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	p := &Pools{
		cfg:     cfg,
		open:    open,
		logger:  logger.Named("tenant_pools"),
		global:  make(chan struct{}, cfg.MaxConns),
		now:     time.Now,
		slots:   map[uuid.UUID]*tenantSlots{},
		entries: map[uuid.UUID]*poolEntry{},
		done:    make(chan struct{}),
	}
	p.wg.Add(1)
	go p.janitor()
	return p, nil
}

// Acquire returns a connection logged in as the tenant's role, and a release
// function. The caller closes the connection and then calls release, which is
// safe to call more than once.
func (p *Pools) Acquire(ctx context.Context, tenantID uuid.UUID) (*sql.Conn, func(), error) {
	ts := p.takeTenantSlots(tenantID)
	waited := false

	select {
	case ts.sem <- struct{}{}:
	default:
		waited = true
		select {
		case ts.sem <- struct{}{}:
		case <-ctx.Done():
			p.dropTenantSlots(tenantID)
			return nil, nil, fmt.Errorf("%w: tenant %s is at its limit of %d connections: %v", ErrPoolExhausted, tenantID, p.cfg.PerTenantMaxConns, ctx.Err())
		}
	}

	select {
	case p.global <- struct{}{}:
	default:
		waited = true
		select {
		case p.global <- struct{}{}:
		case <-ctx.Done():
			<-ts.sem
			p.dropTenantSlots(tenantID)
			return nil, nil, fmt.Errorf("%w: all tenants are at the limit of %d connections: %v", ErrPoolExhausted, p.cfg.MaxConns, ctx.Err())
		}
	}
	if waited {
		p.mu.Lock()
		p.stats.SlotWaits++
		p.mu.Unlock()
	}
	releaseSlots := func() {
		<-p.global
		<-ts.sem
		p.dropTenantSlots(tenantID)
	}

	e, db, err := p.entry(ctx, tenantID)
	if err != nil {
		releaseSlots()
		return nil, nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		p.finish(e)
		releaseSlots()
		return nil, nil, fmt.Errorf("tenant: connection for %s: %w", tenantID, err)
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			p.finish(e)
			releaseSlots()
		})
	}
	return conn, release, nil
}

// entry returns the tenant's open pool and its database, opening the pool if
// needed. The entry's in-use count is already raised, so it cannot be evicted
// or closed before the caller finishes with it. The database is read under
// the lock; callers must use it rather than reading e.db.
func (p *Pools) entry(ctx context.Context, id uuid.UUID) (*poolEntry, *sql.DB, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, nil, errPoolsClosed
	}
	var evicted []*sql.DB
	e, ok := p.entries[id]
	if ok {
		select {
		case <-e.ready:
			p.stats.Hits++
		default:
		}
	} else {
		e = &poolEntry{ready: make(chan struct{})}
		p.entries[id] = e
		p.stats.ColdOpens++
		evicted = p.evictLocked(id)
		go p.openEntry(id, e)
	}
	e.inUse++
	e.lastUsed = p.now()
	p.mu.Unlock()
	closeAll(evicted)

	select {
	case <-e.ready:
	case <-ctx.Done():
		p.finish(e)
		return nil, nil, fmt.Errorf("tenant: waiting for %s's pool to open: %w", id, ctx.Err())
	}

	p.mu.Lock()
	db, err := e.db, e.err
	p.mu.Unlock()
	if err != nil {
		p.finish(e)
		return nil, nil, err
	}
	return e, db, nil
}

// openEntry opens an entry's database. It runs detached from every request,
// so one requester giving up cannot fail the others waiting on the same open.
// It holds no in-use count and is not waited for by Close.
func (p *Pools) openEntry(id uuid.UUID, e *poolEntry) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), poolOpenTimeout)
	defer cancel()

	var db *sql.DB
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				db, err = nil, fmt.Errorf("tenant: opening pool for %s panicked: %v", id, r)
			}
		}()
		db, err = p.open(ctx, id)
	}()

	var toClose *sql.DB
	p.mu.Lock()
	e.db, e.err = db, err
	close(e.ready)
	if err != nil {
		if p.entries[id] == e {
			delete(p.entries, id)
		}
	} else if (e.retired || p.closed) && e.inUse == 0 {
		toClose, e.db = e.db, nil
	}
	p.mu.Unlock()
	if toClose != nil {
		toClose.Close()
	}
}

// evictLocked removes least-recently-used idle pools until at most
// MaxWarmTenants remain, never the one for keep and never one in use. It
// returns the removed pools' databases for the caller to close outside the
// lock. If nothing is evictable the warm count stays above the limit.
func (p *Pools) evictLocked(keep uuid.UUID) []*sql.DB {
	var out []*sql.DB
	for len(p.entries) > p.cfg.MaxWarmTenants {
		var victim uuid.UUID
		var oldest *poolEntry
		for id, e := range p.entries {
			if id == keep || e.inUse > 0 || e.db == nil {
				continue
			}
			if oldest == nil || e.lastUsed.Before(oldest.lastUsed) {
				victim, oldest = id, e
			}
		}
		if oldest == nil {
			break
		}
		delete(p.entries, victim)
		out = append(out, oldest.db)
		oldest.db = nil
		p.stats.Evictions++
	}
	return out
}

// finish lowers an entry's in-use count, closing it if it was retired.
func (p *Pools) finish(e *poolEntry) {
	p.mu.Lock()
	e.inUse--
	e.lastUsed = p.now()
	var toClose *sql.DB
	if e.retired && e.inUse == 0 {
		toClose, e.db = e.db, nil
	}
	p.mu.Unlock()
	if toClose != nil {
		toClose.Close()
	}
}

// Evict closes a tenant's pool, for example when the tenant is suspended.
// Connections already in use keep working until released; the next
// acquisition opens a fresh pool.
func (p *Pools) Evict(tenantID uuid.UUID) {
	p.mu.Lock()
	e, ok := p.entries[tenantID]
	if !ok {
		p.mu.Unlock()
		return
	}
	delete(p.entries, tenantID)
	var toClose *sql.DB
	if e.inUse == 0 && e.db != nil {
		toClose, e.db = e.db, nil
	} else {
		e.retired = true // in use, or still opening: the last user or the opener closes it
	}
	p.mu.Unlock()
	if toClose != nil {
		toClose.Close()
	}
}

func (p *Pools) takeTenantSlots(id uuid.UUID) *tenantSlots {
	p.mu.Lock()
	defer p.mu.Unlock()
	ts, ok := p.slots[id]
	if !ok {
		ts = &tenantSlots{sem: make(chan struct{}, p.cfg.PerTenantMaxConns)}
		p.slots[id] = ts
	}
	ts.refs++
	return ts
}

func (p *Pools) dropTenantSlots(id uuid.UUID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ts := p.slots[id]
	ts.refs--
	if ts.refs == 0 {
		delete(p.slots, id)
	}
}

func (p *Pools) janitor() {
	defer p.wg.Done()
	interval := p.cfg.IdleTimeout / 4
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-tick.C:
			p.closeIdle()
		}
	}
}

// closeIdle closes every pool with nothing in use for IdleTimeout.
func (p *Pools) closeIdle() {
	now := p.now()
	var toClose []*sql.DB
	p.mu.Lock()
	for id, e := range p.entries {
		if e.inUse == 0 && e.db != nil && now.Sub(e.lastUsed) >= p.cfg.IdleTimeout {
			delete(p.entries, id)
			toClose = append(toClose, e.db)
			e.db = nil
		}
	}
	p.mu.Unlock()
	closeAll(toClose)
}

// Stats returns a snapshot of the pools.
func (p *Pools) Stats() PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stats
	s.InUse = len(p.global)
	s.InUseByTenant = map[uuid.UUID]int{}
	for id, ts := range p.slots {
		if n := len(ts.sem); n > 0 {
			s.InUseByTenant[id] = n
		}
	}
	for _, e := range p.entries {
		if e.db != nil {
			s.Warm++
		}
	}
	return s
}

// Close closes every tenant pool and stops the idle janitor.
func (p *Pools) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	close(p.done)
	var dbs []*sql.DB
	for id, e := range p.entries {
		delete(p.entries, id)
		if e.inUse == 0 && e.db != nil {
			dbs = append(dbs, e.db)
			e.db = nil
		} else {
			e.retired = true // in use, or still opening: the last user or the opener closes it
		}
	}
	p.mu.Unlock()
	p.wg.Wait()

	var errs []error
	for _, db := range dbs {
		if err := db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func closeAll(dbs []*sql.DB) {
	for _, db := range dbs {
		db.Close()
	}
}
