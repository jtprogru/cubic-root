package cache

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

// recorder запоминает события кэша.
type recorder struct {
	mu        sync.Mutex
	hits      int
	misses    int
	ghostHits int
	evictions map[EvictReason]int
	ages      []time.Duration
}

func newRecorder() *recorder {
	return &recorder{evictions: map[EvictReason]int{}}
}

func (r *recorder) Hit(age time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits++
	r.ages = append(r.ages, age)
}

func (r *recorder) Miss(ghost bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.misses++
	if ghost {
		r.ghostHits++
	}
}

func (r *recorder) Evict(reason EvictReason) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evictions[reason]++
}

// clock — управляемое время для проверки TTL.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestGetAdd(t *testing.T) {
	rec := newRecorder()
	c := New[string, int](2, 0, WithObserver(rec))

	if _, ok := c.Get("a"); ok {
		t.Fatal("empty cache returned a value")
	}
	c.Add("a", 1)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("Get(a) = %v, %v; want 1, true", v, ok)
	}
	if rec.hits != 1 || rec.misses != 1 {
		t.Errorf("hits=%d misses=%d, want 1 and 1", rec.hits, rec.misses)
	}
	if c.Len() != 1 || c.Cap() != 2 {
		t.Errorf("Len=%d Cap=%d, want 1 and 2", c.Len(), c.Cap())
	}
}

func TestEvictsLeastRecentlyUsed(t *testing.T) {
	rec := newRecorder()
	c := New[string, int](2, 0, WithObserver(rec))

	c.Add("a", 1)
	c.Add("b", 2)
	c.Get("a") // a становится свежее b
	c.Add("c", 3)

	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted as least recently used")
	}
	for _, k := range []string{"a", "c"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("%s should still be cached", k)
		}
	}
	if rec.evictions[EvictCapacity] != 1 {
		t.Errorf("capacity evictions: got %d, want 1", rec.evictions[EvictCapacity])
	}
}

func TestTTL(t *testing.T) {
	rec := newRecorder()
	clk := &clock{t: time.Unix(0, 0)}
	c := New[string, int](10, time.Minute, WithObserver(rec), WithClock(clk.now))

	c.Add("a", 1)
	clk.advance(30 * time.Second)
	if _, ok := c.Get("a"); !ok {
		t.Fatal("entry expired too early")
	}
	if rec.ages[0] != 30*time.Second {
		t.Errorf("age at hit: got %v, want 30s", rec.ages[0])
	}

	clk.advance(30 * time.Second)
	if _, ok := c.Get("a"); ok {
		t.Fatal("entry older than TTL was returned")
	}
	if rec.evictions[EvictExpired] != 1 || rec.misses != 1 {
		t.Errorf("expired=%d misses=%d, want 1 and 1", rec.evictions[EvictExpired], rec.misses)
	}
	if c.Len() != 0 {
		t.Errorf("expired entry was not removed: Len=%d", c.Len())
	}
}

// TestEvictionReasonExpired проверяет, что вытеснение уже устаревшей записи
// считается как expired, а не capacity: иначе метрика подталкивала бы
// увеличить кэш там, где проблема в TTL.
func TestEvictionReasonExpired(t *testing.T) {
	rec := newRecorder()
	clk := &clock{t: time.Unix(0, 0)}
	c := New[string, int](1, time.Minute, WithObserver(rec), WithClock(clk.now))

	c.Add("a", 1)
	clk.advance(2 * time.Minute)
	c.Add("b", 2)

	if rec.evictions[EvictExpired] != 1 || rec.evictions[EvictCapacity] != 0 {
		t.Errorf("evictions: %v, want one expired", rec.evictions)
	}
}

func TestAddRefreshesEntry(t *testing.T) {
	clk := &clock{t: time.Unix(0, 0)}
	c := New[string, int](10, time.Minute, WithClock(clk.now))

	c.Add("a", 1)
	clk.advance(50 * time.Second)
	c.Add("a", 2)
	clk.advance(50 * time.Second)

	if v, ok := c.Get("a"); !ok || v != 2 {
		t.Errorf("Get(a) = %v, %v; want refreshed value 2", v, ok)
	}
}

// reentrant обращается к кэшу из колбэков: если бы наблюдатель вызывался под
// блокировкой, тест бы завис.
type reentrant struct{ c *LRU[string, int] }

func (r reentrant) Hit(time.Duration) { r.c.Len() }
func (r reentrant) Miss(bool)         { r.c.Len() }
func (r reentrant) Evict(EvictReason) { r.c.Len() }

func TestObserverIsCalledOutsideLock(t *testing.T) {
	obs := &reentrant{}
	c := New[string, int](1, time.Nanosecond, WithObserver(obs))
	obs.c = c

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Get("a")
		c.Add("a", 1)
		c.Add("b", 2)
		time.Sleep(time.Millisecond)
		c.Get("b")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: observer is called under the cache lock")
	}
}

func TestConcurrentAccess(t *testing.T) {
	c := New[int, int](100, time.Second)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 1000 {
				k := (g*31 + i) % 200
				if _, ok := c.Get(k); !ok {
					c.Add(k, i)
				}
			}
		})
	}
	wg.Wait()
	if c.Len() > c.Cap() {
		t.Errorf("Len=%d exceeds Cap=%d", c.Len(), c.Cap())
	}
}

// TestGhost: промах по ключу, вытесненному из-за размера, отмечается как
// ghost — кэш вдвое больше дал бы попадание.
func TestGhost(t *testing.T) {
	rec := newRecorder()
	c := New[string, int](2, 0, WithObserver(rec), WithGhost())

	c.Add("a", 1)
	c.Add("b", 2)
	c.Add("c", 3) // a вытеснен и запомнен
	c.Get("a")
	c.Get("never-seen")
	if rec.misses != 2 || rec.ghostHits != 1 {
		t.Fatalf("misses=%d ghost=%d, want 2 and 1", rec.misses, rec.ghostHits)
	}

	c.Add("a", 1) // a снова в кэше и больше не призрак; вытеснен b
	c.Get("d")
	c.Get("b")
	if rec.ghostHits != 2 {
		t.Errorf("ghost hits after re-adding: got %d, want 2 (only b)", rec.ghostHits)
	}
}

func TestGhostIsBounded(t *testing.T) {
	rec := newRecorder()
	c := New[string, int](1, 0, WithObserver(rec), WithGhost())

	c.Add("a", 1)
	c.Add("b", 2) // a → ghost
	c.Add("c", 3) // b → ghost, a выпадает из ghost: он помнит не больше capacity ключей
	c.Get("a")
	c.Get("b")
	if rec.ghostHits != 1 {
		t.Errorf("ghost hits: got %d, want 1 (b only)", rec.ghostHits)
	}
}

// TestGhostIgnoresExpired: устаревшие записи не помогли бы и в большем кэше,
// поэтому в призрачный список не попадают.
func TestGhostIgnoresExpired(t *testing.T) {
	rec := newRecorder()
	clk := &clock{t: time.Unix(0, 0)}
	c := New[string, int](1, time.Minute, WithObserver(rec), WithClock(clk.now), WithGhost())

	c.Add("a", 1)
	clk.advance(2 * time.Minute)
	c.Add("b", 2) // a вытеснен как expired
	c.Get("a")
	if rec.ghostHits != 0 {
		t.Errorf("ghost hits: got %d, want 0", rec.ghostHits)
	}
}

func TestNoGhostByDefault(t *testing.T) {
	rec := newRecorder()
	c := New[string, int](1, 0, WithObserver(rec))
	c.Add("a", 1)
	c.Add("b", 2)
	c.Get("a")
	if rec.ghostHits != 0 {
		t.Errorf("ghost hits without WithGhost: %d", rec.ghostHits)
	}
}

func TestNewPanicsOnZeroCapacity(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New(0, ...) did not panic")
		}
	}()
	New[string, int](0, 0)
}

// BenchmarkGetParallel показывает цену единственного мьютекса: с ростом
// -cpu время на операцию растёт из-за contention.
func BenchmarkGetParallel(b *testing.B) {
	c := New[int, int](1024, 0)
	for i := range 1024 {
		c.Add(i, i)
	}
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.Get(i & 1023)
			i++
		}
	})
}

func BenchmarkAddParallel(b *testing.B) {
	c := New[string, int](1024, 0)
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.Add(strconv.Itoa(i&4095), i)
			i++
		}
	})
}
