// Package cache реализует потокобезопасный LRU-кэш с TTL.
//
// Кэш ничего не знает о метриках: события (попадание, промах, вытеснение)
// получает Observer. Наблюдатель вызывается вне блокировки, поэтому может
// сам обращаться к кэшу.
//
// С опцией WithGhost кэш дополнительно помнит ключи, недавно вытесненные из-за
// размера («призрачный» LRU из одних ключей той же ёмкости). Промах по такому
// ключу означает, что кэш вдвое больше дал бы попадание: так видно, поможет ли
// увеличение размера, ещё до эксперимента.
package cache

import (
	"container/list"
	"sync"
	"time"
)

// EvictReason — почему запись удалена из кэша.
type EvictReason string

const (
	// EvictCapacity — запись вытеснена новой, потому что кэш заполнен.
	EvictCapacity EvictReason = "capacity"
	// EvictExpired — запись старше TTL.
	EvictExpired EvictReason = "expired"
)

// Observer получает события кэша.
type Observer interface {
	// Hit — значение найдено; age — сколько записи было на момент попадания.
	Hit(age time.Duration)
	// Miss — значения нет или оно устарело. ghost — ключ недавно вытеснен из-за
	// размера, и кэш вдвое больше дал бы попадание (только с WithGhost).
	Miss(ghost bool)
	// Evict — запись удалена.
	Evict(reason EvictReason)
}

type noopObserver struct{}

func (noopObserver) Hit(time.Duration) {}
func (noopObserver) Miss(bool)         {}
func (noopObserver) Evict(EvictReason) {}

// Option настраивает LRU.
type Option func(*options)

type options struct {
	observer Observer
	now      func() time.Time
	ghost    bool
}

// WithObserver подключает наблюдателя за событиями кэша.
func WithObserver(o Observer) Option {
	return func(opts *options) { opts.observer = o }
}

// WithGhost включает учёт ключей, вытесненных из-за размера: промахи по ним
// приходят в Observer.Miss с ghost=true. Хранит до capacity ключей без значений.
func WithGhost() Option {
	return func(opts *options) { opts.ghost = true }
}

// WithClock подменяет источник времени (для тестов).
func WithClock(now func() time.Time) Option {
	return func(opts *options) { opts.now = now }
}

// LRU — кэш фиксированного размера. При переполнении вытесняется запись,
// к которой дольше всего не обращались. Записи старше TTL удаляются лениво:
// при обращении к ним или когда до них доходит очередь на вытеснение.
type LRU[K comparable, V any] struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	order    *list.List // в начале — последние использованные записи
	items    map[K]*list.Element
	observer Observer
	now      func() time.Time

	// ghost хранит ключи, вытесненные из-за размера, в порядке вытеснения;
	// nil, если учёт выключен.
	ghost      *list.List
	ghostItems map[K]*list.Element
}

type entry[K comparable, V any] struct {
	key     K
	value   V
	created time.Time
}

// New создаёт кэш на capacity записей. ttl = 0 отключает устаревание.
func New[K comparable, V any](capacity int, ttl time.Duration, opts ...Option) *LRU[K, V] {
	if capacity <= 0 {
		panic("cache: capacity must be positive")
	}
	o := options{observer: noopObserver{}, now: time.Now}
	for _, opt := range opts {
		opt(&o)
	}
	c := &LRU[K, V]{
		capacity: capacity,
		ttl:      ttl,
		order:    list.New(),
		items:    make(map[K]*list.Element, capacity),
		observer: o.observer,
		now:      o.now,
	}
	if o.ghost {
		c.ghost = list.New()
		c.ghostItems = make(map[K]*list.Element, capacity)
	}
	return c
}

// Get возвращает значение по ключу. Устаревшая запись удаляется и считается
// промахом.
func (c *LRU[K, V]) Get(key K) (V, bool) {
	var zero V
	now := c.now()

	c.mu.Lock()
	el, ok := c.items[key]
	if !ok {
		_, ghost := c.ghostItems[key]
		c.mu.Unlock()
		c.observer.Miss(ghost)
		return zero, false
	}
	e := el.Value.(*entry[K, V])
	age := now.Sub(e.created)
	if c.expired(age) {
		c.remove(el)
		c.mu.Unlock()
		c.observer.Evict(EvictExpired)
		c.observer.Miss(false)
		return zero, false
	}
	c.order.MoveToFront(el)
	value := e.value
	c.mu.Unlock()

	c.observer.Hit(age)
	return value, true
}

// Add добавляет значение или обновляет существующее (вместе с его возрастом).
// Если кэш переполнен, вытесняется запись, к которой дольше всего не
// обращались.
func (c *LRU[K, V]) Add(key K, value V) {
	now := c.now()

	c.mu.Lock()
	if el, ok := c.items[key]; ok {
		e := el.Value.(*entry[K, V])
		e.value, e.created = value, now
		c.order.MoveToFront(el)
		c.mu.Unlock()
		return
	}

	c.items[key] = c.order.PushFront(&entry[K, V]{key: key, value: value, created: now})
	c.forget(key)
	var evicted []EvictReason
	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		reason := EvictCapacity
		if c.expired(now.Sub(oldest.Value.(*entry[K, V]).created)) {
			// Запись всё равно устарела: вытеснение из-за размера тут ни при чём.
			reason = EvictExpired
		}
		c.remove(oldest)
		if reason == EvictCapacity {
			c.remember(oldest.Value.(*entry[K, V]).key)
		}
		evicted = append(evicted, reason)
	}
	c.mu.Unlock()

	for _, reason := range evicted {
		c.observer.Evict(reason)
	}
}

// Len возвращает текущее число записей, включая ещё не удалённые устаревшие.
func (c *LRU[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// Cap возвращает максимальное число записей.
func (c *LRU[K, V]) Cap() int {
	return c.capacity
}

func (c *LRU[K, V]) expired(age time.Duration) bool {
	return c.ttl > 0 && age >= c.ttl
}

func (c *LRU[K, V]) remove(el *list.Element) {
	c.order.Remove(el)
	delete(c.items, el.Value.(*entry[K, V]).key)
}

// remember запоминает ключ, вытесненный из-за размера.
func (c *LRU[K, V]) remember(key K) {
	if c.ghost == nil {
		return
	}
	c.ghostItems[key] = c.ghost.PushFront(key)
	if c.ghost.Len() > c.capacity {
		oldest := c.ghost.Back()
		c.ghost.Remove(oldest)
		delete(c.ghostItems, oldest.Value.(K))
	}
}

// forget убирает ключ из призрачного списка: он снова в кэше.
func (c *LRU[K, V]) forget(key K) {
	if el, ok := c.ghostItems[key]; ok {
		c.ghost.Remove(el)
		delete(c.ghostItems, key)
	}
}
