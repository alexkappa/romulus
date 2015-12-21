package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/albertrdixon/gearbox/logger"
	"github.com/cenkalti/backoff"

	"golang.org/x/net/context"
	"k8s.io/kubernetes/pkg/apis/extensions"
	"k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/controller/framework"

	"github.com/timelinelabs/romulus/kubernetes"
	"github.com/timelinelabs/romulus/loadbalancer"
)

var (
	upsertBackoff = backoff.NewExponentialBackOff()
	upsertTimeout = 10 * time.Second
)

func newEngine(kubeapi, kubever string, insecure bool, lb loadbalancer.LoadBalancer, ctx context.Context) (*Engine, error) {
	k, er := kubernetes.NewClient(kubeapi, kubever, insecure)
	if er != nil {
		return nil, er
	}
	return &Engine{
		cache: &kubernetes.KubeCache{},
		lb:    lb,
		kube:  k,
		ctx:   ctx,
	}, nil
}

func (e *Engine) Start(resync time.Duration) error {
	var er error
	if er = kubernetes.Status(e.kube); er != nil {
		return fmt.Errorf("Failed to connect to kubernetes: %v", er)
	}
	if er = e.lb.Status(); er != nil {
		return fmt.Errorf("Failed to connect to loadbalancer: %v", er)
	}

	if e.cache.Service, er = kubernetes.CreateStore(kubernetes.ServicesKind, e.kube, resync, e.ctx); er != nil {
		return fmt.Errorf("Unable to create Service store: %v", er)
	}

	e.cache.Ingress, e.controller = kubernetes.CreateController(kubernetes.IngressKind, e, e.kube, resync)
	go e.controller.Run(e.ctx.Done())
	return nil
}

func (e *Engine) Add(obj interface{}) {
	in, ok := obj.(*extensions.Ingress)
	if !ok {
		logger.Errorf("Got non-Ingress object in Ingress watch")
		logger.Debugf("Object: %+v", obj)
		return
	}

	logger.Debugf("Callback: Add %v", kubernetes.KubeIngress(*in))
	if er := e.add(in); er != nil {
		logger.Errorf("Add failed: %v", er)
	}
}

func (e *Engine) Delete(obj interface{}) {
	in, ok := obj.(*extensions.Ingress)
	if !ok {
		logger.Errorf("Got non-Ingress object in Ingress watch")
		logger.Debugf("Object: %+v", obj)
		return
	}

	logger.Debugf("Callback: Delete %v", kubernetes.KubeIngress(*in))
	if er := e.del(in); er != nil {
		logger.Errorf("Delete failed: %v", er)
	}
}

func (e *Engine) Update(old, next interface{}) {
	in, ok := next.(*extensions.Ingress)
	if !ok {
		logger.Errorf("Got non-Ingress object in Ingress watch")
		logger.Debugf("Object: %+v", next)
		return
	}

	logger.Debugf("Callback: Update %v", kubernetes.KubeIngress(*in))
	if er := e.add(in); er != nil {
		logger.Errorf("Update failed: %v", er)
	}
}

func (e *Engine) add(in *extensions.Ingress) error {
	e.Lock()
	defer e.Unlock()

	services := kubernetes.ServicesFromIngress(e.cache, in)
	if len(services) < 1 {
		return fmt.Errorf("No services to add from %v", kubernetes.KubeIngress(*in))
	}

	for _, svc := range services {
		backend, er := e.lb.NewBackend(svc)
		if er != nil {
			return er
		}
		srvs, er := e.lb.NewServers(svc)
		if er != nil {
			return er
		}
		for i := range srvs {
			backend.AddServer(srvs[i])
		}

		frontend, er := e.lb.NewFrontend(svc)
		if er != nil {
			return er
		}
		mids, er := e.lb.NewMiddlewares(svc)
		if er != nil {
			return er
		}
		for i := range mids {
			frontend.AddMiddleware(mids[i])
		}

		e.commit(func() error {
			logger.Infof("Upserting %v", backend)
			if er := e.lb.UpsertBackend(backend); er != nil {
				return er
			}
			logger.Infof("Upserting %v", frontend)
			return e.lb.UpsertFrontend(frontend)
		})
	}
	return nil
}

func (e *Engine) del(in *extensions.Ingress) error {
	e.Lock()
	defer e.Unlock()

	services := kubernetes.ServicesFromIngress(e.cache, in)
	if len(services) < 1 {
		return fmt.Errorf("No services to add from %v", kubernetes.KubeIngress(*in))
	}

	for _, svc := range services {
		backend, er := e.lb.NewBackend(svc)
		if er != nil {
			return er
		}
		frontend, er := e.lb.NewFrontend(svc)
		if er != nil {
			return er
		}

		e.commit(func() error {
			logger.Infof("Removing %v", frontend)
			if er := e.lb.DeleteFrontend(frontend); er != nil {
				return er
			}
			logger.Infof("Removing %v", backend)
			return e.lb.DeleteBackend(backend)
		})
	}
	return nil
}

func (e *Engine) commit(fn upsertFunc) {
	upsertBackoff.MaxElapsedTime = upsertTimeout
	upsertBackoff.Reset()

	for {
		select {
		case <-e.ctx.Done():
			return
		default:
			duration := upsertBackoff.NextBackOff()
			if duration == backoff.Stop {
				logger.Errorf("Timed out trying to commit changes to loadbalancer")
				return
			}
			er := fn()
			if er == nil {
				return
			}
			logger.Warnf("Commit failed, retry in %v: %v", duration, er)
			time.Sleep(duration)
		}
	}
}

// Engine is the main driver and handles kubernetes callbacks
type Engine struct {
	sync.Mutex
	cache      *kubernetes.KubeCache
	controller *framework.Controller
	lb         loadbalancer.LoadBalancer
	kube       *unversioned.Client
	timeout    time.Duration
	ctx        context.Context
}

type upsertFunc func() error

const (
	interval          = 50 * time.Millisecond
	serviceResource   = "services"
	endpointsResource = "endpoints"
	ingressResource   = "ingress"
)
