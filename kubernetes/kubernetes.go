package kubernetes

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
	uapi "k8s.io/kubernetes/pkg/api/unversioned"
	"k8s.io/kubernetes/pkg/apis/extensions"
	"k8s.io/kubernetes/pkg/client/cache"
	"k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/client/unversioned/clientcmd"
	"k8s.io/kubernetes/pkg/controller/framework"
	"k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/pkg/labels"
	"k8s.io/kubernetes/pkg/runtime"
	"k8s.io/kubernetes/pkg/util/intstr"
	"k8s.io/kubernetes/pkg/watch"
)

var (
	// FakeKubeClient = &testclient.Fake{}
	Keyspace string

	resources = map[string]runtime.Object{
		"services":  &api.Service{},
		"endpoints": &api.Endpoints{},
		"ingresses": &extensions.Ingress{},
	}
)

func NewClient(url, ver string, insecure bool) (*unversioned.Client, error) {
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

// func ResetFakeClient() {
// 	FakeKubeClient = &testclient.Fake{}
// }

func Status(client unversioned.Interface) error {
	_, er := client.ServerVersion()
	return er
}

func getID(m api.ObjectMeta) string {
	return strings.Join([]string{m.Namespace, m.Name}, ".")
}

func getSrvID(u *url.URL, m api.ObjectMeta) string {
	return strings.Join([]string{m.Namespace, m.Name, util.Hashf(md5.New(), u, m.UID)[:hashLen]}, ".")
}

func CreateStore(kind string, c cache.Getter, resync time.Duration, ctx context.Context) (cache.Store, error) {
	obj, ok := resources[kind]
	if !ok {
		return nil, fmt.Errorf("Object type %q not supported", kind)
	}

	store := cache.NewTTLStore(framework.DeletionHandlingMetaNamespaceKeyFunc, cacheTTL)
	// selector := selectorFromMap(sel)
	lw := getListWatch(kind, c, labels.Everything())
	cache.NewReflector(lw, obj, store, resync).RunUntil(ctx.Done())
	return store, nil
}

func CreateController(kind string, w watcher, c cache.Getter, resync time.Duration) (cache.Store, *framework.Controller) {
	obj, ok := resources[kind]
	if !ok {
		return nil, nil
	}

	// sl := selectorFromMap(sel)
	handler := framework.ResourceEventHandlerFuncs{
		AddFunc:    w.Add,
		DeleteFunc: w.Delete,
		UpdateFunc: w.Update,
	}
	return framework.NewInformer(getListWatch(kind, c, labels.Everything()), obj, resync, handler)
}

func getListWatch(kind string, get cache.Getter, selector labels.Selector) *cache.ListWatch {
	return &cache.ListWatch{
		ListFunc: func() (runtime.Object, error) {
			return get.Get().Namespace(api.NamespaceAll).Resource(kind).
				LabelsSelectorParam(selector).FieldsSelectorParam(fields.Everything()).
				Do().Get()
		},
		WatchFunc: func(options uapi.ListOptions) (watch.Interface, error) {
			return get.Get().Prefix("watch").Namespace(api.NamespaceAll).Resource(kind).
				LabelsSelectorParam(selector).FieldsSelectorParam(fields.Everything()).
				Param("resourceVersion", options.ResourceVersion).Watch()
		},
	}
}

func selectorFromMap(m map[string]string) labels.Selector {
	s := labels.Everything()
	for k, v := range m {
		if !strings.HasPrefix(k, Keyspace) {
			k = strings.Join([]string{Keyspace, k}, "")
		}
		s = s.Add(k, labels.DoubleEqualsOperator, []string{v})
	}
	return s
}

func ServicesFromIngress(store *KubeCache, in *extensions.Ingress) []*Service {
	var (
		list []*Service
		s    *api.Service
		er   error
	)

	list = []*Service{}
	if in.Spec.Backend != nil {
		meta := &api.ObjectMeta{Name: in.Spec.Backend.ServiceName, Namespace: in.Namespace}
		if s, er = store.GetService(meta); er != nil {
			logger.Errorf("Unable to find default service %v: %v", KubeService(*s), er)
		} else {
			svc := newService(in.Spec.Backend.ServicePort, s)
			svc.Annotations = mergeAnnotations(in.Annotations, s.Annotations)
			list = append(list, svc)
		}
	}

	for _, rule := range in.Spec.Rules {
		for _, node := range rule.HTTP.Paths {
			meta := &api.ObjectMeta{Name: node.Backend.ServiceName, Namespace: in.Namespace}
			if s, er = store.GetService(meta); er != nil {
				logger.Errorf("Unable to find service %v: %v", KubeService(*s), er)
			} else {
				svc := newService(node.Backend.ServicePort, s)
				svc.Annotations = mergeAnnotations(in.Annotations, s.Annotations)
				svc.Host, svc.Path = rule.Host, node.Path
				list = append(list, svc)
			}
		}
	}
	return list
}

func (k *KubeCache) GetService(o interface{}) (*api.Service, error) {
	obj, ok, er := k.Service.Get(o)
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

func (s *Service) SetBackend(port intstr.IntOrString, backend *api.Service) {
	ur := ""
	for _, sp := range backend.Spec.Ports {
		if sp.Name == port.String() || sp.Port == port.IntValue() {
			ur = fmt.Sprintf("http://%s:%d", backend.Spec.ClusterIP, sp.Port)
		}
	}
	u, _ := url.Parse(ur)
	s.SrvID = getSrvID(u, backend.ObjectMeta)
	s.Backend = u
}

func (s *Service) GetAnnotation(key string) (val string, ok bool) {
	if !strings.HasPrefix(key, Keyspace) {
		key = strings.Join([]string{Keyspace, key}, "/")
	}
	val, ok = s.Annotations[key]
	return
}

const (
	hashLen  = 8
	cacheTTL = 48 * time.Hour

	ServiceKind  = "service"
	ServicesKind = "services"
	IngressKind  = "ingresses"
)

func ServiceReady(s *api.Service) bool {
	return s.Spec.Type == api.ServiceTypeClusterIP && api.IsServiceIPSet(s)
}

func (r Route) Empty() bool {
	return r.Host == "" && r.Path == "" && r.Method == "" && (r.Headers == nil || len(r.Headers) == 0)
}

func mergeAnnotations(defaultMap, overrideMap map[string]string) map[string]string {
	out := make(map[string]string)
	for key, val := range defaultMap {
		if strings.HasPrefix(key, Keyspace) {
			if override, ok := overrideMap[key]; ok {
				out[key] = override
			} else {
				out[key] = val
			}
		}
	}
	return out
}
