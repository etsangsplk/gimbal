// Copyright © 2018 Heptio
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

package k8s

import (
	"fmt"

	"github.com/sirupsen/logrus"

	localmetrics "github.com/heptio/gimbal/discovery/pkg/metrics"
	"github.com/heptio/gimbal/discovery/pkg/sync"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

const (
	kubesystemNamespace = "kube-system"
	kubesystemService   = "kubernetes"
)

// Controller receives notifications from the Kubernetes API and translates those
// objects into additions and removals entries of services / endpoints
type Controller struct {
	Logger          *logrus.Logger
	syncqueue       sync.Queue
	servicesSynced  cache.InformerSynced
	endpointsSynced cache.InformerSynced

	clusterName string
}

// NewController returns a new NewController
func NewController(log *logrus.Logger, gimbalKubeClient kubernetes.Interface, kubeInformerFactory kubeinformers.SharedInformerFactory,
	clusterName string, threadiness int, metrics localmetrics.DiscovererMetrics) *Controller {

	// obtain references to shared index informers for the services types.
	serviceInformer := kubeInformerFactory.Core().V1().Services()
	endpointsInformer := kubeInformerFactory.Core().V1().Endpoints()

	c := &Controller{
		Logger: log,
		syncqueue: sync.Queue{
			KubeClient:  gimbalKubeClient,
			Logger:      log,
			Workqueue:   workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "syncqueue"),
			Threadiness: threadiness,
			Metrics:     metrics,
			ClusterName: clusterName,
		},
		servicesSynced:  serviceInformer.Informer().HasSynced,
		endpointsSynced: endpointsInformer.Informer().HasSynced,
		clusterName:     clusterName,
	}

	// Set up an event handler for when Service resources change.
	serviceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.addService(obj.(*v1.Service))
		},
		UpdateFunc: func(old, new interface{}) {
			c.updateService(new.(*v1.Service))
		},
		DeleteFunc: func(obj interface{}) {
			c.deleteService(obj.(*v1.Service))
		},
	})

	// Set up an event handler for when Endpoint resources change.
	endpointsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.addEndpoints(obj.(*v1.Endpoints))
		},
		UpdateFunc: func(old, new interface{}) {
			c.updateEndpoints(new.(*v1.Endpoints))
		},
		DeleteFunc: func(obj interface{}) {
			c.deleteEndpoints(obj.(*v1.Endpoints))
		},
	})

	return c
}

func (c *Controller) addService(service *v1.Service) {
	if !skipProcessing(service.GetName(), service.GetNamespace()) {
		svc := translateService(service, c.clusterName)
		c.syncqueue.Enqueue(sync.AddServiceAction(svc))
	}
}

func (c *Controller) updateService(service *v1.Service) {
	if !skipProcessing(service.GetName(), service.GetNamespace()) {
		svc := translateService(service, c.clusterName)
		c.syncqueue.Enqueue(sync.UpdateServiceAction(svc))
	}
}

func (c *Controller) deleteService(service *v1.Service) {
	if !skipProcessing(service.GetName(), service.GetNamespace()) {
		svc := translateService(service, c.clusterName)
		c.syncqueue.Enqueue(sync.DeleteServiceAction(svc))
	}
}

func (c *Controller) addEndpoints(endpoints *v1.Endpoints) {
	if !skipProcessing(endpoints.GetName(), endpoints.GetNamespace()) {
		svc := translateEndpoints(endpoints, c.clusterName)
		c.syncqueue.Enqueue(sync.AddEndpointsAction(svc))
	}
}

func (c *Controller) updateEndpoints(endpoints *v1.Endpoints) {
	if !skipProcessing(endpoints.GetName(), endpoints.GetNamespace()) {
		svc := translateEndpoints(endpoints, c.clusterName)
		c.syncqueue.Enqueue(sync.UpdateEndpointsAction(svc))
	}
}

func (c *Controller) deleteEndpoints(endpoints *v1.Endpoints) {
	if !skipProcessing(endpoints.GetName(), endpoints.GetNamespace()) {
		svc := translateEndpoints(endpoints, c.clusterName)
		c.syncqueue.Enqueue(sync.DeleteEndpointsAction(svc))
	}
}

// skipProcessing determines if this should be processed or not
func skipProcessing(name, namespace string) bool {
	if namespace == kubesystemNamespace || (name == kubesystemService && namespace == "default") {
		return true
	}
	return false
}

// Run gets the party started
func (c *Controller) Run(stopCh <-chan struct{}) error {
	defer runtime.HandleCrash()

	// Start the informer factories to begin populating the informer caches
	c.Logger.Infof("Starting k8s controller")

	// Wait for the caches to be synced before starting workers
	c.Logger.Infof("Waiting for services informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.servicesSynced); !ok {
		return fmt.Errorf("failed to wait for service caches to sync")
	}
	c.Logger.Infof("Waiting for services informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.endpointsSynced); !ok {
		return fmt.Errorf("failed to wait for endpoints caches to sync")
	}

	// Start the sync queue
	go c.syncqueue.Run(stopCh)

	c.Logger.Infof("Started workers")
	<-stopCh
	c.Logger.Infof("Shutting down workers")

	return nil
}