package reconciler

import (
	"sync"
)

type DeltaType int

const (
	SNAPSHOT DeltaType = 1
	UPDATE             = 2
	DELETE             = 3
)

type Operation struct {
	ID   string
	Type DeltaType
}

type Matcher interface {
	AreEqual(obj interface{}) bool
}

type ReconcilerEventFunc[T1 any, T2 any] func(desiredItem T1, localItem T2, id string, opType DeltaType) (T2, error)
type KeyerFunc[T1 any] func(obj1 T1) (string, DeltaType)
type MatcherFunc[T1 any, T2 any] func(obj1 T1, obj2 T2) bool

type Reconciler[T1 any, T2 any] struct {
	desiredState map[string]T1
	localState   *map[string]T2
	cb           ReconcilerEventFunc[T1, T2]
	matcher      MatcherFunc[T1, T2]

	processingSnap bool
	mx             sync.Mutex
}

func NewReconciler[T1 any, T2 any](cb ReconcilerEventFunc[T1, T2]) *Reconciler[T1, T2] {
	localState := make(map[string]T2)
	return NewReconcilerFromMap[T1, T2](cb, nil, &localState)
}

func NewReconcilerFromMap[T1 any, T2 any](cb ReconcilerEventFunc[T1, T2],
	matcher MatcherFunc[T1, T2],
	localState *map[string]T2) *Reconciler[T1, T2] {
	r := &Reconciler[T1, T2]{
		desiredState: make(map[string]T1),
		localState:   localState,
		cb:           cb,
		matcher:      matcher,
	}

	return r
}

func (r *Reconciler[T1, T2]) Reconcile(newLs *map[string]T2) error {
	r.localState = newLs
	ls := *r.localState
	toRemove := map[string]bool{}
	for k := range ls {
		toRemove[k] = true
	}

	for key, v1 := range r.desiredState {
		if v2, ok := ls[key]; !ok {
			newV2, err := r.cb(v1, v2, key, UPDATE)
			if err != nil {
				continue
			}
			ls[key] = newV2
		} else {
			if !r.matcher(v1, v2) {
				newV2, err := r.cb(v1, v2, key, UPDATE)
				if err != nil {
					continue
				}
				ls[key] = newV2
			}
			delete(toRemove, key)
		}
	}

	// remove the extra ones
	var empty T1
	for key := range toRemove {
		item := ls[key]
		_, err := r.cb(empty, item, key, DELETE)
		if err != nil {
			return err
		}
		delete(ls, key)
	}

	return nil
}

func (r *Reconciler[T1, T2]) ProcessChangeEvent(obj T1, id string, opType DeltaType) error {
	r.mx.Lock()
	defer r.mx.Unlock()

	oldLocal := (*r.localState)[id]

	if opType == SNAPSHOT {
		if !r.processingSnap {
			r.processingSnap = true
			r.desiredState = make(map[string]T1)
		}
		r.desiredState[id] = obj
		//return nil
	} else {
		r.processingSnap = false

		// finished re-creating the desired state
		err := r.Reconcile(r.localState)
		if err != nil {
			return err
		}
	}

	newV2, err := r.cb(obj, oldLocal, id, opType)
	if err != nil {
		return err
	}

	if opType == UPDATE {
		r.desiredState[id] = obj
		(*r.localState)[id] = newV2
	} else if opType == DELETE {
		delete(*r.localState, id)
		delete(r.desiredState, id)
	}

	return nil
}
