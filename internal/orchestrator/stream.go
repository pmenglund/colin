package orchestrator

import (
	"context"

	"github.com/pmenglund/colin/internal/domain"
)

// LatestSnapshotUpdate returns metadata for the latest published snapshot event.
func (o *Orchestrator) LatestSnapshotUpdate() domain.SnapshotUpdate {
	if o == nil {
		return domain.SnapshotUpdate{}
	}

	o.subscriberMu.Lock()
	defer o.subscriberMu.Unlock()
	return o.lastSnapshotEvent
}

// SubscribeSnapshotUpdates returns a coalescing stream of snapshot update events.
func (o *Orchestrator) SubscribeSnapshotUpdates(ctx context.Context) <-chan domain.SnapshotUpdate {
	if o == nil {
		return nil
	}

	updates := make(chan domain.SnapshotUpdate, 1)

	o.subscriberMu.Lock()
	o.nextSubscriberID++
	subscriberID := o.nextSubscriberID
	o.subscribers[subscriberID] = updates
	o.subscriberMu.Unlock()

	go func() {
		<-ctx.Done()
		o.subscriberMu.Lock()
		delete(o.subscribers, subscriberID)
		o.subscriberMu.Unlock()
	}()

	return updates
}

func (o *Orchestrator) publishSnapshotUpdate(update domain.SnapshotUpdate) {
	if o == nil {
		return
	}

	o.subscriberMu.Lock()
	o.snapshotSequence++
	update.Sequence = o.snapshotSequence
	o.lastSnapshotEvent = update
	subscribers := make([]chan domain.SnapshotUpdate, 0, len(o.subscribers))
	for _, subscriber := range o.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	o.subscriberMu.Unlock()

	for _, subscriber := range subscribers {
		select {
		case subscriber <- update:
		default:
			select {
			case <-subscriber:
			default:
			}
			select {
			case subscriber <- update:
			default:
			}
		}
	}
}
