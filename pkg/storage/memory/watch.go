// Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package memory

import (
	"strings"
	"sync"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/klog/v2"
)

const watchChannelSize = 100

// watchManager tracks active watchers and broadcasts events to them.
// It uses its own mutex, separate from Store.mu, because sendLocked
// is called while the Store write lock is held.
type watchManager struct {
	mu              sync.Mutex
	watchers        map[int]*memoryWatcher
	nextID          int
	watchBufferSize int
}

func newWatchManager(bufferSize int) *watchManager {
	return &watchManager{
		watchers:        make(map[int]*memoryWatcher),
		watchBufferSize: bufferSize,
	}
}

// watch creates a new watcher for the given key prefix and registers it.
// The caller must cancel the context or call Stop() to clean up.
func (wm *watchManager) watch(key string) *memoryWatcher {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	id := wm.nextID
	wm.nextID++

	w := &memoryWatcher{
		id:     id,
		key:    key,
		ch:     make(chan watch.Event, wm.watchBufferSize),
		done:   make(chan struct{}),
		parent: wm,
	}

	wm.watchers[id] = w

	return w
}

// sendLocked broadcasts an event to all registered watchers whose key prefix
// matches the event's object key. This method is called while Store.mu is
// held (write lock), so it uses its own mutex for watcher iteration.
// Sends are non-blocking: if a watcher's channel is full, the event is dropped.
func (wm *watchManager) sendLocked(ev watch.Event, objectKey string) {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	for _, w := range wm.watchers {
		if !strings.HasPrefix(objectKey, w.key) {
			continue
		}

		select {
		case w.ch <- ev:
		default:
			w.droppedEvents.Add(1)
		}
	}
}

// remove unregisters a watcher by ID.
func (wm *watchManager) remove(id int) {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	delete(wm.watchers, id)
}

// memoryWatcher implements watch.Interface for in-memory storage events.
type memoryWatcher struct {
	id            int
	key           string
	ch            chan watch.Event
	done          chan struct{}
	once          sync.Once
	parent        *watchManager
	droppedEvents atomic.Int64
}

var _ watch.Interface = (*memoryWatcher)(nil)

// ResultChan returns the channel that receives watch events.
func (w *memoryWatcher) ResultChan() <-chan watch.Event {
	return w.ch
}

// Stop terminates the watcher, unregisters it from the parent manager,
// and closes the result channel. It is safe to call multiple times.
func (w *memoryWatcher) Stop() {
	w.once.Do(func() {
		if dropped := w.droppedEvents.Load(); dropped > 0 {
			klog.V(2).InfoS("Watch stopped with dropped events",
				"watcherID", w.id,
				"key", w.key,
				"droppedEvents", dropped,
			)
		}

		w.parent.remove(w.id)
		close(w.done)
		close(w.ch)
	})
}
