// Package scenario implements the test scenario abstract interface.
package scenario

import (
	"strconv"

	"github.com/oasislabs/oasis-core/go/oasis-test-runner/env"
	"github.com/oasislabs/oasis-core/go/oasis-test-runner/oasis"
)

// ParametersToStringMap convert scenario-specific key->value parameter set to string->string map.
func ParametersToStringMap(p map[string]interface{}) map[string]string {
	sMap := make(map[string]string)
	for k, v := range p {
		switch v := v.(type) {
		case *int:
			sMap[k] = strconv.Itoa(*v)
		case *int64:
			sMap[k] = strconv.FormatInt(*v, 10)
		case *float64:
			sMap[k] = strconv.FormatFloat(*v, 'E', -1, 64)
		case *bool:
			sMap[k] = strconv.FormatBool(*v)
		default:
			sMap[k] = *v.(*string)
		}
	}
	return sMap
}

// Scenario is a test scenario identified by name.
type Scenario interface {
	// Clone returns a copy of this scenario instance to be run in parallel.
	Clone() Scenario

	// Name returns the name of the scenario.
	//
	// Note: The name is used when selecting which tests to run, and should
	// be something suitable for use as a command line argument.
	Name() string

	// Parameters returns the settable test parameters via CLI.
	//
	// The returned map should contain parameter name -> reference to the
	// variable the parameter value should be stored to.
	Parameters() map[string]interface{}

	// Fixture returns a network fixture to use for this scenario.
	//
	// It may return nil in case the scenario doesn't use a fixture and
	// performs all setup in Init.
	Fixture() (*oasis.NetworkFixture, error)

	// Init initializes the scenario.
	//
	// Network will be provided in case Fixture returned a non-nil value,
	// otherwise it will be nil.
	Init(childEnv *env.Env, net *oasis.Network) error

	// Run runs the scenario.
	Run(childEnv *env.Env) error
}
