package kubernetes

import (
	"fmt"
	"net/url"
	"strings"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/apis/extensions"
	"k8s.io/kubernetes/pkg/client/cache"
	"k8s.io/kubernetes/pkg/util/intstr"
)

type watcher interface {
	Add(obj interface{})
	Delete(obj interface{})
	Update(old, next interface{})
}

type Service struct {
	Route
	ID, SrvID   string
	Annotations map[string]string
	Backend     *url.URL
}

func newService(port intstr.IntOrString, backend *api.Service) *Service {
	svc := &Service{
		ID:          getID(backend.ObjectMeta),
		Route:       Route{Headers: make(map[string]string)},
		Annotations: make(map[string]string),
	}
	if port.String() != "" {
		svc.SetBackend(port, backend)
	}
	return svc
}

type Route struct {
	Host, Path, Method string
	Headers            map[string]string
}

type KubeCache struct {
	Ingress, Service cache.Store
}

type KubeIngress extensions.Ingress
type KubeService api.Service

func (i KubeIngress) String() string {
	return fmt.Sprintf("Ingress(Name=%q, Namespace=%q)", i.ObjectMeta.Name, i.ObjectMeta.Namespace)
}

func (s KubeService) String() string {
	return fmt.Sprintf(`Service(Name=%q, Namespace=%q)`, s.ObjectMeta.Name, s.ObjectMeta.Namespace)
}

func (s Service) String() string {
	return fmt.Sprintf("Service(backend=%v, route=%v, meta=%v)", s.Backend, s.Route, s.Annotations)
}

func (r Route) String() string {
	rt := []string{}
	if r.Host != "" {
		rt = append(rt, fmt.Sprintf("host=%s", r.Host))
	}
	if r.Path != "" {
		rt = append(rt, fmt.Sprintf("path=%s", r.Path))
	}
	if r.Method != "" {
		rt = append(rt, fmt.Sprintf("method=%s", r.Method))
	}
	if len(r.Headers) > 0 {
		rt = append(rt, fmt.Sprintf("headers=%v", r.Headers))
	}
	return fmt.Sprintf("Route(%s)", strings.Join(rt, ", "))
}
