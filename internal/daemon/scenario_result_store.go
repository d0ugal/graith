package daemon

import (
	"github.com/d0ugal/graith/internal/store"
)

// scenarioResultPersistence is the consumer-owned port for publishing result
// documents. Init and Put are deliberately separate: publication preserves
// the existing error ordering where store initialisation fails before any
// document write is attempted.
type scenarioResultPersistence interface {
	Init() error
	Put(key, body string) error
}

// gitScenarioResultPersistence adapts the durable shared document store to the
// scenario-result port. It keeps store locking, atomic writes, and Git commits
// in the store package rather than leaking those details into scenario logic.
type gitScenarioResultPersistence struct {
	dataDir func() string
}

func (p gitScenarioResultPersistence) storePath() string {
	return store.SharedStorePath(p.dataDir())
}

func (p gitScenarioResultPersistence) Init() error {
	return store.Init(p.storePath())
}

func (p gitScenarioResultPersistence) Put(key, body string) error {
	return store.Put(p.storePath(), key, body)
}
