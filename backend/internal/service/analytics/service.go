// Package analytics is the service layer for reports: the dashboard, the sales
// breakdowns, the trading result, the receivables ladder and the GST summary.
//
// Everything here is read-only. There is no write path in this package and no
// transaction that can change a row, which is why the reports can be run
// freely by anyone who can see the store.
//
// # Consistency
//
// A report that issues five queries and adds their answers together is only
// meaningful if the five saw the same database. Under Postgres's default READ
// COMMITTED every statement takes a fresh snapshot, so a sale landing between
// the revenue query and the cost-of-goods query produces a profit figure that
// belongs to no instant at all. The multi-query reports therefore run inside
// one REPEATABLE READ, read-only transaction; see reportSnapshot.
//
// # Caching
//
// The cache is optional and off unless a composition root calls WithCache.
// What may and may not be cached is decided here rather than at the call site:
//
//   - Cached: sales by category, top products, and the multi-store rollup.
//     Each is a multi-table scan over a range of history that does not change,
//     each is refreshed by a human staring at a screen, and each is a figure
//     nobody acts on within the minute.
//
//   - Never cached: anything a write decision depends on. The credit
//     outstanding on the dashboard and the whole credit aging report are
//     balances someone reads before deciding to extend more credit — a stale
//     one authorises a sale that the live balance would have refused. The
//     GSTR-3B summary is prepared to be filed with the government. The profit
//     and loss report is what a shopkeeper decides on. Hourly sales for today
//     changes with every sale and is one day of one store's invoices anyway.
//
// The dashboard as a whole is not cached, because it carries the live credit
// balance. Its own queries are a single index scan each over
// idx_invoices_store_date.
package analytics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	domain "gbos/internal/domain/analytics"
	"gbos/internal/domain/money"
	"gbos/internal/platform/cache"
	"gbos/internal/platform/log"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// ErrStoreRequired is returned when a store-scoped report is asked for without
// a store. Every report here is store-scoped; MultiStore is the exception and
// takes its stores as an explicit list.
var ErrStoreRequired = errors.New("store is required")

// ErrNoStores is returned when the multi-store rollup is asked for with no
// stores.
//
// The handler this replaces treated an empty list as "every store in the
// system", so a request that forgot its parameter silently widened into a
// group-wide report. An owner asking about their shops can name them.
var ErrNoStores = errors.New("multi-store analytics needs at least one store")

// DefaultTimeZone is the zone hours are bucketed in when none is configured.
// A timestamptz has no hour of its own; one has to be chosen, and for this
// system it is the shop's.
const DefaultTimeZone = "Asia/Kolkata"

// DefaultCacheTTL is how long a cached report stays fresh when WithCache is
// given a non-positive TTL. A minute is long enough to absorb a dashboard that
// polls and short enough that nobody notices the lag.
const DefaultCacheTTL = time.Minute

// CacheNamespace prefixes every key this service writes, so that a single
// DeletePrefix drops every cached report at once.
const CacheNamespace = "analytics:"

// reportSnapshot is the transaction the multi-query reports run in.
//
// REPEATABLE READ, not the default: at READ COMMITTED each statement takes its
// own snapshot, so revenue and cost of goods sold could be read either side of
// a sale and the resulting gross profit would describe no moment in time. Read
// only, because none of these queries has any business writing, and saying so
// turns a mistake into an error rather than a corruption.
var reportSnapshot = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}

// Repository is the persistence this service needs. Every method takes a
// Querier, so the service decides which of them share a snapshot.
type Repository interface {
	SalesTotals(ctx context.Context, q platform.Querier, storeID uuid.UUID, rng domain.Range) (domain.SalesTotals, error)
	LowStockCount(ctx context.Context, q platform.Querier, storeID uuid.UUID) (int64, error)

	SalesByCategory(ctx context.Context, q platform.Querier, storeID uuid.UUID, rng domain.Range) ([]domain.CategorySales, error)
	TopProducts(ctx context.Context, q platform.Querier, storeID uuid.UUID, rng domain.Range, limit int) ([]domain.ProductSales, error)
	HourlySales(ctx context.Context, q platform.Querier, storeID uuid.UUID, day time.Time, tz string) ([]domain.HourBucket, error)
	PaymentModes(ctx context.Context, q platform.Querier, storeID uuid.UUID, rng domain.Range) ([]domain.PaymentModeTotal, error)
	StoreSales(ctx context.Context, q platform.Querier, storeIDs []uuid.UUID, rng domain.Range) ([]domain.StoreSales, error)

	COGS(ctx context.Context, q platform.Querier, storeID uuid.UUID, rng domain.Range) (money.Money, error)
	ReturnsTotal(ctx context.Context, q platform.Querier, storeID uuid.UUID, rng domain.Range) (money.Money, error)
	InventoryLossValue(ctx context.Context, q platform.Querier, storeID uuid.UUID, rng domain.Range) (money.Money, error)
	ExpensesTotal(ctx context.Context, q platform.Querier, storeID uuid.UUID, rng domain.Range) (money.Money, error)

	CreditAging(ctx context.Context, q platform.Querier, storeID uuid.UUID, asOf time.Time) ([]domain.CustomerAging, error)

	OutwardSupplies(ctx context.Context, q platform.Querier, storeID uuid.UUID, from, to time.Time) (domain.TaxSummary, error)
	InwardSupplies(ctx context.Context, q platform.Querier, storeID uuid.UUID, from, to time.Time) (domain.TaxSummary, error)
}

// CreditReader is the one figure this service borrows from the credit
// aggregate.
//
// It is borrowed rather than re-queried on purpose. "What still counts as
// money owed" is a rule the credit aggregate owns, and the implementation this
// replaces had it written out by hand in two different forms inside this one
// report file — `status IN ('pending','partial')` on the dashboard and
// `status != 'paid'` in the aging report — so the two disagreed by the value of
// every voided sale.
type CreditReader interface {
	StoreOutstanding(ctx context.Context, storeID uuid.UUID) (money.Money, error)
}

// DB is the database capability this service needs: a read surface, and the
// ability to hold several reads in one snapshot.
type DB interface {
	platform.Querier
	InTxOpts(ctx context.Context, opts *sql.TxOptions, fn func(ctx context.Context, tx platform.Tx) error) error
}

// Service runs the reports.
type Service struct {
	db     DB
	repo   Repository
	credit CreditReader

	// cache is nil unless WithCache was called. Only the three reports named
	// in the package comment ever consult it.
	cache    cache.Cache
	cacheTTL time.Duration

	// timezone is the IANA zone hourly sales are bucketed in.
	timezone string

	now func() time.Time
}

// New returns an analytics service with caching off.
func New(db DB, repo Repository, credit CreditReader) *Service {
	return &Service{
		db:       db,
		repo:     repo,
		credit:   credit,
		timezone: DefaultTimeZone,
		now:      time.Now,
	}
}

// WithClock replaces the clock, for tests that need a fixed today.
func (s *Service) WithClock(now func() time.Time) *Service {
	clone := *s
	clone.now = now
	return &clone
}

// WithTimeZone sets the IANA zone hourly sales are bucketed in. An empty name
// restores the default. The name is validated by Postgres, which rejects an
// unknown zone rather than quietly bucketing in UTC.
func (s *Service) WithTimeZone(tz string) *Service {
	clone := *s
	clone.timezone = strings.TrimSpace(tz)
	if clone.timezone == "" {
		clone.timezone = DefaultTimeZone
	}
	return &clone
}

// WithCache turns caching on for the reports that are allowed to be cached. A
// nil cache turns it off again. A non-positive ttl means DefaultCacheTTL.
//
// Nothing a write decision depends on passes through here; see the package
// comment for the list and the reasoning.
func (s *Service) WithCache(c cache.Cache, ttl time.Duration) *Service {
	clone := *s
	clone.cache = c
	clone.cacheTTL = ttl
	if clone.cacheTTL <= 0 {
		clone.cacheTTL = DefaultCacheTTL
	}
	return &clone
}

// InvalidateCache drops every cached report. A composition root can call it
// after a bulk import or a backdated correction, when waiting out the TTL
// would leave the screens visibly wrong.
func (s *Service) InvalidateCache(ctx context.Context) error {
	if s.cache == nil {
		return nil
	}
	return s.cache.DeletePrefix(ctx, CacheNamespace)
}

// ---------------------------------------------------------------------------
// Dashboard
// ---------------------------------------------------------------------------

// Dashboard returns a store's headline figures for a named period.
func (s *Service) Dashboard(ctx context.Context, storeID uuid.UUID, period domain.Period) (domain.Dashboard, error) {
	return s.DashboardForRange(ctx, storeID, period.Range(s.now()))
}

// DashboardForRange returns a store's headline figures for an explicit range.
//
// The sales figures and the low-stock count come from one snapshot. The credit
// balance is read afterwards, outside it and outside any cache, so that the
// number a shopkeeper looks at before extending more credit is the number the
// credit-limit check would use.
func (s *Service) DashboardForRange(ctx context.Context, storeID uuid.UUID, rng domain.Range) (domain.Dashboard, error) {
	if storeID == uuid.Nil {
		return domain.Dashboard{}, ErrStoreRequired
	}

	var (
		totals   domain.SalesTotals
		lowStock int64
	)
	err := s.db.InTxOpts(ctx, reportSnapshot, func(ctx context.Context, tx platform.Tx) error {
		var err error
		if totals, err = s.repo.SalesTotals(ctx, tx, storeID, rng); err != nil {
			return err
		}
		lowStock, err = s.repo.LowStockCount(ctx, tx, storeID)
		return err
	})
	if err != nil {
		return domain.Dashboard{}, err
	}

	outstanding, err := s.credit.StoreOutstanding(ctx, storeID)
	if err != nil {
		// Returned, not swallowed. The handler this replaces set the balance to
		// zero when this read failed, so a database hiccup was displayed as
		// "no customer owes this shop anything".
		return domain.Dashboard{}, fmt.Errorf("read credit outstanding: %w", err)
	}

	return domain.NewDashboard(rng, totals, lowStock, outstanding), nil
}

// ---------------------------------------------------------------------------
// Sales breakdowns
// ---------------------------------------------------------------------------

// SalesByCategory returns a store's takings grouped by product category,
// largest first. Cached.
func (s *Service) SalesByCategory(ctx context.Context, storeID uuid.UUID, rng domain.Range) ([]domain.CategorySales, error) {
	if storeID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	key := cacheKey("category", storeID.String(), rng.String())
	return cached(ctx, s, key, func() ([]domain.CategorySales, error) {
		return s.repo.SalesByCategory(ctx, s.db, storeID, rng)
	})
}

// TopProducts returns a store's best sellers over a range. Cached.
func (s *Service) TopProducts(ctx context.Context, storeID uuid.UUID, rng domain.Range, limit int) ([]domain.ProductSales, error) {
	if storeID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	key := cacheKey("top-products", storeID.String(), rng.String(), fmt.Sprint(limit))
	return cached(ctx, s, key, func() ([]domain.ProductSales, error) {
		return s.repo.TopProducts(ctx, s.db, storeID, rng, limit)
	})
}

// HourlySales returns one trading day bucketed by hour, in the store's
// timezone. A zero day means today.
//
// Not cached: this is the report someone watches during the day to decide when
// to put a second person on the counter, and it changes with every sale.
func (s *Service) HourlySales(ctx context.Context, storeID uuid.UUID, day time.Time) ([]domain.HourBucket, error) {
	if storeID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	if day.IsZero() {
		day = s.now()
	}
	return s.repo.HourlySales(ctx, s.db, storeID, day, s.timezone)
}

// PaymentModes returns a store's takings split by how they were settled.
//
// Not cached: this is the report a till is balanced against at close of
// business, and a figure that is a minute stale is a cash drawer that does not
// reconcile.
func (s *Service) PaymentModes(ctx context.Context, storeID uuid.UUID, rng domain.Range) ([]domain.PaymentModeTotal, error) {
	if storeID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	return s.repo.PaymentModes(ctx, s.db, storeID, rng)
}

// MultiStore returns the owner's rollup across the named stores. Cached.
//
// The stores are an explicit list. Authorising the request is the caller's job
// — this is the one report that crosses the store boundary every other report
// is scoped by, and it should be reachable only by an owner.
func (s *Service) MultiStore(ctx context.Context, storeIDs []uuid.UUID, period domain.Period) (domain.MultiStore, error) {
	return s.MultiStoreForRange(ctx, storeIDs, period.Range(s.now()))
}

// MultiStoreForRange is MultiStore over an explicit range.
func (s *Service) MultiStoreForRange(ctx context.Context, storeIDs []uuid.UUID, rng domain.Range) (domain.MultiStore, error) {
	if len(storeIDs) == 0 {
		return domain.MultiStore{}, ErrNoStores
	}
	ids := dedupeStores(storeIDs)
	if len(ids) == 0 {
		return domain.MultiStore{}, ErrNoStores
	}

	key := cacheKey("multi-store", joinStores(ids), rng.String())
	stores, err := cached(ctx, s, key, func() ([]domain.StoreSales, error) {
		return s.repo.StoreSales(ctx, s.db, ids, rng)
	})
	if err != nil {
		return domain.MultiStore{}, err
	}
	return domain.SummariseStores(rng, stores), nil
}

// ---------------------------------------------------------------------------
// Profit and loss
// ---------------------------------------------------------------------------

// ProfitAndLoss returns a store's trading result for a range.
//
// All five reads share one snapshot, so revenue, cost of goods sold, returns,
// losses and expenses all describe the same instant.
//
// Not cached: this is a decision document, and it is run rarely enough that the
// cost of getting it live is irrelevant.
func (s *Service) ProfitAndLoss(ctx context.Context, storeID uuid.UUID, rng domain.Range) (domain.ProfitAndLoss, error) {
	if storeID == uuid.Nil {
		return domain.ProfitAndLoss{}, ErrStoreRequired
	}

	in := domain.ProfitInput{Range: rng}
	err := s.db.InTxOpts(ctx, reportSnapshot, func(ctx context.Context, tx platform.Tx) error {
		var err error
		if in.Sales, err = s.repo.SalesTotals(ctx, tx, storeID, rng); err != nil {
			return err
		}
		if in.COGS, err = s.repo.COGS(ctx, tx, storeID, rng); err != nil {
			return err
		}
		if in.Returns, err = s.repo.ReturnsTotal(ctx, tx, storeID, rng); err != nil {
			return err
		}
		if in.Losses, err = s.repo.InventoryLossValue(ctx, tx, storeID, rng); err != nil {
			return err
		}
		in.Expenses, err = s.repo.ExpensesTotal(ctx, tx, storeID, rng)
		return err
	})
	if err != nil {
		return domain.ProfitAndLoss{}, err
	}

	// Every one of those five reads returns its error. The handler this
	// replaces discarded four of the five and substituted zero, so a broken
	// query showed as a shop with no costs and an enormous profit.
	return domain.ComputeProfitAndLoss(in), nil
}

// ---------------------------------------------------------------------------
// Receivables
// ---------------------------------------------------------------------------

// CreditAging returns the store's outstanding receivables split by age, oldest
// balances first. A zero asOf means today.
//
// Never cached. This is the report a shopkeeper reads before deciding whether
// to let a customer take goods on credit again, and a balance that is a minute
// out of date is a balance that authorises a sale the live figure would refuse.
func (s *Service) CreditAging(ctx context.Context, storeID uuid.UUID, asOf time.Time) (domain.AgingReport, error) {
	if storeID == uuid.Nil {
		return domain.AgingReport{}, ErrStoreRequired
	}
	if asOf.IsZero() {
		asOf = s.now()
	}
	customers, err := s.repo.CreditAging(ctx, s.db, storeID, asOf)
	if err != nil {
		return domain.AgingReport{}, err
	}
	return domain.SummariseAging(asOf, customers), nil
}

// ---------------------------------------------------------------------------
// GST
// ---------------------------------------------------------------------------

// GSTR3B returns a month's outward and inward tax position for one store.
//
// The two halves share a snapshot: the net figure is their difference, and a
// difference between numbers read at two different instants is not a
// difference of anything.
//
// Never cached. These figures are transcribed onto a statutory return.
func (s *Service) GSTR3B(ctx context.Context, storeID uuid.UUID, month string) (domain.GSTR3B, error) {
	if storeID == uuid.Nil {
		return domain.GSTR3B{}, ErrStoreRequired
	}
	from, to, err := domain.ParseMonth(month)
	if err != nil {
		return domain.GSTR3B{}, err
	}

	var outward, inward domain.TaxSummary
	err = s.db.InTxOpts(ctx, reportSnapshot, func(ctx context.Context, tx platform.Tx) error {
		var err error
		if outward, err = s.repo.OutwardSupplies(ctx, tx, storeID, from, to); err != nil {
			return err
		}
		inward, err = s.repo.InwardSupplies(ctx, tx, storeID, from, to)
		return err
	})
	if err != nil {
		return domain.GSTR3B{}, err
	}
	return domain.NewGSTR3B(month, storeID, outward, inward), nil
}

// ---------------------------------------------------------------------------
// Caching plumbing
// ---------------------------------------------------------------------------

// cacheKey builds a namespaced key. The parts are joined with a separator that
// cannot appear in a uuid or a date, so two different reports cannot collide.
func cacheKey(report string, parts ...string) string {
	return CacheNamespace + report + ":" + strings.Join(parts, "|")
}

// cached serves a report from the cache when one is configured and warm, and
// otherwise runs load and stores the result.
//
// A cache failure is logged and ignored: a report must degrade to a database
// read, never to an error. A decode failure is treated the same way and is
// logged more loudly, because it means something wrote an incompatible value
// under this key — usually a type that changed without its key being versioned.
func cached[T any](ctx context.Context, s *Service, key string, load func() (T, error)) (T, error) {
	if s.cache == nil {
		return load()
	}

	value, err := cache.GetJSON[T](ctx, s.cache, key)
	switch {
	case err == nil:
		return value, nil
	case errors.Is(err, cache.ErrMiss):
		// Normal: fall through to the database.
	default:
		log.FromContext(ctx).Warn("analytics: cache read failed, falling back to the database",
			"key", key, "error", err)
	}

	fresh, err := load()
	if err != nil {
		return fresh, err
	}
	if err := cache.SetJSON(ctx, s.cache, key, fresh, s.cacheTTL); err != nil {
		log.FromContext(ctx).Warn("analytics: cache write failed", "key", key, "error", err)
	}
	return fresh, nil
}

// dedupeStores sorts and deduplicates a store list, so that the same set of
// stores in a different order is the same cache key and the same query.
func dedupeStores(ids []uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ids))
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if id == uuid.Nil || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func joinStores(ids []uuid.UUID) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = id.String()
	}
	return strings.Join(parts, ",")
}
