package registry

import (
	"github.com/alexkappa/vulcand/plugin/yieldrate"
	"github.com/alexkappa/vulcand/plugin/yieldrauth"
	"github.com/vulcand/vulcand/plugin"
	"github.com/vulcand/vulcand/plugin/cbreaker"
	"github.com/vulcand/vulcand/plugin/connlimit"
	"github.com/vulcand/vulcand/plugin/ratelimit"
	"github.com/vulcand/vulcand/plugin/rewrite"
	"github.com/vulcand/vulcand/plugin/trace"
)

func GetRegistry() (*plugin.Registry, error) {
	r := plugin.NewRegistry()

	specs := []*plugin.MiddlewareSpec{
		yieldrauth.GetSpec(),
		yieldrate.GetSpec(),
		ratelimit.GetSpec(),
		connlimit.GetSpec(),
		rewrite.GetSpec(),
		cbreaker.GetSpec(),
		trace.GetSpec(),
	}

	for _, spec := range specs {
		if err := r.AddSpec(spec); err != nil {
			return r, err
		}
	}

	return r, nil
}
