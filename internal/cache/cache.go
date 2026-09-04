// Package cache implements the bounded in-memory Runtime Cache Service used
// by the private Cache Bridge. Values are explicit namespace/key entries with
// TTL and byte budgets; the cache is process-local and discardable.
package cache

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dgraph-io/ristretto"
)

const (
	SchemaVersion        = 1
	DefaultMaxCost       = 256 * 1024 * 1024
	DefaultMaxValueBytes = 1024 * 1024
	MaxNamespaceLength   = 64
	MaxKeyLength         = 512
)

// Entry is the value envelope stored by the cache.
type Entry struct {
	ValueType string `json:"valueType"`
	Value     string `json:"value"`
}

// Cache is a sharded, bounded TTL cache built on ristretto.
type Cache struct {
	store         *ristretto.Cache
	maxValueBytes int64
	registry      map[string]struct{}
	hits          atomic.Uint64
	misses        atomic.Uint64
	sets          atomic.Uint64
	deletes       atomic.Uint64
	rejected      atomic.Uint64
}

// SetRegistry 配置允许的 namespace 集合（空表示不限制，兼容旧行为）。
// 非空时 Get/Set/Delete 对未注册 namespace 拒绝，配合 Bridge capabilities
// 实现跨端注册表校验（D3）。
func (c *Cache) SetRegistry(namespaces []string) error {
	registry := make(map[string]struct{}, len(namespaces))
	for _, namespace := range namespaces {
		namespace = NormalizeNamespace(namespace)
		if err := ValidateNamespace(namespace); err != nil {
			return fmt.Errorf("invalid registry namespace %q", namespace)
		}
		registry[namespace] = struct{}{}
	}
	c.registry = registry
	return nil
}

// Registry 返回当前允许的 namespace 列表（排序；空表示不限制）。
func (c *Cache) Registry() []string {
	result := make([]string, 0, len(c.registry))
	for namespace := range c.registry {
		result = append(result, namespace)
	}
	sort.Strings(result)
	return result
}

func (c *Cache) registered(namespace string) bool {
	if len(c.registry) == 0 {
		return true
	}
	_, ok := c.registry[namespace]
	return ok
}

func New(maxCostBytes int64, maxValueBytes int64) (*Cache, error) {
	if maxCostBytes <= 0 {
		maxCostBytes = DefaultMaxCost
	}
	if maxValueBytes <= 0 {
		maxValueBytes = DefaultMaxValueBytes
	}
	store, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 1_000_000,
		MaxCost:     maxCostBytes,
		BufferItems: 64,
		Metrics:     true,
	})
	if err != nil {
		return nil, fmt.Errorf("create cache: %w", err)
	}
	return &Cache{store: store, maxValueBytes: maxValueBytes}, nil
}

func ValidateNamespace(namespace string) error {
	if namespace == "" || len(namespace) > MaxNamespaceLength {
		return errors.New("invalid namespace")
	}
	for _, r := range namespace {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return errors.New("invalid namespace")
	}
	return nil
}

func ValidateKey(key string) error {
	if key == "" || len(key) > MaxKeyLength {
		return errors.New("invalid key")
	}
	return nil
}

func (c *Cache) key(namespace, key string) string {
	return namespace + "\x00" + key
}

// Get returns a hit/miss entry. A hit with an empty string is valid and
// distinguishable from a miss through the bool.
func (c *Cache) Get(namespace, key string) (Entry, bool) {
	if ValidateNamespace(namespace) != nil || ValidateKey(key) != nil {
		c.misses.Add(1)
		return Entry{}, false
	}
	if !c.registered(namespace) {
		c.misses.Add(1)
		return Entry{}, false
	}
	value, ok := c.store.Get(c.key(namespace, key))
	if !ok {
		c.misses.Add(1)
		return Entry{}, false
	}
	entry, ok := value.(Entry)
	if !ok {
		c.misses.Add(1)
		c.store.Del(c.key(namespace, key))
		return Entry{}, false
	}
	c.hits.Add(1)
	return entry, true
}

// Set stores a value. valueType must be string or json, TTL seconds must be
// positive and the value must fit the configured byte budget.
func (c *Cache) Set(namespace, key, valueType, value string, ttlSeconds int64) error {
	if ValidateNamespace(namespace) != nil || ValidateKey(key) != nil {
		return errors.New("invalid_request")
	}
	if !c.registered(namespace) {
		c.rejected.Add(1)
		return errors.New("namespace_not_registered")
	}
	if valueType != "string" && valueType != "json" {
		return errors.New("invalid_request")
	}
	if ttlSeconds <= 0 {
		return nil // non-positive TTL means delete/no-op per contract
	}
	if int64(len(value)) > c.maxValueBytes {
		c.rejected.Add(1)
		return errors.New("value_too_large")
	}
	entry := Entry{ValueType: valueType, Value: value}
	cost := int64(len(value) + 128)
	if !c.store.SetWithTTL(c.key(namespace, key), entry, cost, time.Duration(ttlSeconds)*time.Second) {
		c.rejected.Add(1)
		return errors.New("quota_exceeded")
	}
	c.sets.Add(1)
	c.store.Wait()
	return nil
}

func (c *Cache) Delete(namespace, key string) {
	if ValidateNamespace(namespace) != nil || ValidateKey(key) != nil {
		return
	}
	if !c.registered(namespace) {
		return
	}
	c.store.Del(c.key(namespace, key))
	c.deletes.Add(1)
}

func (c *Cache) Close() {
	if c.store != nil {
		c.store.Close()
	}
}

type Stats struct {
	Hits        uint64 `json:"hits"`
	Misses      uint64 `json:"misses"`
	Sets        uint64 `json:"sets"`
	Deletes     uint64 `json:"deletes"`
	Rejected    uint64 `json:"rejected"`
	Items       uint64 `json:"items"`
	Bytes       int64  `json:"bytes"`
	KeysAdded   uint64 `json:"keysAdded"`
	KeysEvicted uint64 `json:"keysEvicted"`
}

func (c *Cache) Stats() Stats {
	m := c.store.Metrics
	return Stats{
		Hits:        c.hits.Load(),
		Misses:      c.misses.Load(),
		Sets:        c.sets.Load(),
		Deletes:     c.deletes.Load(),
		Rejected:    c.rejected.Load(),
		Items:       m.KeysAdded() - m.KeysEvicted(),
		Bytes:       int64(m.CostAdded() - m.CostEvicted()),
		KeysAdded:   m.KeysAdded(),
		KeysEvicted: m.KeysEvicted(),
	}
}

func NormalizeNamespace(namespace string) string {
	return strings.ToLower(strings.TrimSpace(namespace))
}
