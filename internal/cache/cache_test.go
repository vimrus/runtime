package cache

import (
	"strings"
	"testing"
	"time"
)

func TestCacheSetGetDelete(t *testing.T) {
	c, err := New(1<<20, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.Set("product", "p1", "json", `{"id":1}`, 60); err != nil {
		t.Fatal(err)
	}
	entry, ok := c.Get("product", "p1")
	if !ok || entry.ValueType != "json" || entry.Value != `{"id":1}` {
		t.Fatalf("unexpected hit: %#v ok=%v", entry, ok)
	}
	c.Delete("product", "p1")
	if _, ok := c.Get("product", "p1"); ok {
		t.Fatal("delete failed")
	}
}

func TestCacheDistinguishesHitEmptyFromMiss(t *testing.T) {
	c, _ := New(1<<20, 4096)
	defer c.Close()
	if err := c.Set("user", "empty", "string", "", 60); err != nil {
		t.Fatal(err)
	}
	entry, ok := c.Get("user", "empty")
	if !ok || entry.Value != "" {
		t.Fatalf("empty hit must be a hit: %#v ok=%v", entry, ok)
	}
	if _, ok := c.Get("user", "missing"); ok {
		t.Fatal("missing key must be a miss")
	}
}

func TestCacheRejectsInvalidNamespaceKeyAndValueSize(t *testing.T) {
	c, _ := New(1<<20, 16)
	defer c.Close()
	if err := c.Set("Bad Namespace", "k", "string", "v", 60); err == nil {
		t.Fatal("invalid namespace accepted")
	}
	if err := c.Set("default", strings.Repeat("k", MaxKeyLength+1), "string", "v", 60); err == nil {
		t.Fatal("invalid key accepted")
	}
	if err := c.Set("default", "k", "string", strings.Repeat("x", 17), 60); err == nil {
		t.Fatal("oversized value accepted")
	}
}

func TestCacheNonPositiveTTLDoesNotWrite(t *testing.T) {
	c, _ := New(1<<20, 4096)
	defer c.Close()
	if err := c.Set("default", "k", "string", "v", 0); err != nil {
		t.Fatalf("non-positive ttl should be a no-op: %v", err)
	}
	if _, ok := c.Get("default", "k"); ok {
		t.Fatal("non-positive ttl wrote a value")
	}
}

func TestCacheTTLExpiry(t *testing.T) {
	c, _ := New(1<<20, 4096)
	defer c.Close()
	if err := c.Set("default", "k", "string", "v", 1); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("default", "k"); !ok {
		t.Fatal("value should be present before TTL")
	}
	time.Sleep(1100 * time.Millisecond)
	if _, ok := c.Get("default", "k"); ok {
		t.Fatal("value should expire")
	}
}

func TestCacheStatsCountHitsAndMisses(t *testing.T) {
	c, _ := New(1<<20, 4096)
	defer c.Close()
	_ = c.Set("default", "k", "string", "v", 60)
	_, _ = c.Get("default", "k")
	_, _ = c.Get("default", "missing")
	stats := c.Stats()
	if stats.Hits != 1 || stats.Misses != 1 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
}
