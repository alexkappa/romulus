package loadbalancer

import (
	"errors"

	"github.com/timelinelabs/romulus/kubernetes"
)

var (
	ErrUnexpectedFrontendType = errors.New("Frontend is of unexpected type")
	ErrUnexpectedBackendType  = errors.New("Backend is of unexpected type")
)

type LoadBalancer interface {
	NewFrontend(svc *kubernetes.Service) (Frontend, error)
	GetFrontend(svc *kubernetes.Service) (Frontend, error)
	UpsertFrontend(fr Frontend) error
	DeleteFrontend(fr Frontend) error
	NewBackend(svc *kubernetes.Service) (Backend, error)
	GetBackend(svc *kubernetes.Service) (Backend, error)
	UpsertBackend(ba Backend) error
	DeleteBackend(ba Backend) error
	NewServers(svc *kubernetes.Service) ([]Server, error)
	GetServers(svc *kubernetes.Service) ([]Server, error)
	UpsertServer(ba Backend, srv Server) error
	DeleteServer(ba Backend, srv Server) error
	NewMiddlewares(svc *kubernetes.Service) ([]Middleware, error)

	Kind() string
	Status() error
}

type LoadbalancerObject interface {
	GetID() string
}

type Frontend interface {
	LoadbalancerObject
	AddMiddleware(mid Middleware)
}
type Backend interface {
	LoadbalancerObject
	AddServer(srv Server)
}
type Server interface {
	LoadbalancerObject
}
type Middleware interface {
	LoadbalancerObject
}

type ServerMap map[string]Server
