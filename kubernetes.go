package main

import (
	"crypto/md5"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/context"

	"github.com/albertrdixon/gearbox/logger"
	"github.com/albertrdixon/gearbox/util"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/endpoints"
	"k8s.io/kubernetes/pkg/api/meta"
	uapi "k8s.io/kubernetes/pkg/api/unversioned"
	"k8s.io/kubernetes/pkg/apis/extensions"
	"k8s.io/kubernetes/pkg/client/cache"
	"k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/client/unversioned/clientcmd"
	"k8s.io/kubernetes/pkg/client/unversioned/testclient"
	"k8s.io/kubernetes/pkg/controller/framework"
	"k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/pkg/labels"
	"k8s.io/kubernetes/pkg/runtime"
	"k8s.io/kubernetes/pkg/watch"
)

var (
	FakeKubeClient = &testclient.Fake{}
	resources      = map[string]runtime.Object{
		"services":  &api.Service{},
		"endpoints": &api.Endpoints{},
		"ingress":   &extensions.Ingress{},
	}
)

func newKubeClient(url, ver string, insecure bool) (*unversioned.Client, error) {
	config, er := getKubeConfig(url, insecure)
	if er != nil {
		return nil, er
	}
	return unversioned.New(config)
}

func getKubeConfig(url string, insecure bool) (*unversioned.Config, error) {
	config, er := unversioned.InClusterConfig()
	if er != nil {
		config, er = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{},
		).ClientConfig()
		if er != nil {
			return nil, er
		}
		config.Host = url
	}

	config.Insecure = insecure
	return config, nil
}

func ResetFakeClient() {
	FakeKubeClient = &testclient.Fake{}
}

func Status(client unversioned.Interface) error {
	_, er := client.ServerVersion()
	return er
}

func AddressesFromSubsets(subs []api.EndpointSubset) Addresses {
	var addrs = Addresses(make(map[int][]*url.URL))
	subs = endpoints.RepackSubsets(subs)
	for i := range subs {
		for _, port := range subs[i].Ports {
			for k := range subs[i].Addresses {
				ur, er := url.Parse(fmt.Sprintf("http://%s:%d", subs[i].Addresses[k].IP, port.Port))
				if er != nil {
					logger.Warnf("Failed to parse Endpoint Address: %v", er)
					continue
				}
				// if _, ok := addrs[port.Port]; ok {
				//  addrs[port.Port] = append(addrs[port.Port], ur)
				// } else {
				//  addrs[port.Port] = []*url.URL{ur}
				// }
				addrs[port.Port] = append(addrs[port.Port], ur)
			}
		}
	}
	return addrs
}

func GetMetadata(obj runtime.Object) (*Metadata, error) {
	o, er := api.ObjectMetaFor(obj)
	if er != nil {
		return nil, er
	}
	md := &Metadata{*o, "Unknown"}
	a, er := meta.Accessor(obj)
	if er != nil {
		return md, er
	}
	md.Kind = getKind(a, obj)
	return md, nil
}

func getKind(m meta.Interface, r Object) string {
	k := m.Kind()
	if k != "" {
		return k
	}
	switch r.(type) {
	default:
		return "Unknown"
	case *api.Service:
		return ServiceKind
	case *api.Endpoints:
		return EndpointsKind
	case *uapi.Status:
		return StatusKind
	}
}

func GetID(me *Metadata) string {
	return strings.Join([]string{me.Namespace, me.Name}, ".")
}

func GetSrvID(u *url.URL, me *Metadata) string {
	return strings.Join([]string{me.Namespace, me.Name, util.Hashf(md5.New(), u)[:hashLen]}, ".")
}

func createStore(kind string, sel map[string]string, resync time.Duration, ctx context.Context) (cache.Store, error) {
	obj, ok := resources[kind]
	if !ok {
		return nil, fmt.Errorf("Object type %q not supported", kind)
	}

	store := cache.NewTTLStore(framework.DeletionHandlingMetaNamespaceKeyFunc, cacheTTL)
	selector := selectorFromMap(sel)
	lw := getListWatch(kind, selector)
	cache.NewReflector(lw, obj, store, resync).RunUntil(ctx.Done())
	return store, nil
}

func createController(w Watcher, c cache.Getter, res string, sel map[string]string, resync time.Duration) (cache.Store, *framework.Controller) {
	obj, ok := resources[res]
	if !ok {
		return nil, nil
	}

	sl := selectorFromMap(sel)
	handler := framework.ResourceEventHandlerFuncs{
		AddFunc:    w.Add,
		DeleteFunc: w.Delete,
		UpdateFunc: w.Update,
	}
	return framework.NewInformer(getListWatch(res, sl), obj, resync, handler)
}

func getListWatch(resource string, selector labels.Selector) *cache.ListWatch {
	return &cache.ListWatch{
		ListFunc: func() (runtime.Object, error) {
			return c.Get().Namespace(api.NamespaceAll).Resource(resource).
				LabelsSelectorParam(selector).FieldsSelectorParam(fields.Everything()).
				Do().Get()
		},
		WatchFunc: func(options uapi.ListOptions) (watch.Interface, error) {
			return c.Get().Prefix("watch").Namespace(api.NamespaceAll).Resource(res).
				LabelsSelectorParam(sl).FieldsSelectorParam(fields.Everything()).
				Param("resourceVersion", options.ResourceVersion).Watch()
		},
	}
}

func selectorFromMap(m map[string]string) labels.Selector {
	s := labels.Everything()
	for k, v := range m {
		if !strings.HasPrefix(k, RomulusKeyspace) {
			k = strings.Join([]string{RomulusKeyspace, k}, "")
		}
		s = s.Add(k, labels.DoubleEqualsOperator, []string{v})
	}
	return s
}

func serviceListFromIngress(store *kubeCache, in *ingress) ([]*service, error) {
	var (
		host, path string
		list       []*service
		s          *api.Service
		er         error
	)

	if in.Spec.Backend != nil {
		list = make([]*service, 0, len(in.Spec.Rules+1))
		s := &api.Service{Name: in.Spec.Backend.ServiceName, Namespace: in.Namespace}
		if s, er = store.getService(o); er != nil {
			return list, er
		}
		list = append(list, &service{backend})
	} else {
		list = make([]*service, 0, len(in.Spec.Rules))
	}

	for _, rule := range in.Spec.Rules {
		host = rule.Host
		for _, node := range rule.HTTP.Paths {
			path = node.Path
			s = &api.Service{Name: node.Backend.ServiceName, Namespace: in.Namespace}
			if s, er = store.getService(s); er != nil {

			}
		}
	}
}

func getServiceMeta(store *kubeCache, svc *api.Service) (*Metadata, error) {
	s, er := store.getService(svc)
	if er != nil {
		return nil, er
	}
	me, er := GetMetadata(s)
	if er != nil {
		return nil, er
	}
	return me, nil
}

func newDefaultService(name string, backend *extensions.IngressBackend) *service {
	s := &service{}
	if backend == nil {
		return s
	}

	path := extensions.HTTPIngressPath{Backend: *backend}
	s.IngressRuleValue.HTTP = &extensions.HTTPIngressRuleValue{
		Paths: []extensions.HTTPIngressPath{path},
	}
	return s
}

func (k *kubeCache) getService(o runtime.Object) (*Service, error) {
	// k.service.Get(o)
	// key, er := cache.MetaNamespaceKeyFunc(o)
	// if er != nil {
	// 	return nil, er
	// }
	obj, ok, er := k.service.Get(o)
	if er != nil {
		return nil, er
	}
	if !ok {
		return nil, nil
	}
	s, ok := obj.(*api.Service)
	if !ok {
		return nil, errors.New("Service cache returned non-Service object")
	}
	return s, nil
}

type Object interface {
	runtime.Object
}

type Watcher interface {
	Add(obj interface{})
	Delete(obj interface{})
	Update(old, next interface{})
}

type kubeCache struct {
	ingress, service cache.Store
}

type ingress struct {
	extensions.Ingress
}

type service struct {
	host, path  string
	annotations map[string]string
	backend     *api.Service
}

type serviceList []*service

type Service struct {
	api.Service
}

type Endpoints struct {
	api.Endpoints
}

type EndpointSubset struct {
	api.EndpointSubset
}

type EndpointSubsets []api.EndpointSubset

type Metadata struct {
	api.ObjectMeta
	Kind string
}

type Addresses map[int][]*url.URL

func (i Ingress) String() string {
	return fmt.Sprintf("Ingress(Name=%q, Namespace=%q)", i.Name, i.Namespace)
}

func (e Endpoints) String() string {
	return fmt.Sprintf(`Endpoints(Name=%q, Namespace=%q)`, e.ObjectMeta.Name, e.ObjectMeta.Namespace)
}

func (s Service) String() string {
	return fmt.Sprintf(`Service(Name=%q, Namespace=%q)`, s.ObjectMeta.Name, s.ObjectMeta.Namespace)
}

func (e EndpointSubset) String() string {
	ports := make([]string, 0, len(e.Ports))
	addrs := make([]string, 0, len(e.Addresses))

	for _, p := range e.Ports {
		ports = append(ports, fmt.Sprintf("%s:%d", p.Name, p.Port))
	}
	for _, a := range e.Addresses {
		addrs = append(addrs, a.IP)
	}
	return fmt.Sprintf("{ips=[%s], ports=[%s]}",
		strings.Join(addrs, ", "), strings.Join(ports, ", "))
}

func (eps EndpointSubsets) String() string {
	sl := []string{}
	for _, s := range eps {
		sl = append(sl, EndpointSubset{s}.String())
	}
	return fmt.Sprintf("Subsets(%s)", strings.Join(sl, ", "))
}

const (
	ServiceKind   = "Service"
	EndpointsKind = "Endpoints"
	StatusKind    = "Status"

	RomulusKeyspace = "romulus/"
	hashLen         = 8

	cacheTTL = 48 * time.Hour
)

func LabelKeyf(bits ...string) string {
	return strings.Join(append([]string{RomulusKeyspace}, bits...), "")
}

func AnnotationsKeyf(bits ...string) string {
	return strings.Join(append([]string{RomulusKeyspace}, bits...), "")
}
