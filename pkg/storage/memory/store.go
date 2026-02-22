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
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/storage"
)

// item holds an encoded object and its associated resource version.
type item struct {
	key  string
	data []byte
	rv   uint64
}

// Store is a thread-safe, in-memory implementation of storage.Interface.
// Objects are stored as codec-encoded bytes keyed by hierarchical path strings.
type Store struct {
	codec    runtime.Codec
	mu       sync.RWMutex
	items    map[string]*item
	rev      uint64
	watchers *watchManager
}

// Compile-time interface compliance check.
var _ storage.Interface = (*Store)(nil)

// NewStore creates a new in-memory store that encodes and decodes objects
// using the provided codec. The watch channel buffer uses the default size
// (watchChannelSize). Use NewStoreWithOptions for custom buffer sizes.
func NewStore(codec runtime.Codec) *Store {
	return &Store{
		codec:    codec,
		items:    make(map[string]*item),
		watchers: newWatchManager(watchChannelSize),
	}
}

// Versioner returns the storage versioner used to manage resource versions on
// API objects. This implementation uses the standard APIObjectVersioner.
func (s *Store) Versioner() storage.Versioner {
	return storage.APIObjectVersioner{}
}

// Create adds a new object at the given key. If an object already exists at
// that key, a KeyExists error is returned. The out parameter, if non-nil, is
// populated with the stored object including its assigned resource version.
func (s *Store) Create(ctx context.Context, key string, obj, out runtime.Object, ttl uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.items[key]; exists {
		return storage.NewKeyExistsError(key, 0)
	}

	s.rev++
	rv := s.rev

	if err := s.Versioner().PrepareObjectForStorage(obj); err != nil {
		return fmt.Errorf("PrepareObjectForStorage failed: %w", err)
	}

	if err := s.Versioner().UpdateObject(obj, rv); err != nil {
		return fmt.Errorf("UpdateObject failed: %w", err)
	}

	data, err := s.encode(obj)
	if err != nil {
		return err
	}

	s.items[key] = &item{
		key:  key,
		data: data,
		rv:   rv,
	}

	if out != nil {
		if err := s.decode(data, out); err != nil {
			return err
		}
	}

	// DeepCopy is required: watchers must receive an isolated snapshot.
	// The copy runs under s.mu write lock, so watch-heavy workloads
	// should keep stored objects small.
	s.watchers.sendLocked(watch.Event{
		Type:   watch.Added,
		Object: obj.DeepCopyObject(),
	}, key)

	return nil
}

// Delete removes the object at the given key. If the key does not exist,
// a KeyNotFound error is returned. Preconditions and validation callbacks
// are checked before deletion proceeds.
func (s *Store) Delete(
	ctx context.Context,
	key string,
	out runtime.Object,
	preconditions *storage.Preconditions,
	validateDeletion storage.ValidateObjectFunc,
	cachedExistingObject runtime.Object,
	opts storage.DeleteOptions,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.items[key]
	if !ok {
		return storage.NewKeyNotFoundError(key, 0)
	}

	existingObj, err := s.decodeNew(existing.data)
	if err != nil {
		return err
	}

	if err := s.checkPreconditions(key, preconditions, existingObj); err != nil {
		return err
	}

	// validateDeletion must be fast and non-blocking. It runs while the store
	// write lock is held; a slow callback freezes all storage operations.
	if validateDeletion != nil {
		if err := validateDeletion(ctx, existingObj); err != nil {
			return err
		}
	}

	delete(s.items, key)

	s.rev++

	if out != nil {
		if err := s.decode(existing.data, out); err != nil {
			return err
		}
	}

	// Deep copy for watcher isolation.
	s.watchers.sendLocked(watch.Event{
		Type:   watch.Deleted,
		Object: existingObj.DeepCopyObject(),
	}, key)

	return nil
}

// Watch begins watching the specified key prefix. Events matching the key
// prefix are sent on the returned watch.Interface. The watch is automatically
// stopped when the context is cancelled.
//
// The in-memory store does not support resuming watches from a specific
// ResourceVersion. Passing a non-empty ResourceVersion returns an error.
func (s *Store) Watch(ctx context.Context, key string, opts storage.ListOptions) (watch.Interface, error) {
	if opts.ResourceVersion != "" {
		return nil, storage.NewInvalidError(field.ErrorList{
			field.Invalid(
				field.NewPath("resourceVersion"),
				opts.ResourceVersion,
				"in-memory store does not support watch resume from resource version",
			),
		})
	}

	w := s.watchers.watch(key)
	done := w.done // capture before spawning goroutine

	go func() {
		select {
		case <-ctx.Done():
			w.Stop()
		case <-done:
			// Watcher was stopped directly; goroutine can exit.
		}
	}()

	return w, nil
}

// Get retrieves the object stored at the given key and decodes it into objPtr.
// If the key does not exist and opts.IgnoreNotFound is false, a KeyNotFound
// error is returned. If IgnoreNotFound is true, objPtr is left at its zero value.
func (s *Store) Get(ctx context.Context, key string, opts storage.GetOptions, objPtr runtime.Object) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	existing, ok := s.items[key]
	if !ok {
		if opts.IgnoreNotFound {
			return nil
		}

		return storage.NewKeyNotFoundError(key, 0)
	}

	return s.decode(existing.data, objPtr)
}

// GetList retrieves all objects whose keys match the given prefix (when
// opts.Recursive is true) or the exact key (otherwise), and populates
// listObj with the matching items. The list's resource version is set to
// the store's current revision.
func (s *Store) GetList(ctx context.Context, key string, opts storage.ListOptions, listObj runtime.Object) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prefix := key
	if opts.Recursive && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	var objs []runtime.Object

	for k, it := range s.items {
		var match bool
		if opts.Recursive {
			match = strings.HasPrefix(k, prefix)
		} else {
			match = k == key
		}

		if !match {
			continue
		}

		obj, err := s.decodeNew(it.data)
		if err != nil {
			return err
		}

		if !predicateEmpty(opts.Predicate) {
			matches, err := opts.Predicate.Matches(obj)
			if err != nil {
				return err
			}

			if !matches {
				continue
			}
		}

		objs = append(objs, obj)
	}

	if err := meta.SetList(listObj, objs); err != nil {
		return err
	}

	return s.setListRV(listObj, s.rev)
}

// GuaranteedUpdate reads the current object at the given key, passes it to
// tryUpdate, and writes the result back. If the key does not exist and
// ignoreNotFound is false, a KeyNotFound error is returned. The operation
// is retried internally if the tryUpdate function returns a retriable error.
func (s *Store) GuaranteedUpdate(
	ctx context.Context,
	key string,
	destination runtime.Object,
	ignoreNotFound bool,
	preconditions *storage.Preconditions,
	tryUpdate storage.UpdateFunc,
	cachedExistingObject runtime.Object,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.items[key]

	var currentObj runtime.Object
	var currentRV uint64

	if ok {
		obj, err := s.decodeNew(existing.data)
		if err != nil {
			return err
		}

		currentObj = obj
		currentRV = existing.rv
	} else {
		if !ignoreNotFound {
			return storage.NewKeyNotFoundError(key, 0)
		}

		currentObj = destination.DeepCopyObject()
	}

	if err := s.checkPreconditions(key, preconditions, currentObj); err != nil {
		return err
	}

	updated, _, err := tryUpdate(currentObj, storage.ResponseMeta{ResourceVersion: currentRV})
	if err != nil {
		return err
	}

	s.rev++
	rv := s.rev

	if err := s.Versioner().UpdateObject(updated, rv); err != nil {
		return fmt.Errorf("UpdateObject failed: %w", err)
	}

	data, err := s.encode(updated)
	if err != nil {
		return err
	}

	s.items[key] = &item{
		key:  key,
		data: data,
		rv:   rv,
	}

	if err := s.decode(data, destination); err != nil {
		return err
	}

	evType := watch.Modified
	if !ok {
		evType = watch.Added
	}

	// Deep copy for watcher isolation.
	s.watchers.sendLocked(watch.Event{
		Type:   evType,
		Object: updated.DeepCopyObject(),
	}, key)

	return nil
}

// Stats returns basic storage statistics. Currently reports only the number
// of stored objects.
func (s *Store) Stats(ctx context.Context) (storage.Stats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return storage.Stats{
		ObjectCount: int64(len(s.items)),
	}, nil
}

// ReadinessCheck reports whether the store is ready. The in-memory store is
// always ready, so this always returns nil.
func (s *Store) ReadinessCheck() error {
	return nil
}

// RequestWatchProgress is a no-op for the in-memory store. It exists to
// satisfy the storage.Interface and is only meaningful for etcd-backed stores.
func (s *Store) RequestWatchProgress(ctx context.Context) error {
	return nil
}

// GetCurrentResourceVersion returns the store's current monotonic revision.
func (s *Store) GetCurrentResourceVersion(ctx context.Context) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.rev, nil
}

// EnableResourceSizeEstimation is a no-op for the in-memory store. Size
// estimation is only relevant for disk-backed storage backends.
func (s *Store) EnableResourceSizeEstimation(storage.KeysFunc) error {
	return nil
}

// CompactRevision returns the latest observed compacted revision. The
// in-memory store does not perform compaction, so this always returns 0.
func (s *Store) CompactRevision() int64 {
	return 0
}

// --- internal helpers ---

// encode serializes an object into bytes using the store's codec.
func (s *Store) encode(obj runtime.Object) ([]byte, error) {
	var buf bytes.Buffer
	if err := s.codec.Encode(obj, &buf); err != nil {
		return nil, fmt.Errorf("encode failed: %w", err)
	}

	return buf.Bytes(), nil
}

// decode deserializes bytes into an existing object using the store's codec.
func (s *Store) decode(data []byte, into runtime.Object) error {
	_, _, err := s.codec.Decode(data, nil, into)
	if err != nil {
		return fmt.Errorf("decode failed: %w", err)
	}

	return nil
}

// decodeNew deserializes bytes into a new object allocated by the codec.
func (s *Store) decodeNew(data []byte) (runtime.Object, error) {
	obj, _, err := s.codec.Decode(data, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("decode failed: %w", err)
	}

	return obj, nil
}

// setListRV sets the resource version on a list object using the versioner.
func (s *Store) setListRV(listObj runtime.Object, rv uint64) error {
	return s.Versioner().UpdateList(listObj, rv, "", nil)
}

// predicateEmpty returns true if the predicate performs no filtering.
// It guards against nil Label/Field selectors that would panic in
// SelectionPredicate.Empty().
func predicateEmpty(p storage.SelectionPredicate) bool {
	if p.Label == nil && p.Field == nil {
		return true
	}

	return p.Empty()
}

// checkPreconditions verifies that the given preconditions are met by the
// existing object. Returns an error if UID or ResourceVersion do not match.
func (s *Store) checkPreconditions(key string, preconditions *storage.Preconditions, obj runtime.Object) error {
	if preconditions == nil {
		return nil
	}

	if preconditions.UID != nil {
		accessor, err := meta.Accessor(obj)
		if err != nil {
			return err
		}

		if accessor.GetUID() != *preconditions.UID {
			return storage.NewInvalidObjError(key, fmt.Sprintf(
				"precondition UID mismatch: expected %s, got %s",
				*preconditions.UID, accessor.GetUID(),
			))
		}
	}

	if preconditions.ResourceVersion != nil {
		rv, err := s.Versioner().ObjectResourceVersion(obj)
		if err != nil {
			return err
		}

		expectedRV, err := s.Versioner().ParseResourceVersion(*preconditions.ResourceVersion)
		if err != nil {
			return err
		}

		if rv != expectedRV {
			return storage.NewInvalidObjError(key, fmt.Sprintf(
				"precondition ResourceVersion mismatch: expected %d, got %d",
				expectedRV, rv,
			))
		}
	}

	return nil
}
