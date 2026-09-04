package main

import (
	"crypto/subtle"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/vimrus/runtime/internal/cache"
)

// zentao-cachebridge-dev 仅用于本地契约验证：复用 internal/cache 与
// cache.Process 提供 Cache Bridge v1 的独立监听（不经 frankenphp/Caddy）。
func main() {
	service, err := cache.New(256*1024*1024, cache.DefaultMaxValueBytes)
	if err != nil {
		log.Fatal(err)
	}
	defer service.Close()
	rawNamespaces := os.Getenv("CACHE_BRIDGE_NAMESPACES")
	if rawNamespaces == "" {
		rawNamespaces = "product,project,user,dao"
	}
	namespaces := make([]string, 0)
	for _, namespace := range strings.Split(rawNamespaces, ",") {
		namespace = strings.TrimSpace(namespace)
		if namespace != "" {
			namespaces = append(namespaces, namespace)
		}
	}
	if err := service.SetRegistry(namespaces); err != nil {
		log.Fatal(err)
	}

	token := os.Getenv("CACHE_BRIDGE_TOKEN")
	if token == "" {
		token = "dev-secret"
	}

	http.HandleFunc("/internal/runtime/cache/v1/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Zentao-Cache-Token")), []byte(token)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Zentao-Cache-Schema") != "1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		const prefix = "/internal/runtime/cache/v1/"
		op := strings.TrimPrefix(r.URL.Path, prefix)
		switch op {
		case "capabilities", "get", "set", "delete":
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, cache.MaxRequestBytes+1))
		if err != nil || len(body) > cache.MaxRequestBytes {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		response, err := cache.Process(service, op, body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(response)
	})

	addr := os.Getenv("CACHE_BRIDGE_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:8083"
	}
	fmt.Println("cache bridge listening on", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
