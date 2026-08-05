// Command tsp-drawer is a Viam module providing a generic service that turns an
// ordered set of 2D points (e.g. a TSP-art tour) into a series of motion-service
// plan requests, drawing the tour with a pen on the paper plane.
package main

import (
	"go.viam.com/rdk/module"
	"go.viam.com/rdk/resource"
	genericservice "go.viam.com/rdk/services/generic"
)

func main() {
	resource.RegisterService(genericservice.API, Model, resource.Registration[resource.Resource, *Config]{
		Constructor: newDrawer,
	})
	module.ModularMain(resource.APIModel{API: genericservice.API, Model: Model})
}
