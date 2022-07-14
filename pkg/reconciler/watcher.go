package reconciler

import (
	"context"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
)

type ClientType[WatchResponseType any] interface {
	Recv() (WatchResponseType, error)
	grpc.ClientStream
}

type StreamWatcher[WatchClientType ClientType[WatchResponseType], WatchResponseType any] struct {
	//type StreamWatcher[WatchClientType any, WatchResponseType any] struct {
	reconnectTime time.Duration

	reconciler *Reconciler[WatchResponseType, WatchResponseType]
	store      *map[string]WatchResponseType
	keyer      KeyerFunc[WatchResponseType]
	mx         sync.Locker
}

func NewStreamWatcher[WatchClientType ClientType[WatchResponseType],
	WatchResponseType any](
	reconnectTime time.Duration,
	keyer KeyerFunc[WatchResponseType],
	matcher MatcherFunc[WatchResponseType, WatchResponseType]) (*StreamWatcher[WatchClientType, WatchResponseType], error) {

	w, err := NewStreamWatcherWithState[WatchClientType, WatchResponseType](reconnectTime, keyer, matcher, nil, nil)
	return w, err
}

func NewStreamWatcherWithState[WatchClientType ClientType[WatchResponseType],
	WatchResponseType any](reconnectTime time.Duration,
	keyer KeyerFunc[WatchResponseType],
	matcher MatcherFunc[WatchResponseType, WatchResponseType],
	store *map[string]WatchResponseType,
	mx sync.Locker) (*StreamWatcher[WatchClientType, WatchResponseType], error) {

	w := &StreamWatcher[WatchClientType, WatchResponseType]{
		reconnectTime: reconnectTime,
		store:         store,
		keyer:         keyer,
		mx:            mx,
	}

	if store != nil {
		w.reconciler = NewReconcilerFromMap[WatchResponseType, WatchResponseType](w.eventFunc, matcher, store)
	}

	return w, nil
}

func (w *StreamWatcher[WatchClientType, WatchResponseType]) eventFunc(desiredItem WatchResponseType,
	localItem WatchResponseType, id string, opType DeltaType) (WatchResponseType, error) {
	if opType == DELETE {
		var n WatchResponseType
		return n, nil
	}

	return desiredItem, nil
}

func (w *StreamWatcher[WatchClientType, WatchResponseType]) WatchLoop(
	loopID string,
	startFn func(ctx context.Context) (WatchClientType, error),
	onEvent func(event WatchResponseType) error,
	mainCtx context.Context) error {

	subscribed := false
	startWatching := make(chan bool, 1)
	startWatching <- true

	var client WatchClientType
	var ctx context.Context
	var cancel context.CancelFunc

	var nilClient WatchClientType

	l := log.WithField("WatchLoop", loopID)

	if w.store != nil {
		err := w.reconciler.Reconcile(w.store)
		if err != nil {
			return err
		}
	}

loop:
	for {
		var err error

		select {
		case <-time.After(w.reconnectTime):
			if !subscribed {
				startWatching <- true
			} else {
				// quick reconnection validation
				//cancel()
			}

		case <-startWatching:
			l.Infof("Starting subscription")

			ctx, cancel = context.WithCancel(mainCtx)

			client, err = startFn(ctx)
			subscribed = true
			if err != nil {
				return err
			}

			go func() {
				for {
					resp, err := (client).Recv()
					if err != nil {
						l.Infof("  subscription finished [err: %v]", err)
						cancel()
						client = nilClient
						subscribed = false
						break
					}

					l.Infof("Received: %v", resp)

					if w.reconciler != nil {
						key, op := w.keyer(resp)
						w.reconciler.ProcessChangeEvent(resp, key, op)
					}

					err = onEvent(resp)
					if err != nil {
						l.Infof("  subscription finished due to processing err [err: %v]", err)
						cancel()
						client = nilClient
						subscribed = false
						break
					}
				}
			}()

		case <-mainCtx.Done():
			close(startWatching)
			break loop
		}
	}

	return nil
}
