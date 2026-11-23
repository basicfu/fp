package objstore

import (
	"context"
	"sync"
)

// MemoryObject 是 Memory 里存的一个对象。
type MemoryObject struct {
	ContentType  string
	CacheControl string
	Body         []byte
}

// Memory 是测试用的内存实现。FailPut / FailDelete 用来注入失败。
type Memory struct {
	mu         sync.Mutex
	objs       map[string]MemoryObject
	FailPut    error
	FailDelete error
}

func NewMemory() *Memory { return &Memory{objs: map[string]MemoryObject{}} }

func (m *Memory) Put(_ context.Context, key, contentType string, body []byte, cacheControl string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailPut != nil {
		return m.FailPut
	}
	m.objs[key] = MemoryObject{ContentType: contentType, CacheControl: cacheControl, Body: append([]byte(nil), body...)}
	return nil
}

func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailDelete != nil {
		return m.FailDelete
	}
	delete(m.objs, key)
	return nil
}

func (m *Memory) URL(key string) string { return "https://memory.test/" + key }

// Get 取一个对象，供测试断言。
func (m *Memory) Get(key string) (MemoryObject, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objs[key]
	return o, ok
}

// Keys 返回当前全部 key，供测试断言。
func (m *Memory) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.objs))
	for k := range m.objs {
		out = append(out, k)
	}
	return out
}
