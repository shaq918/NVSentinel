//  Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

// TestGPUInformerWithFakeClient demonstrates how to integrate a fake versioned
// clientset with a SharedInformerFactory in tests.
package main_test

import (
	"context"
	"sync"
	"testing"
	"time"

	devicev1alpha1 "github.com/nvidia/nvsentinel/api/device/v1alpha1"
	"github.com/nvidia/nvsentinel/pkg/client-go/client/versioned/fake"
	"github.com/nvidia/nvsentinel/pkg/client-go/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
)

// bookmarkWatch wraps a watch.Interface to inject a bookmark event after
// creation. This is needed because k8s.io/client-go v0.35+ requires bookmark
// events for the reflector to consider initial sync complete, but the fake
// client's ObjectTracker doesn't send them automatically.
type bookmarkWatch struct {
	watch.Interface
	bookmarkCh chan watch.Event
	resultCh   chan watch.Event
	stopCh     chan struct{}
	stopOnce   sync.Once
}

func newBookmarkWatch(w watch.Interface) *bookmarkWatch {
	bw := &bookmarkWatch{
		Interface:  w,
		bookmarkCh: make(chan watch.Event, 1),
		resultCh:   make(chan watch.Event),
		stopCh:     make(chan struct{}),
	}

	// Send initial bookmark to signal list completion.
	// The bookmark object must be the same type as the expected resource (GPU).
	bw.bookmarkCh <- watch.Event{
		Type: watch.Bookmark,
		Object: &devicev1alpha1.GPU{
			ObjectMeta: metav1.ObjectMeta{
				ResourceVersion: "0",
				Annotations: map[string]string{
					metav1.InitialEventsAnnotationKey: "true",
				},
			},
		},
	}

	// Multiplex bookmark and underlying watch events
	go func() {
		defer close(bw.resultCh)
		for {
			select {
			case <-bw.stopCh:
				return
			case ev, ok := <-bw.bookmarkCh:
				if ok {
					select {
					case bw.resultCh <- ev:
					case <-bw.stopCh:
						return
					}
				}
			case ev, ok := <-w.ResultChan():
				if !ok {
					return
				}
				select {
				case bw.resultCh <- ev:
				case <-bw.stopCh:
					return
				}
			}
		}
	}()

	return bw
}

func (bw *bookmarkWatch) ResultChan() <-chan watch.Event {
	return bw.resultCh
}

func (bw *bookmarkWatch) Stop() {
	bw.stopOnce.Do(func() {
		close(bw.stopCh)
	})
	bw.Interface.Stop()
}

func TestGPUInformerWithFakeClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize the fake clientset. The fake client uses an in-memory
	// ObjectTracker to simulate the behavior of a real API server.
	client := fake.NewSimpleClientset()

	// watcherStarted is used to synchronize the informer's transition from the
	// initial LIST phase to the WATCH phase.
	watcherStarted := make(chan struct{})

	// Prepend a WatchReactor to intercept watch actions. This allows us to
	// signal the test when the informer has successfully established its
	// stream, preventing race conditions where events are injected before
	// the watcher is ready.
	//
	// The reactor also wraps the watch to inject a bookmark event, which is
	// required by k8s.io/client-go v0.35+ for the reflector to consider the
	// initial sync complete.
	client.PrependWatchReactor("*", func(action clienttesting.Action) (handled bool, ret watch.Interface, err error) {
		watchAction, ok := action.(clienttesting.WatchActionImpl)
		if !ok {
			return false, nil, nil
		}

		opts := watchAction.ListOptions
		gvr := action.GetResource()
		ns := action.GetNamespace()

		// Manually invoke the tracker to create the watch stream.
		w, err := client.Tracker().Watch(gvr, ns, opts)
		if err != nil {
			return false, nil, err
		}

		// Wrap watch to inject initial bookmark event for reflector sync
		wrappedWatch := newBookmarkWatch(w)

		// Close the channel to notify the test that the Informer is now
		// listening for events.
		close(watcherStarted)
		return true, wrappedWatch, nil
	})

	// Create a factory for the informers.
	// We use a 0 resync period as we are testing event-driven logic.
	gpuChan := make(chan *devicev1alpha1.GPU, 1)
	factory := externalversions.NewSharedInformerFactory(client, 0)
	gpuInformer := factory.Device().V1alpha1().GPUs().Informer()

	// Register an event handler to verify that the informer's cache is
	// correctly updated and that notifications are dispatched.
	gpuInformer.AddEventHandler(&cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			gpu := obj.(*devicev1alpha1.GPU)
			t.Logf("Informer signaled GPU added: %s", gpu.Name)
			gpuChan <- gpu
		},
	})

	// Start the informer factory and wait for the initial cache sync (LIST).
	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), gpuInformer.HasSynced) {
		t.Fatal("Timed out waiting for caches to sync")
	}

	// Ensure the informer has moved past the LIST phase and into WATCH.
	// In the fake client, writes that occur between LIST and WATCH are lost.
	<-watcherStarted

	// Define a test resource to inject into the system.
	testGPU := &devicev1alpha1.GPU{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-1",
		},
		Spec: devicev1alpha1.GPUSpec{
			UUID: "GPU-1",
		},
	}

	// Inject the resource directly into the fake client's ObjectTracker.
	// This simulates a "server-side" event, such as a discovery agent
	// reporting a new device via the gRPC API.
	err := client.Tracker().Add(testGPU)
	if err != nil {
		t.Fatalf("Tracker injection failed: %v", err)
	}

	// Verify that the Informer successfully received and processed the ADD event.
	select {
	case gpu := <-gpuChan:
		if gpu.Name != "gpu-1" {
			t.Errorf("Expected GPU gpu-1, got %s", gpu.Name)
		}
	case <-time.After(wait.ForeverTestTimeout):
		t.Error("Informer failed to receive the added GPU event within timeout")
	}
}
