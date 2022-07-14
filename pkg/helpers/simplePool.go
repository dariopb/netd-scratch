package helpers

import (
	"fmt"
	"sync"
)

type SimplePool struct {
	Name           string                  `json:"name"`
	ItemsAvailable map[string]string       `json:"itemsAvailable"`
	ItemsUsed      map[string]*PoolElement `json:"itemsUsed"`

	mtx sync.Mutex
}

type PoolElement struct {
	Value    string `json:"value"`
	Metadata string `json:"metadata"`
}

func NewSimplePool(name string) *SimplePool {
	pool := &SimplePool{
		Name:           name,
		ItemsAvailable: make(map[string]string),
		ItemsUsed:      make(map[string]*PoolElement),
	}

	return pool
}

func (p *SimplePool) InitializeElements(items []string) {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	for _, i := range items {
		p.ItemsAvailable[i] = i
	}
}

func (p *SimplePool) QueryItem(itemID string) (string, error) {
	return p.GetItemRaw(itemID, "", nil, true)
}

func (p *SimplePool) GetItem(itemID string, metadata string, cb func(item *PoolElement) error) (string, error) {
	return p.GetItemRaw(itemID, metadata, cb, false)
}

func (p *SimplePool) GetItemRaw(itemID string, metadata string, cb func(item *PoolElement) error, failIfMissing bool) (string, error) {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	if itemID == "" {
		return "", fmt.Errorf("Need to provide an itemID")
	}

	item := ""
	pe, ok := p.ItemsUsed[itemID]
	if !ok {
		if failIfMissing {
			return "", fmt.Errorf("Item was not allocated")
		}
		for _, item = range p.ItemsAvailable {
			break
		}
		if item == "" {
			return "", fmt.Errorf("Pool is exhausted")
		}

		pe = &PoolElement{Value: item, Metadata: metadata}
	}

	if cb != nil {
		err := (cb)(pe)
		if err != nil {
			return "", err
		}
	}

	item = pe.Value
	p.ItemsUsed[itemID] = pe
	delete(p.ItemsAvailable, item)
	return item, nil
}

func (p *SimplePool) ReleaseItem(itemID string, cb func(item *PoolElement) error) (string, error) {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	pe, ok := p.ItemsUsed[itemID]
	if !ok {
		return "", fmt.Errorf("Requested itemID [%s] was not in use in the pool", itemID)
	}

	if cb != nil {
		err := (cb)(pe)
		if err != nil {
			return "", err
		}
	}

	p.ItemsAvailable[pe.Value] = pe.Value
	delete(p.ItemsUsed, itemID)
	return pe.Value, nil
}
